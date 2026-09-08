package agent

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The personal AGENTS.md a resumed session reads belongs to whoever restores
// it: a hub passes its own concrete config root on resume exactly as it does
// on spawn, while the snapshot carries whatever was true when the session was
// created — nothing at all for a pre-feature session, a stale root for one
// whose hub moved. An empty override leaves the persisted path alone, so a
// plain `evener serve --resume` still reads the file it always did.
func TestRestoreSessionAppliesTheAgentsDocOverride(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	persisted := SessionConfig{AgentsDocPath: "/old/AGENTS.md"}.toSnapshot()
	for _, tc := range []struct{ override, want string }{
		{override: "", want: "/old/AGENTS.md"},
		{override: "/new/AGENTS.md", want: "/new/AGENTS.md"},
	} {
		meta := schema.SessionMeta{ID: "agents-doc-resume", ProfileID: "openai", Model: "gpt-5.2", Config: persisted}
		restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{AgentsDocPath: tc.override, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}})
		if err != nil {
			t.Fatal(err)
		}
		if got := restored.cfg.AgentsDocPath; got != tc.want {
			t.Errorf("restored AgentsDocPath for override %q = %q, want %q", tc.override, got, tc.want)
		}
		restored.Close()
	}
}

// A stable delegate restarted from its frozen descriptor reads the personal
// doc of whoever is running the tree now, not the one named when it was
// frozen: after a resume through a hub whose config root moved, a root session
// and its delegates would otherwise load different personal instructions.
func TestFrozenDescriptorTakesTheAgentsDocPathFromTheLiveParent(t *testing.T) {
	frozen := SessionConfig{AgentsDocPath: "/old/AGENTS.md", NoProjectPrompts: true}.toSnapshot()
	got := subagentConfigFromFrozenDescriptor(frozen, SessionConfig{AgentsDocPath: "/hub/AGENTS.md"})
	if got.AgentsDocPath != "/hub/AGENTS.md" {
		t.Fatalf("frozen-descriptor AgentsDocPath = %q, want the live parent's %q (frozen was %q)", got.AgentsDocPath, "/hub/AGENTS.md", "/old/AGENTS.md")
	}
}
