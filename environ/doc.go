// Package environ is a process's environment as a map, handed to whatever
// reads it rather than read from the process.
//
// A daemon's configuration, systemd's socket activation and its notify
// socket all come from environment variables. Read through os.Getenv, a test
// can only set them for the whole process, one test at a time; taken as an
// Env, a test passes a map literal of its own and runs in parallel with every
// other. Main reads the real one once, with Current, and passes it down.
//
// It is a package of its own, rather than part of the one package that first
// needed it, because every part of an application takes one: configuration,
// systemd, and what a program hands a command it runs.
package environ
