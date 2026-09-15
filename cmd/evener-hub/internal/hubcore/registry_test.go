package hubcore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestProviderRegistryOrdersConcurrentReloads: a reload is a load and a commit,
// and mu alone serializes only the commit - so two reloads that load
// concurrently can commit in the opposite order, leaving the holder serving the
// view the slower one read. Here the reload that reads first is held inside its
// load until the other has finished, which is the worst case: it commits last.
// (Its wait expires when the two are properly ordered, because then the other
// cannot finish while this one is still loading.)
func TestProviderRegistryOrdersConcurrentReloads(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stateDir := t.TempDir()

	olderPath := filepath.Join(configRoot, "older.toml")
	newerPath := filepath.Join(configRoot, "newer.toml")
	if err := os.WriteFile(olderPath, []byte("[providers.work]\nbase = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerPath, []byte("[providers.work2]\nbase = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var loads int
	secondDone := make(chan struct{})
	load := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		mu.Lock()
		loads++
		n := loads
		mu.Unlock()
		path := newerPath
		if n == 1 {
			path = olderPath
			select {
			case <-secondDone:
			case <-time.After(2 * time.Second):
			}
		}
		opts := append([]registry.Option{}, extra...)
		opts = append(opts, registry.WithConfigPath(path), registry.WithStateRoot(stateDir))
		return hermeticLoader(opts...)
	}

	holder := NewProviderRegistry(load)
	firstErr := make(chan error, 1)
	go func() { firstErr <- holder.Reload() }()
	for deadline := time.Now().Add(2 * time.Second); ; {
		mu.Lock()
		started := loads >= 1
		mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first reload never entered its load")
		}
		time.Sleep(time.Millisecond)
	}

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- holder.Reload()
		close(secondDone)
	}()

	if err := <-firstErr; err != nil {
		t.Fatalf("first Reload: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second Reload: %v", err)
	}
	// The reload that read last wins: the holder has to serve the newer file,
	// not the view a reload that merely committed last happened to load.
	if _, ok := holder.Get().Instance("work2"); !ok {
		t.Fatal("the holder serves the older view: the reload that read first committed last")
	}
	if _, ok := holder.Get().Instance("work"); ok {
		t.Fatal("the holder still serves the instance only the older file has")
	}
}
