//go:build linux

package fakedpi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/florianl/go-nfqueue/v2"
)

const (
	queueMaxLen    = 4096   // packets the kernel holds for us before it drops new ones
	queueMaxPacket = 0xffff // copy whole packets (GSO packets are segmented by the kernel first)
	verdictTimeout = time.Second
)

// Queue is an NFQUEUE binding with the raw sockets that send injected packets. It is created by
// Listen and served by Run.
type Queue struct {
	e   *Engine
	nf  *nfqueue.Nfqueue
	fd4 int // AF_INET, SOCK_RAW, IPPROTO_RAW (header included); -1 if not open
	fd6 int // AF_INET6, SOCK_RAW, IPPROTO_RAW (header included); -1 if not open or no IPv6

	started   chan struct{} // closed when Run starts; packets wait for it
	closed    chan struct{} // closed by Close
	fatal     chan error    // the receive loop stopped on its own
	startOnce sync.Once
	closeOnce sync.Once
	closeErr  error

	mu       sync.Mutex
	failures int   // per-packet failures (verdict, injection, receive)
	first    error // the first of them
}

// Listen binds NFQUEUE queue and opens the raw socket for injected packets, both in the
// calling thread's network namespace (call it inside netns.Do). Run feeds every queued packet through
// Process, sets the verdict and sends Inject; it returns when ctx is done.
//
// Linux only. It needs root (CAP_NET_ADMIN for the queue, CAP_NET_RAW for the raw sockets) and the
// kernel modules nfnetlink_queue and nft_queue; the caller installs the nft rule that queues
// packets ("queue num <queue>", without bypass). The queue is bound with fail-open off, so packets
// that arrive while the queue is full are dropped, like dropped verdicts. Packets that arrive
// before Run starts wait in the kernel queue. When IPv6 sockets are not supported by the kernel,
// IPv6 packets are still judged but their injected RSTs are not sent (counted as failures).
func (e *Engine) Listen(queue uint16) (*Queue, error) {
	q := &Queue{
		e:       e,
		fd4:     -1,
		fd6:     -1,
		started: make(chan struct{}),
		closed:  make(chan struct{}),
		fatal:   make(chan error, 1),
	}
	fd, err := rawSocket(syscall.AF_INET)
	if err != nil {
		return nil, fmt.Errorf("fakedpi: raw IPv4 socket: %w", err)
	}
	q.fd4 = fd
	fd, err = rawSocket(syscall.AF_INET6)
	switch {
	case err == nil:
		q.fd6 = fd
	case errors.Is(err, syscall.EAFNOSUPPORT):
		// No IPv6 in this kernel: nothing to inject to.
	default:
		q.Close()
		return nil, fmt.Errorf("fakedpi: raw IPv6 socket: %w", err)
	}

	nf, err := nfqueue.Open(&nfqueue.Config{
		NfQueue:      queue,
		MaxQueueLen:  queueMaxLen,
		MaxPacketLen: queueMaxPacket,
		Copymode:     nfqueue.NfQnlCopyPacket,
		Flags:        0, // no fail-open: a full queue drops
		WriteTimeout: verdictTimeout,
	})
	if err != nil {
		q.Close()
		return nil, fmt.Errorf("fakedpi: open nfqueue: %w", err)
	}
	q.nf = nf
	// The library's own goroutine reads the socket; nf.Close (from Close) stops it. Binding happens
	// here, synchronously, so bind failures surface from Listen.
	if err := nf.RegisterWithErrorFunc(context.Background(), q.handle, q.onError); err != nil {
		q.Close()
		return nil, fmt.Errorf("fakedpi: bind nfqueue %d: %w", queue, err)
	}
	return q, nil
}

// Run feeds every queued packet through Process (with time.Now), sets the verdict (Accept → accept,
// Drop and Reset → drop) and sends the Decision's Inject packets on the raw socket of their family.
// A failure on one packet (verdict, injection) does not stop it. It returns when ctx is done or
// Close is called, and then closes the queue: Run serves a Queue once.
//
// The returned error is nil after a clean stop without failures. Otherwise it reports the
// failures (how many and the first one) and any error from closing; when the receive loop dies on
// its own, Run returns at once with that error.
func (q *Queue) Run(ctx context.Context) error {
	first := false
	q.startOnce.Do(func() {
		first = true
		close(q.started)
	})
	if !first {
		return errors.New("fakedpi: Queue.Run called twice")
	}
	var fatal error
	select {
	case <-ctx.Done():
	case <-q.closed:
	case fatal = <-q.fatal:
	}
	return errors.Join(fatal, q.Close(), q.failureSummary())
}

// Close unbinds the queue and closes the raw sockets. It is idempotent.
func (q *Queue) Close() error {
	q.closeOnce.Do(func() {
		close(q.closed)
		var errs []error
		if q.nf != nil {
			// Closes the netlink socket and waits for the library's goroutines, so no packet
			// handler runs (or uses the raw sockets) after this returns. The kernel drops the
			// queue binding with the socket.
			if err := q.nf.Close(); err != nil {
				errs = append(errs, fmt.Errorf("fakedpi: close nfqueue: %w", err))
			}
		}
		for _, fd := range []int{q.fd4, q.fd6} {
			if fd >= 0 {
				if err := syscall.Close(fd); err != nil {
					errs = append(errs, fmt.Errorf("fakedpi: close raw socket: %w", err))
				}
			}
		}
		q.fd4, q.fd6 = -1, -1
		q.closeErr = errors.Join(errs...)
	})
	return q.closeErr
}

// handle is the NFQUEUE callback, run on the library's receive goroutine. Returning non-zero stops
// the receive loop.
func (q *Queue) handle(a nfqueue.Attribute) int {
	if a.PacketID == nil {
		return 0
	}
	select {
	case <-q.started:
	case <-q.closed:
		return 1
	}
	d := Decision{Verdict: Accept}
	if a.Payload != nil {
		d = q.e.Process(*a.Payload, time.Now())
	}
	for _, pkt := range d.Inject {
		if err := q.inject(pkt); err != nil {
			q.fail(fmt.Errorf("fakedpi: inject: %w", err))
		}
	}
	verdict := nfqueue.NfAccept
	if d.Verdict != Accept {
		verdict = nfqueue.NfDrop
	}
	if err := q.nf.SetVerdict(*a.PacketID, verdict); err != nil {
		q.fail(fmt.Errorf("fakedpi: verdict: %w", err))
	}
	return 0
}

// onError handles receive errors of the library's loop. Returning non-zero stops the loop.
func (q *Queue) onError(err error) int {
	select {
	case <-q.closed:
		return 1
	default:
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EBADF) {
		select {
		case q.fatal <- fmt.Errorf("fakedpi: nfqueue receive: %w", err):
		default:
		}
		return 1
	}
	// ENOBUFS (the kernel queue overflowed towards us) and other transient errors: keep going.
	q.fail(fmt.Errorf("fakedpi: nfqueue receive: %w", err))
	return 0
}

func (q *Queue) fail(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failures++
	if q.first == nil {
		q.first = err
	}
}

func (q *Queue) failureSummary() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failures == 0 {
		return nil
	}
	return fmt.Errorf("fakedpi: %d packet failure(s), first: %w", q.failures, q.first)
}

// inject sends one raw IP packet (IP header included) on the raw socket of its family. The kernel
// routes it by its destination address in the queue's namespace; it leaves through the output
// path, so it is not queued again by a forward-chain rule.
func (q *Queue) inject(pkt []byte) error {
	if len(pkt) == 0 {
		return errors.New("empty packet")
	}
	switch pkt[0] >> 4 {
	case 4:
		if len(pkt) < 20 {
			return errors.New("short IPv4 packet")
		}
		sa := &syscall.SockaddrInet4{}
		copy(sa.Addr[:], pkt[16:20])
		return sendto(q.fd4, pkt, sa)
	case 6:
		if len(pkt) < 40 {
			return errors.New("short IPv6 packet")
		}
		if q.fd6 < 0 {
			return fmt.Errorf("IPv6: %w", errors.ErrUnsupported)
		}
		sa := &syscall.SockaddrInet6{}
		copy(sa.Addr[:], pkt[24:40])
		return sendto(q.fd6, pkt, sa)
	}
	return fmt.Errorf("unknown IP version %d", pkt[0]>>4)
}

func sendto(fd int, pkt []byte, sa syscall.Sockaddr) error {
	for {
		err := syscall.Sendto(fd, pkt, 0, sa)
		if err != syscall.EINTR {
			return err
		}
	}
}

// rawSocket opens a raw socket that sends whole IP packets (IPPROTO_RAW implies the header is
// included, for IPv4 and IPv6) in the calling thread's network namespace.
func rawSocket(family int) (int, error) {
	return syscall.Socket(family, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.IPPROTO_RAW)
}
