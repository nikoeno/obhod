//go:build linux && lab

package lab

import (
	"fmt"
	"github.com/nikoejento/obhod/lab/netns"
	"net"
	"sync"
	"syscall"
	"time"
)

// Sniffer is a read-only capture of one client interface (AF_PACKET, SOCK_RAW, ETH_P_ALL). It never
// sends and does not switch the interface to promiscuous mode.
type Sniffer struct {
	fd     int
	needle string
	ether  bool // the interface has a link header (a TUN device has none)
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	frames []Frame
}

// maxSniffFrames bounds the memory of one capture.
const maxSniffFrames = 20000

// SniffClient starts capturing IPv4 TCP frames on iface (e.g. "eth0") inside the client namespace.
// needle, if set, is looked for in every payload (Frame.Needle). Call Stop to end it.
func (l *Lab) SniffClient(iface, needle string) (*Sniffer, error) {
	return l.SniffNS(l.cli, iface, needle)
}

// SniffNS is SniffClient for any lab namespace (an ISP's lan and wan, the inet side of a link): the
// socket is created inside ns, read-only, no promiscuous mode.
func (l *Lab) SniffNS(ns *netns.NS, iface, needle string) (*Sniffer, error) {
	s := &Sniffer{fd: -1, needle: needle, stop: make(chan struct{}), done: make(chan struct{})}
	// The socket is created inside the client namespace and keeps it after Do returns.
	err := ns.Do(func() error {
		ifc, err := net.InterfaceByName(iface)
		if err != nil {
			return err
		}
		proto := htons(syscall.ETH_P_ALL)
		fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(proto))
		if err != nil {
			return fmt.Errorf("packet socket: %w", err)
		}
		if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: proto, Ifindex: ifc.Index}); err != nil {
			syscall.Close(fd)
			return fmt.Errorf("bind %s: %w", iface, err)
		}
		tv := syscall.NsecToTimeval(int64(100 * time.Millisecond))
		if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
			syscall.Close(fd)
			return err
		}
		s.ether = len(ifc.HardwareAddr) > 0
		s.fd = fd
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("lab: sniff %s: %w", iface, err)
	}
	go s.run()
	return s, nil
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func (s *Sniffer) run() {
	defer close(s.done)
	buf := make([]byte, 65536)
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		n, from, err := syscall.Recvfrom(s.fd, buf, 0)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				continue
			}
			return
		}
		out := false
		if ll, ok := from.(*syscall.SockaddrLinklayer); ok {
			out = ll.Pkttype == syscall.PACKET_OUTGOING
		}
		parse := parseIPv4
		if s.ether {
			parse = parseFrame
		}
		if f, ok := parse(buf[:n], time.Now(), out, s.needle); ok {
			s.mu.Lock()
			if len(s.frames) < maxSniffFrames {
				s.frames = append(s.frames, f)
			}
			s.mu.Unlock()
		}
	}
}

// Stop ends the capture, closes the socket and returns the frames in capture order.
func (s *Sniffer) Stop() []Frame {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	if s.fd >= 0 {
		syscall.Close(s.fd)
		s.fd = -1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Frame(nil), s.frames...)
}
