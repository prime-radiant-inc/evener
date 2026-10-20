package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
)

// revisionGit runs only against the test's private repositories, without a
// network or the developer's plugin store.
func revisionGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func revisionFile(t *testing.T, root, name string, data []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPluginRevisionSurvivesUpgradeGcAndRestore(t *testing.T) {
	t.Parallel()
	testPluginRevisionPreservation(t, false)
}

func TestPluginRevisionSurvivesAutomaticMigrationAndRestore(t *testing.T) {
	t.Parallel()
	testPluginRevisionPreservation(t, true)
}

func testPluginRevisionPreservation(t *testing.T, migrate bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "nondefault-state")
	workspace := filepath.Join(root, "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := plugins.NewManager(filepath.Join(root, "plugin-store"))
	store.Stderr = io.Discard
	pluginRepo := filepath.Join(root, "source")
	revisionFile(t, pluginRepo, ".claude-plugin/plugin.json", []byte(`{"name":"widget","version":"1.0.0"}`), 0o644)
	revisionFile(t, pluginRepo, "skills/probe/SKILL.md", []byte("---\nname: probe\ndescription: revision fixture\n---\nA-SKILL\n"), 0o644)
	originalCompanion := "A-COMPANION"
	revisionFile(t, pluginRepo, "resources/companion.bin", []byte(originalCompanion), 0o644)
	// This is a real local command hook, not a mocked runner. Its source lives
	// inside the published revision, and its input is another file in that tree.
	observed := filepath.Join(root, "hook-output")
	script := "#!/bin/sh\ncat \"$CLAUDE_PLUGIN_ROOT/resources/companion.bin\" > \"" + observed + "\"\n"
	revisionFile(t, pluginRepo, "scripts/probe.sh", []byte(script), 0o755)
	revisionFile(t, pluginRepo, "hooks/hooks.json", []byte(`{"hooks":{"PreToolUse":[{"matcher":"use_skill","hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/scripts/probe.sh"}]}]}}`), 0o644)
	revisionGit(t, pluginRepo, "init", "-q")
	revisionGit(t, pluginRepo, "add", ".")
	revisionGit(t, pluginRepo, "commit", "-qm", "A")
	marketRepo := filepath.Join(root, "market")
	revisionFile(t, marketRepo, ".claude-plugin/marketplace.json", []byte(`{"name":"acme","owner":{"name":"fixture"},"plugins":[{"name":"widget","source":{"source":"url","url":"`+pluginRepo+`"}}]}`), 0o644)
	revisionGit(t, marketRepo, "init", "-q")
	revisionGit(t, marketRepo, "add", ".")
	revisionGit(t, marketRepo, "commit", "-qm", "catalog")
	ctx := context.Background()
	if _, err := store.AddMarketplace(ctx, "", plugins.Source{Kind: plugins.SourceURL, URL: marketRepo}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Install(ctx, "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	marketplace := "acme"
	if migrate {
		// Reproduce an older installer's on-disk layout before any session
		// selects A. Everything is real local Git and typed store metadata.
		const legacy = "legacy@acme"
		for _, dir := range []string{"marketplaces", "cache"} {
			if err := os.Rename(filepath.Join(store.Root, dir, "acme"), filepath.Join(store.Root, dir, legacy)); err != nil {
				t.Fatal(err)
			}
		}
		mk := plugins.Marketplaces{}
		data, err := os.ReadFile(filepath.Join(store.Root, "known_marketplaces.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &mk); err != nil {
			t.Fatal(err)
		}
		ref := mk["acme"]
		delete(mk, "acme")
		ref.InstallLocation = filepath.Join(store.Root, "marketplaces", legacy)
		mk[legacy] = ref
		reg := plugins.Registry{}
		data, err = os.ReadFile(filepath.Join(store.Root, "installed_plugins.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &reg); err != nil {
			t.Fatal(err)
		}
		first.InstallPath = filepath.Join(store.Root, "cache", legacy, "widget", filepath.Base(first.InstallPath))
		delete(reg.Plugins, "widget@acme")
		reg.Plugins["widget@"+legacy] = []plugins.InstallEntry{first}
		for name, value := range map[string]any{"known_marketplaces.json": mk, "installed_plugins.json": reg} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			revisionFile(t, store.Root, name, data, 0o644)
		}
		marketplace = "legacy-acme"
	}
	oldPath := first.InstallPath
	oldIdentity, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := NewOpenAIProfile("gpt-5.2")
	newSelectedWithFS := func(dir string, metaFS afero.Fs) *Session {
		t.Helper()
		s, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
			StateDir: stateDir, PluginDirs: []string{dir}, testOnly: testConfig{skipGitSnapshot: true, metaFS: metaFS},
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	newSelected := func(dir string) *Session { return newSelectedWithFS(dir, nil) }
	selected := newSelected(oldPath)
	if err := selected.saveMeta(); err != nil {
		t.Fatalf("save selected A: %v", err)
	}
	selectedID := selected.ID()
	selected.Close()

	// Initial metadata persistence is best effort. A second running session
	// has selected A but has no metadata record at the moment GC runs.
	unrecorded := newSelectedWithFS(oldPath, afero.NewReadOnlyFs(afero.NewOsFs()))
	if err := unrecorded.saveMeta(); err == nil {
		t.Fatal("initial metadata write unexpectedly succeeded")
	}
	if _, err := schema.LoadSessionMeta(stateDir, unrecorded.ID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initial metadata failure did not leave the session absent: %v", err)
	}

	// The cold delegate's durable descriptor has an independently frozen A,
	// even while its root has not restored the child runtime.
	frozen := newColdStableDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Config.PluginDirs = []string{oldPath}
	})
	revisionFile(t, pluginRepo, "skills/probe/SKILL.md", []byte("---\nname: probe\ndescription: revision fixture\n---\nB-SKILL\n"), 0o644)
	revisionFile(t, pluginRepo, "resources/companion.bin", []byte("B-COMPANION"), 0o644)
	revisionGit(t, pluginRepo, "add", ".")
	revisionGit(t, pluginRepo, "commit", "-qm", "B")
	second, err := store.Upgrade(ctx, "widget", marketplace)
	if err != nil || second.InstallPath == oldPath {
		t.Fatalf("upgrade B: %+v, %v", second, err)
	}
	t.Logf("published revisions: A=%s B=%s", oldPath, second.InstallPath)
	// A failed subsequent installation must not overwrite A or change B's
	// successful selection.
	revisionFile(t, pluginRepo, ".claude-plugin/plugin.json", []byte("{"), 0o644)
	revisionGit(t, pluginRepo, "add", ".")
	revisionGit(t, pluginRepo, "commit", "-qm", "invalid C")
	if _, err := store.Install(ctx, "widget", marketplace); err == nil {
		t.Fatal("invalid new install unexpectedly succeeded")
	}
	// Restart the store owner before GC and session restore. No in-memory
	// migration state may be needed to keep A's exact source useful.
	store = plugins.NewManager(store.Root)
	store.Stderr = io.Discard
	removed, err := store.Gc(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(removed, oldPath) {
		t.Fatal("GC removed a selected revision")
	}
	if identity, err := os.Stat(oldPath); err != nil || !os.SameFile(oldIdentity, identity) {
		t.Fatalf("original A directory identity changed: %v", err)
	}
	for _, dir := range []string{oldPath, second.InstallPath} {
		if _, err := os.Stat(filepath.Join(dir, "resources", "companion.bin")); err != nil {
			t.Fatalf("revision %s lost companion: %v", dir, err)
		}
	}

	meta, err := schema.LoadSessionMeta(stateDir, selectedID)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMeta(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if !slices.Contains(restored.cfg.PluginDirs, oldPath) {
		t.Fatal("resume changed the selected revision")
	}
	assertRevisionUse := func(s *Session, wantDir, wantSkill, wantCompanion string) {
		t.Helper()
		descriptor, err := s.skills.ResolveExact("widget:probe")
		if err != nil {
			t.Fatalf("resolve skill: %v", err)
		}
		if descriptor.Meta.SkillFile != filepath.Join(wantDir, "skills", "probe", "SKILL.md") {
			t.Fatalf("skill source = %q, want exact selected directory %q", descriptor.Meta.SkillFile, wantDir)
		}
		loaded, _, err := loadSkillInvocationSource(skillInvocation{Name: "widget:probe"}, descriptor)
		if err != nil || loaded.Body != wantSkill {
			t.Fatalf("loaded skill body = %q, err = %v", loaded.Body, err)
		}
		if err := os.Remove(observed); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		result := s.execTool(ctx, useSkillCall("revision-probe", "widget:probe"), "")
		if result.IsError {
			t.Fatalf("actual use_skill failed: %+v", result)
		}
		bytesReadFromCompanion, err := os.ReadFile(observed)
		if err != nil || string(bytesReadFromCompanion) != wantCompanion {
			t.Fatalf("hook companion bytes = %q, err = %v, want %q", bytesReadFromCompanion, err, wantCompanion)
		}
	}
	assertRevisionUse(restored, oldPath, "A-SKILL\n", originalCompanion)
	unrecorded.cfg.testOnly.metaFS = nil // storage recovers only after the no-inventory GC
	assertRevisionUse(unrecorded, oldPath, "A-SKILL\n", originalCompanion)
	unrecorded.Close()

	rootDelegate, err := restoreDelegateResourceBootstrapSession(frozen.client, frozen.profile, frozen.workspace, frozen.meta, frozen.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer rootDelegate.Close()
	aggregate := delegateAggregateSnapshot(t, rootDelegate.delegateController, frozen.delegateID)
	childCfg := subagentConfigFromFrozenDescriptor(aggregate.Descriptor.Config, rootDelegate.cfg)
	if !slices.Equal(childCfg.PluginDirs, []string{oldPath}) {
		t.Fatalf("frozen child selection = %v, want A", childCfg.PluginDirs)
	}
	if bytesReadFromCompanion, err := os.ReadFile(filepath.Join(childCfg.PluginDirs[0], "resources", "companion.bin")); err != nil || string(bytesReadFromCompanion) != originalCompanion {
		t.Fatalf("cold delegate companion = %q, err = %v", bytesReadFromCompanion, err)
	}
	reservation, err := rootDelegate.delegateController.ReserveStart(rootDelegateActor(rootDelegate.id), frozen.delegateID)
	if err != nil {
		t.Fatal(err)
	}
	started, err := rootDelegate.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatal(err)
	}
	child, cold, err := (delegateRuntime{owner: rootDelegate}).restoreIdle(started)
	if err != nil || !cold {
		t.Fatalf("restore cold delegate: cold=%v, err=%v", cold, err)
	}
	defer func() {
		child.sess.discardRestoredCandidate()
		_, _ = rootDelegate.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	}()
	if !slices.Equal(child.sess.cfg.PluginDirs, []string{oldPath}) {
		t.Fatalf("restored cold delegate paths = %v, want A", child.sess.cfg.PluginDirs)
	}
	childSkill, err := child.sess.skills.ResolveExact("widget:probe")
	if err != nil {
		t.Fatalf("cold delegate lost plugin skill: %v", err)
	}
	if childSkill.Meta.SkillFile != filepath.Join(oldPath, "skills", "probe", "SKILL.md") {
		t.Fatalf("cold delegate skill source = %q, want original A", childSkill.Meta.SkillFile)
	}
	loadedChildSkill, _, err := loadSkillInvocationSource(skillInvocation{Name: "widget:probe"}, childSkill)
	if err != nil || loadedChildSkill.Body != "A-SKILL\n" {
		t.Fatalf("cold delegate skill = %q, err = %v", loadedChildSkill.Body, err)
	}

	fresh, err := store.ResolveForLaunch(ctx, nil, nil)
	if err != nil || !slices.Equal(fresh.SelectedDirs, []string{second.InstallPath}) {
		t.Fatalf("fresh launch selection = %v, err = %v, want B", fresh.SelectedDirs, err)
	}
	newSession := newSelected(fresh.SelectedDirs[0])
	defer newSession.Close()
	assertRevisionUse(newSession, second.InstallPath, "B-SKILL\n", "B-COMPANION")
}
