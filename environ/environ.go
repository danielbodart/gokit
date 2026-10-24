package environ

import (
	"maps"
	"os"
	"slices"
	"strings"
)

// Env is an environment, by variable name.
//
// A named map rather than a struct, so a map[string]string - a literal in a
// test, or a field an application already has - is an Env as it stands, and
// a variable that is not there reads as "", as it does from os.Getenv.
type Env map[string]string

// Current is this process's environment.
func Current() Env {
	return Parse(os.Environ())
}

// Parse is an environment from its KEY=value entries, as os.Environ and
// exec.Cmd's Env write it.
//
// An entry without an "=" is not a variable, and is skipped. Where a name
// appears twice the first entry wins, as it does for os.Getenv, so an Env
// read from this process never disagrees with the process itself.
func Parse(entries []string) Env {
	env := Env{}
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" {
			continue
		}
		if _, seen := env[name]; !seen {
			env[name] = value
		}
	}
	return env
}

// List is the environment as KEY=value entries, as exec.Cmd and syscall.Exec
// take it, sorted by name so the same environment is always the same list.
//
// An empty Env is an empty list, never nil: exec.Cmd reads a nil Env as
// "inherit this process's", the opposite of an environment with nothing in
// it.
func (env Env) List() []string {
	list := make([]string, 0, len(env))
	for _, name := range slices.Sorted(maps.Keys(env)) {
		list = append(list, name+"="+env[name])
	}
	return list
}
