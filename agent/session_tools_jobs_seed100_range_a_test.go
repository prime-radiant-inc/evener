//go:build serffuzz

package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// seed100ToolsRangeA replays deterministic fixtures for the front half of the
// job-tool surface. The registered job-manager fuzz target calls this helper;
// keeping the fixtures behind serffuzz preserves the ordinary test boundary.
func seed100ToolsRangeA(t *testing.T) {
	t.Helper()

	// Pure guards whose edge values are otherwise diluted by the larger program.
	_ = clampJobBlockTimeout(minJobBlockTimeoutMS - 1)
	_ = clampJobBlockTimeout(maxJobBlockTimeoutMS + 1)
	for _, args := range []map[string]any{
		{"sandbox_net": true},
		{"sandbox_net": "true"},
		{"max_wait_ms": -1},
		{"delegation_allowance": -1},
		{"delegation_allowance": 1, "result_schema": map[string]any{"type": "string"}},
	} {
		_, _ = decodeDelegateArgs(args)
	}
	bare := newSession(t)
	_, _ = delegateSendTool(context.Background(), bare, map[string]any{"to": "caller"}, 100)
	_, _ = delegateTool(context.Background(), bare, map[string]any{"sandbox_net": "bad"}, 100)
	_, _ = delegateTool(context.Background(), &Session{}, map[string]any{"task": "x"}, 100)
	_, _ = jobWatchTool(bare, map[string]any{"operation": "create", "source": "bad-source"}, 100)
	ended := frozenTestTime

	root := newSession(t)
	coordinator := newSession(t)
	worker := newSession(t)
	root.subagents.track(&subagent{id: coordinator.ID(), sess: coordinator, status: SubagentRunning})
	coordinator.subagents.track(&subagent{id: worker.ID(), sess: worker, status: SubagentRunning})
	deepID := "job_range_a_deep"
	for _, fixture := range []struct {
		sess    *Session
		visible string
	}{
		{root, root.ID()},
		{coordinator, coordinator.ID()},
		{worker, worker.ID()},
	} {
		if err := fixture.sess.jobManager.appendEvent(jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ended, JobID: deepID, Type: jobstore.JobShell,
			OwnerSessionID: worker.ID(), VisibleToSession: fixture.visible, StartedAt: &ended,
		}); err != nil {
			t.Fatal(err)
		}
	}
	child := newSession(t)
	bare.subagents.track(&subagent{id: child.ID(), sess: child, status: SubagentRunning})
	_, _, _ = bare.clearDescendantReceiverWatchByID("missing")

	for _, test := range []func(*testing.T){
		TestDecodeDelegateArgs_Sandbox,
		TestDecodeDelegateArgs_SandboxNetMalformed,
		TestDelegateSendNegativeBlockTimeoutDoesNotStart,
		TestDelegateSendMaxWaitMSDecodeTable,
		TestDelegateAndDelegateSendAcceptZeroMaxWaitMS,
		TestJobWatchParentSourceRequiresGrant,
		TestJobWatchParentSourceInstallsOnParentWithChildReceiver,
		TestJobWatchParentSourcePublicClearRoutesToParent,
		TestJobWatchAllowsDescendantConcreteJobSource,
		TestJobWatchAllowsDirectChildConcreteJobSourceAndManagesIt,
	} {
		t.Run("range-a-fixture", test)
	}
}
