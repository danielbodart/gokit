package systemd

import (
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/danielbodart/gokit/environ"
)

func assertEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("want an error containing %q, got %v", want, err)
	}
}

// shortDir is a directory for unix sockets, whose paths must fit in 108
// bytes, which a test's own TempDir, named after the test, may not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gokit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// passedSocket is one socket for systemd to pass: its descriptor name, and
// "unix" or "tcp".
type passedSocket struct{ name, network string }

// passed is what systemd hands a socket-activated process: real listening
// sockets, made here, as descriptors 3, 4, ... with LISTEN_* saying so.
type passed struct {
	env environ.Env
	// Stands in for os.NewFile over the inherited descriptors.
	file func(fd uintptr, name string) *os.File
	// Each socket's address, in descriptor order.
	addresses []string
	// The unix sockets' paths, in descriptor order, "" for a TCP one.
	paths []string
}

const testPid = 4242

func passing(t *testing.T, sockets ...passedSocket) passed {
	t.Helper()
	p := passed{env: environ.Env{"LISTEN_PID": strconv.Itoa(testPid), "LISTEN_FDS": strconv.Itoa(len(sockets))}}
	files := map[uintptr]*os.File{}
	var names []string
	for i, socket := range sockets {
		var listener net.Listener
		var err error
		path := ""
		if socket.network == "unix" {
			path = shortDir(t) + "/s.sock"
			listener, err = net.Listen("unix", path)
		} else {
			listener, err = net.Listen("tcp4", "127.0.0.1:0")
		}
		if err != nil {
			t.Fatal(err)
		}
		file, err := listener.(interface{ File() (*os.File, error) }).File()
		if err != nil {
			t.Fatal(err)
		}
		// Only the descriptor passes on, as from systemd, which keeps the
		// socket's file: closing this end must not unlink it.
		if unix, ok := listener.(*net.UnixListener); ok {
			unix.SetUnlinkOnClose(false)
		}
		p.addresses = append(p.addresses, listener.Addr().String())
		listener.Close()
		t.Cleanup(func() { file.Close() }) // when the test never takes it
		files[uintptr(ListenFDsStart+i)] = file
		names = append(names, socket.name)
		p.paths = append(p.paths, path)
	}
	p.env["LISTEN_FDNAMES"] = strings.Join(names, ":")
	p.file = func(fd uintptr, _ string) *os.File { return files[fd] }
	return p
}

func TestActivatedIsNoActivationWithoutTheVariables(t *testing.T) {
	inherited, err := Activated(environ.Env{}, testPid, nil)
	assertEqual(t, err, nil)
	assertEqual(t, len(inherited), 0)
}

func TestActivatedLeavesSocketsMeantForAnotherProcessAlone(t *testing.T) {
	p := passing(t, passedSocket{"app", "unix"})
	inherited, err := Activated(p.env, testPid+1, func(uintptr, string) *os.File {
		t.Error("a descriptor meant for another process was taken")
		return nil
	})
	assertEqual(t, err, nil)
	assertEqual(t, len(inherited), 0)
}

func TestActivatedTakesLISTEN_FDSDescriptorsFrom3NamedByLISTEN_FDNAMES(t *testing.T) {
	p := passing(t, passedSocket{"app", "unix"}, passedSocket{"admin", "unix"}, passedSocket{"app", "tcp"})

	inherited, err := Activated(p.env, testPid, p.file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(inherited) })

	assertEqual(t, len(inherited), 3)
	for i, want := range []string{"app", "admin", "app"} {
		assertEqual(t, inherited[i].Name, want)
		assertEqual(t, inherited[i].Listener.Addr().String(), p.addresses[i])
	}
}

// What is taken is served: a client reaches the listener it was given.
func TestAnActivatedListenerAcceptsConnections(t *testing.T) {
	p := passing(t, passedSocket{"app", "unix"})
	inherited, err := Activated(p.env, testPid, p.file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(inherited) })

	accepted := make(chan error, 1)
	go func() {
		conn, err := inherited[0].Listener.Accept()
		if err == nil {
			conn.Close()
		}
		accepted <- err
	}()
	conn, err := net.Dial("unix", p.paths[0])
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := <-accepted; err != nil {
		t.Error(err)
	}
}

func TestAnUnnamedDescriptorIsUnknownAsSystemdsOwnDefault(t *testing.T) {
	p := passing(t, passedSocket{"ignored", "tcp"})
	delete(p.env, "LISTEN_FDNAMES")

	inherited, err := Activated(p.env, testPid, p.file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(inherited) })
	assertEqual(t, inherited[0].Name, "unknown")
}

func TestActivatedFailsWhenLISTEN_FDNAMESDisagreesWithLISTEN_FDS(t *testing.T) {
	p := passing(t, passedSocket{"a", "tcp"})
	p.env["LISTEN_FDNAMES"] = "a:b"
	_, err := Activated(p.env, testPid, p.file)
	assertErrorContains(t, err, "LISTEN_FDNAMES names 2 descriptors but LISTEN_FDS passes 1")
}

func TestActivatedFailsOnVariablesThatDoNotHoldTogether(t *testing.T) {
	for name, env := range map[string]environ.Env{
		"a LISTEN_PID that is not a pid":       {"LISTEN_PID": "me", "LISTEN_FDS": "1"},
		"a LISTEN_FDS that is not a count":     {"LISTEN_PID": strconv.Itoa(testPid), "LISTEN_FDS": "one"},
		"a negative LISTEN_FDS":                {"LISTEN_PID": strconv.Itoa(testPid), "LISTEN_FDS": "-1"},
		"LISTEN_FDS without LISTEN_PID":        {"LISTEN_FDS": "1"},
		"LISTEN_PID for us without LISTEN_FDS": {"LISTEN_PID": strconv.Itoa(testPid)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Activated(env, testPid, func(uintptr, string) *os.File { return nil }); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestADescriptorThatIsNotASocketFailsAndClosesTheOnesTakenBeforeIt(t *testing.T) {
	p := passing(t, passedSocket{"app", "unix"}, passedSocket{"oops", "unix"})
	notASocket, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	file := p.file
	p.file = func(fd uintptr, name string) *os.File {
		if fd == 4 {
			return notASocket
		}
		return file(fd, name)
	}

	_, err = Activated(p.env, testPid, p.file)

	assertErrorContains(t, err, "descriptor 4 (oops) from systemd: not a socket")
	if conn, err := net.Dial("unix", p.paths[0]); err == nil {
		conn.Close()
		t.Error("the socket taken before the failure is still listening")
	}
}

func TestAConnectionRatherThanAListenerIsRefused(t *testing.T) {
	server, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := net.Dial("tcp4", server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	file, err := conn.(*net.TCPConn).File()
	if err != nil {
		t.Fatal(err)
	}

	_, err = Activated(environ.Env{"LISTEN_PID": strconv.Itoa(testPid), "LISTEN_FDS": "1"}, testPid, func(uintptr, string) *os.File { return file })

	assertErrorContains(t, err, "not a listening socket")
}

func TestAMissingDescriptorFails(t *testing.T) {
	_, err := Activated(environ.Env{"LISTEN_PID": strconv.Itoa(testPid), "LISTEN_FDS": "1"}, testPid, func(uintptr, string) *os.File { return nil })
	assertErrorContains(t, err, "descriptor 3 (unknown) from systemd")
}

func TestListenersClearsTheVariables(t *testing.T) {
	// Zero descriptors: activated in form, with none of the test process's
	// own descriptors taken.
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "0")
	t.Setenv("LISTEN_FDNAMES", "")

	inherited, err := Listeners()

	assertEqual(t, err, nil)
	assertEqual(t, len(inherited), 0)
	for _, name := range listenEnv {
		if _, set := os.LookupEnv(name); set {
			t.Errorf("%s is still set", name)
		}
	}
}

// Cleared even when they do not hold together, so a child never sees them.
func TestListenersClearsTheVariablesWhenTheyFail(t *testing.T) {
	t.Setenv("LISTEN_PID", "me")
	t.Setenv("LISTEN_FDS", "1")

	if _, err := Listeners(); err == nil {
		t.Error("want an error")
	}
	for _, name := range listenEnv {
		if _, set := os.LookupEnv(name); set {
			t.Errorf("%s is still set", name)
		}
	}
}
