//go:build linux || darwin

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				text := requestText(req)
				if strings.Count(text, "DELEGATE_RESUME_CONTEXT") != 1 {
					t.Fatalf("delegate first user request resume context count = %d, want 1; text:\n%s", strings.Count(text, "DELEGATE_RESUME_CONTEXT"), text)
				}
				if strings.Contains(text, "DELEGATE_RESUME_USER_MESSAGE") {
					t.Fatalf("delegate first user request included hook user message; text:\n%s", text)
				}
				requireRequestContainsInOrder(t, req, "DELEGATE_RESUME_CONTEXT", "first post-restore delegate user turn")
				return finalResponse("delegate done")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	hookDir := t.TempDir()
	hookMarker := filepath.Join(hookDir, "session-start-hook")
	hookStartedFIFO := filepath.Join(hookDir, "hook-started")
	hookReleaseFIFO := filepath.Join(hookDir, "hook-release")
	if err := syscall.Mkfifo(hookStartedFIFO, 0o600); err != nil {
		t.Fatalf("mkfifo hook-started: %v", err)
	}
	if err := syscall.Mkfifo(hookReleaseFIFO, 0o600); err != nil {
		t.Fatalf("mkfifo hook-release: %v", err)
	}
	hookCommand := "printf '{\"systemMessage\":\"DELEGATE_RESUME_USER_MESSAGE\",\"hookSpecificOutput\":{\"additionalContext\":\"DELEGATE_RESUME_CONTEXT\"}}\\n'; " +
		"echo hook >> " + shellQuote(hookMarker) + "; " +
		"printf started > " + shellQuote(hookStartedFIFO) + "; " +
		"cat " + shellQuote(hookReleaseFIFO) + " >/dev/null"
	pluginDir := sessionStartHookPlugin(t, "sh -c "+shellQuote(hookCommand))
	meta, err := schema.LoadSessionMeta(s.stateDir, childID)
	if err != nil {
		t.Fatalf("load child meta: %v", err)
	}
	meta.Config.PluginDirs = []string{pluginDir}
	if err := schema.SaveSessionMeta(s.stateDir, meta); err != nil {
		t.Fatalf("save child meta: %v", err)
	}
	now := time.Unix(3400, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	preflight := requireDelegateRestorePreflight(t, s, rec)
	// This seam fires once per restore-side-effects run, and only on the WINNING
	// reconstruction (restoreTerminalDelegateChildClaimed reaches it only for the
	// goroutine that claimed the rebuild; the loser parks in pending.wait). So the
	// seam-invocation count is the race-free measure of "side effects run once".
	// Inside the seam we also wrap the winning child's enqueue to count caller
	// watch-send tokens: the winner surfaces the seeded pending token exactly once,
	// before it runs the SessionStart hook (the final, FIFO-blocking side effect).
	var sideEffectsRuns atomic.Int32
	var callerTokens atomic.Int32
	s.delegateRestoreBeforeSideEffects = func(child *Session) {
		sideEffectsRuns.Add(1)
		origEnqueue := child.jobManager.enqueue
		child.jobManager.enqueue = func(n jobNotification) {
			if n.WatchSend != nil && n.WatchSend.Key.ResolvedSendTo == runtimeMessageAliasCaller {
				callerTokens.Add(1)
			}
			if origEnqueue != nil {
				origEnqueue(n)
			}
		}
	}
	start := make(chan struct{})
	type restoreResult struct {
		sub *subagent
		err error
	}
	results := make(chan restoreResult, 2)
	for range 2 {
		go func() {
			<-start
			sub, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
			results <- restoreResult{sub: sub, err: err}
		}()
	}
	close(start)

	startedReader, err := os.OpenFile(hookStartedFIFO, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open hook-started fifo: %v", err)
	}
	started := make([]byte, len("started"))
	if _, err := startedReader.Read(started); err != nil {
		startedReader.Close()
		t.Fatalf("read hook-started fifo: %v", err)
	}
	startedReader.Close()
	if string(started) != "started" {
		t.Fatalf("hook-started fifo read %q, want started", string(started))
	}
	if got := countFileLines(t, hookMarker); got != 1 {
		t.Fatalf("SessionStart hook executions while restore hook is in flight = %d, want 1", got)
	}
	// The winner surfaces the seeded caller watch-send token before it runs the
	// SessionStart hook (token at the head of runDeferredRestoreSideEffects, hook
	// at the tail), so the hook-in-flight signal proves the token is already
	// enqueued. The loser is parked in pending.wait and surfaces nothing, and the
	// first user turn is not launched yet, so this count is race-free: exactly one.
	if got := callerTokens.Load(); got != 1 {
		t.Fatalf("caller watch-send tokens enqueued while restore hook is in flight = %d, want exactly one (side effects run once)", got)
	}

	tracked := s.subagents.get(childID)
	if tracked == nil || tracked.sess == nil {
		t.Fatalf("tracked child while restore hook is in flight = %+v, want retained child", tracked)
	}
	child := tracked.sess
	waitEntered := make(chan struct{})
	var waitEnteredOnce sync.Once
	child.pendingSessionStartWaitEntered = func() {
		waitEnteredOnce.Do(func() { close(waitEntered) })
	}
	userTurnDone := make(chan sendMessageResult, 1)
	go func() {
		userTurnDone <- s.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:         rec.DelegateID,
			Message:        "first post-restore delegate user turn",
			BackgroundSet:  true,
			Background:     false,
			BlockTimeoutMS: 5000,
		})
	}()

	// The first real user turn is racing with an in-flight restore-side-effect
	// SessionStart hook. It must reach the pending drain wait and then wait for
	// that hook result instead of running a second hook or making a model request
	// without the captured output.
	select {
	case <-waitEntered:
	case res := <-userTurnDone:
		t.Fatalf("first user turn completed before reaching in-flight restore hook wait: %+v", res)
	case <-time.After(5 * time.Second):
		t.Fatal("first user turn did not reach in-flight restore hook wait")
	}
	select {
	case res := <-userTurnDone:
		t.Fatalf("first user turn completed before in-flight restore hook was released: %+v", res)
	default:
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests while restore hook is in flight = %+v, want none", requests)
	}
	if got := countFileLines(t, hookMarker); got != 1 {
		t.Fatalf("SessionStart hook executions before releasing restore hook = %d, want 1", got)
	}
	if got := countSteeringEntriesContaining(child, "DELEGATE_RESUME_CONTEXT"); got != 0 {
		t.Fatalf("child resume hook context steering entries before releasing restore hook = %d, want 0; queue = %+v", got, child.SteeringQueueSnapshot())
	}
	if history := sessionHistoryText(child); strings.Contains(history, "DELEGATE_RESUME_CONTEXT") || strings.Contains(history, "DELEGATE_RESUME_USER_MESSAGE") {
		t.Fatalf("child resume hook output was delivered before releasing restore hook; history:\n%s", history)
	}
	if got := drainEventWarningsContaining(child, "DELEGATE_RESUME_USER_MESSAGE"); got != 0 {
		t.Fatalf("child resume hook user messages delivered before releasing restore hook = %d, want 0", got)
	}

	releaseWriter, err := os.OpenFile(hookReleaseFIFO, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open hook-release fifo: %v", err)
	}
	if _, err := releaseWriter.WriteString("release"); err != nil {
		releaseWriter.Close()
		t.Fatalf("write hook-release fifo: %v", err)
	}
	releaseWriter.Close()

	first := <-results
	second := <-results

	if first.err != nil {
		t.Fatalf("first restore error: %v", first.err)
	}
	if second.err != nil {
		t.Fatalf("second restore error: %v", second.err)
	}
	if first.sub == nil || second.sub == nil || first.sub != second.sub {
		t.Fatalf("reconstruction results = %p and %p, want same retained child", first.sub, second.sub)
	}
	if first.sub.sess != child {
		t.Fatalf("tracked child changed after restore: got %p, want %p", first.sub.sess, child)
	}
	// Side effects run exactly once: the loser reuses the winner's child without
	// re-running the restore-side-effects path. The cumulative caller-token count
	// is NOT a valid measure here — the first user turn (launched above, released
	// with the hook) drives the child's run loop, which re-tokens the still-pending
	// caller frame onto the child's own rail every drive (idempotent by key,
	// deduped at render; see drainJobManagerWatchSends). That re-tokening races
	// reconstruction completion under load. The seam invocation count is the
	// race-free proof, and the in-flight assertion above proved exactly one token.
	if got := sideEffectsRuns.Load(); got != 1 {
		t.Fatalf("restore side-effects runs = %d, want exactly one (loser reuses winner without re-running side effects)", got)
	}
	if got := countFileLines(t, hookMarker); got != 1 {
		t.Fatalf("SessionStart hook executions after restore completed = %d, want 1", got)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 0 {
		t.Fatalf("parent watch-frame steering deliveries = %d, want 0 (caller sends never steer); queue = %+v", got, s.SteeringQueueSnapshot())
	}
	if pending := loadSessionWatchSendRecord(t, s.stateDir, childID).Pending; len(pending) != 1 {
		t.Fatalf("child pending after winning reconstruction = %+v, want still pending (token surfaced, not settled)", pending)
	}

	res := <-userTurnDone
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage first post-restore user turn: %v", res.Err)
	}
	if requests := adapter.Requests(); len(requests) == 0 {
		t.Fatal("adapter requests after first post-restore user turn = 0, want at least 1")
	} else {
		text := requestText(requests[0])
		if strings.Count(text, "DELEGATE_RESUME_CONTEXT") != 1 {
			t.Fatalf("first adapter request resume context count = %d, want 1; text:\n%s", strings.Count(text, "DELEGATE_RESUME_CONTEXT"), text)
		}
	}
	if got := drainEventWarningsContaining(child, "DELEGATE_RESUME_USER_MESSAGE"); got != 1 {
		t.Fatalf("child resume hook user messages after first real user turn = %d, want 1", got)
	}
	if got := countFileLines(t, hookMarker); got != 1 {
		t.Fatalf("SessionStart hook executions after first real user turn = %d, want still 1", got)
	}
}
