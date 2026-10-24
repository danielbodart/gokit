package logx

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Logger is where an application says what it is doing.
//
// Each call is one line, its arguments formatted as fmt.Println formats them,
// with no trailing newline of the caller's. A Logger is safe for use from
// many goroutines at once.
type Logger interface {
	// Log is commentary: what the program is doing.
	Log(args ...any)
	// Error is something gone wrong that someone should look at.
	Error(args ...any)
}

// Console logs to stdout, and errors to stderr.
//
// Every Console shares one lock, so two parts of a program that each made
// their own still never interleave a line.
func Console() Logger {
	return &writers{out: os.Stdout, errs: os.Stderr, mu: &console}
}

// console is the lock every Console shares, since they share the streams.
var console sync.Mutex

// Writers logs to out, and errors to errs, one write per line.
//
// The two share one lock, so out and errs may be the same writer: a program
// whose stdout carries only its output passes stderr as both.
func Writers(out, errs io.Writer) Logger {
	return &writers{out: out, errs: errs, mu: &sync.Mutex{}}
}

type writers struct {
	out, errs io.Writer
	mu        *sync.Mutex
}

func (w *writers) Log(args ...any) { w.write(w.out, args) }

func (w *writers) Error(args ...any) { w.write(w.errs, args) }

// write formats the line before taking the lock, and makes one write of it.
// A failed write is dropped: a logger has no one to tell.
func (w *writers) write(to io.Writer, args []any) {
	line := fmt.Sprintln(args...)
	w.mu.Lock()
	defer w.mu.Unlock()
	io.WriteString(to, line)
}

// Discard is the Logger that says nothing, for whatever was given no Logger.
var Discard Logger = discard{}

type discard struct{}

func (discard) Log(...any)   {}
func (discard) Error(...any) {}
