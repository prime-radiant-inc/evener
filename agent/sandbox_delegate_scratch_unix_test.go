//go:build unix

package agent

import (
	"context"
	"os"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

func TestDiscardRestoredCandidateKeepsItsNamedSandboxScratch(t *testing.T) {
	t.Parallel()
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	child := newSession(t, withClient(client), withDir(lane), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))
	local := child.currentEnv().(*execenv.LocalExecutionEnvironment)
	if err := local.EnableSandbox(sbxResolve(t, facts, lane, sandbox.ModeWorkspaceWrite)); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	tmp := local.Wrapper.SessionTmp()
	child.ownsEnv = true
	child.discardRestoredCandidate()
	// The scratch is named after the session, so it is kept for a later restore
	// and its root's archive; what must not survive is its lease.
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("discarded restore candidate removed its named sandbox scratch: %v", err)
	} else if scratchLeaseHeld(t, tmp) {
		t.Errorf("discarded restore candidate left the sandbox scratch %s lease held", tmp)
	}
}

// The DEFAULT session environment is unsandboxed, and it mints a session scratch
// of its own on its first command rather than at construction. A discarded
// candidate was never adopted, so no Close is ever coming to release the flock
// lease under that scratch: settling only the sandbox-owned scratch would hold
// the unsandboxed one's lease for the life of the process.
func TestDiscardRestoredCandidateKeepsItsNamedUnsandboxedScratch(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	candidate := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	local, ok := candidate.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("restore candidate env = %T, want a local environment", candidate.currentEnv())
	}
	// Running a command is what mints the unsandboxed scratch, exactly as a
	// restore's own first command does.
	if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	scratch := local.SessionScratchDir()
	if scratch == "" {
		t.Fatal("an unsandboxed env minted no session scratch, so there is nothing to dispose")
	}

	candidate.ownsEnv = true
	candidate.discardRestoredCandidate()

	// The scratch is named after the session, so it is kept for a later restore
	// and its root's archive; what must not survive is its lease.
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("discarded restore candidate removed its named unsandboxed scratch %s: %v", scratch, err)
	} else if scratchLeaseHeld(t, scratch) {
		t.Errorf("discarded restore candidate left the unsandboxed scratch %s lease held", scratch)
	}
}
