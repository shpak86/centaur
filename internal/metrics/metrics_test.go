package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scrape collects the metrics text through Handler — just like Prometheus does.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	return rec.Body.String()
}

// TestHandlerExposesCountersAndRuntime checks that counters, the histogram and
// the Go runtime collectors make it into the output in the Prometheus text
// format.
func TestHandlerExposesCountersAndRuntime(t *testing.T) {
	m := New()
	m.RequestsTotal.WithLabelValues("/api/generate", "200").Inc()
	m.RequestDuration.WithLabelValues("/api/generate").Observe(0.1)
	m.TunnelRegistrations.Inc()

	body := scrape(t, m)
	for _, want := range []string{
		`centaur_gateway_requests_total{code="200",endpoint="/api/generate"} 1`,
		`centaur_gateway_request_duration_seconds_count{endpoint="/api/generate"} 1`,
		`centaur_gateway_tunnel_registrations_total 1`,
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output does not contain %q\n%s", want, body)
		}
	}
}

// TestRegisterIgnoresDuplicate: registering the same collector twice must not
// panic (otherwise a double initialization would take the gateway down).
func TestRegisterIgnoresDuplicate(t *testing.T) {
	m := New()
	m.Register(m.RequestsTotal)
}
