package logx

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// writes records each Write as one entry, so a test sees how a line arrived,
// not only what it said, and whether any two writes were ever in flight at
// once.
type writes struct {
	active  atomic.Int32
	overlap atomic.Bool
	mu      sync.Mutex
	entries []string
}

func (w *writes) Write(p []byte) (int, error) {
	if w.active.Add(1) > 1 {
		w.overlap.Store(true)
	}
	defer w.active.Add(-1)
	// Gives another writer the chance to arrive mid-write, were the logger
	// to let it.
	runtime.Gosched()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, string(p))
	return len(p), nil
}

func TestWritersPutsLogsOnOutAndErrorsOnErrs(t *testing.T) {
	var out, errs bytes.Buffer
	logger := Writers(&out, &errs)
	logger.Log("Watching for changes")
	logger.Error("Error:", errors.New("bus gone"))

	if out.String() != "Watching for changes\n" {
		t.Errorf("out is %q", out.String())
	}
	if errs.String() != "Error: bus gone\n" {
		t.Errorf("errs is %q", errs.String())
	}
}

func TestALineIsFormattedAsPrintlnFormatsIt(t *testing.T) {
	var out bytes.Buffer
	Writers(&out, &out).Log("port", 8080, "is", true)
	if out.String() != "port 8080 is true\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestALineIsOneWriteEvenWhenItSpansSeveral(t *testing.T) {
	w := &writes{}
	Writers(w, w).Log("\nReceived SIGINT, shutting down...")
	if len(w.entries) != 1 || w.entries[0] != "\nReceived SIGINT, shutting down...\n" {
		t.Errorf("got %q", w.entries)
	}
}

// Many goroutines, logs and errors to the one writer: every line arrives
// whole, in a write of its own, and no two writes overlap.
func TestLinesFromManyGoroutinesNeverInterleave(t *testing.T) {
	w := &writes{}
	logger := Writers(w, w)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			for range 20 {
				if i%2 == 0 {
					logger.Log("a line from", i)
				} else {
					logger.Error("an error from", i)
				}
			}
		})
	}
	wg.Wait()

	if w.overlap.Load() {
		t.Error("two writes overlapped")
	}
	if len(w.entries) != 1000 {
		t.Fatalf("got %d writes", len(w.entries))
	}
	for _, entry := range w.entries {
		if strings.Count(entry, "\n") != 1 || !strings.HasSuffix(entry, "\n") {
			t.Errorf("a write that is not one line: %q", entry)
		}
	}
}

func TestConsoleIsALogger(t *testing.T) {
	var _ Logger = Console()
}

func TestDiscardSaysNothingAndDoesNotFail(t *testing.T) {
	Discard.Log("nothing")
	Discard.Error("nothing")
}
