package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func failedShellResultTurn(callID string, exitCode int64) schema.Turn {
	state, err := json.Marshal(struct {
		ExitCode int64 `json:"exit_code"`
	}{exitCode})
	if err != nil {
		panic(err)
	}
	return schema.NewTurn(schema.TurnToolResults, llm.Message{Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: callID,
			Name:       "shell",
			ToolState:  state,
		},
	}}})
}

func erroredResultTurn(callID string) schema.Turn {
	return schema.NewTurn(schema.TurnToolResults, llm.Message{Content: []llm.ContentPart{{
		Kind:       llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{ToolCallID: callID, Name: "read_file", IsError: true},
	}}})
}

func TestFailedToolCallsSnapshotStartsMeasuredAtZero(t *testing.T) {
	// Zero is a real measurement here, not an absence: the session has a
	// transcript and nothing in it has failed. The strip renders nothing either
	// way, but only one of the two claims is something the strip may make.
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	defer sess.Close()

	count, ok := sess.FailedToolCallsSnapshot()
	if !ok {
		t.Fatal("FailedToolCallsSnapshot() reported absent, want a measured 0 for a fresh session")
	}
	if count != 0 {
		t.Fatalf("FailedToolCallsSnapshot() = %d, want 0", count)
	}
}

func TestFailedToolCallsSnapshotRisesAsFailuresAreRecorded(t *testing.T) {
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	defer sess.Close()

	if err := sess.transcript.Append(failedShellResultTurn("call_1", 1)); err != nil {
		t.Fatalf("append failed shell result: %v", err)
	}
	if count, _ := sess.FailedToolCallsSnapshot(); count != 1 {
		t.Fatalf("FailedToolCallsSnapshot() = %d, want 1 while the session is still running", count)
	}
	if err := sess.transcript.Append(erroredResultTurn("call_2")); err != nil {
		t.Fatalf("append errored result: %v", err)
	}
	if count, _ := sess.FailedToolCallsSnapshot(); count != 2 {
		t.Fatalf("FailedToolCallsSnapshot() = %d, want 2", count)
	}
}

func TestFailedToolCallsSnapshotIsAbsentWithoutAStateDir(t *testing.T) {
	// No transcript means nobody counted. A session that reported a confident 0
	// here would be vouching for a run it never recorded.
	sess := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1}))
	defer sess.Close()

	if count, ok := sess.FailedToolCallsSnapshot(); ok {
		t.Fatalf("FailedToolCallsSnapshot() = (%d, true), want absent with no transcript", count)
	}
}

func TestFailedToolCallsSnapshotCoversTheWholeSessionAfterResume(t *testing.T) {
	// The figure has to be whole-session, not since-restart: a resumed session
	// that restarted its count at 0 would under-report exactly like a windowed
	// one, and under-reporting is the harm the count exists to prevent.
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	if err := sess.transcript.Append(failedShellResultTurn("call_1", 2)); err != nil {
		t.Fatalf("append: %v", err)
	}
	meta := sess.Meta()
	sess.Close()

	resumed, err := RestoreSessionFromMetaWithConfig(
		llm.NewClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir), meta,
		RestoreSessionConfig{StateDir: dir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer resumed.Close()

	count, ok := resumed.FailedToolCallsSnapshot()
	if !ok {
		t.Fatal("FailedToolCallsSnapshot() reported absent after resume, want the pre-restart failure")
	}
	if count != 1 {
		t.Fatalf("FailedToolCallsSnapshot() = %d, want 1 carried across the restart", count)
	}

	if err := resumed.transcript.Append(erroredResultTurn("call_2")); err != nil {
		t.Fatalf("append after resume: %v", err)
	}
	if count, _ := resumed.FailedToolCallsSnapshot(); count != 2 {
		t.Fatalf("FailedToolCallsSnapshot() = %d, want 2 (before the restart plus after it)", count)
	}
}

func TestFailedToolCallsSnapshotDoesNotChargeAForkChildTheParentsFailures(t *testing.T) {
	// A fork child's transcript opens with a verbatim copy of the parent's
	// prefix. DivergenceTurn is where the child's own history begins, and it
	// bounds the live count exactly as it bounds the token sum.
	dir := t.TempDir()
	parent := newSession(t, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	if err := parent.transcript.Append(failedShellResultTurn("call_1", 1)); err != nil {
		t.Fatalf("append parent failure: %v", err)
	}
	parentMeta := parent.Meta()
	parent.Close()

	// The child reuses the parent's transcript bytes, then declares where its
	// own history starts — the shape writeForkChild produces.
	childMeta := parentMeta
	childMeta.ParentSessionID = parentMeta.ID
	childMeta.DivergenceTurn = 2

	child, err := RestoreSessionFromMetaWithConfig(
		llm.NewClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir), childMeta,
		RestoreSessionConfig{StateDir: dir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer child.Close()

	count, ok := child.FailedToolCallsSnapshot()
	if !ok {
		t.Fatal("FailedToolCallsSnapshot() reported absent for a fork child, want a measured count")
	}
	if count != 0 {
		t.Fatalf("FailedToolCallsSnapshot() = %d, want 0: the parent's failure is not the child's", count)
	}
}

func TestFailedToolCallsSnapshotSurvivesTheSessionEnding(t *testing.T) {
	// A session that ends while someone is watching keeps being served from the
	// daemon until the next read reroutes to disk. Its settled count has to
	// still be there, or the figure vanishes at the moment it stops changing.
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	if err := sess.transcript.Append(erroredResultTurn("call_1")); err != nil {
		t.Fatalf("append: %v", err)
	}
	sess.Close()

	count, ok := sess.FailedToolCallsSnapshot()
	if !ok {
		t.Fatal("FailedToolCallsSnapshot() reported absent after Close, want the settled count")
	}
	if count != 1 {
		t.Fatalf("FailedToolCallsSnapshot() after Close = %d, want 1", count)
	}
}

func TestFailedToolCallsSnapshotIgnoresATranscriptItCouldNotOpen(t *testing.T) {
	// Belt and braces on the load-bearing rule: a session whose writer was never
	// installed reports absent rather than a fabricated clean run.
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	defer sess.Close()

	sess.transcript = nil
	if count, ok := sess.FailedToolCallsSnapshot(); ok {
		t.Fatalf("FailedToolCallsSnapshot() = (%d, true), want absent when there is no writer", count)
	}
	_ = filepath.Join(dir, "unused")
}
