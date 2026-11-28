package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// instructionDocConfig builds a workspace holding a repo instruction file, a
// personal instruction file, and a --system-prompt-append file, and returns its
// directory with the session config that reads all three.
func instructionDocConfig(t *testing.T, role string, delegate bool) (string, SessionConfig) {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "AGENTS.md"), sentinelProjectDoc+"\n")
	personal := filepath.Join(t.TempDir(), "AGENTS.md")
	writeTestFile(t, personal, sentinelPersonalDoc+"\n")
	appended := filepath.Join(t.TempDir(), "append.md")
	writeTestFile(t, appended, sentinelAppend+"\n")
	cfg := SessionConfig{
		AgentName:          role,
		MaxSubagentDepth:   2,
		AgentsDocPath:      personal,
		SystemPromptAppend: []string{appended},
		testOnly:           testConfig{skipGitSnapshot: true},
	}
	if delegate {
		cfg.spawn.parentSessionID = "instruction-doc-parent"
		cfg.spawn.depth = 1
	}
	return dir, cfg
}

// checkInstructionDocSentinels asserts whether the rendered prompt carries the
// personal and repo instruction files. The sentinels are opaque markers of the
// input files, so this reads the data flow and no prompt prose.
func checkInstructionDocSentinels(t *testing.T, prompt string, want bool) {
	t.Helper()
	for _, sentinel := range []string{sentinelProjectDoc, sentinelPersonalDoc} {
		if got := strings.Contains(prompt, sentinel); got != want {
			t.Errorf("prompt contains %q = %v, want %v", sentinel, got, want)
		}
	}
}

// TestPromptInstructionDocsByRole pins which sessions render the user's
// instruction files (#2579): a root and every working delegate render the
// personal and repo docs, the read-only explorer skips them, and only a root
// renders the --system-prompt-append files.
func TestPromptInstructionDocsByRole(t *testing.T) {
	t.Parallel()
	roles := []struct {
		name     string
		role     string
		delegate bool
	}{
		{name: "root", role: "", delegate: false},
		{name: "default delegate", role: "default", delegate: true},
		{name: "subagent delegate", role: "subagent", delegate: true},
		{name: "doctor delegate", role: "doctor", delegate: true},
		{name: "custom working delegate", role: "reviewer", delegate: true},
		{name: "explorer delegate", role: "explorer", delegate: true},
	}
	for _, tc := range roles {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, cfg := instructionDocConfig(t, tc.role, tc.delegate)
			sess := newSession(t, withDir(dir), withConfig(cfg))

			// Premise: every session loads both docs and the append file. Only
			// the render gate differs, so an absent sentinel below is the gate
			// and not missing data.
			data, _ := sess.buildPromptData(sess.env)
			if len(data.ProjectDocs) != 2 {
				t.Fatalf("test setup: loaded %d instruction docs, want 2", len(data.ProjectDocs))
			}
			if len(data.CLIAppends) != 1 {
				t.Fatalf("test setup: loaded %d CLI appends, want 1", len(data.CLIAppends))
			}

			prompt, warning, _ := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}

			checkInstructionDocSentinels(t, prompt, tc.role != "explorer")

			// The append files stay root-only: a delegate never gets them.
			wantAppend := !tc.delegate
			if got := strings.Contains(prompt, sentinelAppend); got != wantAppend {
				t.Errorf("prompt contains the CLI append = %v, want %v", got, wantAppend)
			}
		})
	}
}

// buildInstructionDocDelegate spawns a real delegate under a parent whose
// workspace and personal path carry the instruction docs, so the child's own
// init loads them the way a production delegate does.
func buildInstructionDocDelegate(t *testing.T, agentType string) *Session {
	t.Helper()
	dir, cfg := instructionDocConfig(t, "", false)
	cfg.StateDir = t.TempDir()
	cfg.testOnly.sandboxProber = bwrapCapableProber(t.TempDir())
	parent := newSession(t, withDir(dir), withConfig(cfg))
	var allowance int
	prepared := prepareStableChild(t, parent, delegateArgs{Task: "task", AgentType: agentType, DelegationAllowance: &allowance})
	child := prepared.sub.sess
	t.Cleanup(child.Close)
	return child
}

// TestSpawnedDelegateInstructionDocs proves the render gate through the stable
// spawn path: a working delegate's prompt carries the personal and repo docs
// and no append text, while an explorer's prompt carries neither doc.
func TestSpawnedDelegateInstructionDocs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		agentType string
		wantDocs  bool
	}{
		{name: "working subagent", agentType: "", wantDocs: true},
		{name: "explorer", agentType: "explorer", wantDocs: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			child := buildInstructionDocDelegate(t, tc.agentType)
			if !child.isSubagentSession() {
				t.Fatal("test setup: spawned session is not a delegate")
			}
			data, _ := child.buildPromptData(child.env)
			if len(data.ProjectDocs) != 2 {
				t.Fatalf("test setup: delegate loaded %d instruction docs, want 2", len(data.ProjectDocs))
			}
			prompt, warning, _ := child.renderSystemPrompt(child.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			checkInstructionDocSentinels(t, prompt, tc.wantDocs)
			if strings.Contains(prompt, sentinelAppend) {
				t.Error("delegate prompt carries the root-only CLI append")
			}
		})
	}
}
