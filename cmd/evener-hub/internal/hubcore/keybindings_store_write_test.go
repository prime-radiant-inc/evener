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

type keybindingsReadFaultFs struct {
	afero.Fs
	path string
	err  error
}

func (f *keybindingsReadFaultFs) Open(name string) (afero.File, error) {
	if filepath.Clean(name) == filepath.Clean(f.path) {
		return nil, f.err
	}
	return f.Fs.Open(name)
}

func TestKeybindingsStorePatchNormalizesNilRules(t *testing.T) {
	// A direct Go caller can pass Rules:nil; persisted verbatim that is
	// "rules":null, which the loader's shape check rejects on restart - the
	// store normalizes to the empty slice instead (roborev PR #884 round 2).
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	store, err := newKeybindingsStoreFS(fs, root, keybindingsStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: appwire.KeybindingsConfig{Version: 1}}); err != nil {
		t.Fatal(err)
	}
	data, err := afero.ReadFile(fs, keybindingsStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"rules":null`)) {
		t.Fatalf("persisted state contains rules:null: %s", data)
	}
	reloaded, err := newKeybindingsStoreFS(fs, root, keybindingsStoreFaults{})
	if err != nil {
		t.Fatalf("reload after nil-rules patch: %v", err)
	}
	if got := reloaded.Snapshot(); got.Revision != 1 || got.Rules == nil || len(got.Rules) != 0 {
		t.Fatalf("reloaded snapshot = %#v, want revision 1 with empty non-nil rules", got)
	}
}

func TestKeybindingsStorePreRenameFailurePreservesOldState(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	store, err := newKeybindingsStoreFS(fs, root, keybindingsStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: oldConfig}); err != nil {
		t.Fatal(err)
	}
	before, err := afero.ReadFile(fs, keybindingsStatePath(root))
	if err != nil {
		t.Fatal(err)
	}

	wantFailure := errors.New("before rename failed")
	failing, err := newKeybindingsStoreFS(fs, root, keybindingsStoreFaults{
		BeforeRename: func() error { return wantFailure },
	})
	if err != nil {
		t.Fatal(err)
	}
	newConfig := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+alt+n")},
	}}
	if _, err := failing.Patch(appwire.KeybindingsPatchParams{ExpectedRevision: 1, Config: newConfig}); !errors.Is(err, wantFailure) {
		t.Fatalf("pre-rename error=%v, want %v", err, wantFailure)
	}
	if got := failing.Snapshot(); got.Revision != 1 || !reflect.DeepEqual(got.Rules, oldConfig.Rules) {
		t.Fatalf("pre-rename memory=%#v, want revision 1/rules %#v", got, oldConfig.Rules)
	}
	after, err := afero.ReadFile(fs, keybindingsStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("pre-rename disk changed: before=%q after=%q", before, after)
	}
}

func TestKeybindingsStorePostRenameFailurePublishesNewState(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	store, err := newKeybindingsStoreFS(fs, root, keybindingsStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: oldConfig}); err != nil {
		t.Fatal(err)
	}

	wantFailure := errors.New("after rename failed")
	failing, err := newKeybindingsStoreFS(fs, root, keybindingsStoreFaults{
		AfterRename: func() error { return wantFailure },
	})
	if err != nil {
		t.Fatal(err)
	}
	newConfig := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+alt+n")},
	}}
	_, patchErr := failing.Patch(appwire.KeybindingsPatchParams{ExpectedRevision: 1, Config: newConfig})
	if !errors.Is(patchErr, wantFailure) {
		t.Fatalf("post-rename error=%v, want %v", patchErr, wantFailure)
	}
	// The applied-but-unfinished case must be distinguishable from a rejected
	// patch: the RPC layer broadcasts on exactly this type.
	if _, postRename := errors.AsType[*KeybindingsPostRenameError](patchErr); !postRename {
		t.Fatalf("post-rename error type=%T, want *KeybindingsPostRenameError", patchErr)
	}
	if got := failing.Snapshot(); got.Revision != 2 || !reflect.DeepEqual(got.Rules, newConfig.Rules) {
		t.Fatalf("post-rename memory=%#v, want revision 2/rules %#v", got, newConfig.Rules)
	}
	reloaded, err := newKeybindingsStoreFS(fs, root, keybindingsStoreFaults{})
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot(); got.Revision != 2 || !reflect.DeepEqual(got.Rules, newConfig.Rules) {
		t.Fatalf("post-rename reload=%#v, want revision 2/rules %#v", got, newConfig.Rules)
	}
}

func TestKeybindingsStoreWriteValidatesBeforeCreatingState(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	invalid := keybindingsSnapshot{Version: keybindingsSnapshotVersion}
	if renamed, err := saveKeybindingsSnapshotFS(fs, root, invalid, keybindingsStoreFaults{}); renamed || err == nil {
		t.Fatalf("invalid save renamed=%v err=%v, want no publication and error", renamed, err)
	}
	if _, err := fs.Stat(filepath.Join(root, keybindingsStateSubdir)); !errors.Is(err, afero.ErrFileNotFound) {
		t.Fatalf("invalid save state directory error=%v, want absent", err)
	}
}

func TestKeybindingsStoreWriteCleansTempAfterPreRenameFailure(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	state := keybindingsSnapshot{
		Version: keybindingsSnapshotVersion,
		Config:  appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{}},
	}
	wantFailure := errors.New("before rename failed")
	if renamed, err := saveKeybindingsSnapshotFS(fs, root, state, keybindingsStoreFaults{BeforeRename: func() error { return wantFailure }}); renamed || !errors.Is(err, wantFailure) {
		t.Fatalf("pre-rename save renamed=%v err=%v", renamed, err)
	}
	entries, err := afero.ReadDir(fs, filepath.Join(root, keybindingsStateSubdir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pre-rename temp residue=%v", entries)
	}
}

func TestKeybindingsStoreReadFailureFallsBackAndBlocksPatches(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	evidence := []byte("durable evidence that must not be overwritten")
	if err := writeKeybindingsStateFile(fs, root, evidence); err != nil {
		t.Fatal(err)
	}
	wantFailure := errors.New("state read failed")
	faultFS := &keybindingsReadFaultFs{
		Fs:   fs,
		path: keybindingsStatePath(root),
		err:  wantFailure,
	}
	store, loadErr := newKeybindingsStoreFS(faultFS, root, keybindingsStoreFaults{})
	if store == nil || !errors.Is(loadErr, wantFailure) {
		t.Fatalf("store=%#v loadErr=%v, want fallback and %v", store, loadErr, wantFailure)
	}
	if got := store.Snapshot(); !reflect.DeepEqual(got, appwire.KeybindingsShippedDefaults()) {
		t.Fatalf("read-fault fallback=%#v", got)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: new("ctrl+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: config}); !errors.Is(err, wantFailure) {
		t.Fatalf("read-fault patch error=%v, want %v", err, wantFailure)
	}
	after, err := afero.ReadFile(fs, keybindingsStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, evidence) {
		t.Fatalf("read-fault evidence changed: before=%q after=%q", evidence, after)
	}
}
