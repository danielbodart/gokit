package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("%s holds %q, want %q", path, data, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Errorf("%s is %v, want %v", path, info.Mode().Perm(), want)
	}
}

// assertOnly is that dir holds these names and nothing else: no temporary
// file left behind.
func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if len(got) != len(names) {
		t.Fatalf("%s holds %q, want %q", dir, got, names)
	}
	for i := range names {
		if got[i] != names[i] {
			t.Fatalf("%s holds %q, want %q", dir, got, names)
		}
	}
}

// withUmask runs the test under umask, as a process started with it would.
func withUmask(t *testing.T, umask int) {
	t.Helper()
	previous := syscall.Umask(umask)
	t.Cleanup(func() { syscall.Umask(previous) })
}

func TestWriteCreatesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Write(path, []byte(`{"pinned":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	assertContent(t, path, `{"pinned":"x"}`)
	assertOnly(t, dir, "state.json")
}

func TestWriteReplacesAnExistingFileWhole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("a much longer old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertContent(t, path, "new")
	assertOnly(t, dir, "state.json")
}

// os.WriteFile would leave a credential group- or world-readable under a
// loose umask, and a status file unreadable under a strict one.
func TestTheModeIsExactWhateverTheUmask(t *testing.T) {
	dir := t.TempDir()
	for _, umask := range []int{0o000, 0o022, 0o077} {
		withUmask(t, umask)
		for _, perm := range []os.FileMode{0o600, 0o644, 0o640} {
			path := filepath.Join(dir, "file")
			if err := Write(path, []byte("x"), perm); err != nil {
				t.Fatal(err)
			}
			assertMode(t, path, perm)
		}
	}
}

// What os.WriteFile gets wrong: it leaves an existing file's mode alone.
func TestAnExistingFilesModeIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)
}

func TestWriteFailsWhenTheDirectoryIsMissingAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "missing", "state.json"), []byte("x"), 0o644); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("got %v", err)
	}
	assertOnly(t, dir)
}

// A rename that fails, once the temporary file is written: here, a directory
// in the target's place. The temporary file goes, and what was there stays.
func TestAFailedRenameLeavesNoTemporaryFileAndTheOldContentInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.MkdirAll(filepath.Join(path, "kept"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o644); err == nil {
		t.Fatal("want an error")
	}
	assertOnly(t, dir, "state.json")
	assertOnly(t, path, "kept")
}

// A failure before anything is written - here, a directory it may not write
// in - leaves the old file as it was.
func TestAFailedWriteLeavesTheOldContent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes in a read-only directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := Write(path, []byte("new"), 0o644); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("got %v", err)
	}
	assertContent(t, path, "old")
	assertOnly(t, dir, "state.json")
}

func TestCreateMakesAFileThatWasNotThere(t *testing.T) {
	withUmask(t, 0o077)
	dir := t.TempDir()
	path := filepath.Join(dir, "closure.conf")
	if err := Create(path, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertContent(t, path, "[Service]\n")
	assertMode(t, path, 0o644)
	assertOnly(t, dir, "closure.conf")
}

// The first writer wins: a file already there is not replaced.
func TestCreateLeavesAnExistingFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "closure.conf")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Create(path, []byte("second"), 0o644); !errors.Is(err, fs.ErrExist) {
		t.Errorf("got %v", err)
	}
	assertContent(t, path, "first")
	assertOnly(t, dir, "closure.conf")
}
