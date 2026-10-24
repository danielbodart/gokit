package dbusx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// DefaultSystemBus is where the system bus is when nothing says otherwise.
const DefaultSystemBus = "unix:path=/run/dbus/system_bus_socket"

// SystemBusAddress is the system bus's address from DBUS_SYSTEM_BUS_ADDRESS
// in env, the variable libdbus and sd-bus read, or DefaultSystemBus when that
// is unset or empty.
//
// Only unix: addresses are taken, which is all a system bus has ever been:
// anything else is far more likely a mistake than a bus. Of a list of
// addresses, separated by ;, the unix: ones are kept, in order, and it is an
// error for there to be none.
func SystemBusAddress(env map[string]string) (string, error) {
	address := env["DBUS_SYSTEM_BUS_ADDRESS"]
	if address == "" {
		address = DefaultSystemBus
	}
	var usable []string
	for candidate := range strings.SplitSeq(address, ";") {
		if strings.HasPrefix(candidate, "unix:") {
			usable = append(usable, candidate)
		}
	}
	if len(usable) == 0 {
		return "", fmt.Errorf("cannot use DBUS_SYSTEM_BUS_ADDRESS '%s': only unix: addresses are supported", address)
	}
	return strings.Join(usable, ";"), nil
}

// Connect is a connection to the bus at address, authenticated and said
// Hello to, whose signals are handed over in the order the bus sent them.
//
// In order because godbus's default signal handler hands a busy reader's
// signals to goroutines of their own, so a unit's changes can arrive out of
// the order systemd made them in. options come after, so one of them may
// still choose another handler. A failure says the address it was going to.
func Connect(address string, options ...dbus.ConnOption) (*dbus.Conn, error) {
	options = append([]dbus.ConnOption{dbus.WithSignalHandler(dbus.NewSequentialSignalHandler())}, options...)
	conn, err := dbus.Connect(address, options...)
	if err != nil {
		return nil, fmt.Errorf("D-Bus at %s: %w", address, err)
	}
	return conn, nil
}

// Describe adds the error's name to a D-Bus error, whose own text is only its
// message - "Unit x.service not loaded." says less without
// org.freedesktop.systemd1.NoSuchUnit. Anything else is returned as it is.
//
// godbus returns its Error both as a value, for an error reply, and as a
// pointer, from dbus.NewError and its own failures; either is named.
func Describe(err error) error {
	var named dbus.Error
	if errors.As(err, &named) {
		return fmt.Errorf("%s: %w", named.Name, err)
	}
	var pointer *dbus.Error
	if errors.As(err, &pointer) && pointer != nil {
		return fmt.Errorf("%s: %w", pointer.Name, err)
	}
	return err
}

// Names on the bus that every client of systemd uses.
const (
	// Systemd is systemd's bus name, and SystemdPath its Manager's object.
	Systemd     = "org.freedesktop.systemd1"
	SystemdPath = dbus.ObjectPath("/org/freedesktop/systemd1")
	// SystemdManager is the Manager's interface, and SystemdUnit every unit's.
	SystemdManager = "org.freedesktop.systemd1.Manager"
	SystemdUnit    = "org.freedesktop.systemd1.Unit"
	// Properties is the standard interface a property is read through, and
	// whose PropertiesChanged says it changed.
	Properties = "org.freedesktop.DBus.Properties"
	// UnitPathPrefix is the object path every unit's lies beneath.
	UnitPathPrefix = "/org/freedesktop/systemd1/unit"
)

// Subscribe asks the bus for the signals these match rules select - such as
// "type='signal',sender='org.freedesktop.systemd1',member='JobRemoved'" - and
// then asks systemd to send them.
//
// Without Manager.Subscribe systemd sends no signals about units or jobs at
// all. Any user may ask, and both last as long as the connection does: the
// way to stop is to close it. The rules come first so that nothing systemd
// says once subscribed is missed.
func Subscribe(ctx context.Context, conn *dbus.Conn, rules ...string) error {
	bus := conn.BusObject()
	for _, rule := range rules {
		if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.AddMatch", 0, rule).Err; err != nil {
			return fmt.Errorf("asking for systemd's signals: %w", Describe(err))
		}
	}
	if err := conn.Object(Systemd, SystemdPath).CallWithContext(ctx, SystemdManager+".Subscribe", 0).Err; err != nil {
		return fmt.Errorf("subscribing to systemd: %w", Describe(err))
	}
	return nil
}

// Arg is the argument at index of a message's body, such as a signal's, if
// there is one there of type T: a signal from a bus is whatever its sender
// made it, so its shape is checked, not assumed.
func Arg[T any](body []any, index int) (T, bool) {
	var zero T
	if index < 0 || index >= len(body) {
		return zero, false
	}
	value, ok := body[index].(T)
	return value, ok
}

// UnitPath is a unit's object path, such as
// /org/freedesktop/systemd1/unit/sshd_2eservice, escaped as systemd escapes
// it: every byte that is not a letter or digit, and a leading digit, becomes
// `_` and two hex digits.
//
// Where a call can ask systemd for the path instead - LoadUnit, GetUnit - its
// answer is the authority; this is for when it cannot, or should not wait to.
func UnitPath(name string) dbus.ObjectPath {
	var escaped strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			escaped.WriteByte(c)
		} else {
			fmt.Fprintf(&escaped, "_%02x", c)
		}
	}
	return dbus.ObjectPath(UnitPathPrefix + "/" + escaped.String())
}

// UnitName is the reverse of UnitPath: the name of the unit whose object path
// this is, or false if it is not a unit's - not directly beneath
// UnitPathPrefix.
func UnitName(path dbus.ObjectPath) (string, bool) {
	escaped, ok := strings.CutPrefix(string(path), UnitPathPrefix+"/")
	if !ok || escaped == "" || strings.Contains(escaped, "/") {
		return "", false
	}
	var name []byte
	for i := 0; i < len(escaped); i++ {
		if escaped[i] == '_' && i+2 < len(escaped) && hex(escaped[i+1]) && hex(escaped[i+2]) {
			name = append(name, unhex(escaped[i+1])<<4|unhex(escaped[i+2]))
			i += 2
			continue
		}
		name = append(name, escaped[i])
	}
	return string(name), true
}

func hex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'a' + 10
}
