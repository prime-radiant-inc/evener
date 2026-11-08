// Package fsfake provides filesystem test doubles for agent tests.
package fsfake

import (
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/afero"
)

// SyncFS records the Sync calls made through it, by the name of the file or
// directory handle that was synced, and records every Rename. FailOpen, when
// set, fails Open for a path it returns true for.
type SyncFS struct {
	afero.Fs
	FailOpen func(string) bool

	mu  sync.Mutex
	ops []string
}

// NewSyncFS wraps base in a SyncFS.
func NewSyncFS(base afero.Fs) *SyncFS {
	return &SyncFS{Fs: base}
}

func (r *SyncFS) record(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

// Ops returns a copy of the recorded operations.
func (r *SyncFS) Ops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

// SawSync reports whether name was synced.
func (r *SyncFS) SawSync(name string) bool {
	return slices.Contains(r.Ops(), "sync:"+name)
}

// SawSyncUnder reports whether a file under dir was synced.
func (r *SyncFS) SawSyncUnder(dir string) bool {
	for _, op := range r.Ops() {
		if strings.HasPrefix(op, "sync:"+dir+"/") {
			return true
		}
	}
	return false
}

func (r *SyncFS) Open(name string) (afero.File, error) {
	if r.FailOpen != nil && r.FailOpen(name) {
		return nil, os.ErrPermission
	}
	f, err := r.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return &syncFile{File: f, rec: r}, nil
}

func (r *SyncFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := r.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &syncFile{File: f, rec: r}, nil
}

func (r *SyncFS) Rename(oldname, newname string) error {
	r.record("rename:" + oldname + "->" + newname)
	return r.Fs.Rename(oldname, newname)
}

type syncFile struct {
	afero.File
	rec *SyncFS
}

func (f *syncFile) Sync() error {
	f.rec.record("sync:" + f.Name())
	return f.File.Sync()
}
