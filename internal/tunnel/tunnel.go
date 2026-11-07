package tunnel

import "net"

type Tunnel struct {
	Id     string
	Conn   *net.TCPConn
	Models []string
}

func NewTunnel(id string, conn *net.TCPConn, models []string) *Tunnel {
	t := Tunnel{
		Id:     id,
		Conn:   conn,
		Models: models,
	}
	return &t
}

func (t *Tunnel) Close() {
	t.Conn.Close()
}
