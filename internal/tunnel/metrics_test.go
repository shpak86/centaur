package tunnel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shpak86/centaur/internal/metrics"
)

// TestTunnelStateCollectorReportsPendingAndReady: gauge centaur_gateway_tunnels
// считает туннели по состоянию прямо из реестра (pending — ждём handshake,
// ready — готов принять запрос).
func TestTunnelStateCollectorReportsPendingAndReady(t *testing.T) {
	m := metrics.New()
	r := NewRegistry(m)

	// pending: зарегистрирован, но соединение ещё не привязано.
	r.Register([]string{"llama3:8b"})
	// ready: зарегистрирован и привязан к соединению.
	registerBound(t, r, "llama3:8b")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		`centaur_gateway_tunnels{state="pending"} 1`,
		`centaur_gateway_tunnels{state="ready"} 1`,
		`centaur_gateway_tunnel_registrations_total 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output does not contain %q\n%s", want, body)
		}
	}
}
