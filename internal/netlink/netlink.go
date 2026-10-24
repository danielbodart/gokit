package netlink

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Socket is a bound netlink socket of one protocol, with a receive timeout.
type Socket struct {
	fd     int
	buffer []byte
}

// Open is a netlink socket of this protocol, such as NETLINK_SOCK_DIAG, bound
// and with a receive timeout of at most timeout, or less if ctx's deadline is
// sooner. name is what the errors call it.
//
// The timeout bounds what the context does not: a kernel that stopped
// answering part way through a dump would otherwise block a receive for
// ever, whatever happens to ctx. A dump of even a large table takes
// milliseconds.
func Open(ctx context.Context, name string, protocol int, timeout time.Duration) (*Socket, error) {
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, protocol)
	if err != nil {
		return nil, fmt.Errorf("opening %s netlink socket: %w", name, err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("binding %s netlink socket: %w", name, err)
	}
	timeval := syscall.NsecToTimeval(timeout.Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &timeval); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("setting %s netlink timeout: %w", name, err)
	}
	// Twice the most the kernel puts in one datagram of a dump (32KiB), so a
	// datagram always fits whole.
	return &Socket{fd: fd, buffer: make([]byte, 64*1024)}, nil
}

func (s *Socket) Close() error { return syscall.Close(s.fd) }

// Send sends one request to the kernel.
func (s *Socket) Send(message []byte) error {
	return syscall.Sendto(s.fd, message, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK})
}

// Receive is the next datagram from the kernel - a dump's answer comes in as
// many as it needs. It is only good until the next Receive.
func (s *Socket) Receive() ([]byte, error) {
	for {
		n, _, flags, _, err := syscall.Recvmsg(s.fd, s.buffer, nil, 0)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EAGAIN):
			return nil, fmt.Errorf("the kernel stopped answering: %w", context.DeadlineExceeded)
		case err != nil:
			return nil, err
		case flags&syscall.MSG_TRUNC != 0:
			// Half a datagram would lose what was in the other half, silently.
			return nil, errors.New("a netlink datagram was truncated")
		}
		return s.buffer[:n], nil
	}
}

// Request is a netlink message of this type asking for a dump, numbered seq,
// with body after the header.
func Request(kind uint16, seq uint32, body []byte) []byte {
	message := make([]byte, syscall.NLMSG_HDRLEN, syscall.NLMSG_HDRLEN+len(body))
	order := binary.NativeEndian
	order.PutUint32(message[0:], uint32(syscall.NLMSG_HDRLEN+len(body)))
	order.PutUint16(message[4:], kind)
	order.PutUint16(message[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	order.PutUint32(message[8:], seq)
	return append(message, body...)
}

// Collect reads the answer to the dump numbered seq, datagram by datagram from
// receive, handing each of its messages to each, until the kernel says it is
// done or that it failed. A failure is the kernel's errno, as a
// syscall.Errno.
//
// Messages answering any other request - a late answer to one before - are
// passed over, as is NLMSG_NOOP; an NLMSG_OVERRUN is an error, since
// something was lost. Everything else is for each to make sense of, or
// refuse.
func Collect(ctx context.Context, receive func() ([]byte, error), seq uint32, each func(message syscall.NetlinkMessage) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		datagram, err := receive()
		if err != nil {
			return err
		}
		messages, err := syscall.ParseNetlinkMessage(datagram)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.Header.Seq != seq {
				continue
			}
			switch message.Header.Type {
			case syscall.NLMSG_NOOP:
			case syscall.NLMSG_OVERRUN:
				return errors.New("the kernel overran its netlink buffer and lost messages")
			case syscall.NLMSG_DONE, syscall.NLMSG_ERROR:
				// Both carry an errno, negated; DONE's is 0 unless the dump
				// failed part way, and an ERROR of 0 is an acknowledgement.
				if len(message.Data) >= 4 {
					if errno := int32(binary.NativeEndian.Uint32(message.Data)); errno < 0 {
						return syscall.Errno(-errno)
					}
				}
				if message.Header.Type == syscall.NLMSG_DONE {
					return nil
				}
			default:
				if err := each(message); err != nil {
					return err
				}
			}
		}
	}
}

// TypeMask is an attribute's type without NLA_F_NESTED and
// NLA_F_NET_BYTEORDER, which the kernel sets on some attributes and not
// others.
const TypeMask = 0x3fff

// Attributes reads a run of netlink attributes into their payloads by type,
// without its flags. An empty run is no attributes, so an absent nest reads
// as an empty one. Anything that does not hold together - a length past the
// end, stray bytes after the last - is an error: what follows it cannot be
// read.
func Attributes(data []byte) (map[uint16][]byte, error) {
	found := map[uint16][]byte{}
	order := binary.NativeEndian
	for len(data) > 0 {
		if len(data) < syscall.SizeofRtAttr {
			return nil, fmt.Errorf("malformed netlink attribute: %d stray bytes", len(data))
		}
		length := int(order.Uint16(data[0:2]))
		if length < syscall.SizeofRtAttr || length > len(data) {
			return nil, fmt.Errorf("malformed netlink attribute: length %d of %d bytes", length, len(data))
		}
		found[order.Uint16(data[2:4])&TypeMask] = data[syscall.SizeofRtAttr:length]
		aligned := (length + syscall.RTA_ALIGNTO - 1) &^ (syscall.RTA_ALIGNTO - 1)
		data = data[min(aligned, len(data)):]
	}
	return found, nil
}
