package hubcore

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
)

type transcriptDisplayReadFaultFs struct {
	afero.Fs
	path string
	err  error
}

func (f *transcriptDisplayReadFaultFs) Open(name string) (afero.File, error) {
	if filepath.Clean(name) == filepath.Clean(f.path) {
		return nil, f.err
	}
	return f.Fs.Open(name)
}

func TestTranscriptDisplayStorePreRenameFailurePreservesOldState(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	store, err := newTranscriptDisplayStoreFS(fs, root, transcriptDisplayStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	oldConfig.Content.Level = appwire.TranscriptLevelActivity
	if _, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: oldConfig}); err != nil {
		t.Fatal(err)
	}
	before, err := afero.ReadFile(fs, transcriptDisplayStatePath(root))
	if err != nil {
		t.Fatal(err)
	}

	wantFailure := errors.New("before rename failed")
	failing, err := newTranscriptDisplayStoreFS(fs, root, transcriptDisplayStoreFaults{
		BeforeRename: func() error { return wantFailure },
	})
	if err != nil {
		t.Fatal(err)
	}
	newConfig := oldConfig
	newConfig.Content.Level = appwire.TranscriptLevelFull
	if _, err := failing.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, ExpectedRevision: 1, Config: newConfig}); !errors.Is(err, wantFailure) {
		t.Fatalf("pre-rename error=%v, want %v", err, wantFailure)
	}
	if got := failing.Snapshot().Desktop.Config; !reflect.DeepEqual(got, oldConfig) {
		t.Fatalf("pre-rename memory=%#v, want old=%#v", got, oldConfig)
	}
	after, err := afero.ReadFile(fs, transcriptDisplayStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("pre-rename disk changed: before=%q after=%q", before, after)
	}
}

func TestTranscriptDisplayStorePostRenameFailurePublishesNewState(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	store, err := newTranscriptDisplayStoreFS(fs, root, transcriptDisplayStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := appwire.TranscriptDisplayShippedDefaults().Mobile.Config
	oldConfig.Content.Level = appwire.TranscriptLevelActivity
	if _, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportMobile, Config: oldConfig}); err != nil {
		t.Fatal(err)
	}

	wantFailure := errors.New("after rename failed")
	failing, err := newTranscriptDisplayStoreFS(fs, root, transcriptDisplayStoreFaults{
		AfterRename: func() error { return wantFailure },
	})
	if err != nil {
		t.Fatal(err)
	}
	newConfig := oldConfig
	newConfig.Content.Level = appwire.TranscriptLevelFull
	if _, err := failing.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportMobile, ExpectedRevision: 1, Config: newConfig}); !errors.Is(err, wantFailure) {
		t.Fatalf("post-rename error=%v, want %v", err, wantFailure)
	}
	if got := failing.Snapshot().Mobile; got.Revision != 2 || !reflect.DeepEqual(got.Config, newConfig) {
		t.Fatalf("post-rename memory=%#v, want revision 2/config %#v", got, newConfig)
	}
	reloaded, err := newTranscriptDisplayStoreFS(fs, root, transcriptDisplayStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot().Mobile; got.Revision != 2 || !reflect.DeepEqual(got.Config, newConfig) {
		t.Fatalf("post-rename reload=%#v, want revision 2/config %#v", got, newConfig)
	}
}

func TestTranscriptDisplayStoreWriteValidatesBeforeCreatingState(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	invalid := transcriptDisplaySnapshot{Version: transcriptDisplaySnapshotVersion}
	if renamed, err := saveTranscriptDisplaySnapshotFS(fs, root, invalid, transcriptDisplayStoreFaults{}); renamed || err == nil {
		t.Fatalf("invalid save renamed=%v err=%v, want no publication and error", renamed, err)
	}
	if _, err := fs.Stat(filepath.Join(root, transcriptDisplayStateSubdir)); !errors.Is(err, afero.ErrFileNotFound) {
		t.Fatalf("invalid save state directory error=%v, want absent", err)
	}
}

func TestTranscriptDisplayStoreWriteCleansTempAfterPreRenameFailure(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	state := transcriptDisplaySnapshot{
		Version: transcriptDisplaySnapshotVersion,
		Desktop: appwire.TranscriptDisplayShippedDefaults().Desktop,
		Mobile:  appwire.TranscriptDisplayShippedDefaults().Mobile,
	}
	wantFailure := errors.New("before rename failed")
	if renamed, err := saveTranscriptDisplaySnapshotFS(fs, root, state, transcriptDisplayStoreFaults{BeforeRename: func() error { return wantFailure }}); renamed || !errors.Is(err, wantFailure) {
		t.Fatalf("pre-rename save renamed=%v err=%v", renamed, err)
	}
	entries, err := afero.ReadDir(fs, filepath.Join(root, transcriptDisplayStateSubdir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pre-rename temp residue=%v", entries)
	}
}

func TestTranscriptDisplayStoreReadFailureFallsBackAndBlocksPatches(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	evidence := []byte("durable evidence that must not be overwritten")
	if err := writeTranscriptDisplayStateFile(fs, root, evidence); err != nil {
		t.Fatal(err)
	}
	wantFailure := errors.New("state read failed")
	faultFS := &transcriptDisplayReadFaultFs{
		Fs:   fs,
		path: transcriptDisplayStatePath(root),
		err:  wantFailure,
	}
	store, loadErr := newTranscriptDisplayStoreFS(faultFS, root, transcriptDisplayStoreFaults{})
	if store == nil || !errors.Is(loadErr, wantFailure) {
		t.Fatalf("store=%#v loadErr=%v, want fallback and %v", store, loadErr, wantFailure)
	}
	if got := store.Snapshot(); !reflect.DeepEqual(got, appwire.TranscriptDisplayShippedDefaults()) {
		t.Fatalf("read-fault fallback=%#v", got)
	}
	config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	config.Content.Level = appwire.TranscriptLevelActivity
	if _, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: config}); !errors.Is(err, wantFailure) {
		t.Fatalf("read-fault patch error=%v, want %v", err, wantFailure)
	}
	after, err := afero.ReadFile(fs, transcriptDisplayStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, evidence) {
		t.Fatalf("read-fault evidence changed: before=%q after=%q", evidence, after)
	}
}
