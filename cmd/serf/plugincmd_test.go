package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/internal/plugins"
)

func TestPluginMarketplaceList_Empty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"marketplace", "list"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin marketplace list: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "No marketplaces") && strings.TrimSpace(out.String()) != "" {
		// empty store: either a friendly "No marketplaces" line or empty output
		t.Logf("marketplace list output: %q", out.String())
	}
}

func TestPluginUnknownSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	err := runPlugin([]string{"bogus"}, nil, &out, &errb)
	if err == nil {
		t.Fatal("unknown plugin subcommand should error")
	}
}

func TestParseMarketplaceSourceArg(t *testing.T) {
	cases := map[string]plugins.SourceKind{
		"anthropics/claude-plugins-official": plugins.SourceGitHub,
		"https://gitlab.com/x/y.git":         plugins.SourceURL,
		"/some/local/path":                   plugins.SourceDirectory,
	}
	for arg, wantKind := range cases {
		src, err := parseMarketplaceSourceArg(arg)
		if err != nil || src.Kind != wantKind {
			t.Errorf("parseMarketplaceSourceArg(%q) = %+v, %v; want kind %s", arg, src, err, wantKind)
		}
	}
}

func TestPluginMarketplaceAdd_RequiresConfirmation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	err := runPlugin([]string{"marketplace", "add", "https://example.com/repo.git"}, nil, &out, &errb)
	if err == nil {
		t.Fatal("add without --yes should require confirmation")
	}
	if !strings.Contains(err.Error(), "confirmation") && !strings.Contains(errb.String(), "confirmation") {
		t.Logf("Expected confirmation message in error or stderr")
	}
}

func TestPluginMarketplaceRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := plugins.NewManager("")

	// Create a valid marketplace directory with manifest
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifest := plugins.Catalog{Name: "test-marketplace"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "marketplace.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Add a marketplace first
	ctx := context.Background()
	_, err := m.AddMarketplace(ctx, "test-marketplace", plugins.Source{Kind: plugins.SourceDirectory, Path: tmpDir})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	// Remove it via CLI
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"marketplace", "remove", "test-marketplace"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin marketplace remove: %v\n%s", err, errb.String())
	}

	// Verify it's gone
	mk, err := m.ListMarketplaces()
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if _, ok := mk["test-marketplace"]; ok {
		t.Fatal("marketplace should be removed")
	}
}

// TestPluginMarketplaceBrowse_ListsPluginsAndNotesSkipped covers the missing
// CLI capability the user explicitly asked for ("explore plugins in
// marketplaces"): web and TUI already have Browse, the CLI didn't. It also
// proves the browse output surfaces the Fix 1 skip-and-warn behavior (a
// npm-source plugin dropped from the catalog) rather than hiding it.
func TestPluginMarketplaceBrowse_ListsPluginsAndNotesSkipped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := plugins.NewManager("")

	tmpDir := t.TempDir()
	metaDir := filepath.Join(tmpDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mj := `{"name":"browse-mkt","owner":{"name":"o"},"plugins":[` +
		`{"name":"widget","description":"a fine widget","source":"./plugins/widget"},` +
		`{"name":"bad-npm","description":"unsupported","source":{"source":"npm","package":"x"}}` +
		`]}`
	if err := os.WriteFile(filepath.Join(metaDir, "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "browse-mkt", plugins.Source{Kind: plugins.SourceDirectory, Path: tmpDir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	var out, errb bytes.Buffer
	if err := runPlugin([]string{"marketplace", "browse", "browse-mkt"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin marketplace browse: %v\n%s", err, errb.String())
	}
	output := out.String()
	if !strings.Contains(output, "widget") || !strings.Contains(output, "a fine widget") {
		t.Errorf("browse output missing plugin info: %q", output)
	}
	if !strings.Contains(output, "bad-npm") {
		t.Errorf("browse output should note the skipped unsupported-source plugin, got: %q", output)
	}
}

func TestPluginMarketplaceBrowse_JSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := plugins.NewManager("")

	tmpDir := t.TempDir()
	metaDir := filepath.Join(tmpDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mj := `{"name":"browse-json","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`
	if err := os.WriteFile(filepath.Join(metaDir, "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "browse-json", plugins.Source{Kind: plugins.SourceDirectory, Path: tmpDir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	var out, errb bytes.Buffer
	if err := runPlugin([]string{"marketplace", "browse", "--json", "browse-json"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin marketplace browse --json: %v\n%s", err, errb.String())
	}
	var cat plugins.Catalog
	if err := json.Unmarshal(out.Bytes(), &cat); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].Name != "widget" {
		t.Fatalf("catalog = %+v, want one plugin named widget", cat)
	}
}

func TestPluginMarketplaceBrowse_RequiresName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"marketplace", "browse"}, nil, &out, &errb); err == nil {
		t.Fatal("browse without a marketplace name should error")
	}
}

func TestPluginMarketplaceList_WithJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := plugins.NewManager("")

	// Create a valid marketplace directory with manifest
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifest := plugins.Catalog{Name: "test-mp"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "marketplace.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Add a test marketplace
	ctx := context.Background()
	_, err := m.AddMarketplace(ctx, "test-mp", plugins.Source{Kind: plugins.SourceDirectory, Path: tmpDir})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	// List as JSON
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"marketplace", "list", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin marketplace list --json: %v\n%s", err, errb.String())
	}

	output := out.String()
	if !strings.Contains(output, "test-mp") {
		t.Errorf("JSON output should contain marketplace name, got: %q", output)
	}
	if !strings.Contains(output, "{") {
		t.Errorf("JSON output should be valid JSON, got: %q", output)
	}
}

func TestPluginHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"help"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin help: %v", err)
	}
	// Help should print usage (typically to stderr in this style)
	usage := errb.String()
	if !strings.Contains(usage, "plugin") && !strings.Contains(usage, "marketplace") {
		t.Logf("Expected usage output, got: %q", usage)
	}
}

// TestPluginList_JSONEmpty pins the exact `[]` encoding for an empty store,
// mirroring TestPluginGc_JSON: Manager.List() returning a nil slice would
// json.Encode as `null`, which is a worse API for scripts to consume than an
// empty array (null needs a nil check before ranging in most languages; `[]`
// does not).
func TestPluginList_JSONEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"list", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("plugin list --json: %v\n%s", err, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("plugin list --json with nothing installed = %q, want []", got)
	}
}

func TestSplitPluginRef(t *testing.T) {
	p, m, err := splitPluginRef("widget@acme")
	if err != nil || p != "widget" || m != "acme" {
		t.Fatalf("splitPluginRef = %q,%q,%v", p, m, err)
	}
	if _, _, err := splitPluginRef("noatsign"); err == nil {
		t.Fatal("expected error for missing @")
	}
}

// installDirectoryPluginForTest registers a directory-source marketplace with
// one plugin (a relative "./plugins/<name>" source) and installs it, giving
// tests a real registry entry to act on (auto-upgrade toggling, check-now)
// without depending on git.
func installDirectoryPluginForTest(t *testing.T, name, marketplace string) {
	t.Helper()
	mktDir := t.TempDir()
	metaDir := filepath.Join(mktDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mj := `{"name":"` + marketplace + `","owner":{"name":"o"},"plugins":[{"name":"` + name + `","source":"./plugins/` + name + `"}]}`
	if err := os.WriteFile(filepath.Join(metaDir, "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	pluginMetaDir := filepath.Join(mktDir, "plugins", name, ".claude-plugin")
	if err := os.MkdirAll(pluginMetaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginMetaDir, "plugin.json"), []byte(`{"name":"`+name+`","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m := plugins.NewManager("")
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, marketplace, plugins.Source{Kind: plugins.SourceDirectory, Path: mktDir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, name, marketplace); err != nil {
		t.Fatalf("Install: %v", err)
	}
}

func listPluginsJSON(t *testing.T) []plugins.ListItem {
	t.Helper()
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"list", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin list --json: %v\n%s", err, errb.String())
	}
	var items []plugins.ListItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	return items
}

// TestPluginAutoUpgrade_RoundTripsInList covers the missing CLI capability
// the user explicitly asked for ("auto-upgrade plugins"): web's
// setAutoUpgrade and the TUI's 'a' key could already toggle it, the CLI
// could only display the flag in `list`'s AUTO-UPGRADE column, not set it.
func TestPluginAutoUpgrade_RoundTripsInList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	installDirectoryPluginForTest(t, "widget", "local")

	var out, errb bytes.Buffer
	if err := runPlugin([]string{"auto-upgrade", "widget@local"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin auto-upgrade: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "enabled") {
		t.Errorf("expected confirmation output mentioning \"enabled\", got %q", out.String())
	}
	items := listPluginsJSON(t)
	if len(items) != 1 || !items[0].AutoUpgrade {
		t.Fatalf("list --json = %+v, want AutoUpgrade=true after enabling", items)
	}

	out.Reset()
	errb.Reset()
	if err := runPlugin([]string{"auto-upgrade", "--off", "widget@local"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin auto-upgrade --off: %v\n%s", err, errb.String())
	}
	items = listPluginsJSON(t)
	if len(items) != 1 || items[0].AutoUpgrade {
		t.Fatalf("list --json = %+v, want AutoUpgrade=false after --off", items)
	}
}

func TestPluginAutoUpgrade_RequiresRef(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"auto-upgrade"}, nil, &out, &errb); err == nil {
		t.Fatal("auto-upgrade without a plugin ref should error")
	}
}

// TestPluginCheckNow_FreshStoreReportsNothing covers the missing "manual
// check now" surface (design spec §9.1): the hub daemon and its
// serf/plugin/checkNow RPC exist, but nothing reachable from the CLI
// triggered it.
func TestPluginCheckNow_FreshStoreReportsNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"check-now"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin check-now: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "No plugins upgraded") {
		t.Fatalf("expected 'No plugins upgraded', got %q", out.String())
	}
}

func TestPluginCheckNow_JSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"check-now", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin check-now --json: %v\n%s", err, errb.String())
	}
	var resp struct {
		Updated []string `json:"updated"`
		Error   string   `json:"error,omitempty"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if len(resp.Updated) != 0 || resp.Error != "" {
		t.Fatalf("resp = %+v, want empty on a fresh store", resp)
	}
}

// TestPluginCheckNow_SkipsDirectorySourcePlugin proves check-now actually
// wires up Manager.UpdateAutoUpgrade rather than being a no-op stub: an
// auto-upgrade-enabled but directory-sourced plugin is inherently current (no
// sha to move), so the pass must run without error and report zero upgrades.
func TestPluginCheckNow_SkipsDirectorySourcePlugin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	installDirectoryPluginForTest(t, "widget", "local")
	m := plugins.NewManager("")
	if err := m.SetAutoUpgrade("widget", "local", true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}

	var out, errb bytes.Buffer
	if err := runPlugin([]string{"check-now"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin check-now: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "No plugins upgraded") {
		t.Fatalf("expected a directory-source plugin to report no upgrades, got %q", out.String())
	}
}

func TestPluginInstall_RequiresConfirmation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	err := runPlugin([]string{"install", "widget@acme"}, nil, &out, &errb)
	if err == nil {
		t.Fatal("install without --yes should require confirmation")
	}
}

func TestPluginDoctor_JSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"doctor", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin doctor --json: %v\n%s", err, errb.String())
	}
	var findings []plugins.DoctorFinding
	if err := json.Unmarshal(out.Bytes(), &findings); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if len(findings) == 0 {
		t.Fatal("expected at least the environment findings on a fresh store")
	}
}

func TestPluginDoctor_Human(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"doctor"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin doctor: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "OK") {
		t.Errorf("doctor human output should show the OK/WARN/FAIL summary:\n%s", out.String())
	}
}

// TestPluginDoctor_DoesNotSeedMarketplaces guards against Doctor being a
// diagnostic-that-mutates: `serf plugin doctor` must never seed the default
// marketplaces (or otherwise write to the store), unlike every other plugin
// verb. Reproduces the Important finding where runPlugin unconditionally
// seeded before dispatching, so "doctor" wrote known_marketplaces.json + a
// .lock file on a fresh store.
func TestPluginDoctor_DoesNotSeedMarketplaces(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	var out, errb bytes.Buffer
	if err := runPlugin([]string{"doctor"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin doctor: %v\n%s", err, errb.String())
	}

	root := plugins.NewManager("").Root
	if _, err := os.Stat(filepath.Join(root, "known_marketplaces.json")); !os.IsNotExist(err) {
		t.Errorf("doctor should not write known_marketplaces.json (stat err = %v)", err)
	}
}

func TestPluginGc_NothingToRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"gc"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin gc: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "Nothing to remove") {
		t.Fatalf("expected 'Nothing to remove', got %q", out.String())
	}
}

// TestPluginGc_JSON pins the exact `[]` encoding for "nothing to remove":
// Gc() returning a nil slice would json.Encode as `null`, which is a worse
// API for scripts to consume than an empty array (null needs a nil check
// before ranging in most languages; `[]` does not).
func TestPluginGc_JSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"gc", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin gc --json: %v\n%s", err, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("gc --json with nothing to remove = %q, want []", got)
	}
}
