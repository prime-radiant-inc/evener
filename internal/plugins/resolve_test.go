package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentplugin "primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/bundled"
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

				all, err := m.ResolveForLaunch(context.Background(), []string{a, b}, nil)
				if err != nil {
					t.Fatal(err)
				}
				assertCandidateNames(t, all, []string{"alpha", "beta"})
				assertStrings(t, all.SelectedDirs, []string{a, b})

				none := []string{}
				empty, err := m.ResolveForLaunch(context.Background(), []string{a, b}, &none)
				if err != nil {
					t.Fatal(err)
				}
				assertStrings(t, empty.SelectedDirs, []string{})
				if err := empty.ValidateSelection(); err != nil {
					t.Fatal(err)
				}

				names := []string{"beta", "alpha"}
				one, err := m.ResolveForLaunch(context.Background(), []string{a, b}, &names)
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

				got, err := m.ResolveForLaunch(context.Background(), []string{explicit}, nil)
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

				got, err := m.ResolveForLaunch(context.Background(), nil, nil)
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
				got, err := m.ResolveForLaunch(context.Background(), []string{invalid, first, second}, nil)
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
				got, err := m.ResolveForLaunch(context.Background(), []string{valid, invalid}, &names)
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
				got, err := m.ResolveForLaunch(context.Background(), []string{dir}, nil)
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
				got, err := m.ResolveForLaunch(context.Background(), []string{dir}, nil)
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
	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
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
	quiet, err := m.ResolveForLaunch(context.Background(), nil, nil)
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
	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != want {
		t.Fatalf("SelectedDirs = %v, want [%s]", res.SelectedDirs, want)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Path != want {
		t.Fatalf("Candidates = %+v, want Path %s", res.Candidates, want)
	}
	// The published copy is the embedded plugin and nothing else: the marker
	// that makes staging reclaimable stays behind in the staging directory.
	if _, err := os.Stat(filepath.Join(want, stagingMarker)); !os.IsNotExist(err) {
		t.Fatalf("published copy carries the staging marker (stat err = %v)", err)
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
		if path, _, err := m.materializeBundledPlugin(context.Background(), name); err == nil {
			t.Errorf("materializeBundledPlugin(%q) = %s, want an error", name, path)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a rejected name touched the bundled store: %v", err)
	}
}

func TestMaterializeBundledPlugin_NeverReplacesAPublishedCopy(t *testing.T) {
	m := NewManager(t.TempDir())
	first, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	// The directory a live session is reading, identified by more than its
	// path: republishing renames a fresh copy into place, and a reader holding
	// the old one would go on reading a directory nothing else can reach.
	before, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second materialization = %s, want the published %s", second, first)
	}
	after, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("published copy was replaced by a new directory")
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
			paths[i], _, errs[i] = m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
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
// would publish it at, and nothing is published — the staging preview reads is
// gone by the time it returns.
func TestPreviewForLaunch_PublishesNothing(t *testing.T) {
	m := NewManager(t.TempDir())
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
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
	entries, err := os.ReadDir(filepath.Join(m.Root, "bundled"))
	if err != nil {
		t.Fatalf("read the bundled store after a preview: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("preview left %d entries in the plugin store: %v", len(entries), entries)
	}
}

// Preview must agree with launch about the published destination: a
// destination a launch rejects fails preview the same way, and when a copy is
// already published, preview describes that copy rather than the embedded one.
func TestPreviewForLaunch_ClassifiesTheDestinationLikeLaunch(t *testing.T) {
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a foreign destination fails preview as it fails launch", func(t *testing.T) {
		m := NewManager(t.TempDir())
		dest := m.bundledPluginPath("coordinator-workflow", digest)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte("not a plugin"), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
		if err != nil {
			t.Fatal(err)
		}
		if err := res.ValidateSelection(); err == nil {
			t.Errorf("preview accepted a selection a launch rejects: %+v", res.Candidates)
		}
		if len(res.Diagnostics) != 1 || res.Diagnostics[0].Source != LaunchPluginSourceBundled {
			t.Errorf("Diagnostics = %+v, want one bundled diagnostic", res.Diagnostics)
		}
	})

	t.Run("a published copy is the one preview describes", func(t *testing.T) {
		m := NewManager(t.TempDir())
		published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
		if err != nil {
			t.Fatal(err)
		}
		// A published copy holds the embedded contents or it is not adopted at
		// all, so nothing in the result says which directory was read. The
		// loader does: preview must read the published copy, not a staged one.
		var loaded []string
		load := enabledLoad
		enabledLoad = func(dir string) (agentplugin.Instance, error) {
			loaded = append(loaded, dir)
			return load(dir)
		}
		t.Cleanup(func() { enabledLoad = load })

		res, err := m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
		if err != nil {
			t.Fatal(err)
		}
		if err := res.ValidateSelection(); err != nil {
			t.Fatal(err)
		}
		assertStrings(t, loaded, []string{published})
		if len(res.Candidates) != 1 || res.Candidates[0].Path != published {
			t.Errorf("Candidates = %+v, want the published copy at %s", res.Candidates, published)
		}
	})
}

// A launch resolves plugins before the startup call that creates the user
// config tree with a private mode: cmd/evener runs ResolveForLaunch ahead of
// cmdutil.EnsureUserConfigDirs. Materializing a bundled plugin on a fresh
// install must therefore not be what creates those parents, world-readable,
// on the store root's behalf.
func TestBundledStore_CreatesMissingParentsPrivately(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(*Manager) (LaunchPluginResolution, error)
	}{
		{
			name: "preview",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
		{
			name: "launch",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
	}
	pinPermissiveUmask(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			m := NewManager(filepath.Join(base, "evener", "plugins"))
			res, err := test.resolve(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := res.ValidateSelection(); err != nil {
				t.Fatal(err)
			}
			assertPerm(t, filepath.Join(base, "evener"), 0o700)
			assertPerm(t, m.Root, 0o700)
			assertPerm(t, filepath.Join(m.Root, "bundled"), 0o755)
		})
	}
}

// Reclaiming abandoned staging is a launch's job. Preview inspects, so an
// orphan survives a preview and is collected by the launch that follows.
func TestPreviewForLaunch_ReclaimsNothing(t *testing.T) {
	m := NewManager(t.TempDir())
	staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-abandoned")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, stagingMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }

	if _, err := m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("preview reclaimed abandoned staging: %v", err)
	}

	if _, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("the launch after a preview left abandoned staging behind (stat err = %v)", err)
	}
}

func assertPerm(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
}

// An unresolved store root is not a relative one. DefaultRoot returns "" when
// no XDG_CONFIG_HOME is set and the home directory cannot be found; every
// store path built from that root would be relative, so a launch would
// materialize <cwd>/bundled and then load a plugin out of whatever directory
// the process happened to be in. The registry is such a path too: a
// working-directory installed_plugins.json naming the requested plugin would
// answer the request from ambient state and never reach the store check.
// cmdutil owns evener's config-root fallback and already depends on this
// package, so there is no fallback to share here: the root is rejected before
// anything is read or created.
func TestBundledStore_RejectsAnUnresolvedRoot(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(*Manager) (LaunchPluginResolution, error)
	}{
		{
			name: "preview",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
		{
			name: "launch",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)
			t.Setenv(envvars.XDGConfigHome.Name, "")
			restoreHome := pluginUserHomeDir
			pluginUserHomeDir = func() (string, error) { return "", errors.New("no home directory") }
			t.Cleanup(func() { pluginUserHomeDir = restoreHome })

			// The ambient registry an unresolved root would read: it names the
			// requested plugin, so reading it would answer the request.
			ambient := filepath.Join(t.TempDir(), "coordinator-workflow")
			writePlugin(t, ambient, "coordinator-workflow", nil)
			saveTestRegistry(t, NewManager(cwd), map[string][]InstallEntry{
				"coordinator-workflow@ambient": {{InstallPath: ambient, Version: "1", Enabled: true}},
			})

			m := NewManager("")
			if m.Root != "" {
				t.Fatalf("Root = %q, want the unresolved root this test covers", m.Root)
			}
			res, err := test.resolve(m)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Candidates) != 0 {
				t.Errorf("Candidates = %+v, want nothing resolved from the working directory", res.Candidates)
			}
			if err := res.ValidateSelection(); err == nil {
				t.Errorf("selected a bundled plugin with no store root: %+v", res.Candidates)
			}
			if len(res.Diagnostics) != 1 || res.Diagnostics[0].Source != LaunchPluginSourceBundled {
				t.Fatalf("Diagnostics = %+v, want one bundled diagnostic", res.Diagnostics)
			}
			entries, err := os.ReadDir(cwd)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "installed_plugins.json" {
				t.Errorf("working directory holds %v, want only the registry this test planted", entries)
			}
		})
	}
}

// A published copy is reused only when the destination is a real directory. A
// file or a symlink there belongs to someone else: adopting it would load an
// unrelated directory and report it as the bundled plugin.
func TestMaterializeBundledPlugin_RejectsAForeignDestination(t *testing.T) {
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(t *testing.T, dest string){
		"regular file": func(t *testing.T, dest string) {
			if err := os.WriteFile(dest, []byte("not a plugin"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"symlink to an unrelated plugin": func(t *testing.T, dest string) {
			impostor := filepath.Join(t.TempDir(), "impostor")
			writePlugin(t, impostor, "coordinator-workflow", nil)
			if err := os.Symlink(impostor, dest); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range tests {
		t.Run(name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			dest := m.bundledPluginPath("coordinator-workflow", digest)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				t.Fatal(err)
			}
			plant(t, dest)
			path, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
			if err == nil {
				t.Fatalf("materializeBundledPlugin = %s, want an error for a %s at the destination", path, name)
			}
		})
	}
}

// A publish killed between staging and rename leaves an orphan directory in
// the store; the next publish reclaims it. Staging a live publisher may still
// be filling, and a directory this code never staged, are left alone.
func TestMaterializeBundledPlugin_ReclaimsAbandonedStaging(t *testing.T) {
	plantStaging := func(t *testing.T, m *Manager, marked bool) string {
		t.Helper()
		staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-abandoned")
		if err := os.MkdirAll(staging, 0o755); err != nil {
			t.Fatal(err)
		}
		if marked {
			if err := os.WriteFile(filepath.Join(staging, stagingMarker), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return staging
	}

	t.Run("abandoned staging is reclaimed", func(t *testing.T) {
		m := NewManager(t.TempDir())
		m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		staging := plantStaging(t, m, true)
		if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(staging); !os.IsNotExist(err) {
			t.Fatalf("abandoned staging survived a later publish (stat err = %v)", err)
		}
	})

	t.Run("staging in flight is left alone", func(t *testing.T) {
		m := NewManager(t.TempDir())
		staging := plantStaging(t, m, true)
		if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(staging); err != nil {
			t.Fatalf("staging of a concurrent publish was reclaimed: %v", err)
		}
	})

	t.Run("an aged directory this code never staged is left alone", func(t *testing.T) {
		m := NewManager(t.TempDir())
		m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		staging := plantStaging(t, m, false)
		keep := filepath.Join(staging, "someone-elses-data")
		if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("the sweep removed a directory it never staged: %v", err)
		}
	})

	t.Run("an aged entry that is not a staging directory is left alone", func(t *testing.T) {
		m := NewManager(t.TempDir())
		m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		if err := os.MkdirAll(filepath.Join(m.Root, "bundled"), 0o755); err != nil {
			t.Fatal(err)
		}
		stray := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-notes")
		if err := os.WriteFile(stray, []byte("not staging"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stray); err != nil {
			t.Fatalf("the sweep removed an entry it never staged: %v", err)
		}
	})
}

// A publisher that lost the rename and was killed before it cleaned up leaves
// an orphan beside the winner's published copy. Every later materialization
// takes the published path, so the sweep has to run there too or the orphan is
// never reclaimed.
func TestMaterializeBundledPlugin_ReclaimsStagingBesideAPublishedCopy(t *testing.T) {
	m := NewManager(t.TempDir())
	published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(m.Root, "bundled", ".stage-coordinator-workflow-orphan")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, stagingMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }

	again, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	if again != published {
		t.Fatalf("materialization = %s, want the published %s", again, published)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging beside a published copy was never reclaimed (stat err = %v)", err)
	}
}

// The resolver looks a bundled plugin up by its embedded directory name and
// then keys the inventory by the manifest name the loader reports, so every
// bundled plugin must carry the manifest name its directory promises.
func TestBundledPluginsAreNamedAfterTheirDirectory(t *testing.T) {
	entries, err := fs.ReadDir(bundled.Plugins(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no bundled plugins are embedded")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m := NewManager(t.TempDir())
		path, _, err := m.materializeBundledPlugin(context.Background(), entry.Name())
		if err != nil {
			t.Fatalf("materialize %s: %v", entry.Name(), err)
		}
		instance, err := enabledLoad(path)
		if err != nil {
			t.Fatalf("load %s: %v", entry.Name(), err)
		}
		if instance.Manifest.Name != entry.Name() {
			t.Errorf("bundled plugin %s declares manifest name %q, want %q", entry.Name(), instance.Manifest.Name, entry.Name())
		}
	}
}

// Published copies and staging share the <Root>/bundled namespace, and the
// reclaim sweep tells them apart by stagingPrefix. A bundled plugin whose
// directory name wore that prefix would publish to a path the sweep reads as
// an abandoned orphan, so an hour later it would delete a published copy live
// sessions are reading.
func TestBundledPluginNamesStayOutOfTheStagingNamespace(t *testing.T) {
	entries, err := fs.ReadDir(bundled.Plugins(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no bundled plugins are embedded")
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			t.Errorf("bundled plugin %s publishes inside the %q staging namespace; the reclaim sweep would delete its published copy", entry.Name(), stagingPrefix)
		}
	}
}

// A destination is adopted only when its contents are the contents its name
// promises. <name>-<digest> is a claim about what is inside, so a directory
// that hashes to anything else was not published by this code — a foreign
// directory that took the name, or a published copy somebody edited.
func TestBundledStore_AdoptsOnlyTheContentTheDigestNames(t *testing.T) {
	resolvers := []struct {
		name    string
		resolve func(*Manager) (LaunchPluginResolution, error)
	}{
		{
			name: "preview",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
		{
			name: "launch",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
	}
	for _, resolver := range resolvers {
		t.Run(resolver.name+" adopts the published copy", func(t *testing.T) {
			m := NewManager(t.TempDir())
			published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
			if err != nil {
				t.Fatal(err)
			}
			res, err := resolver.resolve(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := res.ValidateSelection(); err != nil {
				t.Fatal(err)
			}
			if len(res.Candidates) != 1 || res.Candidates[0].Path != published || res.Candidates[0].AgentCount < 7 {
				t.Fatalf("Candidates = %+v, want the published copy at %s", res.Candidates, published)
			}
			entries, err := os.ReadDir(filepath.Join(m.Root, "bundled"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("bundled store holds %v, want only the published copy", entries)
			}
		})
	}
}

// <Root>/bundled is evener's own cache of what the running binary ships, so a
// destination that holds something else is stale or foreign rather than
// authoritative: refusing it forever would leave a stray file — a .DS_Store
// Finder wrote, an editor's backup — permanently unlaunchable. The conflicting
// directory is moved to the one sibling slot kept beside it and publication
// continues, so the launch selects a copy that matches the binary. What was
// there is never deleted; only an earlier occupant of that slot is replaced.
func TestBundledStore_SetsAsideAConflictingDestination(t *testing.T) {
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	resolvers := []struct {
		name    string
		resolve func(*Manager) (LaunchPluginResolution, error)
	}{
		{
			name: "preview",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
		{
			name: "launch",
			resolve: func(m *Manager) (LaunchPluginResolution, error) {
				return m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
			},
		},
	}
	for _, resolver := range resolvers {
		t.Run(resolver.name+" sets a conflicting destination aside", func(t *testing.T) {
			m := NewManager(t.TempDir())
			dest := m.bundledPluginPath("coordinator-workflow", digest)
			// A plausible impostor: a loadable plugin under the right name,
			// holding contents the digest never described.
			writePlugin(t, dest, "coordinator-workflow", map[string]string{"theirs.md": "someone else's data"})

			res, err := resolver.resolve(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := res.ValidateSelection(); err != nil {
				t.Fatalf("a conflicting destination left the bundled plugin unselectable: %v", err)
			}
			if len(res.Candidates) != 1 || res.Candidates[0].Path != dest || res.Candidates[0].AgentCount < 7 {
				t.Fatalf("Candidates = %+v, want the copy this build ships at %s", res.Candidates, dest)
			}
			aside := dest + conflictSuffix
			if len(res.Diagnostics) != 1 || res.Diagnostics[0].Source != LaunchPluginSourceBundled ||
				!strings.Contains(res.Diagnostics[0].Message, aside) {
				t.Fatalf("Diagnostics = %+v, want one bundled warning naming %s", res.Diagnostics, aside)
			}
			if content, err := os.ReadFile(filepath.Join(aside, "theirs.md")); err != nil || string(content) != "someone else's data" {
				t.Errorf("set-aside content = %q (err %v), want what was there preserved", content, err)
			}
		})
	}

	// The realistic case: Finder or an editor drops a file into a published
	// copy. The next launch sets the changed copy aside and republishes, so
	// the plugin keeps working without anybody deleting anything.
	t.Run("a stray file in a published copy heals on the next launch", func(t *testing.T) {
		m := NewManager(t.TempDir())
		first, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
		if err != nil {
			t.Fatal(err)
		}
		if err := first.ValidateSelection(); err != nil {
			t.Fatal(err)
		}
		published := first.SelectedDirs[0]
		if err := os.WriteFile(filepath.Join(published, ".DS_Store"), []byte("finder"), 0o644); err != nil {
			t.Fatal(err)
		}

		again, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
		if err != nil {
			t.Fatal(err)
		}
		if err := again.ValidateSelection(); err != nil {
			t.Fatalf("a stray file left the bundled plugin unlaunchable: %v", err)
		}
		if len(again.SelectedDirs) != 1 || again.SelectedDirs[0] != published {
			t.Fatalf("SelectedDirs = %v, want a republished copy at %s", again.SelectedDirs, published)
		}
		if _, err := os.Stat(filepath.Join(published, ".DS_Store")); !os.IsNotExist(err) {
			t.Errorf("the republished copy carries the stray file (stat err = %v)", err)
		}
		if _, err := os.Stat(filepath.Join(published+conflictSuffix, ".DS_Store")); err != nil {
			t.Errorf("the copy that was set aside lost the file it was kept for: %v", err)
		}
	})

	// One slot per plugin, so a store that keeps meeting conflicts does not
	// grow without bound: the second set-aside copy replaces the first.
	t.Run("a later conflict replaces the copy set aside before it", func(t *testing.T) {
		m := NewManager(t.TempDir())
		dest := m.bundledPluginPath("coordinator-workflow", digest)
		writePlugin(t, dest, "coordinator-workflow", map[string]string{"first.md": "first"})
		if _, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "second.md"), []byte("second"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"}); err != nil {
			t.Fatal(err)
		}

		aside := dest + conflictSuffix
		if _, err := os.Stat(filepath.Join(aside, "second.md")); err != nil {
			t.Errorf("the latest conflict was not the one kept: %v", err)
		}
		if _, err := os.Stat(filepath.Join(aside, "first.md")); !os.IsNotExist(err) {
			t.Errorf("an earlier set-aside copy survived (stat err = %v), want one slot per plugin", err)
		}
		entries, err := os.ReadDir(filepath.Join(m.Root, "bundled"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Errorf("bundled store holds %v, want the published copy and the one slot beside it", entries)
		}
	})

	// A set-aside copy lives in the store beside staging, and it is not
	// staging: the sweep that reclaims abandoned publishes must leave it alone
	// however old it gets.
	t.Run("the staging sweep leaves a set-aside copy alone", func(t *testing.T) {
		m := NewManager(t.TempDir())
		dest := m.bundledPluginPath("coordinator-workflow", digest)
		writePlugin(t, dest, "coordinator-workflow", map[string]string{"theirs.md": "someone else's data"})
		if _, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"}); err != nil {
			t.Fatal(err)
		}
		m.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		if _, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dest+conflictSuffix, "theirs.md")); err != nil {
			t.Errorf("the sweep collected a set-aside copy: %v", err)
		}
	})
}

// A published copy is a copy: every entry in it is a regular file or a
// directory. A symlink inside one is neither, however the bytes at the other
// end read today — the target is somebody else's to change, and the copy the
// digest vouched for would change with it. The destination is set aside and a
// real copy published in its place.
func TestBundledStore_SetsAsideADestinationHoldingASymlink(t *testing.T) {
	m := NewManager(t.TempDir())
	published, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(published, ".claude-plugin", "plugin.json")
	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// The very same bytes, one indirection away: the tree reads identically
	// and is still not a copy of anything.
	target := filepath.Join(t.TempDir(), "plugin.json")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, manifest); err != nil {
		t.Fatal(err)
	}

	res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.ValidateSelection(); err != nil {
		t.Fatalf("a linked destination left the bundled plugin unselectable: %v", err)
	}
	if len(res.SelectedDirs) != 1 || res.SelectedDirs[0] != published {
		t.Fatalf("SelectedDirs = %v, want a republished copy at %s", res.SelectedDirs, published)
	}
	info, err := os.Lstat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("republished manifest mode = %v, want a regular file", info.Mode())
	}
	aside, err := os.Lstat(filepath.Join(published+conflictSuffix, ".claude-plugin", "plugin.json"))
	if err != nil || aside.Mode()&os.ModeSymlink == 0 {
		t.Errorf("set-aside manifest mode = %v (err %v), want the symlink preserved", aside, err)
	}
}

// The slot beside a destination holds a copy somebody may still want. Freeing
// the destination must not begin by emptying that slot: a destination that
// turns out not to be movable — gone, or held by something this cannot
// rename — would leave nothing preserved where a copy used to be.
func TestSetAsideBundledConflict_KeepsWhatItHoldsWhenThereIsNothingToMove(t *testing.T) {
	store := t.TempDir()
	dest := filepath.Join(store, "coordinator-workflow-0123456789abcdef")
	aside := dest + conflictSuffix
	writePlugin(t, aside, "coordinator-workflow", map[string]string{"kept.md": "the copy set aside earlier"})

	// Nothing at the destination: the set-aside has no work to do and no
	// business touching what the slot already holds.
	warnings, err := setAsideBundledConflict(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want nothing to report with nothing at the destination", warnings)
	}
	content, err := os.ReadFile(filepath.Join(aside, "kept.md"))
	if err != nil || string(content) != "the copy set aside earlier" {
		t.Errorf("earlier set-aside copy = %q (err %v), want it untouched", content, err)
	}
	entries, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(aside) {
		t.Errorf("store holds %v, want only the slot it started with", entries)
	}
}

// A set-aside interrupted between its two renames leaves the copy it was
// preserving under the ".previous" name with the slot itself empty. That copy
// is the only one there is, so the next set-aside has to put it back before it
// does anything else — reading it as residue and deleting it is how the
// preserved conflict gets lost for good.
func TestSetAsideBundledConflict_RecoversAnInterruptedSetAside(t *testing.T) {
	t.Run("with nothing at the destination the copy goes back", func(t *testing.T) {
		store := t.TempDir()
		dest := filepath.Join(store, "coordinator-workflow-0123456789abcdef")
		aside := dest + conflictSuffix
		writePlugin(t, aside+previousSuffix, "coordinator-workflow", map[string]string{"kept.md": "the copy set aside earlier"})

		if _, err := setAsideBundledConflict(dest); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(aside, "kept.md"))
		if err != nil || string(content) != "the copy set aside earlier" {
			t.Errorf("copy at the slot = %q (err %v), want the interrupted set-aside put back", content, err)
		}
		entries, err := os.ReadDir(store)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != filepath.Base(aside) {
			t.Errorf("store holds %v, want only the slot the copy was put back into", entries)
		}
	})

	// Recovered and then replaced: one slot per plugin, so the copy from the
	// interrupted set-aside is the older occupant, and the conflict this
	// launch found takes its place. It is dropped after the newer one is
	// safely in the slot, never before.
	t.Run("a launch that finds a conflict replaces the recovered copy", func(t *testing.T) {
		m := NewManager(t.TempDir())
		digest, err := bundledPluginDigest("coordinator-workflow")
		if err != nil {
			t.Fatal(err)
		}
		dest := m.bundledPluginPath("coordinator-workflow", digest)
		writePlugin(t, dest, "coordinator-workflow", map[string]string{"newer.md": "the conflict this launch found"})
		writePlugin(t, dest+conflictSuffix+previousSuffix, "coordinator-workflow", map[string]string{"older.md": "the copy set aside earlier"})

		res, err := m.ResolveForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
		if err != nil {
			t.Fatal(err)
		}
		if err := res.ValidateSelection(); err != nil {
			t.Fatalf("a recovered set-aside left the bundled plugin unselectable: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest+conflictSuffix, "newer.md")); err != nil {
			t.Errorf("the slot does not hold the conflict this launch found: %v", err)
		}
		entries, err := os.ReadDir(filepath.Join(m.Root, "bundled"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Errorf("bundled store holds %v, want the published copy and the one slot beside it", entries)
		}
	})
}

// A caller that has already given up must not be handed an inventory to admit
// a launch on. The hub's thread/start validates plugins and then detaches from
// the request context to finish the spawn, so an inventory returned after
// cancellation is a session started for a client that has gone. Nothing else
// on the way there necessarily blocks — with a copy already published there is
// no lock to wait on and no work to interrupt — so the context is what has to
// be read.
func TestResolveForLaunch_StopsForACallerThatHasGivenUp(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(context.Context, *Manager) (LaunchPluginResolution, error)
	}{
		{
			name: "preview",
			resolve: func(ctx context.Context, m *Manager) (LaunchPluginResolution, error) {
				return m.PreviewForLaunch(ctx, nil, &[]string{"coordinator-workflow"})
			},
		},
		{
			name: "launch",
			resolve: func(ctx context.Context, m *Manager) (LaunchPluginResolution, error) {
				return m.ResolveForLaunch(ctx, nil, &[]string{"coordinator-workflow"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			if _, _, err := m.materializeBundledPlugin(context.Background(), "coordinator-workflow"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			res, err := test.resolve(ctx, m)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want the cancellation", err)
			}
			// The cancellation is the answer, carried as the error: an empty
			// inventory with no selection is nothing to admit a launch on, and
			// naming the requested plugin as a missing candidate would blame
			// the plugin for the caller leaving.
			if len(res.Candidates) != 0 || len(res.SelectedDirs) != 0 || len(res.Diagnostics) != 0 {
				t.Errorf("resolved %+v for a caller that had given up", res)
			}
		})
	}
}
