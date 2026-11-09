package tunnel

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testToken is a 16-byte token, like rand.Text()[:16] in the gateway registry:
// the tunnel server reads exactly 16 handshake bytes.
const testToken = "0123456789abcdef"

// testTimeout limits how long client tests wait for events.
const testTimeout = 5 * time.Second

// newRegisterServer starts a fake pool HTTP API: it answers a tunnel
// registration with a token and checks the ApiKey to catch a regression in the
// header.
func newRegisterServer(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tunnels/register" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("ApiKey") != apiKey {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(registerTunnelResponse{Token: testToken}); err != nil {
			t.Errorf("encode register response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTunnelPool starts a fake pool TCP server: it accepts connections, reads the
// 16-byte handshake and passes the connection to handler.
func newTunnelPool(t *testing.T, handler func(conn net.Conn)) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tunnel pool: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				handshake := make([]byte, len(testToken))
				if _, err := io.ReadFull(conn, handshake); err != nil {
					conn.Close()
					return
				}
				handler(conn)
			}(conn)
		}
	}()

	return listener.Addr().String()
}

// startClient runs Serve in a goroutine and guarantees the client is stopped when
// the test finishes.
func startClient(t *testing.T, client *Client) {
	t.Helper()

	served := make(chan struct{})
	go func() {
		defer close(served)
		client.Serve()
	}()

	t.Cleanup(func() {
		client.Close()
		select {
		case <-served:
		case <-time.After(testTimeout):
			t.Error("Serve() did not stop after Close()")
		}
	})
}

// TestServeHoldsConfiguredTunnels checks that the connector holds exactly as many
// tunnels as pool.tunnels specifies: each worker registers its own tunnel and
// keeps it open waiting for a request.
func TestServeHoldsConfiguredTunnels(t *testing.T) {
	const tunnels = 3
	const apiKey = "key"

	register := newRegisterServer(t, apiKey)
	opened := make(chan net.Conn, tunnels)

	held := make([]net.Conn, 0, tunnels)
	tunnelAddr := newTunnelPool(t, func(conn net.Conn) {
		// The tunnel stays open: the connector must keep it ready until the
		// gateway hands it a request.
		opened <- conn
	})

	client := NewClient(register.URL, tunnelAddr, "http://127.0.0.1:1", apiKey, []string{"m"}, tunnels)
	startClient(t, client)

	seen := make(map[net.Conn]struct{}, tunnels)
	for len(seen) < tunnels {
		select {
		case conn := <-opened:
			if _, dup := seen[conn]; dup {
				t.Fatal("same tunnel connection opened twice")
			}
			seen[conn] = struct{}{}
			held = append(held, conn)
		case <-time.After(testTimeout):
			t.Fatalf("%d tunnels opened concurrently, want %d", len(seen), tunnels)
		}
	}

	t.Cleanup(func() {
		for _, conn := range held {
			conn.Close()
		}
	})
}

// TestServeProxiesTunnelRequestToOllama checks the end-to-end path of a single
// tunnel: the client registers, brings up a connection and proxies the request
// arriving over it to Ollama, returning the response back.
func TestServeProxiesTunnelRequestToOllama(t *testing.T) {
	const apiKey = "key"

	targetPath := make(chan string, 1)
	targetBody := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read ollama request body: %v", err)
		}
		select {
		case targetPath <- r.URL.Path:
		default:
		}
		select {
		case targetBody <- string(body):
		default:
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("pong")); err != nil {
			t.Errorf("write ollama response: %v", err)
		}
	}))
	t.Cleanup(target.Close)

	register := newRegisterServer(t, apiKey)

	type tunnelResult struct {
		status int
		body   string
	}
	result := make(chan tunnelResult, 1)

	// newTunnelPool plays the gateway: it writes one request into the tunnel and
	// reads the response, just like tunnelTransport.RoundTrip does.
	tunnelAddr := newTunnelPool(t, func(conn net.Conn) {
		req, err := http.NewRequest(http.MethodPost, "http://tunnel/api/generate", strings.NewReader(`{"model":"m"}`))
		if err != nil {
			t.Errorf("build tunnel request: %v", err)
			return
		}
		req.Close = true
		if err := req.Write(conn); err != nil {
			t.Errorf("write request to tunnel: %v", err)
			return
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), req)
		if err != nil {
			t.Errorf("read response from tunnel: %v", err)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Errorf("read tunnel response body: %v", err)
			return
		}
		select {
		case result <- tunnelResult{status: resp.StatusCode, body: string(body)}:
		default:
		}
	})

	client := NewClient(register.URL, tunnelAddr, target.URL, apiKey, []string{"m"}, 1)
	startClient(t, client)

	select {
	case got := <-result:
		if got.status != http.StatusOK {
			t.Fatalf("tunnel response status = %d, want %d", got.status, http.StatusOK)
		}
		if got.body != "pong" {
			t.Fatalf("tunnel response body = %q, want %q", got.body, "pong")
		}
	case <-time.After(testTimeout):
		t.Fatal("no request was proxied through the tunnel")
	}

	select {
	case path := <-targetPath:
		if path != "/api/generate" {
			t.Fatalf("ollama path = %q, want %q", path, "/api/generate")
		}
	case <-time.After(testTimeout):
		t.Fatal("ollama did not receive the request")
	}

	select {
	case body := <-targetBody:
		if body != `{"model":"m"}` {
			t.Fatalf("ollama body = %q, want %q", body, `{"model":"m"}`)
		}
	case <-time.After(testTimeout):
		t.Fatal("ollama body was not received")
	}
}
