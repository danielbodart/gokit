package dbusx_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/danielbodart/gokit/dbusx"
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

func address(t *testing.T, env map[string]string) string {
	t.Helper()
	address, err := dbusx.SystemBusAddress(env)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func TestSystemBusIsTheStandardSocketByDefault(t *testing.T) {
	assertEqual(t, address(t, map[string]string{}), "unix:path=/run/dbus/system_bus_socket")
	assertEqual(t, address(t, nil), dbusx.DefaultSystemBus)
	assertEqual(t, address(t, map[string]string{"DBUS_SYSTEM_BUS_ADDRESS": ""}), "unix:path=/run/dbus/system_bus_socket")
}

func TestSystemBusFollowsDBUS_SYSTEM_BUS_ADDRESS(t *testing.T) {
	assertEqual(t, address(t, map[string]string{"DBUS_SYSTEM_BUS_ADDRESS": "unix:path=/run/other/bus"}), "unix:path=/run/other/bus")
	assertEqual(t, address(t, map[string]string{"DBUS_SYSTEM_BUS_ADDRESS": "tcp:host=x;unix:guid=1,path=/run/b"}), "unix:guid=1,path=/run/b")
	assertEqual(t, address(t, map[string]string{"DBUS_SYSTEM_BUS_ADDRESS": "unix:path=/a;tcp:host=x;unix:abstract=b"}), "unix:path=/a;unix:abstract=b")
}

func TestSystemBusRefusesAnAddressItWillNotUse(t *testing.T) {
	_, err := dbusx.SystemBusAddress(map[string]string{"DBUS_SYSTEM_BUS_ADDRESS": "tcp:host=x,port=1"})
	assertEqual(t, fmt.Sprint(err), "cannot use DBUS_SYSTEM_BUS_ADDRESS 'tcp:host=x,port=1': only unix: addresses are supported")
}

func TestConnectSaysWhereItWasGoingWhenThereIsNoBus(t *testing.T) {
	_, err := dbusx.Connect("unix:path=/nonexistent/bus")
	if err == nil || !strings.Contains(err.Error(), "D-Bus at unix:path=/nonexistent/bus") {
		t.Fatalf("got %v", err)
	}
}

// godbus's default handler would hand a busy reader's signals to goroutines,
// out of order.
func TestConnectHandsOverSignalsInTheOrderTheyWereSent(t *testing.T) {
	bus := dbustest.New(t)
	conn, err := dbusx.Connect(bus.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	signals := make(chan *dbus.Signal) // unbuffered: always busy
	conn.Signal(signals)
	for i := range 50 {
		bus.Signal("/org/example", "org.example.Thing", "Count", int32(i))
	}
	for i := range 50 {
		count, _ := dbusx.Arg[int32]((<-signals).Body, 0)
		assertEqual(t, count, int32(i))
	}
}

func TestDescribeNamesADBusError(t *testing.T) {
	value := dbus.Error{Name: "org.freedesktop.systemd1.NoSuchUnit", Body: []any{"Unit x.service not loaded."}}
	err := dbusx.Describe(fmt.Errorf("reading x.service: %w", value))
	assertEqual(t, err.Error(), "org.freedesktop.systemd1.NoSuchUnit: reading x.service: Unit x.service not loaded.")
	var unwrapped dbus.Error
	if !errors.As(err, &unwrapped) || unwrapped.Name != value.Name {
		t.Error("the error it describes is no longer in the chain")
	}

	pointer := dbus.NewError("org.freedesktop.login1.OperationInProgress", []any{"Operation denied due to active block inhibitor"})
	assertEqual(t, dbusx.Describe(pointer).Error(),
		"org.freedesktop.login1.OperationInProgress: Operation denied due to active block inhibitor")
}

func TestDescribeLeavesAnyOtherErrorAlone(t *testing.T) {
	plain := errors.New("connection refused")
	assertEqual(t, dbusx.Describe(plain), plain)
	assertEqual(t, dbusx.Describe(nil), nil)
}

// And from a real reply, which godbus hands over as a value.
func TestDescribeNamesAnErrorReply(t *testing.T) {
	bus := dbustest.New(t)
	bus.Answer(func(*dbus.Message) dbustest.Reply {
		return dbustest.Reply{Err: dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"not you"})}
	})
	conn, err := dbusx.Connect(bus.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = conn.Object(dbusx.Systemd, dbusx.SystemdPath).CallWithContext(ctx, dbusx.SystemdManager+".Reload", 0).Err
	assertEqual(t, dbusx.Describe(err).Error(), "org.freedesktop.DBus.Error.AccessDenied: not you")
}

func TestSubscribeAsksForTheSignalsThenForSystemdToSendThem(t *testing.T) {
	bus := dbustest.New(t)
	conn, err := dbusx.Connect(bus.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rules := []string{"type='signal',member='JobRemoved'", "type='signal',member='Reloading'"}
	if err := dbusx.Subscribe(ctx, conn, rules...); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, bus.Members(), []string{"Hello", "AddMatch", "AddMatch", "Subscribe"})
	for i, match := range bus.Calls("AddMatch") {
		assertEqual(t, dbustest.Header[string](match, dbus.FieldDestination), "org.freedesktop.DBus")
		assertEqual(t, match.Body, []any{rules[i]})
	}
	subscribe := bus.Calls("Subscribe")[0]
	assertEqual(t, dbustest.Header[string](subscribe, dbus.FieldDestination), "org.freedesktop.systemd1")
	assertEqual(t, dbustest.Header[dbus.ObjectPath](subscribe, dbus.FieldPath), dbus.ObjectPath("/org/freedesktop/systemd1"))
	assertEqual(t, dbustest.Header[string](subscribe, dbus.FieldInterface), "org.freedesktop.systemd1.Manager")
}

func TestSubscribeSaysWhichStepWasRefused(t *testing.T) {
	for member, want := range map[string]string{
		"AddMatch":  "asking for systemd's signals: org.freedesktop.DBus.Error.AccessDenied: no",
		"Subscribe": "subscribing to systemd: org.freedesktop.DBus.Error.AccessDenied: no",
	} {
		t.Run(member, func(t *testing.T) {
			bus := dbustest.New(t)
			bus.Answer(func(call *dbus.Message) dbustest.Reply {
				if dbustest.Header[string](call, dbus.FieldMember) == member {
					return dbustest.Reply{Err: dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"no"})}
				}
				return dbustest.Reply{}
			})
			conn, err := dbusx.Connect(bus.Address())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			assertEqual(t, fmt.Sprint(dbusx.Subscribe(ctx, conn, "type='signal'")), want)
		})
	}
}

func TestArgIsTheBodysArgumentOfThatType(t *testing.T) {
	body := []any{"org.freedesktop.systemd1.Unit", map[string]dbus.Variant{}, []string{"ActiveState"}}
	iface, ok := dbusx.Arg[string](body, 0)
	assertEqual(t, iface, "org.freedesktop.systemd1.Unit")
	assertEqual(t, ok, true)
	invalidated, ok := dbusx.Arg[[]string](body, 2)
	assertEqual(t, invalidated, []string{"ActiveState"})
	assertEqual(t, ok, true)

	wrong, ok := dbusx.Arg[bool](body, 0)
	assertEqual(t, wrong, false)
	assertEqual(t, ok, false)
	_, ok = dbusx.Arg[string](body, 3)
	assertEqual(t, ok, false)
	_, ok = dbusx.Arg[string](body, -1)
	assertEqual(t, ok, false)
	_, ok = dbusx.Arg[string](nil, 0)
	assertEqual(t, ok, false)
}

func TestUnitPathsAreEscapedAsSystemdEscapesThem(t *testing.T) {
	assertEqual(t, dbusx.UnitPath("sshd.service"), dbus.ObjectPath("/org/freedesktop/systemd1/unit/sshd_2eservice"))
	assertEqual(t, dbusx.UnitPath("frisket-dns.socket"), dbus.ObjectPath("/org/freedesktop/systemd1/unit/frisket_2ddns_2esocket"))
	assertEqual(t, dbusx.UnitPath("1password.service"), dbus.ObjectPath("/org/freedesktop/systemd1/unit/_31password_2eservice"))
	assertEqual(t, dbusx.UnitPath("game@25565.service"), dbus.ObjectPath("/org/freedesktop/systemd1/unit/game_4025565_2eservice"))
}

func TestUnitPathsReadBackToTheUnitsName(t *testing.T) {
	for _, name := range []string{"sshd.service", "frisket-dns.socket", "1password.service", "game@25565.service", "x_y.service", "-.mount"} {
		got, ok := dbusx.UnitName(dbusx.UnitPath(name))
		assertEqual(t, got, name)
		assertEqual(t, ok, true)
	}
}

func TestUnitNameIsOnlyForAUnitsPath(t *testing.T) {
	for _, path := range []dbus.ObjectPath{
		"/org/freedesktop/systemd1",
		"/org/freedesktop/systemd1/unit",
		"/org/freedesktop/systemd1/unit/",
		"/org/freedesktop/systemd1/unit/sshd_2eservice/more",
		"/org/freedesktop/systemd1/units/sshd_2eservice",
		"/org/freedesktop/systemd1/job/7",
	} {
		if name, ok := dbusx.UnitName(path); ok {
			t.Errorf("%s read as %q", path, name)
		}
	}
}
