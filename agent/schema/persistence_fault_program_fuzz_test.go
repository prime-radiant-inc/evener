//go:build serffuzz

package schema

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/llm"
)

// FuzzSchemaPersistenceFaultProgram drives the real persistence seams through
// a memory filesystem and narrow per-call fault wrappers. It covers the atomic
// write failure paths without allowing fuzz input to select host paths.
func FuzzSchemaPersistenceFaultProgram(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))

	f.Fuzz(func(t *testing.T, variant uint8) {
		id := []string{"alpha", "beta"}[int(variant)%2]
		meta := SessionMeta{
			ID:        id,
			Name:      "session",
			CreatedAt: time.Unix(1_700_000_000, int64(variant)).UTC(),
			UpdatedAt: time.Unix(1_700_000_100, int64(variant)).UTC(),
		}

		mem := afero.NewMemMapFs()
		if err := saveSessionMetaFS(mem, "/state", meta); err != nil {
			t.Fatalf("save healthy meta: %v", err)
		}
		if got, err := loadSessionMetaFS(mem, "/state", id); err != nil || got.ID != id {
			t.Fatalf("load healthy meta = %+v, %v", got, err)
		}
		if err := afero.WriteFile(mem, "/state/sessions/corrupt.meta.json", []byte("{"), 0o644); err != nil {
			t.Fatalf("write corrupt fixture: %v", err)
		}
		if err := afero.WriteFile(mem, "/state/sessions/ignored.txt", []byte("ignored"), 0o644); err != nil {
			t.Fatalf("write ignored fixture: %v", err)
		}
		if err := mem.MkdirAll("/state/sessions/nested", 0o755); err != nil {
			t.Fatalf("write directory fixture: %v", err)
		}
		if metas, err := listSessionMetasFS(mem, "/state"); err != nil || len(metas) != 1 || metas[0].ID != id {
			t.Fatalf("list valid metas = %+v, %v", metas, err)
		}

		writeSentinel := errors.New("write fault")
		if err := saveSessionMetaFS(schemaFuzzFaultFS{Fs: afero.NewMemMapFs(), openFileErr: writeSentinel}, "/state", meta); !errors.Is(err, writeSentinel) {
			t.Fatalf("write fault = %v", err)
		}

		renameSentinel := errors.New("rename fault")
		renameBase := afero.NewMemMapFs()
		if err := saveSessionMetaFS(schemaFuzzFaultFS{Fs: renameBase, renameErr: renameSentinel}, "/state", meta); !errors.Is(err, renameSentinel) {
			t.Fatalf("rename fault = %v", err)
		}
		if exists, err := afero.Exists(renameBase, "/state/sessions/"+id+".meta.json.tmp"); err != nil || exists {
			t.Fatalf("rename failure left temp file: exists=%v err=%v", exists, err)
		}

		mkdirSentinel := errors.New("mkdir fault")
		if err := saveSessionMetaFS(schemaFuzzFaultFS{Fs: afero.NewMemMapFs(), mkdirErr: mkdirSentinel}, "/state", meta); !errors.Is(err, mkdirSentinel) {
			t.Fatalf("mkdir fault = %v", err)
		}
		marshalSentinel := errors.New("marshal fault")
		oldMarshal := marshalSessionMeta
		marshalSessionMeta = func(any) ([]byte, error) { return nil, marshalSentinel }
		if err := saveSessionMetaFS(afero.NewMemMapFs(), "/state", meta); !errors.Is(err, marshalSentinel) {
			t.Fatalf("marshal fault = %v", err)
		}
		marshalSessionMeta = oldMarshal

		readSentinel := errors.New("read-dir fault")
		if _, err := listSessionMetasFS(schemaFuzzFaultFS{Fs: afero.NewMemMapFs(), openErr: readSentinel}, "/state"); !errors.Is(err, readSentinel) {
			t.Fatalf("read-dir fault = %v", err)
		}

		turn := NewTurn(TurnUserInput, llm.User("payload"))
		if turn.Kind != TurnUserInput || turn.Message.Text() != "payload" || turn.Timestamp.IsZero() || turn.Timestamp.Location() != time.UTC {
			t.Fatalf("NewTurn = %+v", turn)
		}
	})
}

type schemaFuzzFaultFS struct {
	afero.Fs
	mkdirErr    error
	openErr     error
	openFileErr error
	renameErr   error
}

func (fs schemaFuzzFaultFS) MkdirAll(path string, perm os.FileMode) error {
	if fs.mkdirErr != nil {
		return fs.mkdirErr
	}
	return fs.Fs.MkdirAll(path, perm)
}

func (fs schemaFuzzFaultFS) Open(name string) (afero.File, error) {
	if fs.openErr != nil {
		return nil, fs.openErr
	}
	return fs.Fs.Open(name)
}

func (fs schemaFuzzFaultFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if fs.openFileErr != nil {
		return nil, fs.openFileErr
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

func (fs schemaFuzzFaultFS) Rename(oldname, newname string) error {
	if fs.renameErr != nil {
		return fs.renameErr
	}
	return fs.Fs.Rename(oldname, newname)
}
