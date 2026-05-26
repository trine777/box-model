package cli

import (
	"io"
	"os"
)

// stdoutIsTTY reports whether w is an attached terminal. We deliberately
// avoid pulling in golang.org/x/term: a bare os.File.Stat() interrogation is
// enough to distinguish a real /dev/tty (ModeCharDevice) from a regular file,
// pipe, or in-memory test buffer.
//
// Tests pass a *bytes.Buffer for rc.stdout; the type assertion fails and we
// return false, so test output stays deterministic (no ANSI escapes).
func stdoutIsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
