package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// subsetFixture drops the first n providers (by sorted id) from the fixture,
// so the subset is the same on every run.
func subsetFixture(t *testing.T, n int) []byte {
	t.Helper()
	var all map[string]json.RawMessage
	if err := json.Unmarshal(fixtureBytes(t), &all); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids[:n] {
		delete(all, id)
	}
	out, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// An overlay stanza for a provider that vanished upstream must not fail the
// load: the record has no protocol, is hidden, and its fields go unchecked.
func TestLoad_OverlayProviderMissingUpstreamIsHidden(t *testing.T) {
	r := fixtureLoad(t, nil, "", WithOverlay(overlayWith("[providers.vanished]\nfields = { store = true }\nbase_url = \"https://x/v1\"\n")))
	p, ok := r.Provider("vanished")
	if !ok || !p.Hidden {
		t.Fatalf("vanished provider: ok=%v hidden=%v", ok, p.Hidden)
	}
}

type fakeFetch struct {
	body        []byte
	etag        string
	calls       int
	gotEtag     []string
	notModified bool
	err         error
}

func (f *fakeFetch) fetcher() Fetcher {
	return func(_ context.Context, etag string) ([]byte, string, bool, error) {
		f.calls++
		f.gotEtag = append(f.gotEtag, etag)
		if f.err != nil {
			return nil, "", false, f.err
		}
		if f.notModified {
			return nil, f.etag, true, nil
		}
		return f.body, f.etag, false, nil
	}
}

func TestRefresh_WritesCacheAndRoundTripsEtag(t *testing.T) {
	state := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	f := &fakeFetch{body: subsetFixture(t, 1), etag: `W/"v1"`}
	res, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Force: true, Now: func() time.Time { return now }, Baseline: fixtureBytes(t)})
	if err != nil || !res.Updated || res.ProvidersAfter != 39 || res.ProvidersBefore != 40 {
		t.Fatalf("first refresh: %+v %v", res, err)
	}
	raw, meta, ok := readCache(state)
	if !ok || meta.Etag != `W/"v1"` || !meta.FetchedAt.Equal(now) || len(raw) == 0 {
		t.Fatalf("cache: ok=%v meta=%+v", ok, meta)
	}
	f.notModified = true
	later := now.Add(25 * time.Hour)
	res, err = Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Now: func() time.Time { return later }, Baseline: fixtureBytes(t)})
	if err != nil || !res.NotModified || res.Updated {
		t.Fatalf("304: %+v %v", res, err)
	}
	if f.gotEtag[1] != `W/"v1"` {
		t.Fatalf("If-None-Match must carry the stored etag, got %q", f.gotEtag[1])
	}
	if _, meta, _ := readCache(state); !meta.FetchedAt.Equal(later) {
		t.Fatal("a 304 refreshes fetched_at")
	}
	res, err = Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Now: func() time.Time { return later.Add(time.Hour) }, Baseline: fixtureBytes(t)})
	if err != nil || !res.Skipped || f.calls != 2 {
		t.Fatalf("a fresh cache is skipped without --force: %+v calls=%d", res, f.calls)
	}
}

func TestRefresh_SanityFloorsAndFailuresKeepCache(t *testing.T) {
	state := t.TempDir()
	good := &fakeFetch{body: fixtureBytes(t), etag: "a"}
	if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: good.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := readCache(state)
	for name, f := range map[string]*fakeFetch{
		"too few providers": {body: subsetFixture(t, 10), etag: "b"},
		"invalid json":      {body: []byte("{"), etag: "c"},
		"empty":             {body: []byte("{}"), etag: "d"},
		"network error":     {err: errors.New("boom")},
	} {
		if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		after, _, _ := readCache(state)
		if !bytes.Equal(after, before) {
			t.Errorf("%s: a failed refresh must keep the previous cache", name)
		}
	}
}

func TestLoad_PrefersNewerCacheAndOfflineDefault(t *testing.T) {
	state := t.TempDir()
	data := fixtureBytes(t)
	r, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(state))
	if err != nil {
		t.Fatal(err)
	}
	if r.RefreshStarted() {
		t.Fatal("under go test, Load must not start a refresh")
	}
	if tag, _ := r.Catalog(); tag != LayerSnapshot {
		t.Fatalf("catalog = %q", tag)
	}
	f := &fakeFetch{body: subsetFixture(t, 1), etag: "e"}
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(state), WithFetcher(f.fetcher()), WithLog(func(string, ...any) {}))
	if err != nil {
		t.Fatal(err)
	}
	if !r.RefreshStarted() {
		t.Fatal("an injected fetcher opts back into the refresh")
	}
	r.WaitRefresh()
	if _, _, ok := readCache(state); !ok {
		t.Fatal("background refresh must write the cache")
	}
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(state))
	if err != nil {
		t.Fatal(err)
	}
	if tag, _ := r.Catalog(); tag != LayerCache {
		t.Fatalf("a newer cache must replace the snapshot, got %q", tag)
	}
	if len(r.ProviderIDs()) < 39 {
		t.Fatalf("cache providers = %d", len(r.ProviderIDs()))
	}
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(map[string]string{"EVENER_OFFLINE": "1"})), WithNoUserLayer(), WithStateRoot(t.TempDir()), WithFetcher(f.fetcher()))
	if err != nil || r.RefreshStarted() {
		t.Fatalf("EVENER_OFFLINE=1 must win over an injected fetcher: %v", err)
	}
}

func TestLoad_WithoutCacheIgnoresANewerCache(t *testing.T) {
	state := t.TempDir()
	f := &fakeFetch{body: subsetFixture(t, 1), etag: "n"}
	if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err != nil {
		t.Fatal(err)
	}
	// subsetFixture(t, 1) drops amazon-bedrock (first by sorted id): with the
	// cache in effect the overlay's stanza alone remains, protocol-less.
	r, err := Load(WithSnapshot(fixtureBytes(t)), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(state))
	if err != nil {
		t.Fatal(err)
	}
	if tag, _ := r.Catalog(); tag != LayerCache {
		t.Fatalf("without the option the newer cache wins: %q", tag)
	}
	if p, _ := r.Provider("amazon-bedrock"); p.Protocol != "" {
		t.Fatalf("the cache lacks amazon-bedrock upstream; protocol = %q", p.Protocol)
	}
	r, err = Load(WithSnapshot(fixtureBytes(t)), WithoutCache(), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(state))
	if err != nil {
		t.Fatal(err)
	}
	if tag, _ := r.Catalog(); tag != LayerSnapshot {
		t.Fatalf("WithoutCache must load the snapshot alone: %q", tag)
	}
	if p, _ := r.Provider("amazon-bedrock"); p.Protocol != ProtocolAnthropic {
		t.Fatalf("the snapshot has amazon-bedrock upstream; protocol = %q", p.Protocol)
	}
}

func TestLoad_CorruptCacheFallsBackToSnapshot(t *testing.T) {
	state := t.TempDir()
	jsonPath, metaPath := cachePaths(state)
	_ = os.MkdirAll(filepath.Dir(jsonPath), 0o700)
	_ = os.WriteFile(jsonPath, []byte("{"), 0o600)
	_ = os.WriteFile(metaPath, []byte(`{"fetched_at":"2099-01-01T00:00:00Z","etag":"x","source":"y"}`), 0o600)
	r, err := Load(WithSnapshot(fixtureBytes(t)), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(state))
	if err != nil {
		t.Fatal(err)
	}
	if tag, _ := r.Catalog(); tag != LayerSnapshot || len(r.Warnings()) == 0 {
		t.Fatalf("corrupt cache must fall back with a warning: %q %v", tag, r.Warnings())
	}
}
