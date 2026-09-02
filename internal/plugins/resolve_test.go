package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestResolveForLaunch_Contract(t *testing.T) {
	tests := []struct {
		name  string
		check func(t *testing.T, root string, m *Manager)
	}{
		{
			name: "selection presence and load order",
			check: func(t *testing.T, root string, m *Manager) {
				a := filepath.Join(root, "alpha")
				b := filepath.Join(root, "beta")
				writePlugin(t, a, "alpha", nil)
				writePlugin(t, b, "beta", nil)

				all, err := m.ResolveForLaunch([]string{a, b}, nil)
				if err != nil {
					t.Fatal(err)
				}
				assertCandidateNames(t, all, []string{"alpha", "beta"})
				assertStrings(t, all.SelectedDirs, []string{a, b})

				none := []string{}
				empty, err := m.ResolveForLaunch([]string{a, b}, &none)
				if err != nil {
					t.Fatal(err)
				}
				assertStrings(t, empty.SelectedDirs, []string{})
				if err := empty.ValidateSelection(); err != nil {
					t.Fatal(err)
				}

				names := []string{"beta", "alpha"}
				one, err := m.ResolveForLaunch([]string{a, b}, &names)
				if err != nil {
					t.Fatal(err)
				}
				assertStrings(t, one.SelectedDirs, []string{a, b})
				if !one.Candidates[0].Selected || !one.Candidates[1].Selected {
					t.Fatalf("candidates not selected: %+v", one.Candidates)
				}
			},
		},
		{
			name: "explicit precedence and deterministic registry order",
			check: func(t *testing.T, root string, m *Manager) {
				explicit := filepath.Join(root, "explicit")
				zeta := filepath.Join(root, "zeta")
				alpha := filepath.Join(root, "alpha-installed")
				writePlugin(t, explicit, "same", nil)
				writePlugin(t, filepath.Join(root, "same-installed"), "same", nil)
				writePlugin(t, zeta, "zeta", nil)
				writePlugin(t, alpha, "alpha", nil)
				saveTestRegistry(t, m, map[string][]InstallEntry{
					"zeta@m":  {{InstallPath: zeta, Version: "2", Enabled: true}},
					"same@m":  {{InstallPath: filepath.Join(root, "same-installed"), Version: "3", Enabled: true}},
					"alpha@m": {{InstallPath: alpha, Version: "1", Enabled: true}},
				})

				got, err := m.ResolveForLaunch([]string{explicit}, nil)
				if err != nil {
					t.Fatal(err)
				}
				assertCandidateNames(t, got, []string{"same", "alpha", "zeta"})
				assertStrings(t, got.SelectedDirs, []string{explicit, alpha, zeta})
				if len(got.Diagnostics) != 1 || got.Diagnostics[0].Name != "same" || !strings.Contains(got.Diagnostics[0].Message, "duplicate") {
					t.Fatalf("duplicate diagnostic = %+v", got.Diagnostics)
				}
			},
		},
		{
			name: "disabled and broken registry entries",
			check: func(t *testing.T, root string, m *Manager) {
				good := filepath.Join(root, "good")
				broken := filepath.Join(root, "broken")
				writePlugin(t, good, "good", nil)
				os.MkdirAll(broken, 0o755)
				saveTestRegistry(t, m, map[string][]InstallEntry{
					"disabled@m": {{InstallPath: filepath.Join(root, "disabled"), Enabled: false}},
					"broken@m":   {{InstallPath: broken, Version: "4", Enabled: true}},
					"good@m":     {{InstallPath: good, Version: "2", Enabled: true}},
				})

				got, err := m.ResolveForLaunch(nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				assertCandidateNames(t, got, []string{"good"})
				if len(got.Diagnostics) != 1 || got.Diagnostics[0].Name != "broken" || got.Diagnostics[0].Source != LaunchPluginSourceInstalled {
					t.Fatalf("broken diagnostic = %+v", got.Diagnostics)
				}
			},
		},
		{
			name: "invalid explicit and duplicate losers",
			check: func(t *testing.T, root string, m *Manager) {
				invalid := filepath.Join(root, "invalid")
				first := filepath.Join(root, "first")
				second := filepath.Join(root, "second")
				os.MkdirAll(invalid, 0o755)
				writePlugin(t, first, "dup", nil)
				writePlugin(t, second, "dup", nil)
				got, err := m.ResolveForLaunch([]string{invalid, first, second}, nil)
				if err != nil {
					t.Fatal(err)
				}
				assertCandidateNames(t, got, []string{"dup"})
				if len(got.Diagnostics) != 2 {
					t.Fatalf("diagnostics = %+v", got.Diagnostics)
				}
				if got.Diagnostics[0].Path != invalid || !strings.Contains(got.Diagnostics[0].Message, "load") && !strings.Contains(got.Diagnostics[0].Message, "plugin") {
					t.Fatalf("invalid diagnostic = %+v", got.Diagnostics[0])
				}
				if got.Diagnostics[1].Name != "dup" || got.Diagnostics[1].Path != second {
					t.Fatalf("duplicate diagnostic = %+v", got.Diagnostics[1])
				}
			},
		},
		{
			name: "selection errors are complete and deterministic",
			check: func(t *testing.T, root string, m *Manager) {
				valid := filepath.Join(root, "valid")
				invalid := filepath.Join(root, "invalid")
				writePlugin(t, valid, "valid", nil)
				writePlugin(t, invalid, "selected", map[string]string{"agents/broken.md": "not frontmatter"})
				names := []string{"unknown", "selected", "valid"}
				got, err := m.ResolveForLaunch([]string{valid, invalid}, &names)
				if err != nil {
					t.Fatal(err)
				}
				assertStrings(t, got.SelectedDirs, []string{valid})
				if len(got.SelectionErrors) != 2 {
					t.Fatalf("selection errors = %+v", got.SelectionErrors)
				}
				if got.SelectionErrors[0].Name != "unknown" || got.SelectionErrors[1].Name != "selected" {
					t.Fatalf("selection errors = %+v", got.SelectionErrors)
				}
				if err := got.ValidateSelection(); err == nil || !strings.Contains(err.Error(), "selected") || !strings.Contains(err.Error(), "unknown") {
					t.Fatalf("ValidateSelection error = %v", err)
				}
			},
		},
		{
			name: "metadata and component counts",
			check: func(t *testing.T, root string, m *Manager) {
				dir := filepath.Join(root, "counted")
				installedDir := filepath.Join(root, "installed")
				writeCountedPlugin(t, dir)
				writePlugin(t, installedDir, "installed", nil)
				saveTestRegistry(t, m, map[string][]InstallEntry{
					"installed@market": {{InstallPath: installedDir, Version: "9.9.9", Enabled: true}},
				})
				got, err := m.ResolveForLaunch([]string{dir}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Candidates) != 2 {
					t.Fatalf("candidates = %+v", got.Candidates)
				}
				c := got.Candidates[0]
				want := LaunchPluginCandidate{Name: "counted", Version: "2.3.4", Description: "description", Source: LaunchPluginSourceDirectory, Path: dir, Selected: true, SkillCount: 1, AgentCount: 1, CommandCount: 1, HookCount: 2, MCPCount: 1}
				if !reflect.DeepEqual(c, want) {
					t.Fatalf("candidate = %+v, want %+v", c, want)
				}
				installed := got.Candidates[1]
				wantInstalled := LaunchPluginCandidate{Name: "installed", Version: "9.9.9", Source: LaunchPluginSourceInstalled, Marketplace: "market", Path: installedDir, Selected: true}
				if !reflect.DeepEqual(installed, wantInstalled) {
					t.Fatalf("installed candidate = %+v, want %+v", installed, wantInstalled)
				}
			},
		},
		{
			name: "registry failure preserves explicit candidates",
			check: func(t *testing.T, root string, m *Manager) {
				dir := filepath.Join(root, "explicit")
				writePlugin(t, dir, "explicit", nil)
				if err := os.MkdirAll(m.Root, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(m.registryPath(), []byte("{"), 0o644); err != nil {
					t.Fatal(err)
				}
				got, err := m.ResolveForLaunch([]string{dir}, nil)
				if err == nil {
					t.Fatal("ResolveForLaunch error = nil")
				}
				assertCandidateNames(t, got, []string{"explicit"})
				assertStrings(t, got.SelectedDirs, []string{dir})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.check(t, t.TempDir(), NewManager(filepath.Join(t.TempDir(), "store"))) })
	}
}

func TestResolveForLaunch_SelectionValidationSortsErrors(t *testing.T) {
	r := LaunchPluginResolution{SelectionErrors: []PluginSelectionError{{Name: "z", Reason: "z-reason"}, {Name: "a", Reason: "a-reason"}}}
	if got, want := r.ValidateSelection().Error(), "enabled plugin selection is unavailable: a: a-reason; z: z-reason"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func saveTestRegistry(t *testing.T, m *Manager, plugins map[string][]InstallEntry) {
	t.Helper()
	for _, entries := range plugins {
		for i := range entries {
			if entries[i].Source.Kind == "" {
				entries[i].Source = Source{Kind: SourceDirectory, Path: entries[i].InstallPath}
			}
		}
	}
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: plugins}); err != nil {
		t.Fatal(err)
	}
}

func assertCandidateNames(t *testing.T, r LaunchPluginResolution, want []string) {
	t.Helper()
	got := make([]string, len(r.Candidates))
	for i := range r.Candidates {
		got[i] = r.Candidates[i].Name
	}
	assertStrings(t, got, want)
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func writeCountedPlugin(t *testing.T, dir string) {
	t.Helper()
	manifest := map[string]any{
		"name": "counted", "version": "2.3.4", "description": "description",
		"hooks":      map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "prompt", "prompt": "one"}, map[string]any{"type": "prompt", "prompt": "two"}}}}},
		"mcpServers": map[string]any{"one": map[string]any{"command": "echo"}},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestPluginFile(t, dir, ".claude-plugin/plugin.json", string(body))
	writeTestPluginFile(t, dir, "skills/one/SKILL.md", "---\nname: one\ndescription: one\n---\none\n")
	writeTestPluginFile(t, dir, "agents/one.md", "---\nname: one\ndescription: one\n---\none\n")
	writeTestPluginFile(t, dir, "commands/one.md", "one\n")
}

func writeTestPluginFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A bundled plugin (shipped inside the binary) is a launch candidate only when
// a launch names it; it is materialized under the store root and selected.
func TestResolveForLaunch_BundledPluginByName(t *testing.T) {
	m := NewManager(t.TempDir())
	res, err := m.ResolveForLaunch(nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Fatalf("bundled plugin not selectable: %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Name != "coordinator-workflow" || res.Candidates[0].Source != LaunchPluginSourceBundled || !res.Candidates[0].Selected {
		t.Fatalf("candidates = %+v, want the bundled coordinator-workflow selected", res.Candidates)
	}
	if res.Candidates[0].AgentCount < 7 {
		t.Errorf("bundled coordinator-workflow AgentCount = %d, want the workflow roster", res.Candidates[0].AgentCount)
	}
	if len(res.SelectedDirs) != 1 || filepath.Dir(res.SelectedDirs[0]) != filepath.Join(m.Root, "bundled") {
		t.Fatalf("SelectedDirs = %v, want one materialized dir in %s", res.SelectedDirs, filepath.Join(m.Root, "bundled"))
	}
	if _, err := os.Stat(filepath.Join(res.SelectedDirs[0], ".claude-plugin", "plugin.json")); err != nil {
		t.Errorf("materialized plugin has no manifest: %v", err)
	}
	// Not requested: bundled plugins stay out of the inventory.
	quiet, err := m.ResolveForLaunch(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet.Candidates) != 0 {
		t.Errorf("unrequested launch listed bundled candidates: %+v", quiet.Candidates)
	}
}

// Bundled plugins publish into an immutable content-addressed directory, so a
// launch never rewrites a copy an earlier session may still be reading.
func TestMaterializeBundledPlugin_PublishesContentAddressedDir(t *testing.T) {
	m := NewManager(t.TempDir())
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(m.Root, "bundled", "coordinator-workflow-"+digest)
	res, err := m.ResolveForLaunch(nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != want {
		t.Fatalf("SelectedDirs = %v, want [%s]", res.SelectedDirs, want)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Path != want {
		t.Fatalf("Candidates = %+v, want Path %s", res.Candidates, want)
	}
}

func TestMaterializeBundledPlugin_RejectsTraversingNames(t *testing.T) {
	m := NewManager(t.TempDir())
	keep := filepath.Join(m.Root, "bundled", "keep")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".", "..", "", "a/b", "../coordinator-workflow", "/coordinator-workflow"} {
		if path, err := m.materializeBundledPlugin(name); err == nil {
			t.Errorf("materializeBundledPlugin(%q) = %s, want an error", name, path)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a rejected name touched the bundled store: %v", err)
	}
}

func TestMaterializeBundledPlugin_NeverReplacesAPublishedCopy(t *testing.T) {
	m := NewManager(t.TempDir())
	first, err := m.materializeBundledPlugin("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(first, "in-use-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := m.materializeBundledPlugin("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second materialization = %s, want the published %s", second, first)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("published copy was replaced: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(m.Root, "bundled"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("bundled store has %d entries, want only the published copy: %v", len(entries), entries)
	}
}

func TestMaterializeBundledPlugin_ConcurrentCallsPublishOnce(t *testing.T) {
	m := NewManager(t.TempDir())
	const workers = 8
	paths := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			paths[i], errs[i] = m.materializeBundledPlugin("coordinator-workflow")
		})
	}
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Fatalf("worker %d published %s, worker 0 published %s", i, paths[i], paths[0])
		}
	}
	if _, err := os.Stat(filepath.Join(paths[0], ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("published copy incomplete: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(m.Root, "bundled"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("bundled store has %d entries after a race, want one: %v", len(entries), entries)
	}
}

// Preview only inspects: a bundled plugin is described with the path a launch
// would publish it at, and the store is never written.
func TestPreviewForLaunch_DoesNotWriteToTheStore(t *testing.T) {
	m := NewManager(t.TempDir())
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.PreviewForLaunch(nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Fatalf("bundled plugin not previewable: %v", err)
	}
	want := filepath.Join(m.Root, "bundled", "coordinator-workflow-"+digest)
	if len(res.Candidates) != 1 || res.Candidates[0].Path != want || res.Candidates[0].Source != LaunchPluginSourceBundled || res.Candidates[0].AgentCount < 7 {
		t.Fatalf("Candidates = %+v, want the bundled coordinator-workflow at %s", res.Candidates, want)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "bundled")); !os.IsNotExist(err) {
		t.Fatalf("preview wrote to the plugin store (stat err = %v)", err)
	}
}
