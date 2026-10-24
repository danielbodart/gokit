// Package logx is how an application says what it is doing: a Logger with
// two kinds of line, Log and Error.
//
// Log is commentary - what the program is doing, as it does it. Error is
// something gone wrong that someone should look at. The console puts them on
// stdout and stderr, which is how the journal, `docker logs` and a terminal
// tell them apart, and a test records which was which, so it can assert on
// what went wrong without parsing a prefix.
//
// A line is formatted as fmt.Println would format it, and written whole, in
// one write, under a lock shared by both kinds: a daemon speaks from its
// passes, its event streams and its signal handler at once, and their lines
// must never interleave. Nothing above the Logger needs a lock of its own for
// it.
//
// It is here rather than borrowed because log and log/slog answer other
// questions - timestamps, levels, structured records - that a daemon run
// under systemd or Docker, which stamp every line themselves, has no use for,
// and neither has an interface a test can stand in for.
package logx
