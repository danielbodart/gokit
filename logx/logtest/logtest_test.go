package logtest

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func assertEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestRecordsEachLineWithWhetherItWasAnError(t *testing.T) {
	var log Logger
	log.Log("Watching for changes")
	log.Error("Error:", errors.New("bus gone"))
	log.Line("Unit mc.service inactive")

	assertEqual(t, log.Entries(), []Entry{
		{Text: "Watching for changes"},
		{Error: true, Text: "Error: bus gone"},
		{Text: "Unit mc.service inactive"},
	})
	assertEqual(t, log.Lines(), []string{"Watching for changes", "Error: bus gone", "Unit mc.service inactive"})
	assertEqual(t, log.Logs(), []string{"Watching for changes", "Unit mc.service inactive"})
	assertEqual(t, log.Errors(), []string{"Error: bus gone"})
}

func TestFormatsALineAsPrintlnDoes(t *testing.T) {
	var log Logger
	log.Log("port", 8080, "open")
	assertEqual(t, log.Lines(), []string{"port 8080 open"})
}

func TestAnEntryNamesItsKind(t *testing.T) {
	assertEqual(t, Entry{Text: "GET / → 200 (3ms)"}.String(), "log: GET / → 200 (3ms)")
	assertEqual(t, Entry{Error: true, Text: "GET / → threw"}.String(), "error: GET / → threw")
}

func TestContainsSearchesLogsAndErrorsAlike(t *testing.T) {
	var log Logger
	log.Log("Removing tcp/80")
	log.Error("Error: bus gone")
	assertEqual(t, log.Contains("tcp/80"), true)
	assertEqual(t, log.Contains("bus gone"), true)
	assertEqual(t, log.Contains("nothing said"), false)
	assertEqual(t, log.Text(), "Removing tcp/80\nError: bus gone")
}

func TestErrorContainsSearchesOnlyErrors(t *testing.T) {
	var log Logger
	log.Log("bus gone, reconnecting")
	assertEqual(t, log.ErrorContains("bus gone"), false)
	log.Error("bus gone")
	assertEqual(t, log.ErrorContains("bus gone"), true)
}

func TestClearForgetsWhatWasSaid(t *testing.T) {
	var log Logger
	log.Log("before")
	log.Clear()
	log.Error("after")
	assertEqual(t, log.Entries(), []Entry{{Error: true, Text: "after"}})
}

func TestWhatItHandsBackIsACopy(t *testing.T) {
	var log Logger
	log.Log("said")
	log.Lines()[0] = "changed"
	log.Entries()[0].Text = "changed"
	assertEqual(t, log.Lines(), []string{"said"})
}

func TestRecordsFromManyGoroutinesAtOnce(t *testing.T) {
	var log Logger
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 50 {
				log.Log("line")
				log.Error("error")
			}
		})
	}
	wg.Wait()
	assertEqual(t, len(log.Logs()), 1000)
	assertEqual(t, len(log.Errors()), 1000)
}
