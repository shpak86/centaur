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

// testToken — токен длиной 16 байт, как rand.Text()[:16] в реестре gateway:
// сервер туннелей читает ровно 16 байт handshake.
const testToken = "0123456789abcdef"

// testTimeout ограничивает ожидание событий в тестах клиента.
const testTimeout = 5 * time.Second

// newRegisterServer поднимает фейковый HTTP API пула: отвечает на регистрацию
// туннеля токеном и проверяет ApiKey, чтобы поймать регресс в заголовке.
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

// newTunnelPool поднимает фейковый TCP-сервер пула: принимает соединения,
// читает 16-байтовый handshake и передаёт соединение в handler.
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

// startClient запускает Serve в горутине и гарантирует остановку клиента по
// завершении теста.
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

// TestServeHoldsConfiguredTunnels проверяет, что коннектор держит сразу
// столько туннелей, сколько задано в pool.tunnels: каждый воркер регистрирует
// свой туннель и держит его открытым в ожидании запроса.
func TestServeHoldsConfiguredTunnels(t *testing.T) {
	const tunnels = 3
	const apiKey = "key"

	register := newRegisterServer(t, apiKey)
	opened := make(chan net.Conn, tunnels)

	held := make([]net.Conn, 0, tunnels)
	tunnelAddr := newTunnelPool(t, func(conn net.Conn) {
		// Туннель остаётся открытым: коннектор обязан держать его ready,
		// пока gateway не отдаст ему запрос.
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

// TestServeProxiesTunnelRequestToOllama проверяет сквозной путь одного туннеля:
// клиент регистрируется, поднимает соединение и проксирует пришедший по нему
// запрос в Ollama, возвращая ответ обратно.
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

	// newTunnelPool играет роль gateway: пишет в туннель один запрос и читает
	// ответ, как это делает tunnelTransport.RoundTrip.
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
