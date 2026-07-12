//go:build serffuzz

package agent

import (
	"context"
	"errors"
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
	_ = classifyJobReadWindow(false, false, false, true)
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
	invalidReadArgs := []map[string]any{
		{},
		{"job_id": "dlg_x"},
		{"job_id": "missing", "head_lines": -1},
		{"job_id": "missing", "tail_lines": -1},
		{"job_id": "missing", "from_line": -1},
		{"job_id": "missing", "line_count": -1},
		{"job_id": "missing", "grep": "["},
		{"job_id": "missing", "max_wait_ms": -1},
	}
	for _, args := range invalidReadArgs {
		_, _ = jobReadOutputTool(context.Background(), bare, args, 100)
	}
	readFault := errors.New("range a read fault")
	bare.cfg.spawn.parentGrantedJobRead = func(string, string) (*grantedJobRead, bool) {
		return &grantedJobRead{
			record: &jobstore.JobRecord{JobID: "missing"},
			readWindow: func(int, bool) (string, int64, int64, bool, error) {
				return "", 0, 0, false, readFault
			},
		}, true
	}
	for _, args := range invalidReadArgs[2:] {
		_, _ = jobReadOutputTool(context.Background(), bare, args, 100)
	}
	_, _ = jobReadOutputTool(context.Background(), bare, map[string]any{"job_id": "missing", "tail_lines": 1}, 100)
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
		TestGrantedReadServesWatchedJobCrossStore,
		TestNonGrantedReadPreservesTargetNotFound,
		TestGrantedReadAfterParentClosedPreservesTargetNotFound,
		TestGrantedReadRejectsBlock,
		TestJobReadOutputHeadLinesReadsFromStart,
		TestJobReadOutputFromLineExclusiveWithHeadTail,
		TestJobReadOutputZeroHeadTailTreatedAsUnset,
		TestJobReadOutputNegativeHeadBytesRejected,
		TestJobReadOutputInvalidGrepCarriesPrefix,
		TestJobReadOutputGrepSearchesRetainedOutputBeyondTail,
	} {
		t.Run("range-a-fixture", test)
	}
}
