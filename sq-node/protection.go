package main

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const rateWindow = time.Minute

type requestBucket struct {
	window int64
	count  int
}

type requestProtector struct {
	mu         sync.Mutex
	buckets    map[string]requestBucket
	all        chan struct{}
	activation chan struct{}
	transfer   chan struct{}
	other      chan struct{}
}

func newRequestProtector(config Config) *requestProtector {
	allLimit := maximum(32, config.MaxConcurrency*12)
	return &requestProtector{
		buckets:    make(map[string]requestBucket),
		all:        make(chan struct{}, allLimit),
		activation: make(chan struct{}, maximum(4, config.MaxConcurrency*2)),
		transfer:   make(chan struct{}, maximum(4, config.MaxConcurrency*3)),
		other:      make(chan struct{}, 16),
	}
}

func (protector *requestProtector) allow(key string, limit int, now time.Time) bool {
	window := now.Unix() / int64(rateWindow/time.Second)
	protector.mu.Lock()
	defer protector.mu.Unlock()
	_, exists := protector.buckets[key]
	if !exists && len(protector.buckets) >= 4096 {
		for bucketKey, bucket := range protector.buckets {
			if bucket.window < window {
				delete(protector.buckets, bucketKey)
			}
		}
		if len(protector.buckets) >= 4096 {
			return false
		}
	}
	bucket := protector.buckets[key]
	if bucket.window != window {
		bucket = requestBucket{window: window}
	}
	if bucket.count >= limit {
		protector.buckets[key] = bucket
		return false
	}
	bucket.count++
	protector.buckets[key] = bucket
	return true
}

func (protector *requestProtector) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		remote := requestIP(request)
		limit := 120
		class := protector.other
		switch path {
		case "/activate":
			limit = 20
			class = protector.activation
		case "/download", "/upload", "/release":
			limit = 180
			class = protector.transfer
		case "/healthz", ownershipPath:
			limit = 30
		}
		if !protector.allow(remote+"|"+path, limit, time.Now()) {
			response.Header().Set("Retry-After", "60")
			http.Error(response, "request rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		if !tryAcquire(protector.all) {
			response.Header().Set("Retry-After", "5")
			http.Error(response, "node request capacity is full", http.StatusServiceUnavailable)
			return
		}
		defer release(protector.all)
		if !tryAcquire(class) {
			response.Header().Set("Retry-After", "5")
			http.Error(response, "node endpoint capacity is full", http.StatusTooManyRequests)
			return
		}
		defer release(class)
		next.ServeHTTP(response, request)
	})
}

func tryAcquire(semaphore chan struct{}) bool {
	select {
	case semaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

func release(semaphore chan struct{}) {
	<-semaphore
}

func maximum(left, right int) int {
	if left > right {
		return left
	}
	return right
}

type limitedListener struct {
	net.Listener
	semaphore chan struct{}
}

type limitedConnection struct {
	net.Conn
	once    sync.Once
	release func()
}

func (listener *limitedListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if !tryAcquire(listener.semaphore) {
		_ = connection.Close()
		return nil, temporaryCapacityError{}
	}
	return &limitedConnection{
		Conn: connection,
		release: func() {
			release(listener.semaphore)
		},
	}, nil
}

func (connection *limitedConnection) Close() error {
	err := connection.Conn.Close()
	connection.once.Do(connection.release)
	return err
}

type temporaryCapacityError struct{}

func (temporaryCapacityError) Error() string   { return "connection capacity is full" }
func (temporaryCapacityError) Timeout() bool   { return false }
func (temporaryCapacityError) Temporary() bool { return true }

func limitConnections(listener net.Listener, maximumConnections int) net.Listener {
	return &limitedListener{
		Listener:  listener,
		semaphore: make(chan struct{}, maximumConnections),
	}
}
