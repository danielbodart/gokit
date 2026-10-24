package dbustest

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// Bus is a message bus on a Unix socket, for one test.
//
// Every method call it receives is recorded, and answered by the function
// given to Answer - except Hello, which every client says first and which is
// answered with the client's unique name. Until Answer is called, every call
// is answered with an empty reply.
type Bus struct {
	address string
	server  net.Listener

	mu      sync.Mutex
	calls   []*dbus.Message
	clients map[*client]bool
	serial  uint32
	answer  func(call *dbus.Message) Reply
	// Set by MaxPending: calls answered concurrently, and any that would
	// leave more than this many waiting refused.
	maxPending  int
	pending     int
	mostPending int
}

// Reply is the answer to a method call: a reply with Body, or the error Err
// in its place. Before, when set, is run before the reply is sent - to send a
// Signal ahead of it, say, as a bus may well deliver one first.
type Reply struct {
	Body   []any
	Err    *dbus.Error
	Before func()
}

// UniqueName is the name the bus gives every client in answer to Hello.
const UniqueName = ":1.42"

type client struct {
	conn net.Conn
	mu   sync.Mutex // one message written at a time
}

// New is a bus listening for this test, stopped and every client dropped when
// the test ends. Connect to its Address.
func New(t testing.TB) *Bus {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bus")
	server, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	bus := &Bus{
		address: "unix:path=" + path,
		server:  server,
		clients: map[*client]bool{},
		serial:  1000,
		answer:  func(*dbus.Message) Reply { return Reply{} },
	}
	go bus.accept()
	t.Cleanup(func() {
		server.Close()
		bus.Disconnect()
	})
	return bus
}

// Address is the bus's D-Bus address, such as unix:path=/tmp/.../bus - what
// DBUS_SYSTEM_BUS_ADDRESS would be set to for an application to use it.
func (b *Bus) Address() string { return b.address }

// Answer is how method calls are answered from now on.
func (b *Bus) Answer(answer func(call *dbus.Message) Reply) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.answer = answer
}

// MaxPending has calls answered concurrently, each as it arrives, and any call
// that would leave more than limit waiting for their replies refused with
// org.freedesktop.DBus.Error.LimitsExceeded - as dbus-daemon's
// max_replies_per_connection refuses them. 0, the default, answers calls one
// at a time, in order, with no limit.
func (b *Bus) MaxPending(limit int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maxPending = limit
}

// MostPending is the most calls that have been waiting for their replies at
// once, while MaxPending was set.
func (b *Bus) MostPending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mostPending
}

// Calls is every method call received with this member, in order.
func (b *Bus) Calls(member string) []*dbus.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	var found []*dbus.Message
	for _, call := range b.calls {
		if Header[string](call, dbus.FieldMember) == member {
			found = append(found, call)
		}
	}
	return found
}

// Members is the member of every method call received, in order, Hello
// included.
func (b *Bus) Members() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var members []string
	for _, call := range b.calls {
		members = append(members, Header[string](call, dbus.FieldMember))
	}
	return members
}

// Connected is how many clients are connected, having authenticated.
func (b *Bus) Connected() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// Disconnect drops every client, as a bus restart would.
func (b *Bus) Disconnect() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for client := range b.clients {
		client.conn.Close()
	}
	b.clients = map[*client]bool{}
}

// Signal sends a signal to every client, from the sender :1.1, whatever
// match rules they asked for.
func (b *Bus) Signal(path dbus.ObjectPath, iface, member string, body ...any) {
	headers := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldPath:      dbus.MakeVariant(path),
		dbus.FieldInterface: dbus.MakeVariant(iface),
		dbus.FieldMember:    dbus.MakeVariant(member),
		dbus.FieldSender:    dbus.MakeVariant(":1.1"),
	}
	if len(body) > 0 {
		headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(body...))
	}
	b.mu.Lock()
	clients := make([]*client, 0, len(b.clients))
	for client := range b.clients {
		clients = append(clients, client)
	}
	b.mu.Unlock()
	for _, client := range clients {
		b.send(client, &dbus.Message{Type: dbus.TypeSignal, Headers: headers, Body: body})
	}
}

// Header is one of a message's header fields, or T's zero value if it has
// none of that type: Header[string](call, dbus.FieldMember) is the method a
// call is to.
func Header[T any](message *dbus.Message, field dbus.HeaderField) T {
	value, _ := message.Headers[field].Value().(T)
	return value
}

func (b *Bus) accept() {
	for {
		conn, err := b.server.Accept()
		if err != nil {
			return
		}
		go b.serve(&client{conn: conn})
	}
}

func (b *Bus) serve(client *client) {
	defer client.conn.Close()
	in := bufio.NewReader(client.conn)
	if !b.authenticate(client, in) {
		return
	}
	b.mu.Lock()
	b.clients[client] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.clients, client)
	}()

	for {
		call, err := dbus.DecodeMessage(in)
		if err != nil {
			return
		}
		b.mu.Lock()
		concurrent := b.maxPending > 0
		b.mu.Unlock()
		if concurrent {
			go b.respond(client, call)
		} else {
			b.respond(client, call)
		}
	}
}

// authenticate is the server's half of SASL: refuse the bare AUTH with the
// one mechanism on offer, accept EXTERNAL, decline file descriptors.
func (b *Bus) authenticate(client *client, in *bufio.Reader) bool {
	if nul, err := in.ReadByte(); err != nil || nul != 0 {
		return false
	}
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return false
		}
		line = strings.TrimRight(line, "\r\n")
		var reply string
		switch {
		case line == "AUTH":
			reply = "REJECTED EXTERNAL"
		case strings.HasPrefix(line, "AUTH EXTERNAL"):
			reply = "OK 0123456789abcdef0123456789abcdef"
		case line == "NEGOTIATE_UNIX_FD":
			reply = "ERROR"
		case line == "BEGIN":
			return true
		default:
			reply = "ERROR"
		}
		if _, err := io.WriteString(client.conn, reply+"\r\n"); err != nil {
			return false
		}
	}
}

func (b *Bus) respond(client *client, call *dbus.Message) {
	b.mu.Lock()
	b.calls = append(b.calls, call)
	answerer := b.answer
	refuse := b.maxPending > 0 && b.pending >= b.maxPending
	if !refuse {
		b.pending++
		b.mostPending = max(b.mostPending, b.pending)
	}
	b.mu.Unlock()

	var reply Reply
	switch {
	case refuse:
		reply = Reply{Err: dbus.NewError("org.freedesktop.DBus.Error.LimitsExceeded",
			[]any{"The maximum number of pending replies per connection has been reached"})}
	case Header[string](call, dbus.FieldMember) == "Hello":
		reply = Reply{Body: []any{UniqueName}}
	default:
		reply = answerer(call)
	}
	if !refuse {
		b.mu.Lock()
		b.pending--
		b.mu.Unlock()
	}
	if reply.Before != nil {
		reply.Before()
	}

	headers := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldReplySerial: dbus.MakeVariant(call.Serial()),
		dbus.FieldDestination: dbus.MakeVariant(UniqueName),
	}
	message := &dbus.Message{Type: dbus.TypeMethodReply, Headers: headers, Body: reply.Body}
	if reply.Err != nil {
		headers[dbus.FieldErrorName] = dbus.MakeVariant(reply.Err.Name)
		message = &dbus.Message{Type: dbus.TypeError, Headers: headers, Body: reply.Err.Body}
	}
	if len(message.Body) > 0 {
		headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(message.Body...))
	}
	b.send(client, message)
}

// send writes a message with the bus's next serial. godbus will not set a
// serial on a message it did not send itself, so it is written into the
// encoded header, where it sits at offset 8.
func (b *Bus) send(client *client, message *dbus.Message) {
	var buffer bytes.Buffer
	if err := message.EncodeTo(&buffer, binary.LittleEndian); err != nil {
		panic(err)
	}
	b.mu.Lock()
	b.serial++
	binary.LittleEndian.PutUint32(buffer.Bytes()[8:12], b.serial)
	b.mu.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.conn.Write(buffer.Bytes())
}
