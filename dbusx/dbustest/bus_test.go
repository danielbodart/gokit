package dbustest_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/danielbodart/gokit/dbusx/dbustest"
	"github.com/godbus/dbus/v5"
)

var ctx = context.Background()

func assertEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

// connect is a real godbus client of the bus.
func connect(t *testing.T, bus *dbustest.Bus) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(bus.Address())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func call(conn *dbus.Conn, member string, args ...any) *dbus.Call {
	return conn.Object("org.example", "/org/example").CallWithContext(ctx, "org.example.Thing."+member, 0, args...)
}

// eventually waits for what a client does to reach the bus, which it does
// a moment after the client has moved on.
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !condition(); {
		if time.Now().After(deadline) {
			t.Fatal("never happened")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAClientConnectsAndIsGivenItsName(t *testing.T) {
	bus := dbustest.New(t)
	conn := connect(t, bus)
	assertEqual(t, conn.Names(), []string{dbustest.UniqueName})
	assertEqual(t, bus.Connected(), 1)
	assertEqual(t, bus.Members(), []string{"Hello"})
}

func TestCallsAreAnsweredAsTheTestSaysAndRecorded(t *testing.T) {
	bus := dbustest.New(t)
	bus.Answer(func(call *dbus.Message) dbustest.Reply {
		if dbustest.Header[string](call, dbus.FieldMember) == "Greet" {
			return dbustest.Reply{Body: []any{"hello " + call.Body[0].(string), uint32(7)}}
		}
		return dbustest.Reply{}
	})
	conn := connect(t, bus)

	var greeting string
	var number uint32
	if err := call(conn, "Greet", "world").Store(&greeting, &number); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, greeting, "hello world")
	assertEqual(t, number, uint32(7))
	if err := call(conn, "Poke").Err; err != nil {
		t.Fatal(err)
	}

	assertEqual(t, bus.Members(), []string{"Hello", "Greet", "Poke"})
	greets := bus.Calls("Greet")
	assertEqual(t, len(greets), 1)
	assertEqual(t, greets[0].Body, []any{"world"})
	assertEqual(t, dbustest.Header[dbus.ObjectPath](greets[0], dbus.FieldPath), dbus.ObjectPath("/org/example"))
	assertEqual(t, dbustest.Header[string](greets[0], dbus.FieldInterface), "org.example.Thing")
	assertEqual(t, dbustest.Header[string](greets[0], dbus.FieldDestination), "org.example")
	assertEqual(t, dbustest.Header[uint32](greets[0], dbus.FieldDestination), uint32(0))
}

func TestAnErrorIsAnsweredAsAnErrorReply(t *testing.T) {
	bus := dbustest.New(t)
	bus.Answer(func(*dbus.Message) dbustest.Reply {
		return dbustest.Reply{Err: dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []any{"Unit x.service not loaded."})}
	})
	err := call(connect(t, bus), "Get").Err
	var named dbus.Error
	if !errors.As(err, &named) {
		t.Fatalf("got %#v", err)
	}
	assertEqual(t, named.Name, "org.freedesktop.systemd1.NoSuchUnit")
	assertEqual(t, named.Body, []any{"Unit x.service not loaded."})
}

func TestSignalsReachEveryClient(t *testing.T) {
	bus := dbustest.New(t)
	var received []chan *dbus.Signal
	for range 2 {
		signals := make(chan *dbus.Signal, 1)
		connect(t, bus).Signal(signals)
		received = append(received, signals)
	}
	bus.Signal("/org/example", "org.example.Thing", "Changed", "colour", uint32(3))
	for _, signals := range received {
		signal := <-signals
		assertEqual(t, signal.Path, dbus.ObjectPath("/org/example"))
		assertEqual(t, signal.Name, "org.example.Thing.Changed")
		assertEqual(t, signal.Sender, ":1.1")
		assertEqual(t, signal.Body, []any{"colour", uint32(3)})
	}
}

// What a bus may well do: deliver a signal about a call's effect before the
// call's own reply.
func TestBeforeRunsAheadOfTheReply(t *testing.T) {
	bus := dbustest.New(t)
	conn := connect(t, bus)
	signals := make(chan *dbus.Signal, 1)
	conn.Signal(signals)
	bus.Answer(func(*dbus.Message) dbustest.Reply {
		return dbustest.Reply{Body: []any{"done"}, Before: func() {
			bus.Signal("/org/example", "org.example.Thing", "Finished")
		}}
	})
	var reply string
	if err := call(conn, "Start").Store(&reply); err != nil {
		t.Fatal(err)
	}
	select {
	case signal := <-signals:
		assertEqual(t, signal.Name, "org.example.Thing.Finished")
	default:
		t.Fatal("the reply came before the signal")
	}
}

func TestDisconnectDropsEveryClient(t *testing.T) {
	bus := dbustest.New(t)
	conn := connect(t, bus)
	bus.Disconnect()
	eventually(t, func() bool { return !conn.Connected() })
	assertEqual(t, bus.Connected(), 0)
	// And a client may come back.
	connect(t, bus)
	assertEqual(t, bus.Connected(), 1)
}

func TestMaxPendingRefusesCallsBeyondTheLimit(t *testing.T) {
	bus := dbustest.New(t)
	bus.MaxPending(2)
	release := make(chan struct{})
	var arrived sync.WaitGroup
	arrived.Add(2)
	bus.Answer(func(*dbus.Message) dbustest.Reply {
		arrived.Done()
		<-release
		return dbustest.Reply{}
	})
	conn := connect(t, bus)

	held := []*dbus.Call{
		conn.Object("org.example", "/org/example").Go("org.example.Thing.Wait", 0, nil),
		conn.Object("org.example", "/org/example").Go("org.example.Thing.Wait", 0, nil),
	}
	arrived.Wait()
	err := call(conn, "Wait").Err
	var named dbus.Error
	if !errors.As(err, &named) || named.Name != "org.freedesktop.DBus.Error.LimitsExceeded" {
		t.Fatalf("got %v", err)
	}
	close(release)
	for _, c := range held {
		if err := (<-c.Done).Err; err != nil {
			t.Fatal(err)
		}
	}
	assertEqual(t, bus.MostPending(), 2)
	assertEqual(t, len(bus.Calls("Wait")), 3)
}

func TestTheBusStopsWhenTheTestEnds(t *testing.T) {
	var conn *dbus.Conn
	var address string
	t.Run("using it", func(t *testing.T) {
		bus := dbustest.New(t)
		address = bus.Address()
		var err error
		conn, err = dbus.Connect(address)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer conn.Close()
	eventually(t, func() bool { return !conn.Connected() })
	if again, err := dbus.Connect(address); err == nil {
		again.Close()
		t.Fatal("connected to a bus whose test has ended")
	}
}
