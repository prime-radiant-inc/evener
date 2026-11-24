package execenv

import (
	"os"

	"primeradiant.com/evener/identifier"
)

// ErrNotRegularFile reports that a path exists but is not a regular file, so
// reading it would either block (a FIFO with no writer, a socket) or mean
// something other than data (a device). Callers that can answer without the
// bytes check for it with errors.Is instead of matching the message. It is the
// identifier package's value, so a refusal from ReadGitPointerFile matches it.
var ErrNotRegularFile = identifier.ErrNotRegularFile

// ErrGitPointerTooLarge reports that a ".git" pointer file exceeded the size a
// pointer can occupy. It is neither read whole nor parsed. It is the identifier
// package's value, so a refusal from ReadGitPointerFile matches it.
var ErrGitPointerTooLarge = identifier.ErrGitPointerTooLarge

// OpenRegularNoFollow opens path for reading through the identifier package's
// guard: one descriptor, validated before any read, refusing a symlink at the
// leaf, a FIFO with no writer, a device, and any other non-regular entry
// without blocking. The guard lives there because this package's readers and
// the .git pointer reader both need it and only identifier is importable by
// both; this name stays for the callers and tests that already use it.
func OpenRegularNoFollow(path string) (*os.File, error) {
	return identifier.OpenRegularNoFollow(path)
}
