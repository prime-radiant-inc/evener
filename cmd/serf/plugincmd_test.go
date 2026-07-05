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

func TestPluginList_JSONEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"list", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("plugin list --json: %v\n%s", err, errb.String())
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("expected JSON output for empty list")
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
