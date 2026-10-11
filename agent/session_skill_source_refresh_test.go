package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
)

// installedSkillRevision creates a real manifest and skill in a fixture-owned
// plugin store. The skill's directory may change independently of its name.
func installedSkillRevision(t *testing.T, store, revision, directory, controls, body string) (string, string) {
	t.Helper()
	root := filepath.Join(store, "cache", "market", "scope", revision)
	manifest := filepath.Join(root, ".claude-plugin")
	if err := os.MkdirAll(manifest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifest, "plugin.json"), []byte(`{"name":"scope","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "skills", directory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(source, []byte("---\nname: alpha\ndescription: fixture-description\n"+controls+"---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	return root, source
}

func installedSkillCatalog(t *testing.T, root string) skill.Catalog {
	t.Helper()
	sources, diagnostics := plugin.SkillSources([]string{root})
	if len(diagnostics) != 0 {
		t.Fatalf("fixture discovery: %+v", diagnostics)
	}
	return skill.Discover(nil, skill.DiscoverOptions{Plugins: sources})
}

func pointInstalledSkill(t *testing.T, store, root string) {
	t.Helper()
	if err := plugins.SaveRegistry(filepath.Join(store, "installed_plugins.json"), plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{"scope@market": {{InstallPath: root, Enabled: true, Source: plugins.Source{Kind: plugins.SourceDirectory, Path: root}}}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSkillActivation_UpdatedPluginRevision(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, route string
		alias       bool
	}{
		{name: "user_slash", route: "user_slash"},
		{name: "model_tool", route: "model_tool"},
		{name: "symlink store", route: "user_slash", alias: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := t.TempDir()
			if tc.alias {
				alias := filepath.Join(t.TempDir(), "store-alias")
				if err := os.Symlink(store, alias); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				store = alias
			}
			oldRoot, _ := installedSkillRevision(t, store, "old-revision", "alpha", "", "opaque-old")
			pointInstalledSkill(t, store, oldRoot)
			adapter := &fakeAdapter{name: "openai"}
			if tc.route == "model_tool" {
				adapter.steps = append(adapter.steps, func(llm.Request) llm.Response {
					return toolCallResponse(useSkillCall("skill-1", "scope:alpha"))
				})
			}
			adapter.steps = append(adapter.steps, func(llm.Request) llm.Response {
				return toolCallResponse(communicateCall("done-1", "done"))
			})
			client := llm.NewClient()
			client.Register(adapter)
			s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{PluginDirs: []string{oldRoot}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()

			newRoot, newSource := installedSkillRevision(t, store, "new-revision", "renamed-directory", "", "opaque-new")
			pointInstalledSkill(t, store, newRoot)
			if err := os.RemoveAll(oldRoot); err != nil {
				t.Fatal(err)
			}
			input := "opaque-input"
			if tc.route == "user_slash" {
				input = "/scope:alpha " + input
			}
			if _, err := s.ProcessInput(context.Background(), input, nil); err != nil {
				t.Fatal(err)
			}
			reqs := adapter.Requests()
			if len(reqs) == 0 {
				t.Fatal("no provider request")
			}
			requireSingleEnvelope(t, reqs[len(reqs)-1], "scope:alpha", "opaque-new", newSource)
			entry := s.Meta().Skills.Inventory["scope:alpha"].Ordinary
			if entry == nil || entry.Identity.Source != newSource || entry.Route != tc.route {
				t.Fatalf("activation = %+v", entry)
			}
		})
	}
}

func TestSkillActivation_UpdatedPluginGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, route, controls, code string
		keepSource, continuation    bool
	}{
		{name: "current user restriction", route: "user_slash", controls: "user-invocable: false\n", code: "policy_denied"},
		{name: "authorization stays with source", route: "model_tool", controls: "disable-model-invocation: true\n", code: "policy_denied"},
		{name: "invalid current metadata", route: "user_selection", controls: "user-invocable: invalid\n", code: "invalid_metadata"},
		{name: "pinned continuation", route: "compaction_reload", continuation: true, code: "source_missing"},
		{name: "readable source stays pinned", route: "user_slash", keepSource: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := t.TempDir()
			oldRoot, _ := installedSkillRevision(t, store, "old-revision", "alpha", "", "opaque-old")
			s := newTestSession(t)
			s.skills = installedSkillCatalog(t, oldRoot)
			descriptor, err := s.skills.ResolveExact("scope:alpha")
			if err != nil {
				t.Fatal(err)
			}
			prior := activationRecord(descriptor, true)
			s.skillLifecycle.Inventory["scope:alpha"] = schema.SkillInventoryEntry{Ordinary: &prior}
			newRoot, _ := installedSkillRevision(t, store, "new-revision", "renamed-directory", tc.controls, "opaque-new")
			pointInstalledSkill(t, store, newRoot)
			if !tc.keepSource {
				if err := os.RemoveAll(oldRoot); err != nil {
					t.Fatal(err)
				}
			}
			invocation := skillInvocation{Name: "scope:alpha", Route: tc.route}
			if tc.continuation {
				invocation.Source = &prior.Identity
			}
			batch, err := s.prepareSkillActivations(context.Background(), []skillInvocation{invocation})
			if tc.code != "" {
				requireActivationError(t, err, tc.code)
			} else if err != nil || len(batch.Items) != 1 || batch.Items[0].Loaded.Body != "opaque-old" {
				t.Fatalf("readable original source: batch=%+v err=%v", batch, err)
			}
			if got := s.Meta().Skills.Inventory["scope:alpha"].Ordinary; !reflect.DeepEqual(got, &prior) {
				t.Fatalf("preparation changed prior authorization: %+v", got)
			}
		})
	}
}

func TestSkillActivation_UpdatedPluginUnavailable(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"removed skill", "renamed skill", "renamed plugin", "removed install", "unreadable original", "unrelated broken hooks"} {
		t.Run(change, func(t *testing.T) {
			store := t.TempDir()
			oldRoot, oldSource := installedSkillRevision(t, store, "old-revision", "alpha", "", "opaque-old")
			s := newTestSession(t)
			s.skills = installedSkillCatalog(t, oldRoot)
			newRoot, newSource := installedSkillRevision(t, store, "new-revision", "alpha", "", "opaque-new")
			pointInstalledSkill(t, store, newRoot)
			if err := os.RemoveAll(oldRoot); err != nil {
				t.Fatal(err)
			}
			var err error
			switch change {
			case "removed skill":
				err = os.Remove(newSource)
			case "renamed skill":
				err = os.WriteFile(newSource, []byte("---\nname: beta\ndescription: fixture\n---\nopaque-other"), 0o644)
			case "renamed plugin":
				err = os.WriteFile(filepath.Join(newRoot, ".claude-plugin", "plugin.json"), []byte(`{"name":"other"}`), 0o644)
			case "removed install":
				err = os.Remove(filepath.Join(store, "installed_plugins.json"))
			case "unreadable original":
				// A directory in place of the original file is an I/O error,
				// not a collected revision eligible for recovery.
				err = os.MkdirAll(oldSource, 0o755)
			case "unrelated broken hooks":
				if err := os.MkdirAll(filepath.Join(newRoot, "hooks"), 0o755); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(filepath.Join(newRoot, "hooks", "hooks.json"), []byte("{"), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			batch, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Route: "user_selection"}})
			if change == "unrelated broken hooks" {
				if err != nil || len(batch.Items) != 1 || batch.Items[0].Loaded.Descriptor.Meta.SkillFile != newSource {
					t.Fatalf("skill-only recovery: batch=%+v err=%v", batch, err)
				}
			} else {
				requireActivationError(t, err, "source_missing")
			}
		})
	}
}
