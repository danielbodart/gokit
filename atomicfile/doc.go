// Package atomicfile replaces a file whole, so that whoever reads it - a
// crash, a power cut, another process - finds the old content or the new,
// never half of one, and never an empty file.
//
// A rename is atomic, but without an fsync the kernel may put the rename on
// disk before the data it names, and a power cut in between leaves a file of
// the right name and no content: the state store that a pin depended on,
// gone empty. So the data is written to a temporary file beside the target,
// flushed to disk, renamed over the target, and the directory flushed too,
// so the rename itself survives.
//
// The mode is set on the file itself, not left to the umask, which
// os.WriteFile honours: a credential file asked for at 0600 is 0600, and a
// status file asked for at 0644 is readable by the reader it is for,
// whatever the process's umask.
//
// The directory must already exist. Whether it should be created, and with
// what mode - 0700 for a credential's, 0755 for a status file's - is the
// caller's to say, with os.MkdirAll, not something to guess here.
package atomicfile
