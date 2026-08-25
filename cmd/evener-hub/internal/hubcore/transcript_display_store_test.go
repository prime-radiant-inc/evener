package hubcore

import (
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

func TestTranscriptDisplayStoreMissingRootUsesShippedDefaults(t *testing.T) {
	store, err := NewTranscriptDisplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("NewTranscriptDisplayStore returned nil store")
	}
	if got, want := store.Snapshot(), appwire.TranscriptDisplayShippedDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot = %#v, want %#v", got, want)
	}
}

func TestTranscriptDisplayStorePatchesLayoutsIndependently(t *testing.T) {
	store, err := NewTranscriptDisplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	desktop := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	desktop.Content.Level = appwire.TranscriptLevelActivity
	got, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{
		Layout: appwire.TranscriptViewportDesktop, ExpectedRevision: 0, Config: desktop,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || store.Snapshot().Mobile.Revision != 0 {
		t.Fatalf("patch result=%#v snapshot=%#v", got, store.Snapshot())
	}
	if got.Config != desktop {
		t.Fatalf("patch config=%#v, want %#v", got.Config, desktop)
	}
}

func TestTranscriptDisplayStorePersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	store, err := NewTranscriptDisplayStore(root)
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.TranscriptDisplayShippedDefaults().Mobile.Config
	config.Content.Level = appwire.TranscriptLevelFull
	if _, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{
		Layout: appwire.TranscriptViewportMobile, Config: config,
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTranscriptDisplayStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot(); got.Mobile.Revision != 1 || got.Mobile.Config != config {
		t.Fatalf("reloaded snapshot=%#v, want mobile revision 1/config %#v", got, config)
	}
	if got := reloaded.Snapshot(); got.Desktop != appwire.TranscriptDisplayShippedDefaults().Desktop {
		t.Fatalf("reload changed desktop=%#v", got.Desktop)
	}
}

func TestTranscriptDisplayStoreNoOpDoesNotIncrementRevision(t *testing.T) {
	root := t.TempDir()
	store, err := NewTranscriptDisplayStore(root)
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	config.Content.Level = appwire.TranscriptLevelActivity
	params := appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: config}
	if _, err := store.Patch(params); err != nil {
		t.Fatal(err)
	}
	before, err := afero.ReadFile(afero.NewOsFs(), transcriptDisplayStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	params.ExpectedRevision = 1
	got, err := store.Patch(params)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || got.Config != config {
		t.Fatalf("no-op result=%#v", got)
	}
	after, err := afero.ReadFile(afero.NewOsFs(), transcriptDisplayStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("no-op rewrote durable state: before=%q after=%q", before, after)
	}
}

func TestTranscriptDisplayStoreStaleRevisionReturnsCanonicalConflict(t *testing.T) {
	store, err := NewTranscriptDisplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	first.Content.Level = appwire.TranscriptLevelActivity
	if _, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{
		Layout: appwire.TranscriptViewportDesktop, Config: first,
	}); err != nil {
		t.Fatal(err)
	}
	stale := first
	stale.Content.Level = appwire.TranscriptLevelFull
	_, err = store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{
		Layout: appwire.TranscriptViewportDesktop, Config: stale,
	})
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
	data, ok := wireErr.Data.(appwire.TranscriptDisplayConflictData)
	if !ok {
		t.Fatalf("stale data=%T, want appwire.TranscriptDisplayConflictData", wireErr.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorConflict || data.Layout != appwire.TranscriptViewportDesktop || data.Current.Revision != 1 || data.Current.Config != first {
		t.Fatalf("conflict data=%#v", data)
	}
}

func TestTranscriptDisplayStoreConcurrentDifferentLayoutsBothSucceed(t *testing.T) {
	store, err := NewTranscriptDisplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configs := map[appwire.TranscriptViewportClass]appwire.TranscriptDisplayConfig{
		appwire.TranscriptViewportDesktop: func() appwire.TranscriptDisplayConfig {
			config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
			config.Content.Level = appwire.TranscriptLevelActivity
			return config
		}(),
		appwire.TranscriptViewportMobile: func() appwire.TranscriptDisplayConfig {
			config := appwire.TranscriptDisplayShippedDefaults().Mobile.Config
			config.Content.Level = appwire.TranscriptLevelFull
			return config
		}(),
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, layout := range []appwire.TranscriptViewportClass{appwire.TranscriptViewportDesktop, appwire.TranscriptViewportMobile} {
		layout, config := layout, configs[layout]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: layout, Config: config})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := store.Snapshot()
	if got.Desktop.Revision != 1 || got.Mobile.Revision != 1 {
		t.Fatalf("concurrent snapshot=%#v", got)
	}
}

func TestTranscriptDisplayStoreConcurrentSameLayoutConflicts(t *testing.T) {
	store, err := NewTranscriptDisplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	config.Content.Level = appwire.TranscriptLevelActivity
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: config})
			results <- err
		}()
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
		t.Fatalf("same-layout error=%T %v", err, err)
	}
	if success != 1 || conflicts != 1 || store.Snapshot().Desktop.Revision != 1 {
		t.Fatalf("success=%d conflicts=%d snapshot=%#v", success, conflicts, store.Snapshot())
	}
}

func TestTranscriptDisplayStoreMalformedSnapshotFallsBackAndBlocksPatches(t *testing.T) {
	root := t.TempDir()
	path := transcriptDisplayStatePath(root)
	if err := writeTranscriptDisplayStateFile(afero.NewOsFs(), root, []byte(`{"version":1,"desktop":{}} trailing`)); err != nil {
		t.Fatal(err)
	}
	before, err := afero.ReadFile(afero.NewOsFs(), path)
	if err != nil {
		t.Fatal(err)
	}
	store, loadErr := NewTranscriptDisplayStore(root)
	if store == nil || loadErr == nil {
		t.Fatalf("store=%#v loadErr=%v, want usable fallback and diagnostic", store, loadErr)
	}
	if got, want := store.Snapshot(), appwire.TranscriptDisplayShippedDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback snapshot=%#v, want %#v", got, want)
	}
	config := wantTranscriptDisplayConfig(appwire.TranscriptViewportDesktop, appwire.TranscriptLevelActivity)
	if _, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, Config: config}); err == nil {
		t.Fatal("patch against malformed snapshot unexpectedly succeeded")
	}
	after, err := afero.ReadFile(afero.NewOsFs(), path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("malformed evidence changed: before=%q after=%q", before, after)
	}
}

func TestTranscriptDisplayStoreEmptySnapshotUsesShippedDefaults(t *testing.T) {
	root := t.TempDir()
	if err := writeTranscriptDisplayStateFile(afero.NewOsFs(), root, nil); err != nil {
		t.Fatal(err)
	}
	store, err := NewTranscriptDisplayStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := store.Snapshot(), appwire.TranscriptDisplayShippedDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("empty snapshot=%#v, want %#v", got, want)
	}
}

func TestTranscriptDisplayStoreRevisionOverflowIsRejected(t *testing.T) {
	root := t.TempDir()
	state := transcriptDisplaySnapshot{
		Version: transcriptDisplaySnapshotVersion,
		Desktop: appwire.TranscriptDisplayDefault{
			Revision: math.MaxUint64,
			Config:   appwire.TranscriptDisplayShippedDefaults().Desktop.Config,
		},
		Mobile: appwire.TranscriptDisplayShippedDefaults().Mobile,
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTranscriptDisplayStateFile(afero.NewOsFs(), root, data); err != nil {
		t.Fatal(err)
	}
	store, err := NewTranscriptDisplayStore(root)
	if err != nil {
		t.Fatal(err)
	}
	config := wantTranscriptDisplayConfig(appwire.TranscriptViewportDesktop, appwire.TranscriptLevelActivity)
	if _, err := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{Layout: appwire.TranscriptViewportDesktop, ExpectedRevision: math.MaxUint64, Config: config}); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflow patch error=%v", err)
	}
	if got := store.Snapshot().Desktop.Revision; got != math.MaxUint64 {
		t.Fatalf("overflow changed revision=%d", got)
	}
}

func TestTranscriptDisplayStoreStrictSnapshotDecoding(t *testing.T) {
	valid := transcriptDisplaySnapshot{
		Version: transcriptDisplaySnapshotVersion,
		Desktop: appwire.TranscriptDisplayShippedDefaults().Desktop,
		Mobile:  appwire.TranscriptDisplayShippedDefaults().Mobile,
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
		{name: "invalid config", data: `{"version":1,"desktop":{"revision":0,"config":{"version":99,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}},"mobile":{"revision":0,"config":{"version":1,"content":{"kind":"preset","level":"intent"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := writeTranscriptDisplayStateFile(afero.NewOsFs(), root, []byte(tc.data)); err != nil {
				t.Fatal(err)
			}
			store, loadErr := NewTranscriptDisplayStore(root)
			if store == nil || loadErr == nil {
				t.Fatalf("store=%#v loadErr=%v, want fallback and diagnostic", store, loadErr)
			}
			if got := store.Snapshot(); !reflect.DeepEqual(got, appwire.TranscriptDisplayShippedDefaults()) {
				t.Fatalf("fallback=%#v", got)
			}
		})
	}
}

func writeTranscriptDisplayStateFile(fs afero.Fs, root string, data []byte) error {
	path := transcriptDisplayStatePath(root)
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return afero.WriteFile(fs, path, data, 0o600)
}

func wantTranscriptDisplayConfig(layout appwire.TranscriptViewportClass, level appwire.TranscriptLevel) appwire.TranscriptDisplayConfig {
	config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	if layout == appwire.TranscriptViewportMobile {
		config = appwire.TranscriptDisplayShippedDefaults().Mobile.Config
	}
	config.Content.Level = level
	return config
}
