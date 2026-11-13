package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestService assembles a Service without starting the servers: NewService
// only constructs objects, which is enough to test Handle.
func newTestService() *Service {
	return NewService("127.0.0.1:0", "127.0.0.1:0", nil)
}

func TestHandleRootHealth(t *testing.T) {
	service := newTestService()

	rec := httptest.NewRecorder()
	service.Handle(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "Centaur is running" {
		t.Fatalf("body = %q, want %q", got, "Centaur is running")
	}
}

// TestHandleRejectsNonInferenceEndpoints checks that the gateway does not proxy
// Ollama's info/admin endpoints: there is no matching tunnel, so the request must
// not reach the proxy or occupy a tunnel.
func TestHandleRejectsNonInferenceEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
	}{
		{"version", http.MethodGet, "/api/version"},
		{"tags", http.MethodGet, "/api/tags"},
		{"ps", http.MethodGet, "/api/ps"},
		{"show", http.MethodPost, "/api/show"},
		{"pull", http.MethodPost, "/api/pull"},
		{"push", http.MethodPost, "/api/push"},
		{"create", http.MethodPost, "/api/create"},
		{"copy", http.MethodPost, "/api/copy"},
		{"delete", http.MethodDelete, "/api/delete"},
		{"v1 models", http.MethodGet, "/v1/models"},
		{"v1 model", http.MethodGet, "/v1/models/llama3:8b"},
		{"unknown", http.MethodGet, "/unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newTestService()

			rec := httptest.NewRecorder()
			service.Handle(rec, httptest.NewRequest(tc.method, tc.target, nil))

			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
			}
		})
	}
}

// TestHandleInferenceWithoutTunnel checks that inference endpoints still go to
// the proxy: with no free tunnel we get a 503, not a 501.
func TestHandleInferenceWithoutTunnel(t *testing.T) {
	service := newTestService()

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"llama3:8b","prompt":"hi"}`)
	service.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/generate", body))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestHandleRecordsMetrics checks that Handle records requests in the gateway
// metrics (endpoint + HTTP status) and that /metrics serves them in the
// Prometheus format.
func TestHandleRecordsMetrics(t *testing.T) {
	service := newTestService()

	// health "/" → 200.
	service.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	// inference with no free tunnel → 503 + the miss is counted.
	service.Handle(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/generate", strings.NewReader(`{"model":"llama3:8b","prompt":"hi"}`)))
	// a non-inference endpoint → 501 and collapses into endpoint="other".
	service.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tags", nil))

	rec := httptest.NewRecorder()
	service.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`centaur_gateway_requests_total{code="200",endpoint="/"} 1`,
		`centaur_gateway_requests_total{code="503",endpoint="/api/generate"} 1`,
		`centaur_gateway_requests_total{code="501",endpoint="other"} 1`,
		`centaur_gateway_tunnel_acquire_misses_total 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output does not contain %q\n%s", want, body)
		}
	}
}

// TestHandleInferenceWithDeadTunnel: a dead tunnel never reaches the client.
// Previously such a request went into a closed connection and the client got a
// 502 "tunnel error"; now the tunnel is dropped from the pool and the response is
// a 503, after which the client retries (by then the connector registers new
// tunnels).
func TestHandleInferenceWithDeadTunnel(t *testing.T) {
	service := newTestService()
	registerDeadBound(t, service.registry, "llama3:8b")

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"llama3:8b","prompt":"hi"}`)
	service.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/generate", body))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Body.String(); !strings.Contains(got, "no free tunnel") {
		t.Fatalf("body = %q, want contain %q", got, "no free tunnel")
	}
}

// TestRoundTripRoutesLargeBody: a body well over the old 1 MiB parse limit is
// still routed by model and reaches the tunnel intact. Before the limit was
// removed such a request got a 503 "no free tunnel": getModel returned an empty
// model and Acquire("") found no tunnel for it.
func TestRoundTripRoutesLargeBody(t *testing.T) {
	const model = "llama3:8b"
	body := `{"images":["` + strings.Repeat("a", 8<<20) + `"],"model":"` + model + `"}`

	registry := NewRegistry(nil)
	id := registry.Register([]string{model})
	conn, clientConn := newTCPConn(t)
	if err := registry.Bind(id, conn); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}

	// The connector side: read the request the gateway writes into the tunnel,
	// check that the body arrived in full, and answer with a short response.
	connectorErr := make(chan error, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(clientConn))
		if err != nil {
			connectorErr <- fmt.Errorf("read request: %w", err)
			return
		}
		got, err := io.ReadAll(req.Body)
		if err != nil {
			connectorErr <- fmt.Errorf("read request body: %w", err)
			return
		}
		if len(got) != len(body) || string(got) != body {
			connectorErr <- fmt.Errorf("tunnel body length = %d, want %d", len(got), len(body))
			return
		}
		if _, err := io.WriteString(clientConn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); err != nil {
			connectorErr <- fmt.Errorf("write response: %w", err)
			return
		}
		connectorErr <- nil
	}()

	transport := &tunnelTransport{registry: registry}
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if err := <-connectorErr; err != nil {
		t.Fatal(err)
	}
}

// TestSweepLoopDropsStaleTunnels: the background sweep drops the tunnel of a
// killed connector on its own and terminates when the context is cancelled (which
// is what Stop does).
func TestSweepLoopDropsStaleTunnels(t *testing.T) {
	service := newTestService()
	// The period is shortened: the test should not wait for the real sweepInterval.
	service.sweepEvery = 10 * time.Millisecond
	registerDeadBound(t, service.registry, "llama3:8b")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		service.sweepLoop(ctx)
	}()

	waitEmptyRegistry(t, service.registry, time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sweepLoop did not stop on context cancel")
	}
}
