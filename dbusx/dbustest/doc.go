// Package dbustest is a message bus for tests: a D-Bus server on a Unix
// socket in the test's temporary directory, that answers method calls as the
// test says, records them, and sends signals when it is told to.
//
// Real enough that godbus cannot tell - the SASL handshake, framed messages
// both ways, replies matched by serial - so what a test of an application's
// D-Bus adapter tests is that adapter's conversation with a bus: what it
// asks, in what order, how many at once, and what it makes of the answers and
// the signals. Not godbus, which has tests of its own, and not a real bus,
// which a test cannot make systemd say things on.
package dbustest
