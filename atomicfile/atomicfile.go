package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with data, at mode perm exactly, atomically and
// durably: a reader, or the disk after a power cut, sees the old file or the
// new one whole.
//
// On any failure before the rename, path is untouched and the temporary file
// removed. A failure to flush the directory after it is an error too - the
// new content is in place, but not yet known to survive a power cut.
func Write(path string, data []byte, perm os.FileMode) error {
	temporary, err := staged(path, data, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// Create is Write for a file that must not already exist: when path is there
// it is left as it is, and the error satisfies errors.Is(err, fs.ErrExist).
//
// For a file two writers may race to create, where the first must win -
// link(2) rather than rename(2), which would let the second replace it.
func Create(path string, data []byte, perm os.FileMode) error {
	temporary, err := staged(path, data, perm)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Link(temporary, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// staged is data in a temporary file of its own beside path, at mode perm,
// flushed to disk: ready to move into place. Its name is unique, so two
// writers - two goroutines, or two processes pointed at one path by mistake -
// never write into each other's.
func staged(path string, data []byte, perm os.FileMode) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", err
	}
	temporary := file.Name()
	fail := func(err error) (string, error) {
		file.Close()
		os.Remove(temporary)
		return "", err
	}
	// CreateTemp makes it 0600; set what was asked for, on the file itself,
	// so the umask has no say.
	if err := file.Chmod(perm); err != nil {
		return fail(err)
	}
	if _, err := file.Write(data); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if err := file.Close(); err != nil {
		os.Remove(temporary)
		return "", err
	}
	return temporary, nil
}

// syncDir flushes a directory, so a rename or link in it survives a power
// cut.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("flushing %s: %w", path, err)
	}
	return nil
}
