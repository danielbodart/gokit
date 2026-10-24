// Package systemd is the two halves of systemd's protocol with the services
// it starts: socket activation, the listening sockets systemd opened and
// passes in, and sd_notify, the datagrams a service sends back to say it is
// ready, reloading, stopping or still alive.
//
// Both are environment variables and file descriptors - LISTEN_PID,
// LISTEN_FDS and LISTEN_FDNAMES with descriptors from 3; NOTIFY_SOCKET,
// WATCHDOG_USEC and WATCHDOG_PID - read here from an environ.Env handed in,
// so a test passes variables and sockets of its own rather than being
// started by systemd. Each is read strictly: variables that are meant for
// this process but do not hold together are an error, not a guess, and a
// descriptor that is not a listening socket is refused.
//
// It is here rather than borrowed because the protocol is a few dozen lines
// of the standard library, written down in sd_listen_fds(3) and sd_notify(3),
// and the libraries that offer it bring D-Bus and journal clients along.
// Linux only.
package systemd
