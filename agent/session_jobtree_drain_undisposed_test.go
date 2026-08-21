package agent

import (
	"context"
	"encoding/json"
	"strings"
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
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID: "stop-it", Name: "job_stop", Type: "function",
				Arguments: json.RawMessage(`{"target":"` + jobID + `"}`),
			})
		},
		func(llm.Request) llm.Response { return finalResponse("stopped the scratch job") },
		func(llm.Request) llm.Response { return finalResponse("all wrapped up") },
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID = startUndisposedBackgroundShell(t, sess)

	res, err := runDrainToCompletion(t, sess)
	if err != nil {
		t.Fatalf("drain error: %v", err)
	}
	// The stop's cancelled-notification is delivered either as its own turn
	// (whose reply folds into the result, PRI-2441) or inside the announcement
	// turn's post-tool boundary (discarded with the rest of that housekeeping
	// turn) — real concurrency, both correct. What must NEVER happen is the
	// announcement turn's reply becoming the answer.
	if res != "all wrapped up" && res != "" {
		t.Fatalf("drain result = %q: an announcement-turn reply leaked into the run's answer", res)
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
