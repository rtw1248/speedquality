package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const firewallTable = "sq_node_leases"

const (
	gatePreludeMagic   = "SQG1 "
	gatePreludeContext = "speedquality-gate-v1\x00"
	gatePreludeLimit   = 192
)

type controlCommand struct {
	CommandID    string `json:"command_id"`
	Family       string `json:"family"`
	ClientIP     string `json:"client_ip"`
	ExternalPort int    `json:"external_port"`
	InternalPort int    `json:"internal_port"`
	Status       string `json:"status"`
	ExpiresAt    int64  `json:"expires_at"`
}

type controlResponse struct {
	Status     string           `json:"status"`
	ServerTime int64            `json:"server_time"`
	Commands   []controlCommand `json:"commands"`
}

type controlAcknowledgement struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
}

type gateStatus struct {
	Ready        bool   `json:"ready"`
	ActiveLeases int    `json:"active_leases"`
	UpdatedAt    int64  `json:"updated_at"`
	LastError    string `json:"last_error,omitempty"`
}

type leaseGate struct {
	command  controlCommand
	listener net.Listener
	cancel   context.CancelFunc
	timer    *time.Timer
	once     sync.Once
}

type sourceRestrictedListener struct {
	net.Listener
	clientIP  string
	semaphore chan struct{}
}

type leaseFirewall struct {
	mu sync.Mutex
}

var nftCommand = func(arguments ...string) *exec.Cmd {
	return exec.Command("nft", arguments...)
}

var gateListen = net.Listen
var gateDial = func(network, address string) (net.Conn, error) {
	return net.DialTimeout(network, address, 5*time.Second)
}

func gateStatusPath(configFile string) string {
	return filepath.Join(filepath.Dir(configFile), "gate-status.json")
}

func saveGateStatus(path string, status gateStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func currentGateStatus(configFile string, now time.Time) gateStatus {
	data, err := os.ReadFile(gateStatusPath(configFile))
	if err != nil || len(data) > 16*1024 {
		return gateStatus{}
	}
	var status gateStatus
	if json.Unmarshal(data, &status) != nil || status.UpdatedAt < now.Add(-20*time.Second).Unix() {
		return gateStatus{}
	}
	return status
}

func (firewall *leaseFirewall) apply(script string) error {
	firewall.mu.Lock()
	defer firewall.mu.Unlock()
	command := nftCommand("-f", "-")
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nftables 操作失败: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (firewall *leaseFirewall) initialize() error {
	_ = nftCommand("delete", "table", "inet", firewallTable).Run()
	return firewall.apply(fmt.Sprintf(`
add table inet %s
add set inet %s ports { type inet_service; flags timeout; }
add set inet %s leases4 { type ipv4_addr . inet_service; flags timeout; }
add set inet %s leases6 { type ipv6_addr . inet_service; flags timeout; }
add chain inet %s input { type filter hook input priority -10; policy accept; }
add rule inet %s input ip saddr . tcp dport @leases4 accept
add rule inet %s input ip6 saddr . tcp dport @leases6 accept
add rule inet %s input tcp dport @ports drop
`, firewallTable, firewallTable, firewallTable, firewallTable,
		firewallTable, firewallTable, firewallTable, firewallTable))
}

func (firewall *leaseFirewall) add(command controlCommand, ttl time.Duration) error {
	seconds := int(ttl.Round(time.Second) / time.Second)
	if seconds < 1 {
		return errors.New("防火墙租约已经过期")
	}
	set := "leases4"
	if command.Family == "v6" {
		set = "leases6"
	}
	clientIP := net.ParseIP(command.ClientIP)
	if clientIP == nil {
		return errors.New("防火墙租约来源地址无效")
	}
	script := fmt.Sprintf(
		"add element inet %s ports { %d timeout %ds }\nadd element inet %s %s { %s . %d timeout %ds }\n",
		firewallTable, command.ExternalPort, seconds, firewallTable, set,
		clientIP.String(), command.ExternalPort, seconds,
	)
	return firewall.apply(script)
}

func (firewall *leaseFirewall) remove(command controlCommand) {
	set := "leases4"
	if command.Family == "v6" {
		set = "leases6"
	}
	script := fmt.Sprintf(
		"delete element inet %s %s { %s . %d }\ndelete element inet %s ports { %d }\n",
		firewallTable, set, command.ClientIP, command.ExternalPort,
		firewallTable, command.ExternalPort,
	)
	_ = firewall.apply(script)
}

func cleanupLeaseFirewall() {
	_ = nftCommand("delete", "table", "inet", firewallTable).Run()
}

func validControlCommand(command controlCommand, now time.Time, internalPort int) bool {
	ip := net.ParseIP(command.ClientIP)
	familyMatches := command.Family == "v4" && ip != nil && ip.To4() != nil ||
		command.Family == "v6" && ip != nil && ip.To4() == nil
	return command.CommandID != "" && len(command.CommandID) <= 96 && familyMatches &&
		command.ExternalPort >= 50000 && command.ExternalPort <= 59999 &&
		command.InternalPort == internalPort && command.ExternalPort != command.InternalPort &&
		(command.Status == "pending" || command.Status == "ready" || command.Status == "revoked") &&
		command.ExpiresAt > now.Unix() && command.ExpiresAt <= now.Add(15*time.Minute).Unix()
}

func gateSignature(apiToken, clientIP string) string {
	ip := net.ParseIP(clientIP)
	if apiToken == "" || ip == nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(apiToken))
	_, _ = mac.Write([]byte(gatePreludeContext + ip.String()))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func gatePrelude(apiToken, clientIP string) string {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return ""
	}
	canonical := ip.String()
	signature := gateSignature(apiToken, canonical)
	if signature == "" {
		return ""
	}
	return gatePreludeMagic + canonical + " " + signature + "\r\n"
}

func parseGatePrelude(line, apiToken string) (string, error) {
	if len(line) > gatePreludeLimit || !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("invalid gate prelude")
	}
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), " ")
	if len(fields) != 3 || fields[0] != strings.TrimSpace(gatePreludeMagic) {
		return "", errors.New("invalid gate prelude")
	}
	ip := net.ParseIP(fields[1])
	if ip == nil {
		return "", errors.New("invalid gate source")
	}
	canonical := ip.String()
	expected := gateSignature(apiToken, canonical)
	if expected == "" || !hmac.Equal([]byte(fields[2]), []byte(expected)) {
		return "", errors.New("invalid gate proof")
	}
	return canonical, nil
}

type trustedProxyListener struct {
	net.Listener
	apiToken string
}

type trustedProxyConnection struct {
	net.Conn
	reader     *bufio.Reader
	apiToken   string
	once       sync.Once
	initialize error
	remote     net.Addr
}

func (listener *trustedProxyListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &trustedProxyConnection{
		Conn: connection, reader: bufio.NewReaderSize(connection, gatePreludeLimit),
		apiToken: listener.apiToken,
	}, nil
}

func (connection *trustedProxyConnection) initializeSource() {
	connection.once.Do(func() {
		host, _, err := net.SplitHostPort(connection.Conn.RemoteAddr().String())
		if err != nil {
			return
		}
		direct := net.ParseIP(strings.Trim(host, "[]"))
		if direct == nil || !direct.IsLoopback() {
			return
		}
		prefix, err := connection.reader.Peek(len(gatePreludeMagic))
		if err != nil || string(prefix) != gatePreludeMagic {
			return
		}
		lineBytes, err := connection.reader.ReadSlice('\n')
		if err != nil {
			connection.initialize = errors.New("incomplete gate prelude")
			return
		}
		clientIP, err := parseGatePrelude(string(lineBytes), connection.apiToken)
		if err != nil {
			connection.initialize = err
			return
		}
		connection.remote = &net.TCPAddr{IP: net.ParseIP(clientIP)}
	})
}

func (connection *trustedProxyConnection) Read(value []byte) (int, error) {
	connection.initializeSource()
	if connection.initialize != nil {
		_ = connection.Conn.Close()
		return 0, connection.initialize
	}
	return connection.reader.Read(value)
}

func (connection *trustedProxyConnection) RemoteAddr() net.Addr {
	connection.initializeSource()
	if connection.remote != nil {
		return connection.remote
	}
	return connection.Conn.RemoteAddr()
}

func (listener *sourceRestrictedListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		remoteHost, _, splitErr := net.SplitHostPort(connection.RemoteAddr().String())
		if splitErr != nil || !sameIP(remoteHost, listener.clientIP) || !tryAcquire(listener.semaphore) {
			_ = connection.Close()
			continue
		}
		return &limitedConnection{Conn: connection, release: func() { release(listener.semaphore) }}, nil
	}
}

func openLeaseGate(
	command controlCommand,
	firewall *leaseFirewall,
	maximumConnections int,
	apiToken string,
) (*leaseGate, error) {
	network := "tcp4"
	host := "0.0.0.0"
	if command.Family == "v6" {
		network = "tcp6"
		host = "::"
	}
	listener, err := gateListen(network, net.JoinHostPort(host, strconv.Itoa(command.ExternalPort)))
	if err != nil {
		return nil, err
	}
	ttl := time.Until(time.Unix(command.ExpiresAt, 0))
	if err := firewall.add(command, ttl); err != nil {
		_ = listener.Close()
		return nil, err
	}
	restricted := &sourceRestrictedListener{
		Listener: listener, clientIP: command.ClientIP,
		semaphore: make(chan struct{}, maximumConnections),
	}
	ctx, cancel := context.WithCancel(context.Background())
	gate := &leaseGate{command: command, listener: listener, cancel: cancel}
	gate.timer = time.NewTimer(ttl)
	go func() {
		select {
		case <-gate.timer.C:
			gate.close(firewall)
		case <-ctx.Done():
		}
	}()
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(command.InternalPort))
	go gate.serve(ctx, restricted, target, apiToken)
	return gate, nil
}

func (gate *leaseGate) serve(ctx context.Context, listener net.Listener, target, apiToken string) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			proxyLeaseConnection(
				ctx, connection, target, gate.command.ClientIP, apiToken,
				shortDiagnosticID(gate.command.CommandID),
			)
		}()
	}
}

func proxyLeaseConnection(
	ctx context.Context,
	incoming net.Conn,
	target, clientIP, apiToken, leaseID string,
) {
	defer incoming.Close()
	outgoing, err := gateDial("tcp4", target)
	if err != nil {
		nodeLog(ctx, slog.LevelError, "gate.proxy_dial_failed",
			"lease_id", leaseID,
			"error", safeLogError(err),
		)
		return
	}
	defer outgoing.Close()
	prelude := gatePrelude(apiToken, clientIP)
	if prelude == "" {
		return
	}
	_ = outgoing.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(outgoing, prelude); err != nil {
		return
	}
	_ = outgoing.SetWriteDeadline(time.Time{})

	completed := make(chan struct{}, 2)
	copyStream := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		if writer, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = writer.CloseWrite()
		}
		completed <- struct{}{}
	}
	go copyStream(outgoing, incoming)
	go copyStream(incoming, outgoing)
	for finished := 0; finished < 2; finished++ {
		select {
		case <-ctx.Done():
			_ = incoming.Close()
			_ = outgoing.Close()
			return
		case <-completed:
		}
	}
}

func (gate *leaseGate) close(firewall *leaseFirewall) {
	gate.once.Do(func() {
		if gate.timer != nil {
			gate.timer.Stop()
		}
		gate.cancel()
		_ = gate.listener.Close()
		firewall.remove(gate.command)
	})
}

func pollNodeControl(ctx context.Context, config Config) (controlResponse, error) {
	var response controlResponse
	err := platformJSON(ctx, config, "POST", "/api/nodes/control", config.APIToken, map[string]any{}, &response)
	return response, err
}

func acknowledgeNodeControl(ctx context.Context, config Config, values []controlAcknowledgement) error {
	if len(values) == 0 {
		return nil
	}
	return platformJSON(ctx, config, "POST", "/api/nodes/control/ack", config.APIToken,
		map[string]any{"acknowledgements": values}, nil)
}

var (
	gatePollInterval       = time.Second
	pollNodeControlRequest = pollNodeControl
	ackNodeControlRequest  = acknowledgeNodeControl
)

func runGate(ctx context.Context, path string, output io.Writer) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	if config.APIToken == "" {
		return errors.New("节点尚未注册")
	}
	firewall := &leaseFirewall{}
	if err := firewall.initialize(); err != nil {
		nodeLog(ctx, slog.LevelError, "gate.firewall_init_failed", "error", safeLogError(err))
		_ = saveGateStatus(gateStatusPath(path), gateStatus{
			UpdatedAt: time.Now().Unix(), LastError: safeLogText(err.Error()),
		})
		return err
	}
	defer cleanupLeaseFirewall()
	active := map[string]*leaseGate{}
	defer func() {
		for _, gate := range active {
			gate.close(firewall)
		}
		_ = saveGateStatus(gateStatusPath(path), gateStatus{UpdatedAt: time.Now().Unix()})
		nodeLog(ctx, slog.LevelInfo, "gate.stopped")
	}()
	fmt.Fprintln(output, "sq-node 临时租约网关已启动。")
	nodeLog(ctx, slog.LevelInfo, "gate.started", "node_id", shortNodeID(config.NodeID))
	_ = saveGateStatus(gateStatusPath(path), gateStatus{Ready: true, UpdatedAt: time.Now().Unix()})
	ticker := time.NewTicker(gatePollInterval)
	defer ticker.Stop()
	controlFailures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if ctx.Err() != nil {
				return nil
			}
		}
		config, err = loadConfig(path)
		if err != nil {
			continue
		}
		now := time.Now()
		for id, gate := range active {
			if gate.command.ExpiresAt <= now.Unix() {
				gate.close(firewall)
				delete(active, id)
				nodeLog(ctx, slog.LevelInfo, "gate.lease_closed",
					"lease_id", shortDiagnosticID(id),
					"reason", "expired",
				)
			}
		}
		status := currentGateStatus(path, time.Now())
		if config.AccessMode == "paused" {
			for id, gate := range active {
				gate.close(firewall)
				delete(active, id)
				nodeLog(ctx, slog.LevelInfo, "gate.lease_closed",
					"lease_id", shortDiagnosticID(id),
					"reason", "paused",
				)
			}
			_ = saveGateStatus(gateStatusPath(path), gateStatus{Ready: true, UpdatedAt: time.Now().Unix()})
			continue
		}
		control, pollErr := pollNodeControlRequest(ctx, config)
		if pollErr != nil {
			controlFailures++
			if controlFailures == 1 || controlFailures%5 == 0 {
				nodeLog(ctx, slog.LevelWarn, "gate.control_failed",
					"consecutive_failures", controlFailures,
					"error", safeLogError(pollErr),
				)
			}
			status.Ready = false
			status.LastError = safeLogText(pollErr.Error())
			status.ActiveLeases = len(active)
			status.UpdatedAt = time.Now().Unix()
			_ = saveGateStatus(gateStatusPath(path), status)
			continue
		}
		if controlFailures > 0 {
			nodeLog(ctx, slog.LevelInfo, "gate.control_recovered",
				"previous_failures", controlFailures,
			)
			controlFailures = 0
		}
		seen := map[string]bool{}
		acks := make([]controlAcknowledgement, 0)
		now = time.Now()
		for _, command := range control.Commands {
			if !validControlCommand(command, now, config.Port) {
				if command.Status == "pending" && command.CommandID != "" && len(command.CommandID) <= 96 {
					acks = append(acks, controlAcknowledgement{CommandID: command.CommandID, Status: "failed"})
				}
				continue
			}
			seen[command.CommandID] = true
			if command.Status == "revoked" {
				if gate := active[command.CommandID]; gate != nil {
					gate.close(firewall)
					delete(active, command.CommandID)
					nodeLog(ctx, slog.LevelInfo, "gate.lease_closed",
						"lease_id", shortDiagnosticID(command.CommandID),
						"reason", "revoked",
					)
				}
				continue
			}
			if active[command.CommandID] != nil {
				if command.Status == "pending" {
					acks = append(acks, controlAcknowledgement{
						CommandID: command.CommandID,
						Status:    "ready",
					})
				}
				continue
			}
			gate, openErr := openLeaseGate(
				command, firewall, maximum(4, config.MaxConcurrency*3), config.APIToken,
			)
			if openErr != nil {
				nodeLog(ctx, slog.LevelError, "gate.lease_open_failed",
					"lease_id", shortDiagnosticID(command.CommandID),
					"error", safeLogError(openErr),
				)
				acks = append(acks, controlAcknowledgement{CommandID: command.CommandID, Status: "failed"})
				continue
			}
			active[command.CommandID] = gate
			nodeLog(ctx, slog.LevelInfo, "gate.lease_opened",
				"lease_id", shortDiagnosticID(command.CommandID),
				"family", command.Family,
			)
			if command.Status == "pending" {
				acks = append(acks, controlAcknowledgement{CommandID: command.CommandID, Status: "ready"})
			}
		}
		for id, gate := range active {
			if !seen[id] || gate.command.ExpiresAt <= now.Unix() {
				gate.close(firewall)
				delete(active, id)
				reason := "withdrawn"
				if gate.command.ExpiresAt <= now.Unix() {
					reason = "expired"
				}
				nodeLog(ctx, slog.LevelInfo, "gate.lease_closed",
					"lease_id", shortDiagnosticID(id),
					"reason", reason,
				)
			}
		}
		if ackErr := ackNodeControlRequest(ctx, config, acks); ackErr != nil {
			nodeLog(ctx, slog.LevelWarn, "gate.ack_failed", "error", safeLogError(ackErr))
		}
		_ = saveGateStatus(gateStatusPath(path), gateStatus{
			Ready: true, ActiveLeases: len(active), UpdatedAt: now.Unix(),
		})
	}
}
