package tunnel

import "net"

type Tunnel struct {
	Id   string
	Conn *net.TCPConn
	// Busy — резервирование туннеля под конкретный запрос.
	// Меняется только под мьютексом Registry, читать/писать напрямую нельзя.
	Busy bool
}

func NewTunnel(id string, conn *net.TCPConn) *Tunnel {
	t := Tunnel{
		Id:   id,
		Conn: conn,
	}
	return &t
}

func (t *Tunnel) Close() {
	t.Conn.Close()
}
