package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Mutation caught: removing the settled Manager outcome, or changing terminal
// shell outcomes when stopping a naturally settled delegate tree.
// Both delegates and the background shell are created through actual tools.
func TestManagerSettledDelegateAfterRealTreeDrain(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("SETTLED-COORD", "SETTLED-WORKER")
	adapter.script("root", func(llm.Request) llm.Response {
		return delegateToolResponse("coordinator", "SETTLED-COORD")
	}, func(llm.Request) llm.Response { return agenttest.FinalResponse("root") })
	adapter.script("SETTLED-COORD", func(llm.Request) llm.Response {
		return delegateToolResponse("worker", "SETTLED-WORKER")
	}, func(llm.Request) llm.Response { return agenttest.FinalResponse("coordinator") })
	adapter.script("SETTLED-WORKER", func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": "printf settled", "mode": "background"})
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: args, Type: "function"})
	}, func(llm.Request) llm.Response { return agenttest.FinalResponse("worker") })
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(t.Context(), "start", nil); err != nil {
		t.Fatal(err)
	}
	awaitRootReport(t, root)
	rows := root.delegateController.Snapshot().rows
	if len(rows) != 2 {
		t.Fatalf("delegate rows=%d, want 2", len(rows))
	}
	var coordinator, workerSession string
	for _, row := range rows {
		if row.parentID == "" {
			coordinator = row.id
		} else {
			workerSession = row.descriptor.ChildSessionID
			// A root report can precede a shell-notification resumed turn.
			// The live parent, not the root authority owner in the descriptor,
			// holds this nested worker's existing supervision registry.
			root.delegateController.mu.Lock()
			parent := root.delegateController.live[row.parentID].runtime
			root.delegateController.mu.Unlock()
			waitForStableSupervisionRun(t, parent, workerSession)
		}
	}
	for _, row := range rows {
		if row.id == coordinator {
			waitForStableSupervisionRun(t, root, row.descriptor.ChildSessionID)
		}
	}
	for _, row := range root.delegateController.Snapshot().rows {
		if row.currentRunOpen || row.pendingStopSeq != 0 {
			t.Fatalf("unsettled delegate: %s generation=%d", row.id, row.generation)
		}
	}
	store, err := jobstore.Open(filepath.Join(jobsDir(root.stateDir, workerSession), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("worker shells=%d, want 1", len(records))
	}
	for _, record := range records {
		if !record.Status.IsTerminal() {
			t.Fatalf("settled tree retains running shell: %+v", record)
		}
	}
	result, err := root.StopOwnedDelegateForManager(t.Context(), appwire.SessionOwnedDelegateStopParams{DelegateID: coordinator})
	if err != nil || result.Outcome != "already_stopped" {
		t.Fatalf("Manager stop=%+v err=%v", result, err)
	}
	// Compare actual human stop outcome, not its additional idle journal event.
	if _, err := jobStopTool(t.Context(), root, map[string]any{"target": coordinator}, 0); err != nil {
		t.Fatal(err)
	}
	for _, row := range root.delegateController.Snapshot().rows {
		if row.currentRunOpen {
			t.Fatalf("human stop exposed live run: %+v", row)
		}
	}
	after, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for id, record := range after {
		if !record.Status.IsTerminal() || record.Status != records[id].Status {
			t.Fatalf("human stop changed shell outcome: %+v", record)
		}
	}
}
