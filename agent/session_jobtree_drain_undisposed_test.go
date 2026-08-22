package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// startUndisposedBackgroundShell launches a real background shell through the
// session's own registry and returns its job id. The command never finishes on
// its own; callers stop it (or expect the drain to give up on it).
func startUndisposedBackgroundShell(t *testing.T, sess *Session) string {
	t.Helper()
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 300","mode":"background"}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil || out.JobID == "" {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, res.Output)
	}
	t.Cleanup(func() {
		_, _ = sess.jobManager.stop(out.JobID)
		waitForShellDone(t, sess.jobManager, out.JobID)
	})
	return out.JobID
}

// feedRechecks streams recheck ticks to the drain until ctx ends, standing in
// for the production 250ms ticker: the first announcement deliberately waits
// out one park in waitDrainWake (so an in-flight completion is delivered
// instead of announced), and without ticks that park would never end.
func feedRechecks(ctx context.Context) <-chan time.Time {
	recheck := make(chan time.Time)
	go func() {
		for {
			select {
			case recheck <- time.Time{}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return recheck
}

// runDrainToCompletion runs the REAL drain — kickDriveTree and ProcessInputKind,
// no substitutes — and returns its result. Substituting the turn runner is how
// two earlier attempts at this feature shipped inert; nothing here may stub it.
func runDrainToCompletion(t *testing.T, sess *Session) (string, error) {
	t.Helper()
	type drainDone struct {
		res string
		err error
	}
	// TRIPWIRE: the escalation is turn-paced (each pass either runs a scripted
	// model turn, parks for a fed recheck, or returns), so the drain finishes
	// in milliseconds; 30s only fires on a genuine hang — which is the #297
	// regression this suite exists to catch.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan drainDone, 1)
	go func() {
		res, err := sess.drainJobTreeWith(ctx, feedRechecks(ctx), sess.kickDriveTree, sess.ProcessInputKind)
		done <- drainDone{res, err}
	}()
	select {
	case d := <-done:
		return d.res, d.err
	case <-ctx.Done():
		t.Fatal("drain never returned with an undisposed background job: this is the #297 hang")
		return "", nil
	}
}

// assertDrainParks runs the real drain in a goroutine and positively confirms
// it stays parked (no return) — the pre-existing waiting behaviour that every
// out-of-scope case must keep byte-identical.
func assertDrainParks(t *testing.T, sess *Session, msg string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = sess.drainJobTreeWith(ctx, feedRechecks(ctx), sess.kickDriveTree, sess.ProcessInputKind)
		close(done)
	}()
	select {
	case <-done:
		cancel()
		t.Fatalf("drain returned when it should have kept waiting: %s", msg)
	// TRIPWIRE: not a completion wait — this deliberately blocks a short window
	// to positively confirm the drain stayed parked instead of firing.
	case <-time.After(150 * time.Millisecond):
	}
	return func() {
		cancel()
		select {
		case <-done:
		// TRIPWIRE: awaits the drain goroutine's own exit after cancellation;
		// 30s only fires on a genuine hang.
		case <-time.After(30 * time.Second):
			t.Fatal("drain did not exit after cancellation")
		}
	}
}

// TestOneShotDrainEscalatesAndExitsOnAnUndisposedBackgroundJob is the headline
// #297 regression: a one-shot session whose model leaves a background service
// running and ignores both announcements must still exit — announce, warn,
// then give up so Close()'s kill path runs — instead of blocking until
// something external kills the process. The escalation is paced by the model's
// own turns, not a clock: each announcement IS a turn, and the count advances
// only when a turn completes with the job still undisposed.
func TestOneShotDrainEscalatesAndExitsOnAnUndisposedBackgroundJob(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("I hear you, doing nothing") },
		func(llm.Request) llm.Response { return finalResponse("still doing nothing") },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID := startUndisposedBackgroundShell(t, sess)

	res, err := runDrainToCompletion(t, sess)
	if err != nil {
		t.Fatalf("drain error: %v", err)
	}
	if res != "" {
		t.Fatalf("drain result = %q; announcement replies must never become the run's answer", res)
	}

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls = %d, want exactly 2 (announce, then final warning)", len(reqs))
	}
	if !requestsContain(reqs[:1], jobID, "cannot finish", "job_stop", "job_watch", "progress_interval_ms") {
		t.Fatalf("first announcement must name the job and all remedies, got: %v", reqs[0].Messages[len(reqs[0].Messages)-1].Text())
	}
	if !requestsContain(reqs[1:], jobID, "killed") {
		t.Fatalf("second announcement must warn the job will be killed, got: %v", reqs[1].Messages[len(reqs[1].Messages)-1].Text())
	}

	warnings := collectStallWarnings(sess)
	if len(warnings) != 1 {
		t.Fatalf("want exactly one killed-job warning, got %d: %+v", len(warnings), warnings)
	}
	msg := warnings[0].Data.(events.WarningData).Message
	if !strings.Contains(msg, jobID) || !strings.Contains(msg, "sleep 300") {
		t.Fatalf("killed-job warning must carry the job id and command text for diagnosis, got: %q", msg)
	}
}

// TestOneShotDrainAnnouncementStopResolves: the model answers the first
// announcement by stopping the job. The drain then collects the terminal
// notification as ordinary work — whose reply IS the run's answer (PRI-2441) —
// and returns cleanly with no second announcement and no warning.
func TestOneShotDrainAnnouncementStopResolves(t *testing.T) {
	t.Parallel()
	var jobID string
	adapter := &fakeAdapter{name: "openai"}
	stopRequested := make(chan struct{})
	stopTurnReturned := make(chan struct{})
	allowStopTurn := make(chan struct{})
	var finalWarningSeen atomic.Bool
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID: "stop-it", Name: "job_stop", Type: "function",
				Arguments: json.RawMessage(`{"target":"` + jobID + `"}`),
			})
		},
		func(llm.Request) llm.Response {
			// ProcessInputKind executes the job_stop tool before requesting this
			// follow-up response. Signalling here gives the pending-state assertions
			// a happens-before edge from the actual stop request.
			close(stopRequested)
			<-allowStopTurn
			close(stopTurnReturned)
			return finalResponse("stopped the scratch job")
		},
		func(req llm.Request) llm.Response {
			if strings.Contains(req.Messages[len(req.Messages)-1].Text(), "Final notice:") {
				finalWarningSeen.Store(true)
			}
			return finalResponse("all wrapped up")
		},
		func(llm.Request) llm.Response { return finalResponse("all wrapped up") },
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	se := newDelayedExitStreamingExecutor()
	started := runShell(context.Background(), sess.jobManager, se, shellArgs{
		Command:    "controlled stop",
		Mode:       shellModeBackground,
		Background: true,
	})
	if started.JobID == "" || !started.RunningInBackground {
		t.Fatalf("controlled shell start = %+v, want a live background job", started)
	}
	jobID = started.JobID
	releaseShell := func() {
		select {
		case <-se.release:
		default:
			close(se.release)
		}
	}
	t.Cleanup(func() {
		releaseShell()
		waitForShellDone(t, sess.jobManager, jobID)
	})
	if ids, sole, err := sess.undisposedBackgroundDrainJobs(); err != nil || !sole || len(ids) != 1 || ids[0] != jobID {
		t.Fatalf("controlled shell drain candidate = (%v, %v, %v), want (%v, true, nil)", ids, sole, err, jobID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct {
		res string
		err error
	}, 1)
	go func() {
		res, err := sess.drainJobTreeWith(ctx, feedRechecks(ctx), sess.kickDriveTree, sess.ProcessInputKind)
		done <- struct {
			res string
			err error
		}{res, err}
	}()
	// The fed rechecks advance the first-sighting arm and start the real
	// ProcessInputKind announcement turn without a wall-clock park.
	select {
	case <-done:
		t.Fatal("drain returned before the stop turn")
	case <-stopRequested:
	case <-ctx.Done():
		t.Fatalf("drain did not reach the stop turn (model requests: %d)", len(adapter.Requests()))
	}
	// The tool call has synchronously set stopStatus while the executor remains
	// behind its release barrier. The job is still ordinary outstanding work, but
	// it must no longer be an announcement candidate.
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("outstanding drain jobs while stop is pending = (%d, %v), want (1, nil)", n, err)
	}
	if ids, sole, err := sess.undisposedBackgroundDrainJobs(); err != nil || sole || len(ids) != 0 {
		t.Fatalf("undisposed while stop is pending = (%v, %v, %v), want no announcement candidate", ids, sole, err)
	}
	close(allowStopTurn)
	<-stopTurnReturned
	// Rechecks continue after the stop turn returns, while finalization is still
	// held by the executor. A pending stop must not produce a final warning.
	releaseShell()
	var d struct {
		res string
		err error
	}
	select {
	case d = <-done:
	case <-ctx.Done():
		t.Fatal("drain did not resolve after controlled shell completion")
	}
	if d.err != nil {
		t.Fatalf("drain error: %v", d.err)
	}
	if finalWarningSeen.Load() {
		t.Fatal("drain escalated to a final warning while stopStatus was pending")
	}
	// The stop's cancelled notification is the only post-stop model turn. What
	// must NEVER happen is the announcement turn's reply becoming the answer.
	if d.res != "all wrapped up" && d.res != "" {
		t.Fatalf("drain result = %q: an announcement-turn reply leaked into the run's answer", d.res)
	}
	if warnings := collectStallWarnings(sess); len(warnings) != 0 {
		t.Fatalf("no job was killed, want no warning, got %+v", warnings)
	}
	// Exactly one announcement, however the notification was sequenced.
	announcements := 0
	for _, r := range adapter.Requests() {
		if strings.Contains(r.Messages[len(r.Messages)-1].Text(), "cannot finish") {
			announcements++
		}
	}
	if announcements != 1 {
		t.Fatalf("announcements = %d, want exactly 1: stopping the job must resolve the escalation", announcements)
	}
}

// TestOneShotDrainWatchSuppressesTheAnnouncement: the model answers the first
// announcement by arming a progress watch on the job — the affirmative "this
// terminates and I need its result". The drain must then WAIT, exactly as it
// does today, with no further announcements; and the check must be the watch
// registry itself, not the incidental presence of watch frames in the
// notification queue, which is empty between frames on a long interval.
func TestOneShotDrainWatchSuppressesTheAnnouncement(t *testing.T) {
	t.Parallel()
	var jobID string
	adapter := &fakeAdapter{name: "openai"}
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID: "watch-it", Name: "job_watch", Type: "function",
				Arguments: json.RawMessage(`{"operation":"create","source":"` + jobID + `","progress_interval_ms":120000}`),
			})
		},
		func(llm.Request) llm.Response { return finalResponse("watching; it will finish") },
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID = startUndisposedBackgroundShell(t, sess)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type drainDone struct {
		res string
		err error
	}
	done := make(chan drainDone, 1)
	go func() {
		res, err := sess.drainJobTreeWith(ctx, feedRechecks(ctx), sess.kickDriveTree, sess.ProcessInputKind)
		done <- drainDone{res, err}
	}()

	// The announcement turn arms the watch; after that the drain must park.
	select {
	case d := <-done:
		t.Fatalf("drain returned (%q, %v) while an armed watch said the job terminates; it must wait", d.res, d.err)
	// TRIPWIRE: a positive stayed-parked confirmation, not a completion wait —
	// the window is generous slack for the negative result, not a work budget.
	case <-time.After(300 * time.Millisecond):
	}
	// The single announcement turn is two model requests: the job_watch tool
	// call and the turn's final text. No third request may follow — the armed
	// watch suppresses any further announcement.
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("model requests while watched = %d, want exactly the one announcement turn (tool call + final)", got)
	}

	cancel()
	select {
	case <-done:
	// TRIPWIRE: awaits the drain goroutine's own exit after cancellation; 30s
	// only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not exit after cancellation")
	}
}

// TestOneShotDrainFirstSightingWaitsForAWake pins the announcement's race
// absorber: the FIRST sighting of an undisposed set must fall through to
// waitDrainWake rather than announce, so the ordinary handoff — a background
// job finishing exactly as the drain starts — has its completion delivered
// instead of being nagged about. With no ticks and no wakes, the drain
// therefore parks with zero model calls; the announcement needs the condition
// to survive one park.
func TestOneShotDrainFirstSightingWaitsForAWake(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai"}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	startUndisposedBackgroundShell(t, sess)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// A never-ticking recheck: the only way out of the first park is a wake.
		_, _ = sess.drainJobTreeWith(ctx, make(chan time.Time), sess.kickDriveTree, sess.ProcessInputKind)
		close(done)
	}()
	select {
	case <-done:
		cancel()
		t.Fatal("drain returned on its first sighting; the announcement must wait out one park")
	// TRIPWIRE: a positive stayed-parked confirmation, not a completion wait.
	case <-time.After(150 * time.Millisecond):
	}
	if got := len(adapter.Requests()); got != 0 {
		cancel()
		t.Fatalf("first sighting made %d model calls, want 0: the completion in flight must win the race", got)
	}
	cancel()
	select {
	case <-done:
	// TRIPWIRE: awaits the drain goroutine's own exit after cancellation; 30s
	// only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not exit after cancellation")
	}
}

// TestServeDrainNeverAnnounces: without TurnEndsProcess the session outlives
// the turn, background jobs genuinely report later, and today's behaviour must
// stay byte-identical — the drain waits, with zero model calls.
func TestServeDrainNeverAnnounces(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai"}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
	}))
	startUndisposedBackgroundShell(t, sess)

	stop := assertDrainParks(t, sess, "serve session with a live background job")
	defer stop()
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("serve drain made %d model calls, want 0", got)
	}
}

// TestOneShotDrainWaitsForForegroundShell: a foreground shell still inside its
// block window is ordinary bounded work — not an undisposed background job —
// and must keep today's waiting behaviour even one-shot.
func TestOneShotDrainWaitsForForegroundShell(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai"}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	se := newDelayedSuccessStreamingExecutor()
	releaseShell := func() {
		select {
		case <-se.release:
		default:
			close(se.release)
		}
	}
	t.Cleanup(releaseShell)
	resCh := make(chan shellResult, 1)
	go func() {
		resCh <- runShell(context.Background(), sess.jobManager, se, shellArgs{
			Command:        "delayed foreground success",
			BlockTimeoutMS: 60000,
		})
	}()
	// TRIPWIRE: polls for the in-process executor's registration, which happens
	// in microseconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "foreground shell to appear in the running map", func() bool {
		return sess.jobManager.hasRunningDrainJob()
	})

	stop := assertDrainParks(t, sess, "one-shot session with a live FOREGROUND shell")
	stop()
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("foreground shell drew %d announcement calls, want 0", got)
	}
	releaseShell()
	<-resCh
}

// TestUndisposedJobsYieldToOtherOutstandingWork pins the predicate's honesty: a
// background job alongside ANY other outstanding drain work is not "the sole
// remaining reason to wait", so nothing fires.
func TestUndisposedJobsYieldToOtherOutstandingWork(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	startUndisposedBackgroundShell(t, sess)
	seedOwnedDurablePending(t, sess.jobManager, "other-owed-work", jobstore.JobShell)

	ids, ok, err := sess.undisposedBackgroundDrainJobs()
	if err != nil {
		t.Fatalf("undisposedBackgroundDrainJobs: %v", err)
	}
	if ok || len(ids) != 0 {
		t.Fatalf("undisposed = (%v, %v) with a pending notification also owed; the background job must not be the announced reason", ids, ok)
	}
}

// TestUndisposedJobsYieldToQueuedNotifications pins the ELSEWHERE half of the
// predicate: outstanding work that is not a job of this session's — here, a
// queued notification — is a real reason to keep waiting, distinct from the
// all-jobs-background condition the sibling test covers.
func TestUndisposedJobsYieldToQueuedNotifications(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	startUndisposedBackgroundShell(t, sess)

	sess.pendingJobNotifsMu.Lock()
	sess.pendingJobNotifs = append(sess.pendingJobNotifs, jobNotification{JobID: "elsewhere-job", Status: "completed"})
	sess.pendingJobNotifsMu.Unlock()

	ids, ok, err := sess.undisposedBackgroundDrainJobs()
	if err != nil {
		t.Fatalf("undisposedBackgroundDrainJobs: %v", err)
	}
	if ok || len(ids) != 0 {
		t.Fatalf("undisposed = (%v, %v) with a queued notification deliverable; the drain has real work before any announcement", ids, ok)
	}
}

// TestUndisposedJobsSuppressesStopPendingAnnouncement keeps the two drain
// predicates distinct: a stop request does not make the job quiescent, but it
// does mean the model has already disposed of it and must not be nagged again.
func TestUndisposedJobsSuppressesStopPendingAnnouncement(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID := startUndisposedBackgroundShell(t, sess)
	// startUndisposedBackgroundShell's cleanup must be allowed to signal the
	// real shell after this test's synthetic stop-pending state is removed.
	t.Cleanup(func() {
		sess.jobManager.mu.Lock()
		if run := sess.jobManager.running[jobID]; run != nil {
			run.stopStatus = ""
			run.stopReason = ""
		}
		sess.jobManager.mu.Unlock()
	})
	sess.jobManager.mu.Lock()
	run := sess.jobManager.running[jobID]
	if run == nil {
		sess.jobManager.mu.Unlock()
		t.Fatalf("running job %s disappeared before stop-pending setup", jobID)
	}
	run.stopStatus = jobstore.StatusCancelled
	run.stopReason = "stopped_by_parent"
	sess.jobManager.mu.Unlock()

	n, err := sess.jobManager.outstandingDrainJobCount()
	if err != nil || n != 1 {
		t.Fatalf("ordinary outstanding work = (%d, %v), want (1, nil)", n, err)
	}
	ids, sole, err := sess.undisposedBackgroundDrainJobs()
	if err != nil {
		t.Fatalf("undisposedBackgroundDrainJobs: %v", err)
	}
	if sole || len(ids) != 0 {
		t.Fatalf("stop-pending announcement candidates = (%v, %v), want ([], false)", ids, sole)
	}
}

// TestClearedWatchResumesEscalation pins the resume path: a watch that is
// cleared (the delivery-budget auto-clear, or the model clearing it) stops
// excusing the job, so the predicate reports it undisposed again.
func TestClearedWatchResumesEscalation(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID := startUndisposedBackgroundShell(t, sess)

	watchRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "watch-it", Name: "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"` + jobID + `","progress_interval_ms":120000}`),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch create: %s", watchRes.Output)
	}
	if ids, ok, err := sess.undisposedBackgroundDrainJobs(); err != nil || ok || len(ids) != 0 {
		t.Fatalf("undisposed = (%v, %v, %v) with an armed watch, want none", ids, ok, err)
	}

	var created struct {
		WatchID string `json:"watch_id"`
	}
	if err := json.Unmarshal(toolResultJSON(watchRes), &created); err != nil || created.WatchID == "" {
		t.Fatalf("unmarshal watch result: %v (output: %s)", err, watchRes.Output)
	}
	clearRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "clear-it", Name: "job_watch",
		Arguments: json.RawMessage(`{"operation":"clear","watch_id":"` + created.WatchID + `"}`),
	})
	if clearRes.IsError {
		t.Fatalf("job_watch clear: %s", clearRes.Output)
	}
	ids, ok, err := sess.undisposedBackgroundDrainJobs()
	if err != nil || !ok || len(ids) != 1 || ids[0] != jobID {
		t.Fatalf("undisposed after clear = (%v, %v, %v), want the job reported again", ids, ok, err)
	}
}

// TestClearedWatchStartsAFreshAnnouncementEpisode drives the real budget
// auto-clear transition. The first announcement arms a watch, the next matched
// event crosses watchDeliveryBudget and clears it, and the cleared notification
// is then drained before the same job set is considered again. That new episode
// must start with the first announcement, not jump straight to Final notice.
func TestClearedWatchStartsAFreshAnnouncementEpisode(t *testing.T) {
	t.Parallel()
	var jobID string
	watchArmed := make(chan struct{})
	var clearedNoticeSeen atomic.Bool
	adapter := &fakeAdapter{name: "openai"}
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID: "watch-it", Name: "job_watch", Type: "function",
				Arguments: json.RawMessage(`{"operation":"create","source":"` + jobID + `","events":["job.notification"]}`),
			})
		},
		func(llm.Request) llm.Response {
			close(watchArmed)
			return finalResponse("watching; it will finish")
		},
		func(req llm.Request) llm.Response {
			if strings.Contains(req.Messages[len(req.Messages)-1].Text(), "watch cleared") {
				clearedNoticeSeen.Store(true)
			}
			return finalResponse("the watch was cleared; reconsidering")
		},
		func(llm.Request) llm.Response { return finalResponse("still waiting for the job") },
		func(llm.Request) llm.Response { return finalResponse("exiting") },
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID = startUndisposedBackgroundShell(t, sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type drainDone struct {
		res string
		err error
	}
	done := make(chan drainDone, 1)
	go func() {
		res, err := sess.drainJobTreeWith(ctx, feedRechecks(ctx), sess.kickDriveTree, sess.ProcessInputKind)
		done <- drainDone{res, err}
	}()
	select {
	case <-watchArmed:
	case d := <-done:
		t.Fatalf("drain returned before the live watch was armed: (%q, %v)", d.res, d.err)
	case <-ctx.Done():
		t.Fatal("drain did not arm the live watch")
	}

	sess.jobManager.mu.Lock()
	var watched *watchConfig
	for _, cfg := range sess.jobManager.watches {
		if cfg != nil && cfg.target == jobID {
			watched = cfg
			break
		}
	}
	if watched == nil {
		sess.jobManager.mu.Unlock()
		t.Fatalf("live watch for %s disappeared before budget crossing", jobID)
	}
	watched.deliveries = watchDeliveryBudget - 1
	sess.jobManager.mu.Unlock()

	// This is a real matching session event, and the next delivery crosses the
	// actual budget auto-clear path rather than merely deleting jm.watches.
	onSessionEventKD(sess.jobManager, events.EventJobFinished, events.JobFinishedData{
		JobID: jobID, JobType: "shell", Status: "completed",
	})
	sess.jobManager.mu.Lock()
	stillLive := false
	for _, cfg := range sess.jobManager.watches {
		if cfg == watched {
			stillLive = true
			break
		}
	}
	sess.jobManager.mu.Unlock()
	if stillLive {
		t.Fatal("budget-crossing delivery left the watch live")
	}

	var d drainDone
	select {
	case d = <-done:
	case <-ctx.Done():
		t.Fatal("drain did not complete after the watch auto-clear")
	}
	if d.err != nil {
		t.Fatalf("drain error: %v", d.err)
	}
	if !clearedNoticeSeen.Load() {
		t.Fatal("drain did not process the watch-cleared notification")
	}
	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("model requests = %d, want 5 (initial watch turn, cleared notification, fresh announcement, final warning)", len(reqs))
	}
	if strings.Contains(reqs[3].Messages[len(reqs[3].Messages)-1].Text(), "Final notice:") ||
		!strings.Contains(reqs[3].Messages[len(reqs[3].Messages)-1].Text(), "cannot finish") {
		t.Fatalf("fresh episode request = %q, want the first announcement rather than the final warning", reqs[3].Messages[len(reqs[3].Messages)-1].Text())
	}
}

// TestUndisposedBackgroundJobsFinalWarningUsesAvailableRemedy keeps the final
// warning truthful in a sandbox that cannot detach processes. The warning must
// direct the model to stop and report, never promise an unavailable detached
// mode. The companion announcement assertions pin the available-host wording.
func TestUndisposedBackgroundJobsFinalWarningUsesAvailableRemedy(t *testing.T) {
	t.Parallel()
	final := undisposedBackgroundJobsFinalWarning([]string{"job_sandbox"}, false)
	if strings.Contains(final, "detach them") {
		t.Fatalf("sandbox final warning advertises detached mode: %q", final)
	}
	if !strings.Contains(final, "stop") || !strings.Contains(final, "report") {
		t.Fatalf("sandbox final warning must describe stop-and-report: %q", final)
	}
	finalAvailable := undisposedBackgroundJobsFinalWarning([]string{"job_host"}, true)
	if !strings.Contains(finalAvailable, "detach") || !strings.Contains(finalAvailable, "job_stop") {
		t.Fatalf("detached-capable final warning omitted stop-then-detach remedy: %q", finalAvailable)
	}
	available := undisposedBackgroundJobsAnnouncement([]string{"job_host"}, "shell", true)
	if !strings.Contains(available, `mode="detached"`) {
		t.Fatalf("detached-capable warning omitted detached remedy: %q", available)
	}
	sandbox := undisposedBackgroundJobsAnnouncement([]string{"job_sandbox"}, "shell", false)
	if strings.Contains(sandbox, `mode="detached"`) || !strings.Contains(sandbox, "Stop the job and say so plainly") {
		t.Fatalf("sandbox announcement advertised detached mode or omitted stop-and-report: %q", sandbox)
	}
}

// TestOneShotDrainAnnouncementErrorDoesNotFailTheRun: a provider error on a
// housekeeping turn must not convert a successful run into a failed one. The
// escalation proceeds as if the turn was declined, and the drain still returns
// nil so run.go prints the answer it already has.
//
// The turn runner here is a stub that always errors: this test pins the
// drain's ERROR-HANDLING contract, not announcement delivery, which every
// other test in this file drives through the real ProcessInputKind.
func TestOneShotDrainAnnouncementErrorDoesNotFailTheRun(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	startUndisposedBackgroundShell(t, sess)

	calls := 0
	failing := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		calls++
		return "", context.DeadlineExceeded
	}
	// TRIPWIRE: turn-paced escalation over fed rechecks; 30s only fires on a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.drainJobTreeWith(ctx, feedRechecks(ctx), sess.kickDriveTree, failing)
	if err != nil {
		t.Fatalf("drain error = %v; an announcement-turn failure must not fail the run", err)
	}
	if res != "" {
		t.Fatalf("drain result = %q, want empty", res)
	}
	if calls != 2 {
		t.Fatalf("announcement attempts = %d, want 2 (each failed turn still advances the escalation)", calls)
	}
	warnings := collectStallWarnings(sess)
	if len(warnings) != 3 {
		t.Fatalf("want 2 failed-announcement warnings plus the killed-job warning, got %d: %+v", len(warnings), warnings)
	}
	if last := warnings[2].Data.(events.WarningData).Message; !strings.Contains(last, "sleep 300") {
		t.Fatalf("final warning must still report the killed job, got %q", last)
	}
}

// TestOneShotDrainSetChangeRestartsEscalation: starting a NEW background job
// during an announcement turn changes the undisposed set, and the new set gets
// its own full escalation — announce, warn, give up — rather than inheriting a
// spent count from the old one.
func TestOneShotDrainSetChangeRestartsEscalation(t *testing.T) {
	t.Parallel()
	var secondJob string
	adapter := &fakeAdapter{name: "openai"}
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID: "start-another", Name: "shell", Type: "function",
				Arguments: json.RawMessage(`{"command":"sleep 301","mode":"background"}`),
			})
		},
		func(llm.Request) llm.Response { return finalResponse("started another one") },
		func(llm.Request) llm.Response { return finalResponse("ignoring the fresh announcement") },
		func(llm.Request) llm.Response { return finalResponse("ignoring the final warning") },
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	firstJob := startUndisposedBackgroundShell(t, sess)
	t.Cleanup(func() {
		if secondJob != "" {
			_, _ = sess.jobManager.stop(secondJob)
			waitForShellDone(t, sess.jobManager, secondJob)
		}
	})

	res, err := runDrainToCompletion(t, sess)
	sess.jobManager.mu.Lock()
	for id := range sess.jobManager.running {
		if id != firstJob {
			secondJob = id
		}
	}
	sess.jobManager.mu.Unlock()
	if err != nil {
		t.Fatalf("drain error: %v", err)
	}
	if res != "" {
		t.Fatalf("drain result = %q, want empty", res)
	}
	reqs := adapter.Requests()
	if len(reqs) != 4 {
		t.Fatalf("model requests = %d, want 4: announce {A} (tool call + final), fresh announce {A,B}, final warning", len(reqs))
	}
	warnings := collectStallWarnings(sess)
	if len(warnings) != 1 {
		t.Fatalf("want one killed-jobs warning, got %+v", warnings)
	}
	msg := warnings[0].Data.(events.WarningData).Message
	if !strings.Contains(msg, firstJob) || !strings.Contains(msg, "sleep 301") {
		t.Fatalf("warning must name both undisposed jobs, got: %q", msg)
	}
}
