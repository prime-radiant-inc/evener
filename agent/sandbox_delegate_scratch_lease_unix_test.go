//go:build unix

package agent

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// TestStableDelegate_PerDelegateSandboxCleansScratchOnSpawnFailure: when a
// per-delegate sandbox EnableSandbox's a fresh env and the spawn then fails at
// NewSession, the provisioned scratch dir must release its lease, not leak it. A nil child
// client (via childClientFactory) forces NewSession to fail AFTER EnableSandbox. Not
// parallel: it isolates TMPDIR to observe the sandbox scratch base.
func TestStableDelegate_PerDelegateSandboxCleansScratchOnSpawnFailure(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)

	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(c), withDir(lane), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-leak-*"),
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: facts},
			childClientFactory:  func() *llm.Client { return nil },
		},
	}))

	if before := sandboxScratchDirs(t, isolated); len(before) != 0 {
		t.Fatalf("isolated tmp base must start free of sandbox scratch, got %v", before)
	}

	res := s.createDelegate(context.Background(), delegateArgs{Task: "child task", Sandbox: "restricted"})
	if res.Err == nil {
		t.Fatal("expected NewSession to fail with a nil child client")
	}
	// The failed start keeps its stable identity, so its named scratch stays
	// in the root's tree for the archive; what must not outlive the failure
	// is the lease that keeps the crashed-scratch sweeper away from it.
	kept := sandboxScratchDirs(t, isolated)
	if len(kept) == 0 {
		t.Fatal("the failed start kept no scratch, so the lease check below would prove nothing")
	}
	for _, dir := range kept {
		if scratchLeaseHeld(t, filepath.Join(isolated, dir)) {
			t.Errorf("per-delegate sandbox scratch %s still holds its lease after the failed start", dir)
		}
	}
}

// TestDegradedDelegateSpawnFailureReleasesItsScratchLease: the degraded box
// provisions a scratch dir like any other per-delegate sandbox, so a spawn that
// fails after provisioning must release it. The scratch holds a flock lease
// until it is retained or cleaned, and the crashed-scratch sweeper only
// reclaims candidates whose lease it can acquire — so a leaked lease survives
// the sweeper for the daemon's whole uptime. Driven through the real abort path
// (a nil child client fails NewSession after the environment is prepared). Not
// parallel: it isolates TMPDIR to observe the scratch base.
func TestDegradedDelegateSpawnFailureReleasesItsScratchLease(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)

	lane, home := sbxLane(t)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(client), withDir(lane), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-degrade-leak-*"),
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: sbxNoBackendFacts(home)},
			childClientFactory:  func() *llm.Client { return nil },
		},
	}))
	if before := sandboxScratchDirs(t, isolated); len(before) != 0 {
		t.Fatalf("isolated tmp base must start free of sandbox scratch, got %v", before)
	}

	// A read-only agent type derives the degraded box on its own.
	res := s.createDelegate(context.Background(), delegateArgs{Task: "child task", AgentType: "explorer"})
	if res.Err == nil {
		t.Fatal("expected NewSession to fail with a nil child client")
	}
	kept := sandboxScratchDirs(t, isolated)
	if len(kept) == 0 {
		t.Fatal("the failed start kept no scratch, so the lease check below would prove nothing")
	}
	for _, dir := range kept {
		if scratchLeaseHeld(t, filepath.Join(isolated, dir)) {
			t.Errorf("degraded delegate scratch %s still holds its lease after the failed start", dir)
		}
	}
}
