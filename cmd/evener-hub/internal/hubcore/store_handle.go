package hubcore

import (
	"database/sql"
	"os"
	"sync"

	"github.com/spf13/afero"
)

// keeperHandle is one SQLite file's process-lifetime *sql.DB. The first call
// that needs it creates it, and every later call reuses it, so a call pays the
// connect, the DDL and the schema check once instead of once per call.
//
// It caches the file, it is not bound to one inode. Before each reuse get
// checks that the path still names the file the handle was opened on, and
// reopens when it does not: a per-call open always read whatever file was
// there, so an index.db deleted and recreated, or replaced by a rename,
// underneath the hub must not stay invisible behind a handle that still
// points at the old inode. The check compares inodes, so a rewrite of the
// same inode (a restore over the file in place) is not a replacement and
// keeps the handle.
type keeperHandle struct {
	mu   sync.Mutex
	db   *sql.DB
	file os.FileInfo
	// retired holds handles that were replaced. They are not closed at the
	// moment of replacement because a caller that already received one may
	// still be reading through it; closing it there turns that read into
	// "sql: database is closed". database/sql keeps no finalizer, so an
	// abandoned *sql.DB would instead hold its opener goroutine and its
	// connections for the life of the process, hence the list.
	retired []*sql.DB
}

// get returns the shared handle, opening it on first use and reopening it when
// path no longer names the file the current handle was opened on.
func (h *keeperHandle) get(path string, fsys afero.Fs, open func() (*sql.DB, error)) (*sql.DB, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.db != nil {
		if h.stillCurrent(path, fsys) {
			return h.db, nil
		}
		// The file this handle was opened on is gone or replaced. Retire it so
		// the open below reads the file that is there now, without closing a
		// handle another caller may be mid-query on.
		h.retired = append(h.retired, h.db)
		h.db, h.file = nil, nil
	}
	// Stat around the open, not merely after it: a replacement landing between
	// the connect and a single trailing Stat would record the replacement's
	// identity for a pool connected to the predecessor, and every later check
	// would then pass over a handle reading a file nothing writes any more.
	// The identity is cached only when the path named one file throughout the
	// open, which takes an identity on both sides of it.
	before, beforeErr := fsys.Stat(path)
	db, err := open()
	if err != nil {
		return nil, err
	}
	after, err := fsys.Stat(path)
	if err != nil {
		// No identity to compare against, so do not cache: the next call
		// reopens, which is what a per-call open did. Retire it so shutdown
		// still closes a pool that no caller will ever look up again.
		h.retired = append(h.retired, db)
		return db, nil
	}
	if beforeErr != nil {
		// The path named no file before the open, so the open created the one
		// this pool connected to and there is no before-identity to hold it to.
		// A replacement landing in that window would otherwise be recorded as
		// the identity of a pool on the file the open created. Decline for the
		// same reason as a detected swap; the next call finds the file present
		// and caches it then.
		h.retired = append(h.retired, db)
		return db, nil
	}
	if !os.SameFile(before, after) {
		// The path named one file before the open and another after it, so this
		// pool is on the predecessor while the path now names the replacement.
		// Hand the connection out but do not cache this identity; retire it so
		// shutdown closes it.
		h.retired = append(h.retired, db)
		return db, nil
	}
	h.db, h.file = db, after
	return db, nil
}

// stillCurrent reports whether path still names the file the handle was opened
// on. A path that cannot be stat'd is not current, so the next call reopens
// rather than serving an inode the path no longer names.
func (h *keeperHandle) stillCurrent(path string, fsys afero.Fs) bool {
	info, err := fsys.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(h.file, info)
}

// close releases the shared handle and every retired one, and forgets them, so
// a later call reopens instead of using a closed handle.
func (h *keeperHandle) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var err error
	handles := h.retired
	if h.db != nil {
		handles = append(handles, h.db)
	}
	h.db, h.file, h.retired = nil, nil, nil
	for _, db := range handles {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}
