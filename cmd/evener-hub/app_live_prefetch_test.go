package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/valueexpr"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

func TestPrefetchLiveModelsPopulatesHeldRegistry(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"},{"id":"text-embedding-3-small"}]}`)
	dir := filepath.Dir(tomlPath)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	prefetchAllLiveModels(context.Background(), ctl.reg, func() {})
	got := entry(t, ctl.List(), "gw")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "gpt-live" && !m.Disabled }) {
		t.Fatalf("entry models = %+v, want live gpt-live", got.Models)
	}
	if slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "text-embedding-3-small" }) {
		t.Fatalf("entry models = %+v, want the embedding id filtered out", got.Models)
	}
}

// The hub never executes a credential command (spec §10.1): the prefetch
// pass skips an instance whose credential material is command-backed
// rather than mint for it — no command runs, no request leaves, and the
// holder keeps whatever rows it already has. The agent's own live
// listing is the child's to make and stays untouched.
func TestPrefetchSkipsCommandCredentialedInstances(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-live"}]}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key = '''$(gw-mint)'''\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	prefetchAllLiveModels(context.Background(), ctl.reg, func() {})
	if runs != 0 {
		t.Fatalf("the prefetch executed the credential command %d time(s); the hub never runs credential commands", runs)
	}
	if hits != 0 {
		t.Fatal("the prefetch contacted the endpoint for an instance whose credential it never materializes")
	}
}

// The instance-list RPC mints nothing: its destination fingerprints read
// the transport identity alone, and the hub executes credential commands
// never (spec §10.1) — opening the settings pane must not run a
// password-manager command with no session launched.
func TestInstanceListNeverMintsCommandCredentials(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key = '''$(gw-mint)'''\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(ctl.List().Instances) == 0 {
		t.Fatal("no instances listed")
	}
	if runs != 0 {
		t.Fatalf("the instance-list RPC executed the credential command %d time(s); the pane displays destinations, the child alone runs credential commands", runs)
	}
}

func TestPrefetchLiveModelsBroadcastsOnCapabilityChange(t *testing.T) {
	// Same id set, different advertised facts: the pass must still
	// broadcast, or the picker's cached descriptors go stale.
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"}]}`)
	dir := filepath.Dir(tomlPath)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	announced := 0
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 1 {
		t.Fatalf("announced = %d, want 1 after initial pass", announced)
	}
	// Simulate a capability change behind the same id: re-apply live
	// rows with a context window the cache did not have, then run a
	// pass whose endpoint reports the same id. The broadcast must
	// fire on the facts change even though the id set is identical.
	reg := ctl.reg.Get()
	contextWindow := 100
	reg.ApplyLive("gw", []registry.Model{{ID: "gpt-live", Caps: registry.Caps{ContextWindow: &contextWindow}}})
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 2 {
		t.Fatalf("announced = %d, want 2 after a capability-only change with identical ids", announced)
	}
}

// liveClientTestRegistry loads a minimal registry pinned to a state
// root, so LiveRegistryClient concurrency tests can alternate roots
// without any network.
func liveClientTestRegistry(t *testing.T, root string) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte("[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"k\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(path),
		registry.WithStateRoot(filepath.Join(t.TempDir(), root)),
		registry.WithOffline(true),
		registry.WithoutCache(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLiveRegistryClientTouchesNoProcessGlobals(t *testing.T) {
	// LiveRegistryClient must not write process globals: concurrent
	// fetches bind scoped authenticators per request instead. Pin by
	// recording the globals, hammering construction from goroutines
	// with alternating roots, and asserting the globals never move.
	// (Fails if the builder rewrites DefaultCodex.StateDir or
	// ClientVersion per call.)
	r1 := liveClientTestRegistry(t, "root-one")
	r2 := liveClientTestRegistry(t, "root-two")
	beforeRoot, beforeVersion := tokenauth.DefaultCodex.StateDir, tokenauth.ClientVersion
	done := make(chan *llm.Client, 16)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); done <- LiveRegistryClient(r1) }()
		go func() { defer wg.Done(); done <- LiveRegistryClient(r2) }()
	}
	wg.Wait()
	close(done)
	for c := range done {
		if c == nil {
			t.Fatal("LiveRegistryClient returned nil under concurrency")
		}
	}
	if tokenauth.DefaultCodex.StateDir != beforeRoot || tokenauth.ClientVersion != beforeVersion {
		t.Fatalf(
			"LiveRegistryClient moved process globals: StateDir %q -> %q, version %q -> %q",
			beforeRoot, tokenauth.DefaultCodex.StateDir, beforeVersion, tokenauth.ClientVersion,
		)
	}
}

func TestPrefetchLiveModelsBroadcastsOnlyOnChange(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"},{"id":"text-embedding-3-small"}]}`)
	dir := filepath.Dir(tomlPath)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	announced := 0
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 1 {
		t.Fatalf("announced = %d, want 1 after a pass that adds live ids", announced)
	}
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 1 {
		t.Fatalf("announced = %d, want still 1 after a pass that changes nothing", announced)
	}
}

func TestPrefetchLiveModelsSurvivesUnreachable(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"}]}`)
	dir := filepath.Dir(tomlPath)
	// A closed-port sibling must not fail or stall the pass.
	cfg, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("[providers.dead]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"test-key\"\n")...)
	if err := os.WriteFile(tomlPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	prefetchAllLiveModels(context.Background(), ctl.reg, func() {})
	got := entry(t, ctl.List(), "gw")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "gpt-live" }) {
		t.Fatalf("entry models = %+v, want live gpt-live despite dead sibling", got.Models)
	}
}
