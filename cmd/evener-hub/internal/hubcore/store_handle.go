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
// there, so an index.db restored, replaced, or deleted and recreated
// underneath the hub must not stay invisible behind a handle that still
// points at the old inode.
type keeperHandle struct {
	mu   sync.Mutex
	db   *sql.DB
	file os.FileInfo
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
		// The file this handle was opened on is gone or replaced. Drop it so
		// the open below reads the file that is there now.
		_ = h.db.Close()
		h.db, h.file = nil, nil
	}
	db, err := open()
	if err != nil {
		return nil, err
	}
	h.db = db
	if info, err := fsys.Stat(path); err == nil {
		h.file = info
	}
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

// close releases the shared handle and forgets it, so a later call reopens
// instead of using a closed handle.
func (h *keeperHandle) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.db == nil {
		return nil
	}
	db := h.db
	h.db, h.file = nil, nil
	return db.Close()
}
