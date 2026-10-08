package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func newMemoryListener() *memoryListener {
	return &memoryListener{connections: make(chan net.Conn), closed: make(chan struct{})}
}

func (listener *memoryListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *memoryListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func (listener *memoryListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 51235}
}

type remoteAddressConnection struct {
	net.Conn
	remote net.Addr
}

func (connection *remoteAddressConnection) RemoteAddr() net.Addr { return connection.remote }

func (listener *memoryListener) connect(t *testing.T, source string) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	wrapped := &remoteAddressConnection{
		Conn:   server,
		remote: &net.TCPAddr{IP: net.ParseIP(source), Port: 43123},
	}
	select {
	case listener.connections <- wrapped:
	case <-time.After(time.Second):
		t.Fatal("test listener did not accept connection")
	}
	return client
}

func fakeNFTCommand(_ ...string) *exec.Cmd {
	return exec.Command("sh", "-c", "cat >/dev/null")
}

func acceptMemoryConnection(t *testing.T, listener *trustedProxyListener, source string) (net.Conn, net.Conn) {
	t.Helper()
	accepted := make(chan net.Conn, 1)
	failures := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			failures <- err
			return
		}
		accepted <- connection
	}()
	memory, ok := listener.Listener.(*memoryListener)
	if !ok {
		t.Fatal("expected memory listener")
	}
	client := memory.connect(t, source)
	select {
	case server := <-accepted:
		return client, server
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("connection was not accepted")
	}
	return nil, nil
}

func TestTrustedProxyListenerRestoresAuthenticatedSource(t *testing.T) {
	token := "sqa_" + strings.Repeat("a", 43)
	listener := &trustedProxyListener{Listener: newMemoryListener(), apiToken: token}
	client, server := acceptMemoryConnection(t, listener, "127.0.0.1")
	defer client.Close()
	defer server.Close()

	request := "GET /healthz HTTP/1.1\r\nHost: node\r\n\r\n"
	go func() { _, _ = io.WriteString(client, gatePrelude(token, "9.9.9.9")+request) }()
	remoteHost, _, err := net.SplitHostPort(server.RemoteAddr().String())
	if err != nil || !sameIP(remoteHost, "9.9.9.9") {
		t.Fatalf("restored source=%q err=%v", server.RemoteAddr(), err)
	}
	payload := make([]byte, len(request))
	if _, err := io.ReadFull(server, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != request {
		t.Fatalf("application payload=%q", payload)
	}
}

func TestTrustedProxyListenerKeepsDirectLoopbackTraffic(t *testing.T) {
	listener := &trustedProxyListener{Listener: newMemoryListener(), apiToken: "secret"}
	client, server := acceptMemoryConnection(t, listener, "127.0.0.1")
	defer client.Close()
	defer server.Close()

	request := "GET /healthz HTTP/1.1\r\nHost: node\r\n\r\n"
	go func() { _, _ = io.WriteString(client, request) }()
	remoteHost, _, err := net.SplitHostPort(server.RemoteAddr().String())
	if err != nil || !sameIP(remoteHost, "127.0.0.1") {
		t.Fatalf("direct source=%q err=%v", server.RemoteAddr(), err)
	}
	payload := make([]byte, len(request))
	if _, err := io.ReadFull(server, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != request {
		t.Fatalf("direct payload=%q", payload)
	}
}

func TestTrustedProxyListenerRejectsForgedPrelude(t *testing.T) {
	listener := &trustedProxyListener{Listener: newMemoryListener(), apiToken: "real-secret"}
	client, server := acceptMemoryConnection(t, listener, "127.0.0.1")
	defer client.Close()
	defer server.Close()

	go func() { _, _ = io.WriteString(client, gatePrelude("wrong-secret", "9.9.9.9")) }()
	buffer := make([]byte, 1)
	if _, err := server.Read(buffer); err == nil {
		t.Fatal("forged gate prelude was accepted")
	}
}

func TestHTTPServerReceivesAuthenticatedGateSource(t *testing.T) {
	token := "sqa_" + strings.Repeat("b", 43)
	memory := newMemoryListener()
	listener := &trustedProxyListener{Listener: memory, apiToken: token}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, requestIP(request))
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})

	client := memory.connect(t, "127.0.0.1")
	defer client.Close()
	request := gatePrelude(token, "2001:db8::1234") +
		"GET / HTTP/1.1\r\nHost: node\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "2001:db8::1234" {
		t.Fatalf("HTTP request source=%q", body)
	}
}

func TestLeaseGateRestrictsSourceForwardsTCPAndExpires(t *testing.T) {
	previousNFTCommand := nftCommand
	previousGateListen := gateListen
	previousGateDial := gateDial
	nftCommand = fakeNFTCommand
	listener := newMemoryListener()
	gateListen = func(_, _ string) (net.Listener, error) { return listener, nil }
	token := "sqa_" + strings.Repeat("a", 43)
	preludes := make(chan string, 1)
	gateDial = func(_, _ string) (net.Conn, error) {
		proxy, node := net.Pipe()
		go func() {
			defer node.Close()
			reader := bufio.NewReader(node)
			line, _ := reader.ReadString('\n')
			preludes <- line
			payload := make([]byte, 4)
			if _, err := io.ReadFull(reader, payload); err == nil && string(payload) == "ping" {
				_, _ = io.WriteString(node, "pong")
			}
		}()
		return proxy, nil
	}
	t.Cleanup(func() {
		nftCommand = previousNFTCommand
		gateListen = previousGateListen
		gateDial = previousGateDial
	})

	command := controlCommand{
		CommandID: "fw_1234567890abcdef", Family: "v4", ClientIP: "127.0.0.1",
		ExternalPort: 51235, InternalPort: 51234, Status: "pending",
		ExpiresAt: time.Now().Add(2 * time.Second).Unix(),
	}
	if !validControlCommand(command, time.Now(), 51234) {
		t.Fatal("valid control command was rejected")
	}
	gate, err := openLeaseGate(command, &leaseFirewall{}, 2, token)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close(&leaseFirewall{})

	client := listener.connect(t, "127.0.0.1")
	if _, err := io.WriteString(client, "ping"); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "pong" {
		t.Fatalf("proxied response=%q", response)
	}
	_ = client.Close()
	if source, err := parseGatePrelude(<-preludes, token); err != nil || source != "127.0.0.1" {
		t.Fatalf("gate prelude source=%q err=%v", source, err)
	}

	unauthorized := listener.connect(t, "127.0.0.2")
	_ = unauthorized.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(unauthorized, "ping")
	if _, readErr := bufio.NewReader(unauthorized).ReadByte(); readErr == nil {
		t.Fatal("unauthorized source received data")
	}
	_ = unauthorized.Close()

	select {
	case <-listener.closed:
	case <-time.After(4 * time.Second):
		t.Fatal("lease gate still accepted connections after expiry")
	}
}

func TestControlCommandCannotProxyAnotherLocalPort(t *testing.T) {
	now := time.Now()
	command := controlCommand{
		CommandID: "fw_1234567890abcdef", Family: "v4", ClientIP: "8.8.8.8",
		ExternalPort: 51234, InternalPort: 51235, Status: "pending",
		ExpiresAt: now.Add(time.Minute).Unix(),
	}
	if validControlCommand(command, now, 51236) {
		t.Fatal("control command was allowed to proxy an unrelated local port")
	}
	command.InternalPort = 51234
	if validControlCommand(command, now, 51234) {
		t.Fatal("control command reused the internal listening port externally")
	}
}

func TestGateRetriesReadyAcknowledgementWithoutReopeningLease(t *testing.T) {
	path, config, _ := testConfig(t)
	previousNFTCommand := nftCommand
	previousGateListen := gateListen
	previousPoll := pollNodeControlRequest
	previousAck := ackNodeControlRequest
	previousInterval := gatePollInterval
	nftCommand = fakeNFTCommand
	listenCount := 0
	listener := newMemoryListener()
	gateListen = func(_, _ string) (net.Listener, error) {
		listenCount++
		return listener, nil
	}
	gatePollInterval = 5 * time.Millisecond
	command := controlCommand{
		CommandID: "fw_retryready123456", Family: "v4", ClientIP: "8.8.8.8",
		ExternalPort: 51235, InternalPort: config.Port, Status: "pending",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	pollNodeControlRequest = func(context.Context, Config) (controlResponse, error) {
		return controlResponse{Status: "ok", Commands: []controlCommand{command}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	acknowledgements := 0
	ackNodeControlRequest = func(_ context.Context, _ Config, values []controlAcknowledgement) error {
		if len(values) != 1 || values[0].CommandID != command.CommandID || values[0].Status != "ready" {
			t.Fatalf("unexpected acknowledgements: %+v", values)
		}
		acknowledgements++
		if acknowledgements == 1 {
			return errors.New("temporary acknowledgement failure")
		}
		cancel()
		return nil
	}
	t.Cleanup(func() {
		cancel()
		nftCommand = previousNFTCommand
		gateListen = previousGateListen
		pollNodeControlRequest = previousPoll
		ackNodeControlRequest = previousAck
		gatePollInterval = previousInterval
	})

	if err := runGate(ctx, path, io.Discard); err != nil {
		t.Fatal(err)
	}
	if acknowledgements != 2 {
		t.Fatalf("ready acknowledgement attempts=%d", acknowledgements)
	}
	if listenCount != 1 {
		t.Fatalf("lease listener opened %d times", listenCount)
	}
}
