package systemd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielbodart/gokit/environ"
)

// listening is systemd's end of NOTIFY_SOCKET: a real unixgram socket.
func listening(t *testing.T, name string) *net.UnixConn {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// received is the next datagram at systemd's end.
func received(t *testing.T, conn *net.UnixConn) string {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	datagram := make([]byte, 4096)
	n, _, err := conn.ReadFromUnix(datagram)
	if err != nil {
		t.Fatal(err)
	}
	return string(datagram[:n])
}

func TestNotifySendsAStateAsOneDatagramToAPath(t *testing.T) {
	path := filepath.Join(shortDir(t), "notify")
	systemd := listening(t, path)

	if err := Notify(environ.Env{"NOTIFY_SOCKET": path}, Ready); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, received(t, systemd), "READY=1")
}

func TestNotifySendsSeveralStatesNewlineJoinedInOneDatagram(t *testing.T) {
	path := filepath.Join(shortDir(t), "notify")
	systemd := listening(t, path)

	if err := Notify(environ.Env{"NOTIFY_SOCKET": path}, Ready, Status("Serving on :8080")); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, received(t, systemd), "READY=1\nSTATUS=Serving on :8080")
}

// systemd's own socket is abstract on some setups: '@' for its leading NUL.
func TestNotifyReachesAnAbstractSocket(t *testing.T) {
	name := fmt.Sprintf("@gokit-notify-%d-%d", os.Getpid(), time.Now().UnixNano())
	systemd := listening(t, name)

	if err := Notify(environ.Env{"NOTIFY_SOCKET": name}, Stopping); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, received(t, systemd), "STOPPING=1")
}

func TestNotifyDoesNothingOutsideSystemd(t *testing.T) {
	assertEqual(t, Notify(environ.Env{}, Ready), nil)
}

func TestNotifyFailsLoudlyWhenSystemdsSocketCannotBeReached(t *testing.T) {
	assertErrorContains(t, Notify(environ.Env{"NOTIFY_SOCKET": filepath.Join(shortDir(t), "missing")}, Ready), "sd_notify")
}

func TestNotifyRefusesASocketThatIsNeitherAPathNorAbstract(t *testing.T) {
	assertErrorContains(t, Notify(environ.Env{"NOTIFY_SOCKET": "vsock:2:1234"}, Ready), "must be a path or an abstract socket")
}

// Refused with no socket too, so a malformed state fails every test rather
// than first under systemd.
func TestNotifyRefusesWhatIsNotAStateOnOneLine(t *testing.T) {
	for _, state := range []string{"READY", "=1", "STATUS=one\nREADY=1", "STATUS=one\r", ""} {
		t.Run(strconv.Quote(state), func(t *testing.T) {
			assertErrorContains(t, Notify(environ.Env{}, state), "is not a VARIABLE=value state on one line")
		})
	}
}

func TestNotifyRefusesNothingToSend(t *testing.T) {
	assertErrorContains(t, Notify(environ.Env{}), "no state to send")
}

func TestStatusPutsTextOnOneLine(t *testing.T) {
	assertEqual(t, Status("Serving"), "STATUS=Serving")
	assertEqual(t, Status("Error: bus gone\nretrying\r\nsoon\r"), "STATUS=Error: bus gone retrying soon ")
	assertEqual(t, Notify(environ.Env{}, Status("one\ntwo")), nil)
}

func TestMonotonicIsTheMonotonicClockInMicroseconds(t *testing.T) {
	read := func() int64 {
		t.Helper()
		value, found := strings.CutPrefix(Monotonic(), "MONOTONIC_USEC=")
		if !found {
			t.Fatalf("got %q", Monotonic())
		}
		usec, err := strconv.ParseInt(value, 10, 64)
		if err != nil || usec <= 0 {
			t.Fatalf("got %q", value)
		}
		return usec
	}
	first, second := read(), read()
	if second < first {
		t.Errorf("went backwards: %d then %d", first, second)
	}
	assertEqual(t, Notify(environ.Env{}, Reloading, Monotonic()), nil)
}

func TestWatchdogIntervalIsWATCHDOG_USEC(t *testing.T) {
	interval, enabled := WatchdogInterval(environ.Env{"WATCHDOG_USEC": "30000000"}, testPid)
	assertEqual(t, enabled, true)
	assertEqual(t, interval, 30*time.Second)
}

func TestWatchdogIntervalForThisProcessByWATCHDOG_PID(t *testing.T) {
	interval, enabled := WatchdogInterval(environ.Env{"WATCHDOG_USEC": "500", "WATCHDOG_PID": strconv.Itoa(testPid)}, testPid)
	assertEqual(t, enabled, true)
	assertEqual(t, interval, 500*time.Microsecond)
}

func TestNoWatchdogUnlessItHoldsTogetherForThisProcess(t *testing.T) {
	for name, env := range map[string]environ.Env{
		"none set":                       {},
		"meant for another process":      {"WATCHDOG_USEC": "1000", "WATCHDOG_PID": strconv.Itoa(testPid + 1)},
		"a WATCHDOG_PID that is not one": {"WATCHDOG_USEC": "1000", "WATCHDOG_PID": "me"},
		"zero":                           {"WATCHDOG_USEC": "0"},
		"not a number":                   {"WATCHDOG_USEC": "30s"},
		"negative":                       {"WATCHDOG_USEC": "-1"},
		"beyond a duration":              {"WATCHDOG_USEC": "18446744073709551615"},
	} {
		t.Run(name, func(t *testing.T) {
			if interval, enabled := WatchdogInterval(env, testPid); enabled {
				t.Errorf("enabled at %v", interval)
			}
		})
	}
}
