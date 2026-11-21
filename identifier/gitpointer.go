package identifier

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNotRegularFile reports that a path exists but is not a regular file, so
// reading it would either block (a FIFO with no writer, a socket) or mean
// something other than data (a device). Callers that can answer without the
// bytes check for it with errors.Is instead of matching the message.
var ErrNotRegularFile = errors.New("not a regular file")

// ErrGitPointerTooLarge reports that a ".git" pointer file exceeded the size a
// pointer can occupy. It is neither read whole nor parsed.
var ErrGitPointerTooLarge = errors.New("git pointer file is larger than any pointer")

// gitPointerMaxBytes bounds a .git pointer read. A pointer is one path line, so
// this is generous by three orders of magnitude; a longer file is not a pointer
// and is refused rather than loaded into the daemon's memory.
const gitPointerMaxBytes = 4096

// ReadGitPointerFile reads a .git pointer file. The leaf may be a symlink (a
// linked checkout whose .git points at a shared pointer file is legitimate), so
// the link is resolved first; the resolved target must then be a regular file,
// opened nonblocking, so a FIFO with no writer or a device returns
// ErrNotRegularFile instead of hanging the caller. A socket is refused earlier,
// by the open itself (ENXIO), and so is an unresolvable leaf symlink; both fail
// with the OS error rather than ErrNotRegularFile. A regular file that is really
// a pseudo-filesystem stream — /proc/kmsg and anything reachable through a link
// to it — stats regular and can block in read(2) forever, so it is refused on
// the OPENED DESCRIPTOR, which no swap between stat and open can hide. The read
// is capped at gitPointerMaxBytes and a longer file is refused with
// ErrGitPointerTooLarge.
//
// Every reader of a workspace-controlled .git entry needs exactly this guard —
// the Git-root resolver, sandbox policy classification, and project identity
// resolution — so it lives here, where those readers can reach it without an
// import cycle, and the symlink allowance, the regular-file requirement, and
// the size cap cannot drift apart. This package stays stdlib-only.
func ReadGitPointerFile(path string) ([]byte, error) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	} else if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		// A leaf symlink that does not resolve is not a pointer this walk can
		// read, and it must not be opened to find that out.
		return nil, fmt.Errorf("open %q: %w", path, ErrNotRegularFile)
	}
	f, err := OpenRegularNoFollow(target)
	if err != nil {
		return nil, err
	}
	data, readErr := readGitPointerDescriptor(path, f)
	// The descriptor is read-only, so Close cannot lose buffered writes and its
	// error cannot change what the read returned: the bytes are already in hand
	// or the read already failed with the reason that matters. It is still not
	// dropped silently — a failure here is surfaced only when nothing else went
	// wrong, so it never masks the refusal that made the caller fall back.
	closeErr := f.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %q: %w", path, closeErr)
	}
	return data, nil
}

// readGitPointerDescriptor reads a pointer from an already-validated descriptor.
// It refuses a descriptor on a pseudo filesystem first: a genuine .git pointer
// never lives on procfs, sysfs, or devtmpfs, and a file there stats regular
// while its read(2) can block indefinitely, so the byte cap bounds the bytes
// but not the wait. The mount is read from the OPENED descriptor, so no swap
// between stat and open can hide it.
func readGitPointerDescriptor(path string, f *os.File) ([]byte, error) {
	if FDOnPseudoMount(int(f.Fd())) {
		return nil, fmt.Errorf("open %q: %w", path, ErrNotRegularFile)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, gitPointerMaxBytes+1))
	if readErr != nil {
		return nil, readErr
	}
	if len(data) > gitPointerMaxBytes {
		return nil, fmt.Errorf("read %q: %w", path, ErrGitPointerTooLarge)
	}
	return data, nil
}
