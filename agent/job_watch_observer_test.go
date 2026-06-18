package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// stateDirForJM derives the project state dir from a jobManager's per-session
// jobs dir (<stateDir>/sessions/<id>), the location of session .meta.json files.
func stateDirForJM(jm *jobManager) string {
	return filepath.Dir(filepath.Dir(jm.dir))
}

// TestMintWatchCreateReadGrantStampsObservedBy proves that installing a sidecar
// watch (concrete worker target, delivered to a concrete observer delegate)
// stamps the observer's session id onto the watched WORKER's SessionMeta, so the
// hub can later auto-open the observer beside the worker.
func TestMintWatchCreateReadGrantStampsObservedBy(t *testing.T) {
	jm := newTestJM(t)
	stateDir := stateDirForJM(jm)

	// The watched worker is a running delegate whose transcript ref resolves to
	// its child session id; the observer is a delegate send target.
	const workerSessionID = "WORKER"
	var signals atomic.Int32
	workerJobID := seedRunningDelegate(t, jm, encodeRef("", workerSessionID), &signals)
	seedWatchSendDelegateTarget(t, jm, "job_obs")
	observerSessionID := "child_job_obs"

	// The worker's meta must exist for the stamp to land on it (the worker is a
	// real child session with its own meta on disk).
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: workerSessionID, IsSubagent: true}); err != nil {
		t.Fatalf("seed worker meta: %v", err)
	}

	if _, err := jm.configureWatch(watchArgs{
		Target:      workerJobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configureWatch: %v", err)
	}

	got, err := schema.LoadSessionMeta(stateDir, workerSessionID)
	if err != nil {
		t.Fatalf("load worker meta: %v", err)
	}
	if len(got.ObservedBy) != 1 || got.ObservedBy[0] != observerSessionID {
		t.Fatalf("worker meta ObservedBy = %v, want [%s]", got.ObservedBy, observerSessionID)
	}
}

// TestMintWatchCreateReadGrantObservedByDedups proves repeated watch installs of
// the same (worker, observer) pair do not duplicate the observer id on the
// worker's meta — the set is append-only and deduped, mirroring the grant log.
func TestMintWatchCreateReadGrantObservedByDedups(t *testing.T) {
	jm := newTestJM(t)
	stateDir := stateDirForJM(jm)
	var signals atomic.Int32
	workerJobID := seedRunningDelegate(t, jm, encodeRef("", "WORKER"), &signals)
	seedWatchSendDelegateTarget(t, jm, "job_obs")
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: "WORKER", IsSubagent: true}); err != nil {
		t.Fatalf("seed worker meta: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := jm.configureWatch(watchArgs{
			Target:      workerJobID,
			OutputMatch: "(?i)ready",
			Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
		}); err != nil {
			t.Fatalf("configureWatch #%d: %v", i, err)
		}
	}

	got, err := schema.LoadSessionMeta(stateDir, "WORKER")
	if err != nil {
		t.Fatalf("load worker meta: %v", err)
	}
	if len(got.ObservedBy) != 1 {
		t.Fatalf("ObservedBy must dedup; got %v", got.ObservedBy)
	}
}

// TestOrdinaryWorkerHasNoObservedBy proves a delegate worker that is not the
// target of any watch never gains an ObservedBy entry — the stamp is confined to
// the watch-install seam.
func TestOrdinaryWorkerHasNoObservedBy(t *testing.T) {
	jm := newTestJM(t)
	stateDir := stateDirForJM(jm)
	var signals atomic.Int32
	_ = seedRunningDelegate(t, jm, encodeRef("", "WORKER"), &signals)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: "WORKER", IsSubagent: true}); err != nil {
		t.Fatalf("seed worker meta: %v", err)
	}

	got, err := schema.LoadSessionMeta(stateDir, "WORKER")
	if err != nil {
		t.Fatalf("load worker meta: %v", err)
	}
	if len(got.ObservedBy) != 0 {
		t.Fatalf("un-watched worker meta ObservedBy = %v, want empty", got.ObservedBy)
	}
}

func TestWatchSendBuildsObserverFrame(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	captured := captureWatchSends(t, s.jobManager)
	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "watch",
		Name: "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(
			`{"target":%q,"output_match":"(?i)ready","send":{"to":"job_obs","message":"observe"}}`,
			shellOut.JobID,
		)),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch returned error: %s", watchRes.Output)
	}

	feedJob(s.jobManager, shellOut.JobID, []byte("server READY\n"))
	sends := captured()
	if len(sends) != 1 {
		t.Fatalf("expected one watch send, got %#v", sends)
	}
	if sends[0].Target != "job_obs" {
		t.Fatalf("watch send target = %q, want job_obs", sends[0].Target)
	}
	if !sends[0].FromWatch || !sends[0].Background || !sends[0].BackgroundSet {
		t.Fatalf("watch send args = %+v, want background watch delivery", sends[0])
	}
	if !strings.Contains(sends[0].Message, "observe") || !strings.Contains(sends[0].Message, "server READY") {
		t.Fatalf("watch send message = %q, want configured message and trigger context", sends[0].Message)
	}
}

// captureWatchSends returns a closure that drives the drain's delivery primitive
// for every recorded delegate-targeted pending send and returns the captured
// delivery args. Observation only records pending intent (spec §3); calling the
// returned closure stands in for the loop-owned drain, capturing what it delivers.
func captureWatchSends(t *testing.T, jm *jobManager) func() []sendMessageArgs {
	t.Helper()
	var mu sync.Mutex
	var sent []sendMessageArgs
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, a)
		return sendMessageResult{}
	}

	seedCommonWatchSendTargets(t, jm)

	return func() []sendMessageArgs {
		_ = drainWatchSendsVia(t, jm, send)
		mu.Lock()
		defer mu.Unlock()
		return append([]sendMessageArgs(nil), sent...)
	}
}
