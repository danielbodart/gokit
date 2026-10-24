package systemd

import (
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/danielbodart/gokit/environ"
)

// The states a service sends most, as sd_notify(3) names them.
const (
	// Ready is start-up finished, for Type=notify: systemd starts what is
	// ordered after the service only once it is sent, so it belongs after
	// every listener is serving.
	Ready = "READY=1"
	// Reloading is a reload begun, ended by Ready. Type=notify-reload also
	// needs Monotonic() in the same Notify, or systemd ignores it.
	Reloading = "RELOADING=1"
	// Stopping is a shutdown begun.
	Stopping = "STOPPING=1"
	// Watchdog is still alive, for WatchdogSec=: see WatchdogInterval.
	Watchdog = "WATCHDOG=1"
)

// Status is a STATUS= state: one line of free text that systemctl status
// shows beside the unit.
//
// A state ends at a newline, so line breaks in text - an error's message, say
// - become spaces rather than being taken for the next state.
func Status(text string) string {
	return "STATUS=" + lineBreaks.Replace(text)
}

var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// Monotonic is a MONOTONIC_USEC= state: CLOCK_MONOTONIC now, in
// microseconds, which Type=notify-reload needs alongside Reloading to tell
// one reload from the next.
//
// The clock cannot fail to be read on Linux; were it to, the state is ""
// and Notify refuses it, rather than sending a time that is not one.
func Monotonic() string {
	var now syscall.Timespec
	if _, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&now)), 0); errno != 0 {
		return ""
	}
	return "MONOTONIC_USEC=" + strconv.FormatInt(now.Nano()/1000, 10)
}

// clockMonotonic is CLOCK_MONOTONIC, which the syscall package does not name.
const clockMonotonic = 1

// Notify sends states to systemd, as sd_notify does: newline-joined, in one
// datagram to NOTIFY_SOCKET, a path or, starting with '@', an abstract
// socket.
//
// Sent by the main process itself, it needs no NotifyAccess= beyond the
// default. Without NOTIFY_SOCKET - under Docker, from a shell - it sends
// nothing and returns nil. Every state must be a VARIABLE=value on one line,
// and there must be at least one; anything else is an error whether or not
// there is a socket to send to, so a malformed state shows in every test, not
// first under systemd.
func Notify(env environ.Env, states ...string) error {
	if len(states) == 0 {
		return errors.New("sd_notify: no state to send")
	}
	for _, state := range states {
		name, _, found := strings.Cut(state, "=")
		if !found || name == "" || strings.ContainsAny(state, "\r\n") {
			return fmt.Errorf("sd_notify: %q is not a VARIABLE=value state on one line", state)
		}
	}
	socket := env["NOTIFY_SOCKET"]
	if socket == "" {
		return nil
	}
	if !strings.HasPrefix(socket, "/") && !strings.HasPrefix(socket, "@") {
		return fmt.Errorf("sd_notify: NOTIFY_SOCKET must be a path or an abstract socket, not %q", socket)
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("sd_notify: %w", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(strings.Join(states, "\n"))); err != nil {
		return fmt.Errorf("sd_notify: %w", err)
	}
	return nil
}

// WatchdogInterval is how often systemd expects Watchdog, as
// sd_watchdog_enabled reads it: WATCHDOG_USEC, for the process whose pid is
// WATCHDOG_PID when that is set.
//
// False is no watchdog for this process: none set, one meant for another
// process, or variables that do not hold together, which sd_watchdog_enabled
// refuses too. systemd's advice is to send Watchdog at half the interval.
func WatchdogInterval(env environ.Env, pid int) (time.Duration, bool) {
	usec, err := strconv.ParseUint(env["WATCHDOG_USEC"], 10, 64)
	if err != nil || usec == 0 || usec > math.MaxInt64/uint64(time.Microsecond) {
		return 0, false
	}
	if value := env["WATCHDOG_PID"]; value != "" {
		if target, err := strconv.Atoi(value); err != nil || target != pid {
			return 0, false
		}
	}
	return time.Duration(usec) * time.Microsecond, true
}
