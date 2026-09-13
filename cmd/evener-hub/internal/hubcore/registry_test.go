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
	older := h.BeginLiveFetch("gw")
	_ = h.BeginLiveFetch("gw") // newer fetch, fails: applies nothing
	h.ReapplyLive(older, "gw", []registry.Model{{ID: "gpt-live"}})
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
	slow := h.BeginLiveFetch("gw")
	fast := h.BeginLiveFetch("gw")
	h.ReapplyLive(fast, "gw", []registry.Model{{ID: "gpt-new"}})
	h.ReapplyLive(slow, "gw", []registry.Model{{ID: "gpt-old"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		if id == "gpt-old" {
			t.Fatalf("live ids = %v, want out-of-order gpt-old discarded", ids)
		}
	}
	if len(ids) == 0 {
		t.Fatal("live ids empty, want gpt-new applied")
	}
}

func TestReapplyLiveDiscardsSupersededFetch(t *testing.T) {
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	stale := h.BeginLiveFetch("gw")
	fresh := h.BeginLiveFetch("gw")
	h.ReapplyLive(fresh, "gw", []registry.Model{{ID: "gpt-new"}})
	h.ReapplyLive(stale, "gw", []registry.Model{{ID: "gpt-old"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		if id == "gpt-old" {
			t.Fatalf("live ids = %v, want superseded gpt-old discarded", ids)
		}
	}
}
