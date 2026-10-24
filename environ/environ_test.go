package environ

import (
	"os"
	"reflect"
	"testing"
)

func assertEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestParseReadsKeyValueEntries(t *testing.T) {
	assertEqual(t, Parse([]string{"HOME=/root", "EMPTY=", "URL=http://x/?a=b"}), Env{"HOME": "/root", "EMPTY": "", "URL": "http://x/?a=b"})
}

func TestParseSkipsWhatIsNotAVariable(t *testing.T) {
	assertEqual(t, Parse([]string{"no-equals", "=nameless", "KEPT=1"}), Env{"KEPT": "1"})
}

// os.Getenv takes the first of two entries with one name; an Env must agree.
func TestParseKeepsTheFirstOfTwoEntriesAsGetenvDoes(t *testing.T) {
	assertEqual(t, Parse([]string{"A=first", "A=second"}), Env{"A": "first"})
}

func TestCurrentIsThisProcesssEnvironment(t *testing.T) {
	t.Setenv("GOKIT_ENVIRON_TEST", "set")
	env := Current()
	assertEqual(t, env["GOKIT_ENVIRON_TEST"], "set")
	assertEqual(t, env["PATH"], os.Getenv("PATH"))
}

func TestListIsSortedEntries(t *testing.T) {
	assertEqual(t, Env{"B": "2", "A": "1", "C": ""}.List(), []string{"A=1", "B=2", "C="})
}

// Not nil, which exec.Cmd would take for "inherit this process's".
func TestListOfNothingIsEmptyNotNil(t *testing.T) {
	if list := (Env{}).List(); list == nil || len(list) != 0 {
		t.Errorf("got %#v", list)
	}
}

// What lets an application's own map[string]string, and a test's literal,
// be passed as one.
func TestAPlainMapIsAnEnv(t *testing.T) {
	plain := map[string]string{"A": "1"}
	var env Env = plain
	assertEqual(t, env.List(), []string{"A=1"})
}
