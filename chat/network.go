package chat

import (
	"net"
	"time"
)

type PacketConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, addr net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
	LocalAddr() net.Addr
}

type Network interface {
	Listen(addr string) (PacketConn, error)
	Resolve(addr string) (net.Addr, error)
}

type UDPNetwork struct{}

func (UDPNetwork) Listen(addr string) (PacketConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}

	return &udpPacketConn{UDPConn: conn}, nil
}

func (UDPNetwork) Resolve(addr string) (net.Addr, error) {
	return net.ResolveUDPAddr("udp", addr)
}

type udpPacketConn struct {
	*net.UDPConn
}

func (c *udpPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return c.UDPConn.ReadFrom(b)
}

func (c *udpPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.UDPConn.WriteTo(b, addr)
}
