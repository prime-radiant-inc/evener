//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Mutation caught: treating a closed exhausted generation as a drained subtree
// while its real committed background shell is still alive. The human stop is
// the independent control and cleanup, not a hand-seeded controller state.
func TestManagerStopExhaustedDelegateWithRunningShell(t *testing.T) {
	t.Parallel()
	const marker = "EXHAUSTED-STOP-WORKER"
	pidPath := filepath.Join(t.TempDir(), "shell.pid")
	command := fmt.Sprintf("printf '%%s\\n' \"$$\" > %q; exec tail -f /dev/null", pidPath)
	adapter := newMessagingAdapter(marker)
	secondWorkerResponse := make(chan struct{})
	releaseSecondWorkerResponse := sync.OnceFunc(func() { close(secondWorkerResponse) })
	adapter.script("root", func(llm.Request) llm.Response {
		return delegateToolResponse("worker", marker)
	}, func(llm.Request) llm.Response { return agenttest.FinalResponse("root") })
	adapter.script(marker, func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": command, "mode": "background"})
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: args, Type: "function"})
	}, func(llm.Request) llm.Response {
		<-secondWorkerResponse
		return jobListResponse("exhaust-rounds")
	})
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.MaxToolRoundsPerInput = 2
	})

	// Registered after the fixture so stop/exit proof precedes Session.Close and
	// TempDir removal even when a prerequisite or assertion fails.
	t.Cleanup(func() {
		for _, row := range root.delegateController.Snapshot().rows {
			if _, err := jobStopTool(context.Background(), root, map[string]any{"target": row.id, "max_wait_ms": 5000}, 0); err != nil {
				t.Errorf("cleanup human stop %s: %v", row.id, err)
			}
		}
		root.Close()
		data, err := os.ReadFile(pidPath)
		if os.IsNotExist(err) {
			t.Log("CLEANUP: no owned shell PID file was created; session close joined admitted work")
			return
		}
		if err != nil {
			t.Errorf("cleanup PID read: %v", err)
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			t.Errorf("cleanup invalid PID: %q err=%v", data, err)
			return
		}
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Errorf("cleanup owned PID %d still exists: %v", pid, err)
			return
		}
		t.Logf("CLEANUP: owned PID=%d absent, kill(0)=ESRCH before fixture removal", pid)
	})
	// Registered after process cleanup so LIFO releases a blocked scripted
	// provider step before stop/Close on every exit. The successful path releases
	// it as soon as root processing returns.
	t.Cleanup(releaseSecondWorkerResponse)

	if _, err := root.ProcessInput(t.Context(), "start", nil); err != nil {
		t.Fatalf("SETUP: root input failed: %v", err)
	}
	releaseSecondWorkerResponse()
	awaitRootReport(t, root)
	rows := root.delegateController.Snapshot().rows
	if len(rows) != 1 {
		t.Fatalf("SETUP: delegate rows=%d, want 1", len(rows))
	}
	waitForStableSupervisionRun(t, root, rows[0].descriptor.ChildSessionID)
	rows = root.delegateController.Snapshot().rows
	if len(rows) != 1 {
		t.Fatalf("SETUP: finalized delegate rows=%d, want 1", len(rows))
	}
	row := rows[0]
	if row.generation != 1 || row.currentRunOpen || row.pendingStopSeq != 0 {
		t.Fatalf("SETUP: generation=%d open=%v stopSeq=%d", row.generation, row.currentRunOpen, row.pendingStopSeq)
	}
	outcome := row.lastOutcome
	if outcome == nil || outcome.Status != delegatestore.OutcomeExhausted || outcome.ExhaustionBudget != delegatestore.ExhaustionBudgetToolRounds || outcome.ExhaustionLimit != 2 {
		t.Fatalf("SETUP: expected real tool-round exhaustion, got %+v", outcome)
	}
	if got := len(adapter.requestsFor(marker)); got != 2 {
		t.Fatalf("SETUP: child provider calls=%d, want exactly 2", got)
	}
	root.delegateController.mu.Lock()
	live := root.delegateController.live[row.id]
	var child *Session
	if live != nil {
		child = live.runtime
	}
	root.delegateController.mu.Unlock()
	if child == nil {
		t.Fatal("SETUP: exhausted delegate lost its live shell-owning runtime")
	}
	jm := child.jobManager
	records, err := jm.store.Load()
	if err != nil || len(records) != 1 {
		t.Fatalf("SETUP: shell records=%d err=%v", len(records), err)
	}
	var jobID string
	for id, record := range records {
		if record.Status != jobstore.StatusRunning || record.Command != command || record.ParentDelegateID != row.id {
			t.Fatalf("SETUP: expected one running owned real shell, got %+v", record)
		}
		jobID = id
	}
	done, found := jobDone(jm, jobID)
	if !found {
		t.Fatal("SETUP: no live shell completion handle")
	}
	select {
	case <-done:
		t.Fatal("SETUP: shell already completed")
	default:
	}
	var pid int
	// TRIPWIRE: the real shell writes readiness once, with no timer-driven work.
	waitForCondition(t, 30*time.Second, "owned shell PID readiness", func() bool {
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	})
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 0 {
		t.Fatalf("SETUP: owned PID=%d pgid=%d err=%v", pid, pgid, err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("SETUP: owned PID=%d is not alive: %v", pid, err)
	}
	t.Logf("PREREQUISITES: delegate=%s child=%s generation=%d open=%v stopSeq=%d outcome=%s budget=%s limit=%d providerCalls=2 job=%s status=running PID=%d PGID=%d completion=open", row.id, child.ID(), row.generation, row.currentRunOpen, row.pendingStopSeq, outcome.Status, outcome.ExhaustionBudget, outcome.ExhaustionLimit, jobID, pid, pgid)

	manager, managerErr := root.StopOwnedDelegateForManager(t.Context(), appwire.SessionOwnedDelegateStopParams{DelegateID: row.id, MaxWaitMS: 5000})
	managerLeftRunning := false
	select {
	case <-done:
	default:
		managerLeftRunning = true
	}
	if managerLeftRunning {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("SETUP: Manager comparison lost live process before human control: %v", err)
		}
	}
	t.Logf("MANAGER: result=%+v err=%v shellCompletionOpen=%v", manager, managerErr, managerLeftRunning)

	// On RED this cancels the exact work Manager left alive. On GREEN it
	// verifies that the independent human route leaves the cancelled shell so.
	if _, err := jobStopTool(t.Context(), root, map[string]any{"target": row.id, "max_wait_ms": 5000}, 0); err != nil {
		t.Fatalf("SETUP: human control stop failed: %v", err)
	}
	waitForTestSignal(t, done, "the exact owned shell completion")
	after, err := jm.store.Load()
	if err != nil || after[jobID] == nil || after[jobID].Status != jobstore.StatusCommandKilled {
		t.Fatalf("SETUP: human control did not prove shell cancellation: record=%+v err=%v", after[jobID], err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("SETUP: stopped owned PID=%d still exists: %v", pid, err)
	}
	if err := syscall.Kill(-pgid, 0); err != syscall.ESRCH {
		t.Fatalf("SETUP: stopped owned PGID=%d still exists: %v", pgid, err)
	}
	t.Logf("HUMAN CONTROL: job=%s status=%s completion=closed PID=%d PGID=%d both absent, kill(0)=ESRCH", jobID, after[jobID].Status, pid, pgid)
	if managerErr != nil {
		t.Fatalf("Manager stop error after prerequisites and successful human cancellation: %v", managerErr)
	}
	if managerLeftRunning {
		t.Fatalf("BEHAVIORAL RED: Manager outcome=%s left owned shell running, human subtree stop cancelled it", manager.Outcome)
	}
}
