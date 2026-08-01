package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/llm"
)

// railStep is one fake-model turn for the observer-grant rail fixture: the
// first turn that is offered job_watch installs the parent-source watch (only a
// delegate(watch_parent=true) child is offered it), every other turn reports
// through communicate and ends the job. Content-driven rather than positional
// so an extra resumed turn cannot shift the script.
func railStep(installed *atomic.Bool) func(llm.Request) llm.Response {
	return func(req llm.Request) llm.Response {
		if requestHasTool(req, "job_watch") && !installed.Swap(true) {
			return toolCallResponse(llm.ToolCallData{
				ID:        "call_watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(`{"operation":"create","source":"parent","events":["job.notification"]}`),
			})
		}
		return communicateWithDefaultOutput(railWorkerReport)
	}
}

// railWorkerReport is the payload every fixture delegate communicates; the
// granted read has to return the WORKER's copy of it.
const railWorkerReport = "HANDOFF_WORKER_RESULT artifact=summary status=ready"

func newObserverRailSession(t *testing.T) *Session {
	t.Helper()
	var installed atomic.Bool
	steps := make([]func(llm.Request) llm.Response, 12)
	for i := range steps {
		steps[i] = railStep(&installed)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: steps})
	return newDelegateTestSession(t, c)
}

// captureRailFrames drains every pending delegate-targeted watch send through
// the drain's own delivery primitive and returns the frames the observer would
// receive, standing in for the loop-owned drain (spec §3).
func captureRailFrames(t *testing.T, jm *jobManager) []string {
	t.Helper()
	var mu sync.Mutex
	var frames []string
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, a.Message)
		return sendMessageResult{}
	}
	if err := drainWatchSendsVia(t, jm, send); err != nil {
		t.Fatalf("drain watch sends: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), frames...)
}

func onlyRailFrame(t *testing.T, frames []string) string {
	t.Helper()
	if len(frames) != 1 {
		t.Fatalf("delivered frames = %d, want exactly 1: %q", len(frames), frames)
	}
	return frames[0]
}

// TestParentSourceWatchRailGrantsObserverReadOnRealDelegateFinish drives the
// spec §5.1 mint from the rail a live observer actually rides, end to end: a
// delegate(watch_parent=true) child installs its own source:"parent" watch from
// inside its turn, the parent's OWN delegate finish emits the job.notification
// event through emitJobFinished -> Session.emit -> onSessionEvent, and the
// resulting delivery is what mints. The eqs0 fixtures hand JobFinishedData to
// onSessionEvent directly, so this is the seam none of them exercise.
//
// It pins all four halves of the contract at once: the observer's OWN callback
// job mints nothing and its frame names no read (Jesse's ruling 5), another
// delegate's completion mints exactly one durable grant, that delivery's frame
// names the one call that spends it, and the observer's live session can make
// that call.
func TestParentSourceWatchRailGrantsObserverReadOnRealDelegateFinish(t *testing.T) {
	t.Parallel()
	parent := newObserverRailSession(t)

	observer := parent.createDelegate(context.Background(), delegateArgs{Task: "observe", BlockTimeoutMS: 5000, WatchParent: true})
	if observer.Err != nil {
		t.Fatalf("observer delegate: %v", observer.Err)
	}
	if !observer.Watching || len(observer.Watches) != 1 {
		t.Fatalf("observer result = %+v, want watching with one installed watch", observer)
	}
	_, observerSessionID, err := decodeRef(observer.TranscriptRef)
	if err != nil {
		t.Fatalf("decode observer transcript ref: %v", err)
	}

	// The observer's own job finishing already fired its watch once. That
	// delivery is the live-verified NEGATIVE: a finished job whose delegate is
	// the receiver is skipped at the mint, so the grant table stays empty and
	// the frame advertises no read.
	selfFrame := onlyRailFrame(t, captureRailFrames(t, parent.jobManager))
	if !strings.Contains(selfFrame, "job_id: "+observer.JobID) {
		t.Fatalf("self frame = %q, want the observer's own job %s in the event block", selfFrame, observer.JobID)
	}
	if strings.Contains(selfFrame, "read with:") {
		t.Fatalf("self frame = %q, want no read line on the observer's own callback job", selfFrame)
	}
	if grants := loadGrantTable(t, parent.jobManager); len(grants) != 0 {
		t.Fatalf("grants after the observer's own job finished = %+v, want none", grants)
	}

	worker := parent.createDelegate(context.Background(), delegateArgs{Task: "work", BlockTimeoutMS: 5000})
	if worker.Err != nil {
		t.Fatalf("worker delegate: %v", worker.Err)
	}
	if worker.Status != "completed" {
		t.Fatalf("worker delegate = %+v, want completed", worker)
	}

	workerFrame := onlyRailFrame(t, captureRailFrames(t, parent.jobManager))
	wantRead := `read with: read_transcript(transcript_ref="job:` + worker.JobID + `")`
	if !strings.Contains(workerFrame, wantRead) {
		t.Fatalf("worker frame = %q, want it to contain %q", workerFrame, wantRead)
	}
	if !loadGrantTable(t, parent.jobManager)[observerSessionID][worker.JobID] {
		t.Fatalf("grants = %+v, want %s -> %s", loadGrantTable(t, parent.jobManager), observerSessionID, worker.JobID)
	}
	if got := countWatchReadGrantEvents(t, parent.jobManager); got != 1 {
		t.Fatalf("grant events in the journal = %d, want 1 (only the worker's completion mints)", got)
	}

	// Spend side: the observer's own live session resolves the read through the
	// parent-injected grant seam, and gets the worker's report.
	sub := parent.subagents.get(observerSessionID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("observer subagent %q is not tracked", observerSessionID)
	}
	envelope, err := readJobTranscriptFor(t, sub.sess, worker.JobID)
	if err != nil {
		t.Fatalf("observer granted read_transcript: %v", err)
	}
	if !strings.Contains(envelope.Content, railWorkerReport) {
		t.Fatalf("granted read content = %q, want the worker's report", envelope.Content)
	}
}

// TestParentSourceWatchRailGrantsOnBackgroundDelegateFinish covers the other
// finalize path a live parent takes: a background delegate finalizes through
// finalizeDelegate/finishJob on the bridge goroutine rather than the foreground
// finalizeKeptSync, and its job.notification must mint the same grant.
func TestParentSourceWatchRailGrantsOnBackgroundDelegateFinish(t *testing.T) {
	t.Parallel()
	parent := newObserverRailSession(t)

	observer := parent.createDelegate(context.Background(), delegateArgs{Task: "observe", BlockTimeoutMS: 5000, WatchParent: true})
	if observer.Err != nil {
		t.Fatalf("observer delegate: %v", observer.Err)
	}
	_, observerSessionID, err := decodeRef(observer.TranscriptRef)
	if err != nil {
		t.Fatalf("decode observer transcript ref: %v", err)
	}
	// Settle the observer's own self-frame so the worker's is the only pending.
	captureRailFrames(t, parent.jobManager)

	worker := parent.createDelegate(context.Background(), delegateArgs{Task: "work", Background: true})
	if worker.Err != nil {
		t.Fatalf("background worker delegate: %v", worker.Err)
	}
	waitForShellDone(t, parent.jobManager, worker.JobID)

	workerFrame := onlyRailFrame(t, captureRailFrames(t, parent.jobManager))
	wantRead := `read with: read_transcript(transcript_ref="job:` + worker.JobID + `")`
	if !strings.Contains(workerFrame, wantRead) {
		t.Fatalf("background worker frame = %q, want it to contain %q", workerFrame, wantRead)
	}
	if !loadGrantTable(t, parent.jobManager)[observerSessionID][worker.JobID] {
		t.Fatalf("grants = %+v, want %s -> %s", loadGrantTable(t, parent.jobManager), observerSessionID, worker.JobID)
	}
}
