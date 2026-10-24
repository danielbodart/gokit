// Package dbusx is what talking to systemd over the system bus with godbus
// takes that is not about what an application asks systemd for: where the
// system bus is, a connection whose signals arrive in the order they were
// sent, a D-Bus error that says its own name, a unit's object path and back,
// subscribing to systemd's signals, and reading a signal's body.
//
// It is a module of its own, github.com/danielbodart/gokit/dbusx, so that the
// rest of gokit keeps to the standard library: this is the one part that
// needs a dependency, godbus. Its tests' fake bus is dbustest.
package dbusx
