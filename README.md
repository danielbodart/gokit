# gokit

Small helpers shared by Dan Bodart's Go daemons -
[Portical](https://github.com/danielbodart/portical), Tidewaiter and
Treesinger - pulled out once a second one needed them.

```sh
go get github.com/danielbodart/gokit
```

## Packages

**[httpx](httpx)** joins `http.Handler` and `http.RoundTripper`. Every client
takes an `*http.Client` and every fake is a `Handler`, so `HandlerTransport`
serves a request in memory and `StreamingHandlerTransport` does it for a
response that never ends, such as an event stream. `UnixSocket` reaches a
server such as Docker's over its socket, and `Check` turns a failed response
into an error carrying the server's own words.

**[clock](clock)** is the one source of time for everything that waits. `System`
is the real clock; `Stopped` only moves on `Advance`, and `BlockUntil` says a
goroutine has reached its wait, so a test never sleeps for real.

**[sockdiag](sockdiag)** lists the host's listening TCP and bound UDP sockets,
IPv4 and IPv6, with the cgroup that created each, by asking the kernel over
NETLINK_SOCK_DIAG - what `ss -Hlntu -e` shows, with no `ss` to install or parse.
`Listener.Within` puts a socket to a systemd unit or a container by its
cgroup. Where the sockets can be listed but their cgroups cannot be told (no
cgroup2, a kernel before 5.7) the listing still comes back, with an error
wrapping `ErrUnknownCgroups`. Linux only, unprivileged.

**[conntrack](conntrack)** reads the kernel's connection tracking table over
ctnetlink - what `conntrack -L` shows, with no conntrack-tools to install or
parse. Every flow, IPv4 and IPv6, any protocol: both directions' addresses and
ports (so NAT shows), status flags such as `Assured`, the TCP state by
conntrack-tools' names, the timeout, mark, zone, id and, where accounting is
on, counters. Parsed strictly: a flow that does not hold together fails the
dump rather than going missing from it. Linux only; it needs `CAP_NET_ADMIN`,
and without it the error is `errors.Is(err, os.ErrPermission)`.

**[dbusx](dbusx)** is what talking to systemd over the system bus with
[godbus](https://github.com/godbus/dbus) takes outside an application's own
questions: the system bus's address from `DBUS_SYSTEM_BUS_ADDRESS`, a
connection whose signals arrive in order, `Describe` to name a D-Bus error,
a unit's object path and back, subscribing to systemd's signals, and `Arg` to
read a signal's body. **[dbusx/dbustest](dbusx/dbustest)** is a fake bus on a
Unix socket - real enough for godbus - that answers, records and signals as a
test says, and refuses calls past a limit as dbus-daemon does.

dbusx is a module of its own, because it needs godbus, so that the rest stays
dependency-free. It is fetched and tagged on its own, as `dbusx/vX.Y.Z`:

```sh
go get github.com/danielbodart/gokit/dbusx
```

## Rules

- **The standard library only.** The root `go.mod` has no requirements, and
  keeps none. What needs a dependency is a module of its own, as dbusx is for
  godbus, so an application takes on only the dependencies it asked for.
- **Every leaf is injectable.** Each package hands an application an interface
  or a func type its tests can fake - a `RoundTripper`, a `Clock`, a
  `sockdiag.Lister`, a `conntrack.Dumper` - and no package knows what the
  application is for.
- **Tests run without the world.** Fakes are `Handler`s, stopped clocks and
  dbustest's bus. Tests that need the real kernel - its sockets, its cgroupfs,
  its conntrack table - run on any Linux host and skip cleanly where it is
  missing or closed to them, such as Nix's build sandbox, which has no `/sys`,
  or a user without `CAP_NET_ADMIN`.

```sh
mise exec -- go test ./...
(cd dbusx && mise exec -- go test ./...)
```

## License

Apache License 2.0
