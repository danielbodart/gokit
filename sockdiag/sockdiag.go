package sockdiag

import (
	"context"
	"errors"
	"net/netip"
	"strings"
)

// Protocol is a socket's transport, lower case as ss and most configuration
// write it.
type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

// Listener is a socket waiting for traffic: a TCP socket in LISTEN, or a
// bound, unconnected UDP socket.
type Listener struct {
	Protocol Protocol
	Address  netip.Addr
	Port     int
	// The interface the socket is bound to, by index - SO_BINDTODEVICE, or a
	// link-local address's scope - or 0 for none.
	Interface int
	// An IPv6 socket that does not also take IPv4.
	V6Only bool
	// The cgroup of whoever created the socket, by id, or 0 when the kernel
	// does not say (before Linux 5.7).
	CgroupID uint64
	// The same cgroup by path in the cgroup2 hierarchy, such as
	// /system.slice/sshd.service, or "" when it is not known.
	Cgroup string
}

// Within is whether the socket was created in one of these cgroups, or
// beneath one.
//
// Beneath, because a service may put its workers in sub-cgroups of its own -
// systemd's Delegate=yes, a container runtime's scopes. A sibling with a
// longer name is not beneath: /system.slice/foo.service2 is not in
// /system.slice/foo.service. A socket whose cgroup is unknown is within
// nothing, and an empty group holds nothing, so a unit that is not running
// claims no sockets rather than all of them.
func (l Listener) Within(groups ...string) bool {
	if l.Cgroup == "" {
		return false
	}
	for _, group := range groups {
		if group == "" {
			continue
		}
		if l.Cgroup == group || strings.HasPrefix(l.Cgroup, strings.TrimSuffix(group, "/")+"/") {
			return true
		}
	}
	return false
}

// Lister lists the host's listening sockets.
//
// An interface, so an application's tests hand it listeners of their own with
// a ListerFunc, and no kernel.
//
// When the sockets were listed but which cgroup each came from cannot be told,
// it returns them, with Cgroup "", together with an error wrapping
// ErrUnknownCgroups. That is a partial answer rather than a failure: whatever
// needs only the ports goes on, and whatever needs their owners can say why
// it cannot.
type Lister interface {
	Listening(ctx context.Context) ([]Listener, error)
}

// ListerFunc is a function as a Lister.
type ListerFunc func(ctx context.Context) ([]Listener, error)

func (f ListerFunc) Listening(ctx context.Context) ([]Listener, error) { return f(ctx) }

// ErrUnknownCgroups is the sockets having been listed without being able to
// say which cgroup created each. The error that wraps it says why, for the
// caller to add what it does without them.
var ErrUnknownCgroups = errors.New("the sockets' cgroups are unknown")
