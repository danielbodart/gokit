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

## Rules

- **The standard library only.** `go.mod` has no requirements, and keeps none:
  anything that needs a dependency, such as D-Bus, stays in the application.
- **Every leaf is injectable.** Each package hands an application an interface
  or a func type its tests can fake - a `RoundTripper`, a `Clock`, a
  `sockdiag.Lister` - and no package knows what the application is for.
- **Tests run without the world.** Fakes are `Handler`s and stopped clocks.
  Tests that need the real kernel - its sockets, its cgroupfs - run on any
  Linux host and skip cleanly where it is missing, such as Nix's build sandbox,
  which has no `/sys`.

```sh
mise exec -- go test ./...
```

## License

Apache License 2.0
