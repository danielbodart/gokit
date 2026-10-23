// Package sockdiag lists the host's listening sockets, and the cgroup that
// created each, by asking the kernel over NETLINK_SOCK_DIAG.
//
// That is what `ss -Hlntu -e` shows: every TCP socket in LISTEN and every
// bound, unconnected UDP socket, IPv4 and IPv6, with its cgroup. A cgroup is
// how a socket is put back to whoever opened it - a systemd unit's
// ControlGroup, a container's scope - on a host where every service shares
// one network namespace and a port number alone could be anyone's.
//
// It is here rather than borrowed, or run as ss, because the kernel's own
// interface is a few hundred lines of the standard library's syscall package:
// ss needs iproute2 installed, a new enough one to print cgroups, and its text
// parsed, and the netlink libraries are dependencies far larger than the one
// request this makes.
package sockdiag
