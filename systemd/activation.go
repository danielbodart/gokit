package systemd

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/danielbodart/gokit/environ"
)

// Inherited is a listening socket systemd opened for this process, by the
// name its socket unit gave it: FileDescriptorName=, else "unknown", as
// systemd's own default.
type Inherited struct {
	Name     string
	Listener net.Listener
}

// ListenFDsStart is the first descriptor systemd passes: the one after
// stdin, stdout and stderr, as SD_LISTEN_FDS_START.
const ListenFDsStart = 3

// Activated is the listeners systemd passed, by its socket-activation
// protocol (sd_listen_fds_with_names): LISTEN_FDS descriptors from fd 3,
// named by the colon-separated LISTEN_FDNAMES, meant only for the process
// whose pid is LISTEN_PID.
//
// Variables meant for another process - inherited by a child, say - are not
// an activation, and nil is returned with the descriptors left alone, as it
// is when there are none. Variables meant for this one that do not hold
// together are an error: a LISTEN_PID that is not a pid, a LISTEN_FDS that is
// not a count, or names that do not number the descriptors. So is any
// descriptor that is not a listening socket, and every listener already
// taken is closed before it returns.
//
// The pid and the descriptors come in as arguments, so a test hands in
// listeners of its own; file is os.NewFile in production, and Listeners
// calls it so.
func Activated(env environ.Env, pid int, file func(fd uintptr, name string) *os.File) ([]Inherited, error) {
	if env["LISTEN_PID"] == "" && env["LISTEN_FDS"] == "" {
		return nil, nil
	}
	target, err := strconv.Atoi(env["LISTEN_PID"])
	if err != nil {
		return nil, fmt.Errorf("LISTEN_PID must be a pid, not %q", env["LISTEN_PID"])
	}
	if target != pid {
		return nil, nil
	}
	count, err := strconv.Atoi(env["LISTEN_FDS"])
	if err != nil || count < 0 {
		return nil, fmt.Errorf("LISTEN_FDS must be a count of descriptors, not %q", env["LISTEN_FDS"])
	}
	names := make([]string, count)
	for i := range names {
		names[i] = "unknown"
	}
	if value := env["LISTEN_FDNAMES"]; value != "" {
		if names = strings.Split(value, ":"); len(names) != count {
			return nil, fmt.Errorf("LISTEN_FDNAMES names %d descriptors but LISTEN_FDS passes %d", len(names), count)
		}
	}
	var inherited []Inherited
	for i, name := range names {
		fd := ListenFDsStart + i
		listener, err := listenerOn(file(uintptr(fd), name))
		if err != nil {
			closeAll(inherited)
			return nil, fmt.Errorf("descriptor %d (%s) from systemd: %w", fd, name, err)
		}
		inherited = append(inherited, Inherited{Name: name, Listener: listener})
	}
	return inherited, nil
}

// listenerOn is the listener over a passed descriptor. net.FileListener
// works on a duplicate, close-on-exec so a child never inherits it, and the
// original is closed. A socket that is not listening is refused: a socket
// unit with Accept=yes passes a connection, which a server cannot serve.
func listenerOn(file *os.File) (net.Listener, error) {
	if file == nil {
		return nil, fmt.Errorf("no such descriptor")
	}
	defer file.Close()
	conn, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	accepting, sockErr := 0, error(nil)
	if err := conn.Control(func(fd uintptr) {
		accepting, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	}); err != nil {
		return nil, err
	}
	if sockErr != nil {
		return nil, fmt.Errorf("not a socket: %w", sockErr)
	}
	if accepting == 0 {
		return nil, fmt.Errorf("not a listening socket (is the socket unit Accept=yes?)")
	}
	return net.FileListener(file)
}

func closeAll(inherited []Inherited) {
	for _, each := range inherited {
		each.Listener.Close()
	}
}

// listenEnv is the socket-activation variables, which Listeners clears once
// read.
var listenEnv = []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"}

// Listeners is Activated for this process, its variables then cleared from
// the environment, so that nothing it goes on to start mistakes them for its
// own - whether or not they were an activation, and whether or not they held
// together.
func Listeners() ([]Inherited, error) {
	inherited, err := Activated(environ.Current(), os.Getpid(), os.NewFile)
	for _, name := range listenEnv {
		os.Unsetenv(name)
	}
	return inherited, err
}
