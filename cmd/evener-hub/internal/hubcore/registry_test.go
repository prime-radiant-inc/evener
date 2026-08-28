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
