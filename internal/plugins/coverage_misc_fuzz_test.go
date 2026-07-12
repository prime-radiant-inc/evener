//go:build serffuzz

package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func FuzzPluginsMiscCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Run("render", fuzzRenderDoctor)
		t.Run("paths", fuzzManagerDefaults)
		t.Run("catalog", fuzzCatalogEdges)
		t.Run("registry", fuzzRegistryEdges)
		t.Run("gc-doctor", fuzzGCDoctorEdges)
		t.Run("install-errors", fuzzInstallErrors)
	})
}

func fuzzInstallErrors(t *testing.T) {
	ctx := context.Background()
	m := NewManager(t.TempDir())
	if _, err := m.Install(ctx, "plugin", "../bad"); err == nil {
		t.Fatal("invalid marketplace installed")
	}
	if _, err := m.Install(ctx, "../bad", "market"); err == nil {
		t.Fatal("invalid plugin installed")
	}
	if _, err := m.Upgrade(ctx, "plugin", "../bad"); err == nil {
		t.Fatal("invalid marketplace upgraded")
	}
	if _, err := m.Upgrade(ctx, "../bad", "market"); err == nil {
		t.Fatal("invalid plugin upgraded")
	}
	if _, err := m.Upgrade(ctx, "missing", "market"); err == nil {
		t.Fatal("missing plugin upgraded")
	}
	if err := m.SetEnabled("missing", "market", true); err == nil {
		t.Fatal("missing plugin mutated")
	}
	if err := m.Remove("missing", "market"); err == nil {
		t.Fatal("missing plugin removed")
	}

	reg := Registry{Plugins: map[string][]InstallEntry{"empty@market": {}}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}
	items, err := m.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("empty list = %#v, %v", items, err)
	}
	if _, err := m.UpdateAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("empty", "market"); err != nil {
		t.Fatal(err)
	}

	staging := filepath.Join(t.TempDir(), "staging")
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	blockRoot := t.TempDir()
	bm := NewManager(blockRoot)
	if err := os.MkdirAll(filepath.Join(blockRoot, "cache", "market", "plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	finalParent := filepath.Join(blockRoot, "cache", "market", "plugin")
	if err := os.RemoveAll(finalParent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalParent, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bm.commitStaged("market", "plugin", staging, "sha"); err == nil {
		t.Fatal("commit below file succeeded")
	}

	if err := os.WriteFile(m.registryPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(); err == nil {
		t.Fatal("corrupt registry listed")
	}
	if _, err := m.UpdateAll(ctx); err == nil {
		t.Fatal("corrupt registry updated")
	}
	if err := m.SetAutoUpgrade("p", "m", true); err == nil {
		t.Fatal("corrupt registry mutated")
	}
	if err := m.Remove("p", "m"); err == nil {
		t.Fatal("corrupt registry removed")
	}
}

func fuzzRenderDoctor(t *testing.T) {
	findings := []DoctorFinding{
		{Level: LevelOK, Category: catEnvironment, Message: "ok"},
		{Level: LevelWarn, Category: catRegistry, Message: "warn", Remediation: "repair"},
		{Level: LevelFail, Category: catRegistry, Message: "fail"},
		{Level: "other", Category: catComponent, Message: "other"},
	}
	got := RenderDoctorFindings(findings)
	for _, want := range []string{"1 OK, 1 WARN, 1 FAIL", "[environment]", "[registry]", "-> repair", "other"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report %q lacks %q", got, want)
		}
	}
}

func fuzzManagerDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager("")
	if m.Root == "" {
		t.Fatal("empty default root")
	}
	m.Now = nil
	if m.now().IsZero() {
		t.Fatal("zero default time")
	}
	m.Stderr = nil
	if m.stderr() == nil {
		t.Fatal("nil default stderr")
	}
	if got := computeVersion("", "", "short"); got != "short" {
		t.Fatalf("short sha = %q", got)
	}
	if got := computeVersion("", "", "1234567890123"); got != "123456789012" {
		t.Fatalf("long sha = %q", got)
	}
	if got := computeVersion("", "", ""); got != "unknown" {
		t.Fatalf("empty version = %q", got)
	}
	if p, market := splitKey("plain"); p != "plain" || market != "" {
		t.Fatalf("split = %q,%q", p, market)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
	if got := DefaultRoot(); got == "" {
		t.Fatal("home fallback empty")
	}
	origHome := pluginUserHomeDir
	pluginUserHomeDir = func() (string, error) { return "", errors.New("home fault") }
	t.Cleanup(func() { pluginUserHomeDir = origHome })
	if got := DefaultRoot(); got != "" {
		t.Fatalf("home fault root = %q", got)
	}
}

func fuzzCatalogEdges(t *testing.T) {
	root := t.TempDir()
	if _, err := ParseCatalog(root); err == nil {
		t.Fatal("missing catalog succeeded")
	}
	dir := filepath.Join(root, ".claude-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marketplace.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(root); err == nil {
		t.Fatal("malformed catalog succeeded")
	}
	if got := catalogPluginName([]byte("{")); got != "(unknown)" {
		t.Fatalf("bad plugin name = %q", got)
	}
	if got := catalogPluginName([]byte(`{"name":""}`)); got != "(unknown)" {
		t.Fatalf("empty plugin name = %q", got)
	}

	m := NewManager(t.TempDir())
	if _, err := m.Browse(context.Background(), "missing"); err == nil {
		t.Fatal("missing browse succeeded")
	}
	blocked := filepath.Join(t.TempDir(), "root")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(blocked).Browse(context.Background(), "missing"); err == nil {
		t.Fatal("browse lock fault succeeded")
	}
}

func fuzzRegistryEdges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "registry.json")
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(path)
	if err != nil || r.Plugins == nil {
		t.Fatalf("nil plugins normalization: %#v, %v", r, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Fatal("malformed registry succeeded")
	}

	block := filepath.Join(root, "block")
	if err := os.WriteFile(block, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegistry(filepath.Join(block, "registry.json"), Registry{}); err == nil {
		t.Fatal("registry below file succeeded")
	}
	origMarshal := marshalRegistry
	marshalRegistry = func(any, string, string) ([]byte, error) { return nil, errors.New("marshal fault") }
	t.Cleanup(func() { marshalRegistry = origMarshal })
	if err := SaveRegistry(filepath.Join(root, "marshal.json"), Registry{}); err == nil {
		t.Fatal("marshal fault succeeded")
	}

	var warnings bytes.Buffer
	m := &Manager{Root: block, Stderr: &warnings}
	if got := listOrWarn(m); got != nil || warnings.Len() == 0 {
		t.Fatalf("listOrWarn = %#v, %q", got, warnings.String())
	}
	if got := m.EnabledPluginDirs([]string{filepath.Join(root, "missing")}); len(got) != 0 {
		t.Fatalf("enabled = %#v", got)
	}
}

func fuzzGCDoctorEdges(t *testing.T) {
	root := t.TempDir()
	m := &Manager{Root: root, Now: func() time.Time { return time.Unix(1, 0) }, Stderr: &bytes.Buffer{}}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.cacheDir(), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Gc(); err == nil {
		t.Fatal("gc cache file succeeded")
	}
	findings := m.doctorOrphanCacheDirs(nil)
	if len(findings) != 1 || findings[0].Level != LevelFail {
		t.Fatalf("orphan findings = %#v", findings)
	}

	fileRoot := filepath.Join(t.TempDir(), "store")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fm := &Manager{Root: fileRoot}
	if exists, err := fm.checkStoreWritable(); !exists || err == nil {
		t.Fatalf("writable file = %v,%v", exists, err)
	}
	env := fm.doctorEnvironment()
	if len(env) != 2 || env[1].Level != LevelFail {
		t.Fatalf("environment = %#v", env)
	}
}
