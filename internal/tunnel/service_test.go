package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestService собирает Service без запуска серверов: NewService только
// конструирует объекты, поэтому для проверки Handle этого достаточно.
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

// TestHandleRejectsNonInferenceEndpoints проверяет, что инфо/админ-ручки Ollama
// gateway не проксирует: подходящего туннеля нет, поэтому запрос не должен
// доходить до прокси и не занимать туннель.
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

// TestHandleInferenceWithoutTunnel проверяет, что инференс-ручки по-прежнему
// идут в прокси: без свободного туннеля получаем 503, а не 501.
func TestHandleInferenceWithoutTunnel(t *testing.T) {
	service := newTestService()

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"llama3:8b","prompt":"hi"}`)
	service.Handle(rec, httptest.NewRequest(http.MethodPost, "/api/generate", body))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestHandleRecordsMetrics проверяет, что Handle пишет запросы в метрики
// gateway (эндпоинт + HTTP-статус), а /metrics отдаёт их в формате Prometheus.
func TestHandleRecordsMetrics(t *testing.T) {
	service := newTestService()

	// health «/» → 200.
	service.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	// инференс без свободного туннеля → 503 + учёт промаха.
	service.Handle(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/generate", strings.NewReader(`{"model":"llama3:8b","prompt":"hi"}`)))
	// не-инференс-ручка → 501 и схлопывается в endpoint="other".
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

// TestHandleInferenceWithDeadTunnel: мёртвый туннель не доходит до клиента.
// Раньше такой запрос уходил в закрытое соединение и клиент получал 502
// «tunnel error»; теперь туннель выбрасывается из пула, а ответ — 503, после
// которого клиент повторяет запрос (коннектор к этому моменту регистрирует
// новые туннели).
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

// TestSweepLoopDropsStaleTunnels: фоновая вычистка сама убирает туннель убитого
// коннектора и завершается по отмене контекста (это же делает Stop).
func TestSweepLoopDropsStaleTunnels(t *testing.T) {
	service := newTestService()
	// Период сокращён: тест не должен ждать настоящий sweepInterval.
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
