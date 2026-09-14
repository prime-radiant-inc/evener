package hubcore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// hermeticLoader is cmdutil.LoadRegistry with the network and the catalog
// cache taken away, so a test observes only the user layer and the env it
// sets itself.
func hermeticLoader(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
	return cmdutil.LoadRegistry(append(extra, registry.WithOffline(true), registry.WithoutCache())...)
}

func TestProviderRegistryDegradesOnOldSchema(t *testing.T) {
	configRoot := t.TempDir()
	path := filepath.Join(configRoot, "providers.toml")
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "gk")

	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err == nil || !errors.Is(err, registry.ErrOldSchema) {
		t.Fatalf("Reload reports the pointer: %v", err)
	}
	if h.Get() == nil || !h.WritesRefused() {
		t.Fatal("the hub keeps an implicit-only registry and refuses writes (spec §10)")
	}
	if _, ok := h.Get().Instance("groq"); !ok {
		t.Fatal("implicit instances still exist without the user layer")
	}
	diags := strings.Join(h.Diagnostics(), "\n")
	if !strings.Contains(diags, "§14.1") || !strings.Contains(diags, "user layer: none") {
		t.Fatalf("diagnostics carry the pointer and the user-layer note: %s", diags)
	}

	if err := os.WriteFile(path, []byte("default = \"groq\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil || h.WritesRefused() {
		t.Fatalf("a fixed file clears the refusal: %v %v", err, h.WritesRefused())
	}
	if got := h.LoadError(); got != nil {
		t.Fatalf("LoadError after a good reload = %v, want nil", got)
	}
	diags = strings.Join(h.Diagnostics(), "\n")
	if !strings.Contains(diags, "user layer: "+path) {
		t.Fatalf("diagnostics name the file that loaded: %s", diags)
	}
}

// TestReapplyLiveDiscardsSupersededFetch proves the token contract: a
// fetch that claims after a newer claim for the same instance (or after
// a Reload) must not overwrite the newer listing. Tokens are claimed
// once a fetch has a listing to publish, so a failed fetch never mints
// one at all.
func TestReloadFailurePreservesLiveForRestoredInstances(t *testing.T) {
	// A failed reload parks the holder on the implicit-only fallback;
	// when the file is fixed and the last-good registry comes back,
	// live-only ids for its explicit instances must come back too —
	// not just the ids the fallback knew.
	t.Setenv("GROQ_API_KEY", "gk")
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte("[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	h.ReapplyLive(mustBegin(t, h, "gw"), "gw", h.identityOf(t, "gw"), []registry.Model{{ID: "gpt-live-x"}})
	if got := h.Get().LiveModels("gw"); len(got) == 0 {
		t.Fatal("no live rows before failure")
	}
	// Break the file: the holder falls back to implicit-only, which
	// knows no gw at all.
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err == nil {
		t.Fatal("broken file must fail Reload")
	}
	// Fix the file: gw comes back with its live-only id.
	if err := os.WriteFile(path, []byte("[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := h.Get().LiveModels("gw"); len(got) == 0 {
		t.Fatal("live ids lost through failed-reload recovery")
	}
}

func mustBegin(t *testing.T, h *ProviderRegistry, instance string) uint64 {
	t.Helper()
	_, tok, _ := h.BeginLiveFetchReg(instance)
	return tok
}

func (h *ProviderRegistry) identityOf(t *testing.T, instance string) string {
	t.Helper()
	inst, ok := h.Get().Instance(instance)
	if !ok {
		t.Fatalf("no %s instance to fingerprint", instance)
	}
	return instanceIdentity(inst)
}

func TestReapplyLiveDiscardsListingForChangedEndpoint(t *testing.T) {
	// A fetch that began against endpoint A must not publish into a
	// registry whose instance now points at endpoint B: the rows came
	// from the wrong transport. Simulate the re-point by recording a
	// new snapshot identity for the name, then replaying the old
	// token: the rows must be dropped.
	t.Setenv("GROQ_API_KEY", "gk")
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	reg, tok, _ := h.BeginLiveFetchReg("groq")
	if reg == nil {
		t.Fatal("no registry snapshot")
	}
	if _, ok := reg.Instance("groq"); !ok {
		t.Fatal("no groq instance to fingerprint")
	}
	// The real race: the instance is re-pointed and fetch B begins;
	// A's apply is then judged against the endpoint A actually
	// queried, not the latest recorded.
	_, _, _ = h.BeginLiveFetchReg("groq")
	h.ReapplyLive(tok, "groq", "stale-endpoint-X", []registry.Model{{ID: "gpt-live-x"}})
	if got := h.Get().LiveModels("groq"); len(got) != 0 {
		t.Fatalf("live ids = %+v, want stale-endpoint rows dropped", got)
	}
}

func TestReapplyLiveFailedNewerDoesNotBlockOlderSuccess(t *testing.T) {
	// Fetch A starts first and succeeds; fetch B starts after but fails
	// and so never applies. B's failure must not invalidate A's success:
	// the older listing still lands.
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	_, older, _ := h.BeginLiveFetchReg("gw")
	_, _, _ = h.BeginLiveFetchReg("gw") // newer fetch, fails: applies nothing
	h.ReapplyLive(older, "gw", "", []registry.Model{{ID: "gpt-live-x"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		t.Fatal("live ids empty, want the older success to land despite the failed newer fetch")
	}
}

func TestReapplyLiveDiscardsOutOfOrderFetch(t *testing.T) {
	// Fetch A starts first but finishes last: its token was minted at
	// request start, so the faster fetch B's apply supersedes it and A's
	// stale listing must not overwrite B's newer one.
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	_, slow, _ := h.BeginLiveFetchReg("gw")
	_, fast, _ := h.BeginLiveFetchReg("gw")
	h.ReapplyLive(fast, "gw", "", []registry.Model{{ID: "gpt-new-x"}})
	h.ReapplyLive(slow, "gw", "", []registry.Model{{ID: "gpt-old-x"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		if id == "gpt-old-x" {
			t.Fatalf("live ids = %v, want out-of-order gpt-old discarded", ids)
		}
	}
	if len(ids) == 0 {
		t.Fatal("live ids empty, want gpt-new applied")
	}
}

func TestReapplyLiveDiscardsSupersededFetch(t *testing.T) {
	// The genuinely out-of-order pair: the older token applies first,
	// then the newer claim overwrites it and a repeat of the older
	// apply is discarded — coverage the failed-newer and
	// out-of-order arrivals above do not give.
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	_, older, idOld := h.BeginLiveFetchReg("gw")
	_, newer, _ := h.BeginLiveFetchReg("gw")
	h.ReapplyLive(older, "gw", idOld, []registry.Model{{ID: "gpt-old-x"}})
	h.ReapplyLive(newer, "gw", "", []registry.Model{{ID: "gpt-new-x"}})
	h.ReapplyLive(older, "gw", idOld, []registry.Model{{ID: "gpt-old-x"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		if id == "gpt-old-x" {
			t.Fatalf("live ids = %v, want replayed older apply discarded", ids)
		}
	}
	if len(ids) == 0 {
		t.Fatal("live ids empty, want gpt-new applied")
	}
}
