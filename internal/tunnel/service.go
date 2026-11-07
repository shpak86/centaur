package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// requestWriteTimeout ограничивает запись запроса в туннель: если коннектор
// не читает (мёртвый туннель), не ждём вечно. На чтение ответа дедлайна нет,
// чтобы не рвать длинные стриминговые ответы Ollama.
const requestWriteTimeout = 30 * time.Second

// pastDeadline — дедлайн в прошлом: так разблокируется заблокированное чтение.
var pastDeadline = time.Unix(1, 0)

// errNoTunnel возвращается, когда в пуле нет свободных туннелей.
var errNoTunnel = errors.New("no free tunnel")

type Service struct {
	apiServer    *ApiServer
	tunnelServer *TunnelServer
	registry     *Registry
	tokenManager *TokenManager
	proxy        *httputil.ReverseProxy
}

func NewService(apiAddr, tunnelAddr string, apiKeys []string) *Service {
	httpOps := httpServerOps{
		Address:           apiAddr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	tunnelOps := tunnelServerOps{
		Address:           tunnelAddr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	service := Service{}
	service.tokenManager = NewTokenManager()
	service.registry = NewRegistry()
	authManager := NewAuthManager(apiKeys)
	service.apiServer = NewApiServer(httpOps, service.tokenManager, authManager)
	service.tunnelServer = NewTunnelServer(tunnelOps, service.tokenManager, service.registry)
	service.proxy = newTunnelProxy(service.registry)
	return &service
}

// newTunnelProxy собирает обратный прокси, который отправляет запрос в туннель.
// httputil.ReverseProxy сам разбирает входящий запрос и сериализует его заново
// (тело при этом стримится), поэтому не нужны ни hijack клиентского
// соединения, ни ручной разбор HTTP.
func newTunnelProxy(registry *Registry) *httputil.ReverseProxy {
	// Адрес фиктивный: транспорт всё равно подключается к сокету туннеля,
	// но ReverseProxy требует абсолютный URL.
	target := &url.URL{Scheme: "http", Host: "tunnel"}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
		},

		// Для NDJSON-стриминга Ollama: отправлять каждый кусок сразу.
		FlushInterval: -1,

		Transport: &tunnelTransport{registry: registry},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, errNoTunnel) {
				slog.Debug("No free tunnel for request", "path", r.URL.Path)
				http.Error(w, "no free tunnel", http.StatusServiceUnavailable)
				return
			}
			slog.Error("Proxy request", "error", err)
			http.Error(w, "tunnel error", http.StatusBadGateway)
		},
	}
}

// tunnelTransport отправляет каждый запрос в отдельный туннель.
// Туннели одноразовые: после ответа соединение закрывается, а коннектор
// регистрирует новый туннель — так переподключение не зависит от того,
// что происходит с клиентским соединением.
type tunnelTransport struct {
	registry *Registry
}

func (t *tunnelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Туннеля может не быть несколько миллисекунд, пока коннектор
	// переподключается, — коротко ждём вместо мгновенного 503.
	tunnel, exists := t.registry.Acquire(req.Context())
	if !exists {
		return nil, errNoTunnel
	}

	// Клиент мог отвалиться, пока мы ждём ответ: закрываем туннель,
	// чтобы разблокировать чтение и не терять соединение.
	stopWatch := context.AfterFunc(req.Context(), func() {
		tunnel.Conn.SetReadDeadline(pastDeadline)
	})
	defer stopWatch()

	// Просим коннектор закрыть туннель после ответа (Connection: close):
	// туннель остаётся одноразовым и не висит в ожидании следующего запроса.
	req.Close = true

	tunnel.Conn.SetWriteDeadline(time.Now().Add(requestWriteTimeout))
	err := req.Write(tunnel.Conn)
	tunnel.Conn.SetWriteDeadline(time.Time{})
	if err != nil {
		t.release(tunnel)
		return nil, fmt.Errorf("write request to tunnel: %w", err)
	}
	slog.Debug("tunnel request sent", "method", req.Method, "path", req.URL.Path)

	resp, err := http.ReadResponse(bufio.NewReader(tunnel.Conn), req)
	if err != nil {
		t.release(tunnel)
		return nil, fmt.Errorf("read response from tunnel: %w", err)
	}
	slog.Debug("tunnel response received", "status", resp.Status)

	// Туннель освобождаем вместе с телом ответа: к этому моменту оно прочитано.
	resp.Body = &tunnelBody{ReadCloser: resp.Body, tunnel: tunnel, registry: t.registry}

	return resp, nil
}

func (t *tunnelTransport) release(tunnel *Tunnel) {
	t.registry.Delete(tunnel)
	tunnel.Close()
}

// tunnelBody закрывает одноразовый туннель, когда тело ответа дочитано.
type tunnelBody struct {
	io.ReadCloser
	once     sync.Once
	tunnel   *Tunnel
	registry *Registry
}

func (b *tunnelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		b.registry.Delete(b.tunnel)
		b.tunnel.Close()
	})
	return err
}

func (s *Service) ListenAndServe() error {
	go s.tokenManager.Serve()
	wg := sync.WaitGroup{}
	wg.Go(func() {
		err := s.apiServer.ListenAndServe()
		if err != nil {
			slog.Error("Tunnel API server", "error", err)
		}
	})
	wg.Go(func() {
		err := s.tunnelServer.ListenAndServe()
		if err != nil {
			slog.Error("Tunnel server", "error", err)
		}
	})
	wg.Wait()
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	errs := make([]error, 0, 2)
	if err := s.apiServer.Stop(ctx); err != nil {
		errs = append(errs, fmt.Errorf("stop api server: %w", err))
	}
	if err := s.tunnelServer.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("stop tunnel server: %w", err))
	}
	s.tokenManager.Close()
	return errors.Join(errs...)
}

// Handle проксирует клиентский запрос в свободный туннель.
func (s *Service) Handle(w http.ResponseWriter, r *http.Request) {
	s.proxy.ServeHTTP(w, r)
}
