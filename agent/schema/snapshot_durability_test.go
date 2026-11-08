package schema

import (
	"errors"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/fsfake"
	"primeradiant.com/evener/execsupport/fsatomic"
)

func TestSaveSessionMetaSyncsFileAndDirectory(t *testing.T) {
	t.Parallel()
	fs := fsfake.NewSyncFS(afero.NewMemMapFs())
	dir := "/state"
	meta := SessionMeta{
		ID:        "02wMz5TxuyedWwYBSOAa00",
		Name:      "session",
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		UpdatedAt: time.Unix(1_700_000_100, 0).UTC(),
	}
	if err := SaveSessionMetaWithFS(fs, dir, meta); err != nil {
		t.Fatalf("SaveSessionMetaWithFS: %v", err)
	}
	sessionsDir := SessionsDir(dir)
	if !fs.SawSyncUnder(sessionsDir) {
		t.Fatalf("session meta write did not sync its temp file; operations: %v", fs.Ops())
	}
	if !fs.SawSync(sessionsDir) {
		t.Fatalf("session meta write did not sync its directory; operations: %v", fs.Ops())
	}
}

func TestSaveSessionMetaDirectorySyncFailureReportsPublished(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	dir := "/state"
	id := "02wMz5TxuyedWwYBSOAa00"
	old := SessionMeta{ID: id, Name: "old", CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC()}
	if err := SaveSessionMetaWithFS(base, dir, old); err != nil {
		t.Fatalf("seed old meta: %v", err)
	}
	fresh := old
	fresh.Name = "new"
	fresh.UpdatedAt = time.Unix(2, 0).UTC()
	fs := &dirOpenFailFS{Fs: base, dir: SessionsDir(dir)}
	err := SaveSessionMetaWithFS(fs, dir, fresh)
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("SaveSessionMetaWithFS error = %v, want fsatomic.ErrPublished", err)
	}
	got, loadErr := LoadSessionMetaWithFS(base, dir, id)
	if loadErr != nil {
		t.Fatalf("load published meta: %v", loadErr)
	}
	if got.Name != "new" {
		t.Fatalf("published meta name = %q, want new", got.Name)
	}
}

// dirOpenFailFS fails Open for one directory path, leaving every file open and
// the rest of the filesystem intact.
type dirOpenFailFS struct {
	afero.Fs
	dir string
}

func (f *dirOpenFailFS) Open(name string) (afero.File, error) {
	if name == f.dir {
		return nil, errors.New("injected directory open failure")
	}
	return f.Fs.Open(name)
}
