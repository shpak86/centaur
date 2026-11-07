package tunnel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
