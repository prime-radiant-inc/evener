package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadCustomSnapshotMustConvert(t *testing.T) {
	t.Parallel()
	_, err := Load(
		WithSnapshot([]byte("{")),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithStateRoot(t.TempDir()),
		WithOffline(true),
		WithoutCache(),
	)
	if err == nil {
		t.Fatal("a custom snapshot that does not convert must fail Load")
	}
}

func TestLoadConfigPathIsDirectory(t *testing.T) {
	t.Parallel()
	_, err := Load(
		WithSnapshot(fixtureBytes(t)),
		WithConfigPath(t.TempDir()),
		WithEnv(mapEnv(nil)),
		WithStateRoot(t.TempDir()),
		WithOffline(true),
	)
	if err == nil {
		t.Fatal("a config path that is a directory must fail Load")
	}
}

// An overlay entry may base on another curated id; a base naming nothing in
// the catalog is a load error, not a hidden provider.
func TestLoadOverlayBaseMustNameACuratedID(t *testing.T) {
	t.Parallel()
	_, err := Load(
		WithSnapshot(fixtureBytes(t)),
		WithOverlay(overlayWith("[providers.ghostly]\nbase = \"no-such-provider\"\n")),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithStateRoot(t.TempDir()),
		WithOffline(true),
		WithoutCache(),
	)
	if err == nil {
		t.Fatal("an overlay base naming an unknown provider must fail Load")
	}
}

// A snapshot row that names a transport preset the overlay does not define
// is a load error: the row's transport cannot be built. The converter names
// the vertex presets for cross-protocol rows, so an overlay without them
// refuses such a snapshot.
func TestLoadRowPresetMustExistInOverlay(t *testing.T) {
	t.Parallel()
	body := []byte(`{"prov":{"npm":"@ai-sdk/openai","models":{"m":{"provider":{"npm":"@ai-sdk/google-vertex"},"modalities":{"output":["text"]}}}}}`)
	_, err := Load(
		WithSnapshot(body),
		WithOverlay([]byte("")),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithStateRoot(t.TempDir()),
		WithOffline(true),
		WithoutCache(),
	)
	if err == nil {
		t.Fatal("a row preset the overlay does not define must fail Load")
	}
}

// Two individually-clean layers may still collide once merged: an overlay
// "Authorization" and a config "authorization" fold into one record, and
// the fold refuses the pair (header names are case-insensitive on the wire).
func TestLoadCredentialHeadersCollidingAcrossLayers(t *testing.T) {
	t.Parallel()
	overlay := overlayWith("[providers.gwtest]\ncredential_headers = { Authorization = \"$A\" }\n")
	config := "[providers.gwtest]\ncredential_headers = { authorization = \"$B\" }\n"
	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(
		WithSnapshot(fixtureBytes(t)),
		WithOverlay(overlay),
		WithConfigPath(path),
		WithEnv(mapEnv(nil)),
		WithStateRoot(t.TempDir()),
		WithOffline(true),
		WithoutCache(),
	)
	if err == nil {
		t.Fatal("credential headers colliding case-insensitively across layers must fail Load")
	}
}

// cloneAnyValue deep-copies every kind a transport body can carry; a shallow
// copy would alias the layer's own slices and maps.
func TestCloneAnyValueKindsAreIndependent(t *testing.T) {
	t.Parallel()
	inner := map[string]any{"b": 1}
	orig := map[string]any{
		"arr":      []any{1, inner},
		"strs":     []string{"x"},
		"tablearr": []map[string]any{{"k": "v"}},
		"strmap":   map[string]string{"k": "v"},
		"obj":      map[string]any{"deep": inner},
		"scalar":   42,
	}
	clone := cloneAnyMap(orig)
	clone["arr"].([]any)[0] = 99
	clone["arr"].([]any)[1].(map[string]any)["b"] = 100
	clone["strs"].([]string)[0] = "y"
	clone["tablearr"].([]map[string]any)[0]["k"] = "w"
	clone["strmap"].(map[string]string)["k"] = "z"
	if !reflect.DeepEqual(orig, map[string]any{
		"arr":      []any{1, map[string]any{"b": 1}},
		"strs":     []string{"x"},
		"tablearr": []map[string]any{{"k": "v"}},
		"strmap":   map[string]string{"k": "v"},
		"obj":      map[string]any{"deep": map[string]any{"b": 1}},
		"scalar":   42,
	}) {
		t.Fatalf("cloneAnyValue aliases the original: %#v", orig)
	}
}

func TestKeepMatchingRowsNilInNilOut(t *testing.T) {
	t.Parallel()
	if got := keepMatchingRows(nil, []string{"*"}); got != nil {
		t.Fatalf("keepMatchingRows(nil) = %v, want nil", got)
	}
	rows := map[string]Model{"a": {}, "b": {}}
	got := keepMatchingRows(rows, []string{"a*"})
	if len(got) != 1 {
		t.Fatalf("keepMatchingRows = %v, want only a", got)
	}
	if _, ok := got["a"]; !ok {
		t.Fatal("keepMatchingRows lost the matching row")
	}
	if _, ok := rows["b"]; !ok {
		t.Fatal("keepMatchingRows must not mutate the caller's map")
	}
}

func TestUserLayerNoteDescribesTheConfigFile(t *testing.T) {
	t.Parallel()
	config := "[providers.notep]\nprotocol = \"openai-chat\"\nbase_url = \"https://x/v1\"\n"
	r := fixtureLoad(t, nil, config)
	if r.UserLayerNote() == "" {
		t.Fatal("a loaded config file must be described in the user-layer note")
	}
}

// The config root falls back to HOME when XDG_CONFIG_HOME is unset — the
// same root an explicitly-empty XDG value selects.
func TestDefaultConfigRootWithoutXDG(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HOME": "/home/t"}
	got := defaultConfigRoot(mapEnv(env))
	if got == "" {
		t.Fatal("defaultConfigRoot returned empty")
	}
	withXDG := defaultConfigRoot(mapEnv(map[string]string{"XDG_CONFIG_HOME": "", "HOME": "/home/t"}))
	if got != withXDG {
		t.Fatalf("unset XDG = %q, explicit empty = %q; they must agree", got, withXDG)
	}
}

// A fresh cache suppresses the background refresh even when the load is not
// offline: nothing is fetched, nothing starts.
func TestLoadFreshCacheSkipsRefresh(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	f := &fakeFetch{body: fixtureBytes(t), etag: "fresh"}
	if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err != nil {
		t.Fatal(err)
	}
	r, err := Load(
		WithSnapshot(fixtureBytes(t)),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithStateRoot(state),
		WithOffline(false),
	)
	if err != nil {
		t.Fatal(err)
	}
	if r.RefreshStarted() {
		t.Fatal("a fresh cache must suppress the background refresh")
	}
	if f.calls != 1 {
		t.Fatalf("fetcher calls = %d, want 1 (the priming refresh only)", f.calls)
	}
}

// A background refresh that fails logs and keeps the previous catalog.
func TestLoadFailedBackgroundRefreshLogs(t *testing.T) {
	t.Parallel()
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, format) }
	f := &fakeFetch{err: errors.New("boom")}
	r, err := Load(
		WithSnapshot(fixtureBytes(t)),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithStateRoot(t.TempDir()),
		WithFetcher(f.fetcher()),
		WithLog(logf),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !r.RefreshStarted() {
		t.Fatal("an injected fetcher starts the background refresh")
	}
	r.WaitRefresh()
	if len(logs) == 0 {
		t.Fatal("a failed background refresh must log")
	}
	if _, _, ok := readCache(r.StateRoot()); ok {
		t.Fatal("a failed background refresh must not write the cache")
	}
}
