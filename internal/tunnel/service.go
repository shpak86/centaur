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

// errBadRequestBody возвращается, когда тело запроса на модельную ручку
// не разбирается как JSON: прокси отвечает 400, не занимая туннель.
var errBadRequestBody = errors.New("bad request body")

type Service struct {
	apiServer    *ApiServer
	tunnelServer *TunnelServer
	registry     *Registry
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
	service.registry = NewRegistry()
	authManager := NewAuthManager(apiKeys)
	service.apiServer = NewApiServer(httpOps, authManager, service.registry)
	service.tunnelServer = NewTunnelServer(tunnelOps, service.registry)
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
			if errors.Is(err, errBadRequestBody) {
				slog.Debug("Bad request body", "path", r.URL.Path, "error", err)
				http.Error(w, "bad request body", http.StatusBadRequest)
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
	// Имя модели нужно для маршрутизации, поэтому тело читается до Acquire:
	// на битом теле отвечаем 400, не занимая туннель. getModel возвращает
	// тело обратно в req.Body — ниже его пишет req.Write.
	model, err := getModel(req)
	if err != nil {
		return nil, err
	}

	// Туннели одноразовые: если подходящего туннеля сейчас нет, сразу отдаём
	// errNoTunnel (503) — повторять запрос или нет, решает клиент.
	tunnel, exists := t.registry.Acquire(model)
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
	err = req.Write(tunnel.Conn)
	tunnel.Conn.SetWriteDeadline(time.Time{})
	if err != nil {
		tunnel.Close()
		return nil, fmt.Errorf("write request to tunnel: %w", err)
	}
	slog.Info(
		"tunnel request sent",
		"token", tunnel.Id,
		"remote", tunnel.Conn.RemoteAddr().String(),
		"method", req.Method,
		"path", req.URL.Path,
		"model", model,
	)

	resp, err := http.ReadResponse(bufio.NewReader(tunnel.Conn), req)
	if err != nil {
		tunnel.Close()
		return nil, fmt.Errorf("read response from tunnel: %w", err)
	}
	slog.Info("tunnel response received",
		"token", tunnel.Id,
		"remote", tunnel.Conn.RemoteAddr().String(),
		"status", resp.Status)

	// Туннель освобождаем вместе с телом ответа: к этому моменту оно прочитано.
	resp.Body = &tunnelBody{ReadCloser: resp.Body, tunnel: tunnel, registry: t.registry}

	return resp, nil
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
		b.tunnel.Close()
	})
	return err
}

func (s *Service) ListenAndServe() error {
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
	return errors.Join(errs...)
}

// Handle проксирует клиентский запрос в свободный туннель.
// Проксируются только инференс-ручки (modelPaths): gateway выполняет инференс,
// а не управляет удалённой Ollama, поэтому инфо/админ-ручки (tags, ps, show,
// pull, push, create, copy, delete, /v1/models) он не проксирует и отвечает 501.
func (s *Service) Handle(w http.ResponseWriter, r *http.Request) {
	// GET / — проверка живости gateway: Ollama-клиенты пингуют корень.
	// Отвечаем сами, в туннель не ходим.
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := w.Write([]byte("Centaur is running")); err != nil {
			slog.Debug("Write health response", "error", err)
		}
		return
	}

	if _, ok := modelPaths[r.URL.Path]; !ok {
		slog.Debug("Rejected non-inference request", "method", r.Method, "path", r.URL.Path)
		http.Error(w, "centaur gateway proxies inference endpoints only", http.StatusNotImplemented)
		return
	}

	s.proxy.ServeHTTP(w, r)
}
