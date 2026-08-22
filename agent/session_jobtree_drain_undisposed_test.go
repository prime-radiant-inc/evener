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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		res string
		err error
	}, 1)
	finished := make(chan struct{})
	releaseStopTurn := func() {
		select {
		case <-allowStopTurn:
		default:
			close(allowStopTurn)
		}
	}
	t.Cleanup(func() {
		cancel()
		releaseStopTurn()
		releaseShell()
		<-finished
	})
	go func() {
		defer close(finished)
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
	releaseStopTurn()
	<-stopTurnReturned
	// Rechecks continue after the stop turn returns, while finalization is still
	// held by the executor. A pending stop must not produce a final warning.
	releaseShell()
	d := <-done
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
	shellFinished := make(chan struct{})
	go func() {
		defer close(shellFinished)
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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	drainStarted := make(chan struct{})
	kick := func(ctx context.Context) error {
		select {
		case <-drainStarted:
		default:
			close(drainStarted)
		}
		return sess.kickDriveTree(ctx)
	}
	t.Cleanup(func() {
		cancel()
		releaseShell()
		<-done
		<-shellFinished
	})
	go func() {
		_, _ = sess.drainJobTreeWith(ctx, feedRechecks(ctx), kick, sess.ProcessInputKind)
		close(done)
	}()
	<-drainStarted
	releaseShell()
	<-resCh
	cancel()
	<-done
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("foreground shell drew %d announcement calls, want 0", got)
	}
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

	ctx, cancel := context.WithCancel(context.Background())
	type drainDone struct {
		res string
		err error
	}
	done := make(chan drainDone, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	go func() {
		defer close(finished)
		res, err := sess.drainJobTreeWith(ctx, feedRechecks(ctx), sess.kickDriveTree, sess.ProcessInputKind)
		done <- drainDone{res, err}
	}()
	select {
	case <-watchArmed:
	case d := <-done:
		t.Fatalf("drain returned before the live watch was armed: (%q, %v)", d.res, d.err)
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

	d := <-done
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
