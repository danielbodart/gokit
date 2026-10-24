package logtest

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/danielbodart/gokit/logx"
)

// Logger records every line it is given. The zero value is ready to use, and
// it is safe for use from many goroutines at once.
type Logger struct {
	mu      sync.Mutex
	entries []Entry
}

var _ logx.Logger = (*Logger)(nil)

// Entry is one recorded line.
type Entry struct {
	// Whether it was an Error rather than a Log.
	Error bool
	// The line as logx formats it, without the trailing newline.
	Text string
}

// String is the line with its kind, such as "error: bus gone", for a
// failure message or a comparison of a whole log.
func (e Entry) String() string {
	if e.Error {
		return "error: " + e.Text
	}
	return "log: " + e.Text
}

func (l *Logger) Log(args ...any) { l.record(false, args) }

func (l *Logger) Error(args ...any) { l.record(true, args) }

// Line logs line, for a seam that still takes a func(string).
func (l *Logger) Line(line string) { l.Log(line) }

func (l *Logger) record(isError bool, args []any) {
	text := strings.TrimSuffix(fmt.Sprintln(args...), "\n")
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, Entry{Error: isError, Text: text})
}

// Entries is every line recorded since the last Clear, with its kind, in
// the order they were said.
func (l *Logger) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

// Lines is every line recorded since the last Clear, logs and errors alike,
// in the order they were said.
func (l *Logger) Lines() []string { return l.texts(func(Entry) bool { return true }) }

// Logs is only the lines that were Logs.
func (l *Logger) Logs() []string { return l.texts(func(e Entry) bool { return !e.Error }) }

// Errors is only the lines that were Errors.
func (l *Logger) Errors() []string { return l.texts(func(e Entry) bool { return e.Error }) }

func (l *Logger) texts(keep func(Entry) bool) []string {
	var texts []string
	for _, entry := range l.Entries() {
		if keep(entry) {
			texts = append(texts, entry.Text)
		}
	}
	return texts
}

// Text is every line, logs and errors alike, joined with newlines.
func (l *Logger) Text() string { return strings.Join(l.Lines(), "\n") }

// Contains is whether any line, log or error, contains text.
func (l *Logger) Contains(text string) bool { return strings.Contains(l.Text(), text) }

// ErrorContains is whether any line that was an Error contains text.
func (l *Logger) ErrorContains(text string) bool {
	return strings.Contains(strings.Join(l.Errors(), "\n"), text)
}

// Clear forgets everything recorded so far.
func (l *Logger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
}
