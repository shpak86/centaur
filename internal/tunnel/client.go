package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

const (
	// retryDelay — пауза перед повторной попыткой поднять туннель.
	retryDelay = time.Second
	// registerTimeout ограничивает запрос регистрации в пуле туннелей.
	registerTimeout = 10 * time.Second
	// dialTimeout ограничивает подключение к серверу туннелей.
	dialTimeout = 5 * time.Second
)

type Client struct {
	ctx        context.Context
	cancel     context.CancelFunc
	apiAddr    string
	tunnelAddr string
	targetAddr string
	apiKey     string
	models     []string
	transport  *http.Transport
	proxy      *httputil.ReverseProxy
}

func NewClient(apiAddr, tunnelAddr, targetAddr string) *Client {
	client := Client{
		apiAddr:    apiAddr,
		tunnelAddr: tunnelAddr,
		targetAddr: targetAddr,
		transport: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	client.ctx, client.cancel = context.WithCancel(context.Background())
	client.proxy = newOllamaProxy(targetAddr, client.transport)
	return &client
}

// newOllamaProxy собирает прокси, который отправляет приходящие из туннеля
// запросы в Ollama. Прокси и транспорт общие для всех туннелей: иначе на каждый
// туннель создавалось бы своё соединение к Ollama и файловые дескрипторы текли.
func newOllamaProxy(targetAddr string, transport http.RoundTripper) *httputil.ReverseProxy {
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("Invalid ollama target address", "address", targetAddr, "error", err)
		return nil
	}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},

		// Для NDJSON-стриминга Ollama: отправлять каждый кусок сразу.
		FlushInterval: -1,

		Transport: transport,

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("Ollama proxy error", "error", err)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
}

func (c *Client) Serve() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			c.serveTunnel()
		}
	}
}

func (c *Client) serveTunnel() error {
	regUrl, err := url.JoinPath(c.apiAddr, "api", "v1", "tunnels", "register")
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	data, err := json.Marshal(registerTunnelRequest{Models: c.models})
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	// Контекст привязан к жизни клиента: Close() прерывает регистрацию,
	// а таймаут не даёт зависнуть на недоступном API пула.
	ctx, cancel := context.WithTimeout(c.ctx, registerTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, regUrl, bytes.NewReader(data))
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("register tunnel: unexpected status %q", resp.Status)
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	var reg registerTunnelResponse
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	if reg.Token == "" {
		err := errors.New("register tunnel: empty token")
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}

	conn, err := net.DialTimeout("tcp", c.tunnelAddr, dialTimeout)
	if err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(reg.Token)); err != nil {
		slog.Error("Serve tunnel", "error", err)
		c.pause(retryDelay)
		return err
	}
	// handshake завершён: gateway знает про туннель и может отдать его клиенту
	return c.startOllamaBridge(conn)
}

// pause ждёт d или завершения клиента через Close(),
// чтобы остановка не ждала retryDelay.
func (c *Client) pause(d time.Duration) {
	select {
	case <-c.ctx.Done():
	case <-time.After(d):
	}
}

// Close останавливает клиент: Serve перестаёт поднимать новые туннели,
// а соединения к Ollama, простаивающие в пуле транспорта, закрываются.
func (c *Client) Close() {
	c.cancel()
	c.transport.CloseIdleConnections()
}

type singleConnListener struct {
	ch   chan net.Conn
	once sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	ch := make(chan net.Conn, 1)
	ch <- conn
	return &singleConnListener{ch: ch}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.ch) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

// trackedConn сообщает, когда http.Server закрыл соединение туннеля.
// Это единственный надёжный признак конца туннеля: singleConnListener отдаёт
// ровно одно соединение, поэтому после его обслуживания Serve навсегда
// остаётся в Accept и сам не вернётся.
type trackedConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func newTrackedConn(conn net.Conn) *trackedConn {
	return &trackedConn{Conn: conn, done: make(chan struct{})}
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

func (c *Client) startOllamaBridge(conn net.Conn) error {
	if c.proxy == nil {
		return errors.New("ollama proxy is not configured")
	}

	// ReadHeaderTimeout не задаём: туннель одноразовый и может простаивать
	// сколько угодно, пока gateway не отдаст ему запрос (или не закроет
	// соединение). Дедлайн на заголовки убивал бы такой туннель, оставляя
	// в реестре gateway мёртвую запись — первый запрос после паузы получал
	// бы 502.
	server := &http.Server{
		Handler: c.proxy,
		// ReadTimeout и WriteTimeout тоже не задавайте:
		// генерация Ollama может длиться долго.
	}

	// Serve обслуживает единственное соединение туннеля, поэтому держим его
	// в горутине, а завершение туннеля ждём по trackedConn.done (или по Close()).
	tracked := newTrackedConn(conn)
	listener := newSingleConnListener(tracked)

	var serveErr error
	served := make(chan struct{})
	go func() {
		serveErr = server.Serve(listener)
		close(served)
	}()

	select {
	case <-tracked.done: // gateway закончил или оборвал соединение
		slog.Info("tunnel connection closed")
	case <-served: // Serve завершился сам
		slog.Info("ollama bridge stopped")
	case <-c.ctx.Done(): // останавливают коннектор
		slog.Info("closing ollama bridge")
	}

	// Закрываем сервер: разблокирует Accept и закрывает соединение туннеля.
	if err := server.Close(); err != nil {
		slog.Error("Close ollama bridge", "error", err)
	}
	<-served // дожидаемся выхода Serve, чтобы не терять serveErr

	if serveErr != nil &&
		!errors.Is(serveErr, http.ErrServerClosed) &&
		!errors.Is(serveErr, net.ErrClosed) {
		slog.Error("Serve ollama bridge", "error", serveErr)
	}

	return nil
}
