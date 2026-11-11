package tunnel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shpak86/centaur/internal/metrics"
)

// TestTunnelStateCollectorReportsPendingAndReady: the centaur_gateway_tunnels
// gauge counts tunnels by state straight from the registry (pending — waiting for
// the handshake, ready — ready to accept a request).
func TestTunnelStateCollectorReportsPendingAndReady(t *testing.T) {
	m := metrics.New()
	r := NewRegistry(m)

	// pending: registered, but no connection bound yet.
	r.Register([]string{"llama3:8b"})
	// ready: registered and bound to a connection.
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
