package tunnel

import (
	"net"
	"time"
)

type Tunnel struct {
	Id           string
	Conn         *net.TCPConn
	Models       []string
	RegisteredAt time.Time
}

func NewTunnel(id string, conn *net.TCPConn, models []string) *Tunnel {
	t := Tunnel{
		Id:           id,
		Conn:         conn,
		Models:       models,
		RegisteredAt: time.Now(),
	}
	return &t
}

func (t *Tunnel) Close() {
	t.Conn.Close()
}

// alive проверяет, что соединение туннеля ещё живо.
//
// Проба смотрит во входящий поток сокета, ничего не потребляя (MSG_PEEK):
// закрытие коннектора видно как 0 байт (FIN), обрыв — как ошибка (RST и прочее),
// живой молчащий сокет — как EAGAIN.
//
// Опорный инвариант: туннель, лежащий в реестре, гарантированно молчит — запрос
// в него пишет только RoundTrip и только после Acquire, а коннектор пишет лишь
// ответ на запрос. Поэтому любые данные в потоке в покое означают, что туннель
// уже не тот, за кого себя выдаёт, и отдавать его клиенту нельзя.
//
// Дедлайн чтения в прошлом (pastDeadline) для пробы не годится: internal/poll
// проверяет дедлайн до сисколла, и мёртвый сокет отвечает так же, как живой.
//
// Обрыв без FIN/RST («чёрная дыра») пробой не виден: его ловит TCP-keepalive,
// включённый туннельным сервером (см. TunnelServer.handleTunnel).
func (t *Tunnel) alive() bool {
	raw, err := t.Conn.SyscallConn()
	if err != nil {
		return false
	}
	return !peekClosed(raw)
}
