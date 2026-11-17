package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"time"
)

func (c *probeClient) connect(ctx context.Context) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", c.address)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	stopIO := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopIO()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	reply := make([]byte, 2)
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return nil, errProbe
	}
	if _, err = io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, []byte{5, 2}) {
		return nil, errProbe
	}
	user := "meshstrata"
	hello := append([]byte{1, byte(len(user))}, user...)
	hello = append(append(hello, byte(len(c.auth))), c.auth...)
	if _, err = conn.Write(hello); err != nil {
		return nil, errProbe
	}
	if _, err = io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, []byte{1, 0}) {
		return nil, errProbe
	}
	ok = true
	return conn, nil
}

func socksAddress(a netip.AddrPort) []byte {
	kind := byte(1)
	if a.Addr().Is6() {
		kind = 4
	}
	out := append([]byte{kind}, a.Addr().AsSlice()...)
	return binary.BigEndian.AppendUint16(out, a.Port())
}

func readSocksAddress(r io.Reader, kind byte) (netip.AddrPort, error) {
	var a netip.AddrPort
	size := 4
	switch kind {
	case 1:
	case 4:
		size = 16
	default:
		return a, errProbe
	}
	var suffix [18]byte
	if _, err := io.ReadFull(r, suffix[:size+2]); err != nil {
		return a, err
	}
	ip, _ := netip.AddrFromSlice(suffix[:size])
	return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(suffix[size:])), nil
}

func socksRequest(conn net.Conn, command byte, target string) (byte, netip.AddrPort, error) {
	var addr netip.AddrPort
	dest, err := netip.ParseAddrPort(target)
	if err != nil {
		return 255, addr, errProbe
	}
	if _, err = conn.Write(append([]byte{5, command, 0}, socksAddress(dest)...)); err != nil {
		return 255, addr, err
	}
	var reply [4]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil {
		return 255, addr, err
	}
	if reply[0] != 5 || reply[2] != 0 {
		return 255, addr, errProbe
	}
	addr, err = readSocksAddress(conn, reply[3])
	return reply[1], addr, err
}

func probeUDP(ctx context.Context, socks net.Conn, target string, payload []byte) bool {
	rep, addr, err := socksRequest(socks, 3, "0.0.0.0:0")
	if err != nil || rep != 0 || (!addr.Addr().IsUnspecified() && !addr.Addr().IsLoopback()) || addr.Port() == 0 {
		return false
	}
	if addr.Addr().IsUnspecified() {
		addr = netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), addr.Port())
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "udp", addr.String())
	if err != nil {
		return false
	}
	defer c.Close()
	stopIO := context.AfterFunc(ctx, func() { c.Close() })
	defer stopIO()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	dest, err := netip.ParseAddrPort(target)
	if err != nil {
		return false
	}
	prefix := append([]byte{0, 0, 0}, socksAddress(dest)...)
	packet := append(prefix, payload...)
	for range 3 {
		deadline := time.Now().Add(300 * time.Millisecond)
		if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
			deadline = limit
		}
		_ = c.SetDeadline(deadline)
		if _, err = c.Write(packet); err != nil {
			return false
		}
		var buf [256]byte
		n, err := c.Read(buf[:])
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			continue
		}
		if n < 4 || !bytes.Equal(buf[:3], []byte{0, 0, 0}) {
			continue
		}
		reader := bytes.NewReader(buf[4:n])
		reply, err := readSocksAddress(reader, buf[3])
		if err != nil || reply.Port() != dest.Port() || reply.Addr().Unmap() != dest.Addr().Unmap() {
			continue
		}
		received := make([]byte, reader.Len())
		_, err = io.ReadFull(reader, received)
		if err == nil && bytes.Equal(received, payload) {
			return true
		}
	}
	return false
}
