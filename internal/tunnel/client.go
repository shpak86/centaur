package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

const (
	// retryDelay is the pause before retrying to bring up a tunnel.
	retryDelay = time.Second
	// registerTimeout limits the registration request to the tunnel pool.
	registerTimeout = 10 * time.Second
	// dialTimeout limits connecting to the tunnel server.
	dialTimeout = 5 * time.Second
)

// Client is the connector side: it holds the configured number of tunnels open
// to the pool and proxies the requests arriving over them to the local Ollama.
type Client struct {
	ctx        context.Context
	cancel     context.CancelFunc
	apiAddr    string
	tunnelAddr string
	targetAddr string
	apiKey     string
	models     []string
	transport  *http.Transport
	proxy      *httputil.ReverseProxy
	tunnels    int
}

// NewClient creates a connector client. apiAddr is the pool registration API,
// tunnelAddr is the pool's TCP server, targetAddr is the local Ollama to proxy
// to, and tunnels is the number of tunnels to hold open.
func NewClient(apiAddr, tunnelAddr, targetAddr, apiKey string, models []string, tunnels int) *Client {
	client := Client{
		apiAddr:    apiAddr,
		tunnelAddr: tunnelAddr,
		targetAddr: targetAddr,
		apiKey:     apiKey,
		models:     models,
		tunnels:    tunnels,
		transport: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	client.ctx, client.cancel = context.WithCancel(context.Background())
	client.proxy = newOllamaProxy(targetAddr, client.transport)
	return &client
}

// newOllamaProxy builds the proxy that forwards requests arriving from a tunnel
// to Ollama. The proxy and transport are shared by all tunnels: otherwise each
// tunnel would create its own connection to Ollama and leak file descriptors.
func newOllamaProxy(targetAddr string, transport http.RoundTripper) *httputil.ReverseProxy {
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("Invalid ollama target address", "address", targetAddr, "error", err)
		return nil
	}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},

		// For Ollama's NDJSON streaming: flush every chunk immediately.
		FlushInterval: -1,

		Transport: transport,

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("Ollama proxy error", "error", err)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
}

// Serve runs the connector until Close: it holds tunnels open and serves the
// requests that arrive over them.
func (c *Client) Serve() {
	var wg sync.WaitGroup
	for range c.tunnels { // range-over-int (go.mod: 1.27)
		wg.Add(1)
		go func() { defer wg.Done(); c.serveLoop() }()
	}
	wg.Wait()
}

// serveLoop keeps a single tunnel alive, re-establishing it as needed.
func (c *Client) serveLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			c.serveTunnel()
		}
	}
}

func (c *Client) serveTunnel() error {
	regUrl, err := url.JoinPath(c.apiAddr, "api", "v1", "tunnels", "register")
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	data, err := json.Marshal(registerTunnelRequest{Models: c.models})
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	// The context is tied to the client's lifetime: Close interrupts the
	// registration, and the timeout prevents hanging on an unreachable pool API.
	ctx, cancel := context.WithTimeout(c.ctx, registerTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, regUrl, bytes.NewReader(data))
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ApiKey", c.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("register tunnel: unexpected status %q", resp.Status)
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	var reg registerTunnelResponse
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	if reg.Token == "" {
		err := errors.New("register tunnel: empty token")
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	conn, err := net.DialTimeout("tcp", c.tunnelAddr, dialTimeout)
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(reg.Token)); err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	// Handshake complete: the gateway knows about the tunnel and can hand it to
	// a client.
	return c.startOllamaBridge(conn)
}

// pause waits for d or for the client to finish via Close, so that shutdown
// does not wait for retryDelay.
func (c *Client) pause(d time.Duration) {
	select {
	case <-c.ctx.Done():
	case <-time.After(d):
	}
}

// Close stops the client: Serve stops bringing up new tunnels, and idle
// connections to Ollama in the transport pool are closed.
func (c *Client) Close() {
	c.cancel()
	c.transport.CloseIdleConnections()
}

type singleConnListener struct {
	ch   chan net.Conn
	once sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	ch := make(chan net.Conn, 1)
	ch <- conn
	return &singleConnListener{ch: ch}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.ch) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

// trackedConn signals when http.Server has closed the tunnel connection.
// This is the only reliable sign of the tunnel's end: singleConnListener yields
// exactly one connection, so after serving it Serve stays in Accept forever and
// never returns on its own.
type trackedConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func newTrackedConn(conn net.Conn) *trackedConn {
	return &trackedConn{Conn: conn, done: make(chan struct{})}
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

func (c *Client) startOllamaBridge(conn net.Conn) error {
	if c.proxy == nil {
		return errors.New("ollama proxy is not configured")
	}

	// ReadHeaderTimeout is not set: a tunnel is single-use and may sit idle for
	// as long as it takes for the gateway to hand it a request (or close the
	// connection). A header deadline would kill such a tunnel, leaving a dead
	// entry in the gateway registry — the first request after a pause would then
	// get a 502.
	server := &http.Server{
		Handler: c.proxy,
		// ReadTimeout and WriteTimeout are not set either:
		// an Ollama generation can take a long time.
	}

	// Serve handles the tunnel's single connection, so it runs in a goroutine
	// while we wait for the tunnel's end via trackedConn.done (or Close).
	tracked := newTrackedConn(conn)
	listener := newSingleConnListener(tracked)

	var serveErr error
	served := make(chan struct{})
	go func() {
		serveErr = server.Serve(listener)
		close(served)
	}()

	select {
	case <-tracked.done: // the gateway finished or dropped the connection
		slog.Info("tunnel connection closed")
	case <-served: // Serve returned on its own
		slog.Info("ollama bridge stopped")
	case <-c.ctx.Done(): // the connector is being stopped
		slog.Info("closing ollama bridge")
	}

	// Close the server: it unblocks Accept and closes the tunnel connection.
	if err := server.Close(); err != nil {
		slog.Error("Close ollama bridge", "error", err)
	}
	<-served // wait for Serve to return so serveErr is not lost

	if serveErr != nil &&
		!errors.Is(serveErr, http.ErrServerClosed) &&
		!errors.Is(serveErr, net.ErrClosed) {
		slog.Error("Serve ollama bridge", "error", serveErr)
	}

	return nil
}
