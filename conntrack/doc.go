// Package conntrack reads the host's connection tracking table by asking the
// kernel over NETLINK_NETFILTER - the ctnetlink interface `conntrack -L`
// itself uses.
//
// Every flow the kernel is tracking, IPv4 and IPv6, every protocol: its
// original and reply tuples (addresses, ports, ICMP type and id), where
// NAT shows as the two not being each other reversed; its status flags, such
// as whether traffic has been seen both ways; its TCP state, named as
// conntrack-tools names it; how long it has left; and its mark, zone, id and,
// where accounting is on, its packet and byte counts. A flow is how traffic
// that has already passed - a client still connected, a connection lingering
// in TIME_WAIT - is seen on a host, whichever network namespace it ended up
// in.
//
// It is here rather than borrowed, or run as conntrack, because the kernel's
// own interface is a few hundred lines of the standard library's syscall
// package: conntrack-tools needs installing and its text parsed, and the
// netlink libraries are dependencies far larger than the one request this
// makes.
//
// Reading the table needs CAP_NET_ADMIN in the network namespace's user
// namespace; without it the kernel refuses with EPERM, which
// errors.Is(err, os.ErrPermission) recognises.
package conntrack
