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
into an error carrying the server's own words. What a client does around every
request is a `Decorator`, applied in order by `Decorate`: `Identifying` sets
its User-Agent, `Retrying` retries transport errors, 429s and 5xx with
exponential backoff and honours `Retry-After` up to a cap, and `Logging` logs
`METHOD URL → status (Nms)`.

**[clock](clock)** is the one source of time for everything that waits. `System`
is the real clock; `Stopped` only moves on `Advance`, and `BlockUntil` says a
goroutine has reached its wait, so a test never sleeps for real. `Instant`
releases every wait at once and records it, for a test that asserts on how
long something waited rather than stepping through it.

**[logx](logx)** is how an application says what it is doing: a `Logger` of
`Log` for commentary and `Error` for what went wrong. `Console` puts them on
stdout and stderr, `Writers` on any two writers (the same one twice, for a
program whose stdout is only its output), each line formatted as `fmt.Println`
does and written whole, so lines from many goroutines never interleave.
**[logx/logtest](logx/logtest)** records every line, and whether it was an
error, for a test to assert on.

**[await](await)** is `Until`, for a test waiting on a goroutine it does not
own to get round to something - a stream opened, a line said - and never for
letting time pass, which is a `clock.Stopped`'s job.

**[environ](environ)** is the environment as a map, `Env`, read once by
`Current` and passed down, so a test hands configuration a map literal rather
than setting the process's variables; `List` is it back as `KEY=value` for a
command to run with.

**[systemd](systemd)** is systemd's protocol with the services it starts,
without libsystemd: `Activated` takes the sockets a socket unit passes
(LISTEN_PID, LISTEN_FDS, LISTEN_FDNAMES) strictly - refusing what is not a
listening socket - and `Listeners` does it for this process, clearing the
variables after. `Notify` sends sd_notify states - `Ready`, `Reloading` with
`Monotonic`, `Stopping`, `Watchdog`, `Status` - in one datagram to
NOTIFY_SOCKET, and does nothing outside systemd; `WatchdogInterval` says how
often the watchdog wants to hear. Linux only.

**[atomicfile](atomicfile)** replaces a file whole and durably: `Write` puts
the data in a temporary file beside it at exactly the mode asked for, whatever
the umask, flushes it, renames it over, and flushes the directory, so a crash
or a power cut leaves the old file or the new, never an empty one. `Create` is
the same for a file that must not already exist.

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
dependency-free. It is fetched on its own, and tagged `dbusx/vX.Y.Z` at the
same version as the root module:

```sh
go get github.com/danielbodart/gokit/dbusx
```

## Rules

- **The standard library only.** The root `go.mod` has no requirements, and
  keeps none. What needs a dependency is a module of its own, as dbusx is for
  godbus, so an application takes on only the dependencies it asked for.
- **Every leaf is injectable.** Each package hands an application an interface
  or a func type its tests can fake - a `RoundTripper`, a `Clock`, a
  `logx.Logger`, an `environ.Env`, a `sockdiag.Lister`, a `conntrack.Dumper` -
  and no package knows what the application is for.
- **Tests run without the world.** Fakes are `Handler`s, stopped clocks,
  logtest's logger and dbustest's bus. Tests that need the real kernel - its
  sockets, its cgroupfs, its conntrack table - run on any Linux host and skip
  cleanly where it is missing or closed to them, such as Nix's build sandbox,
  which has no `/sys`, or a user without `CAP_NET_ADMIN`.

```sh
./run check   # gofmt, and vet on amd64, arm, arm64 and 386, in both modules
./run test    # the tests in both modules, natively and on 386
```

## Versions

Every green build of `main` is a release, tagged by CI: `vMAJOR.MINOR.PATCH`
for the root module and `dbusx/vMAJOR.MINOR.PATCH` for dbusx, both at the
same commit, so nothing is bumped by hand. It is the scheme Portical,
Tidewaiter and Treesinger use:

- **MAJOR** is the one number committed, in `MAJOR`, because moving it is a
  decision: it says compatibility broke. Past 1, Go also wants the module path
  to end in `/vN`.
- **MINOR** is the commit count, so it only goes up and names one commit.
- **PATCH** is the CI run number, which tells a re-run from the first build.

`./run version` prints it; locally the patch is a timestamp instead. CI then
asks the module proxy for each new tag, so the proxy has it before any
application looks.

## License

Apache License 2.0
