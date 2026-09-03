package hubcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
)

func TestKeybindingsStoreMissingRootUsesShippedDefaults(t *testing.T) {
	store, err := NewKeybindingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("NewKeybindingsStore returned nil store")
	}
	if got, want := store.Snapshot(), appwire.KeybindingsShippedDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot = %#v, want %#v", got, want)
	}
}

func TestKeybindingsStorePatchIncrementsRevision(t *testing.T) {
	store, err := NewKeybindingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+n")},
		{Action: "thread.close", Chord: nil},
	}}
	got, err := store.Patch(appwire.KeybindingsPatchParams{ExpectedRevision: 0, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || !reflect.DeepEqual(got.Rules, config.Rules) || got.Version != 1 {
		t.Fatalf("patch result=%#v", got)
	}
	if snapshot := store.Snapshot(); !reflect.DeepEqual(snapshot, got) {
		t.Fatalf("snapshot=%#v, want patch result %#v", snapshot, got)
	}
}

func TestKeybindingsStorePersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	store, err := NewKeybindingsStore(root)
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+shift+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: config}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewKeybindingsStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Snapshot()
	if got.Revision != 1 || !reflect.DeepEqual(got.Rules, config.Rules) {
		t.Fatalf("reloaded snapshot=%#v, want revision 1/rules %#v", got, config.Rules)
	}
}

func TestKeybindingsStoreNoOpDoesNotIncrementRevision(t *testing.T) {
	root := t.TempDir()
	store, err := NewKeybindingsStore(root)
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: config}); err != nil {
		t.Fatal(err)
	}
	before, err := afero.ReadFile(afero.NewOsFs(), keybindingsStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Patch(appwire.KeybindingsPatchParams{ExpectedRevision: 1, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || !reflect.DeepEqual(got.Rules, config.Rules) {
		t.Fatalf("no-op result=%#v", got)
	}
	after, err := afero.ReadFile(afero.NewOsFs(), keybindingsStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("no-op rewrote durable state: before=%q after=%q", before, after)
	}
}

func TestKeybindingsStoreStaleRevisionReturnsCanonicalConflict(t *testing.T) {
	store, err := NewKeybindingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: first}); err != nil {
		t.Fatal(err)
	}
	stale := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+alt+n")},
	}}
	_, err = store.Patch(appwire.KeybindingsPatchParams{Config: stale})
	if err == nil {
		t.Fatal("stale patch unexpectedly succeeded")
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("stale error=%T %v, want appwire.WireError", err, err)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("stale code=%d, want %d", wireErr.Code, appwire.CodeConflict)
	}
	data, ok := wireErr.Data.(appwire.KeybindingsConflictData)
	if !ok {
		t.Fatalf("stale data=%T, want appwire.KeybindingsConflictData", wireErr.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorConflict || data.Current.Revision != 1 || !reflect.DeepEqual(data.Current.Rules, first.Rules) {
		t.Fatalf("conflict data=%#v", data)
	}
}

func TestKeybindingsStoreConcurrentPatchesSerialize(t *testing.T) {
	store, err := NewKeybindingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+n")},
	}}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, err := store.Patch(appwire.KeybindingsPatchParams{Config: config})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var success, conflicts int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		var wireErr appwire.WireError
		if errors.As(err, &wireErr) && wireErr.Code == appwire.CodeConflict {
			conflicts++
			continue
		}
		t.Fatalf("concurrent patch error=%T %v", err, err)
	}
	if success != 1 || conflicts != 1 || store.Snapshot().Revision != 1 {
		t.Fatalf("success=%d conflicts=%d snapshot=%#v", success, conflicts, store.Snapshot())
	}
}

func TestKeybindingsStoreMalformedSnapshotFallsBackAndBlocksPatches(t *testing.T) {
	root := t.TempDir()
	path := keybindingsStatePath(root)
	if err := writeKeybindingsStateFile(afero.NewOsFs(), root, []byte(`{"version":1,"revision":0,"config":{}} trailing`)); err != nil {
		t.Fatal(err)
	}
	before, err := afero.ReadFile(afero.NewOsFs(), path)
	if err != nil {
		t.Fatal(err)
	}
	store, loadErr := NewKeybindingsStore(root)
	if store == nil || loadErr == nil {
		t.Fatalf("store=%#v loadErr=%v, want usable fallback and diagnostic", store, loadErr)
	}
	if got, want := store.Snapshot(), appwire.KeybindingsShippedDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback snapshot=%#v, want %#v", got, want)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: config}); err == nil {
		t.Fatal("patch against malformed snapshot unexpectedly succeeded")
	}
	after, err := afero.ReadFile(afero.NewOsFs(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("malformed evidence changed: before=%q after=%q", before, after)
	}
}

func TestKeybindingsStoreEmptySnapshotUsesShippedDefaults(t *testing.T) {
	root := t.TempDir()
	if err := writeKeybindingsStateFile(afero.NewOsFs(), root, nil); err != nil {
		t.Fatal(err)
	}
	store, err := NewKeybindingsStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := store.Snapshot(), appwire.KeybindingsShippedDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("empty snapshot=%#v, want %#v", got, want)
	}
}

func TestKeybindingsStoreRevisionOverflowIsRejected(t *testing.T) {
	root := t.TempDir()
	state := keybindingsSnapshot{
		Version:  keybindingsSnapshotVersion,
		Revision: math.MaxUint64,
		Config:   appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeybindingsStateFile(afero.NewOsFs(), root, data); err != nil {
		t.Fatal(err)
	}
	store, err := NewKeybindingsStore(root)
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
		{Action: "thread.new", Chord: keybindingsChord("ctrl+n")},
	}}
	if _, err := store.Patch(appwire.KeybindingsPatchParams{ExpectedRevision: math.MaxUint64, Config: config}); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflow patch error=%v", err)
	}
	if got := store.Snapshot().Revision; got != math.MaxUint64 {
		t.Fatalf("overflow changed revision=%d", got)
	}
}

func TestKeybindingsStoreStrictSnapshotDecoding(t *testing.T) {
	valid := keybindingsSnapshot{
		Version: keybindingsSnapshotVersion,
		Config:  appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{}},
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data string
	}{
		{name: "unknown field", data: strings.TrimSuffix(string(encoded), "}") + `,"unknown":true}`},
		{name: "trailing value", data: string(encoded) + ` {}`},
		{name: "invalid config", data: `{"version":1,"revision":0,"config":{"version":99,"rules":[]}}`},
		{name: "empty rule action", data: `{"version":1,"revision":0,"config":{"version":1,"rules":[{"action":"","chord":"ctrl+n"}]}}`},
		{name: "empty rule chord", data: `{"version":1,"revision":0,"config":{"version":1,"rules":[{"action":"thread.new","chord":""}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := writeKeybindingsStateFile(afero.NewOsFs(), root, []byte(tc.data)); err != nil {
				t.Fatal(err)
			}
			store, loadErr := NewKeybindingsStore(root)
			if store == nil || loadErr == nil {
				t.Fatalf("store=%#v loadErr=%v, want fallback and diagnostic", store, loadErr)
			}
			if got := store.Snapshot(); !reflect.DeepEqual(got, appwire.KeybindingsShippedDefaults()) {
				t.Fatalf("fallback=%#v", got)
			}
		})
	}
}

func TestKeybindingsStoreStrictSnapshotRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{name: "missing revision", data: `{"version":1,"config":{"version":1,"rules":[]}}`},
		{name: "missing config", data: `{"version":1,"revision":0}`},
		{name: "missing config version", data: `{"version":1,"revision":0,"config":{"rules":[]}}`},
		{name: "missing rules", data: `{"version":1,"revision":0,"config":{"version":1}}`},
		{name: "null rules", data: `{"version":1,"revision":0,"config":{"version":1,"rules":null}}`},
		{name: "missing rule action", data: `{"version":1,"revision":0,"config":{"version":1,"rules":[{"chord":"ctrl+n"}]}}`},
		{name: "missing rule chord", data: `{"version":1,"revision":0,"config":{"version":1,"rules":[{"action":"thread.new"}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := writeKeybindingsStateFile(afero.NewOsFs(), root, []byte(tc.data)); err != nil {
				t.Fatal(err)
			}
			before, err := afero.ReadFile(afero.NewOsFs(), keybindingsStatePath(root))
			if err != nil {
				t.Fatal(err)
			}
			store, loadErr := NewKeybindingsStore(root)
			if store == nil || loadErr == nil {
				t.Fatalf("store=%#v loadErr=%v, want fallback and diagnostic", store, loadErr)
			}
			if got := store.Snapshot(); !reflect.DeepEqual(got, appwire.KeybindingsShippedDefaults()) {
				t.Fatalf("fallback=%#v", got)
			}
			config := appwire.KeybindingsConfig{Version: 1, Rules: []appwire.KeybindingsRule{
				{Action: "thread.new", Chord: keybindingsChord("ctrl+n")},
			}}
			if _, err := store.Patch(appwire.KeybindingsPatchParams{Config: config}); err == nil {
				t.Fatal("patch against malformed snapshot unexpectedly succeeded")
			}
			after, err := afero.ReadFile(afero.NewOsFs(), keybindingsStatePath(root))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("malformed evidence changed: before=%q after=%q", before, after)
			}
		})
	}
}

func writeKeybindingsStateFile(fs afero.Fs, root string, data []byte) error {
	path := keybindingsStatePath(root)
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return afero.WriteFile(fs, path, data, 0o600)
}

func keybindingsChord(chord string) *string {
	return &chord
}
