package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// reminderRig is a session on a fake clock whose wake func counts kicks, so a
// test can tell an idle session was woken without running its serve loop.
type reminderRig struct {
	sess  *Session
	clock *agenttest.FakeClock
	woken chan struct{}
}

func newReminderRig(t *testing.T, opts ...sessionOpt) *reminderRig {
	t.Helper()
	r := &reminderRig{clock: agenttest.NewFakeClock(), woken: make(chan struct{}, 16)}
	r.sess = newSession(t, append([]sessionOpt{withClock(r.clock)}, opts...)...)
	r.sess.jobManager.closeGrace = 5 * time.Second
	r.sess.jobManager.reminderClock = r.clock
	r.sess.SetNotifyFunc(func() { r.woken <- struct{}{} })
	return r
}

func (r *reminderRig) startShell(t *testing.T) string {
	t.Helper()
	res := r.sess.reg.ExecuteCall(context.Background(), r.sess.env, llm.ToolCallData{
		ID: "start", Name: "shell",
		Arguments: idleShellJSON(`,"mode":"background"`),
	})
	if res.IsError {
		t.Fatalf("shell: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil || out.JobID == "" {
		t.Fatalf("shell result has no job_id: %v (%s)", err, res.Output)
	}
	return out.JobID
}

// advance moves the fake clock and waits for the reminder callback it fired.
func (r *reminderRig) advance(d time.Duration) {
	r.clock.Advance(d)
	r.clock.Drain()
}

// reminders drains the session's pending notifications and returns the
// long-running-job reminders among them: watch-kind notifications that name no
// job. Everything else (job completions) is discarded.
func (r *reminderRig) reminders() []jobNotification {
	var out []jobNotification
	for _, n := range r.sess.drainJobNotifications() {
		if n.isWatch() && n.JobID == "" {
			out = append(out, n)
		}
	}
	return out
}

// listed reports whether the reminder's rendered block names jobID.
func listed(n jobNotification, jobID string) bool {
	return strings.Contains(formatJobNotificationBlock(n, notificationExcerpt{}, false), jobID)
}

func TestIdleSessionIsWokenWithLongRunningJobReminder(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	jobID := r.startShell(t)

	r.advance(longRunningJobReminderInterval - time.Minute)
	if got := len(r.woken); got != 0 {
		t.Fatalf("woken %d times before the job was %v old", got, longRunningJobReminderInterval)
	}

	for tick := 1; tick <= 2; tick++ {
		r.advance(longRunningJobReminderInterval)
		if got := len(r.woken); got != 1 {
			t.Fatalf("tick %d: wakes = %d, want 1", tick, got)
		}
		<-r.woken
		reminders := r.reminders()
		if len(reminders) != 1 || !listed(reminders[0], jobID) {
			t.Fatalf("tick %d: reminders = %+v, want one naming %s", tick, reminders, jobID)
		}
	}
}

func TestLongRunningJobReminderIsOnePerSession(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	ids := []string{r.startShell(t), r.startShell(t), r.startShell(t)}

	r.advance(longRunningJobReminderInterval)

	if got := len(r.woken); got != 1 {
		t.Fatalf("wakes = %d, want 1", got)
	}
	reminders := r.reminders()
	if len(reminders) != 1 {
		t.Fatalf("reminders = %d, want 1", len(reminders))
	}
	for _, id := range ids {
		if !listed(reminders[0], id) {
			t.Errorf("reminder does not list %s", id)
		}
	}
}

func TestActiveTurnReceivesLongRunningJobReminder(t *testing.T) {
	t.Parallel()
	var r *reminderRig
	age := llm.ToolCallData{ID: "age", Name: "age_jobs", Arguments: []byte(`{}`), Type: "function"}
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.ToolCallResponse(age) }, func(llm.Request) llm.Response { return agenttest.FinalResponse("noted") }, func(llm.Request) llm.Response { return agenttest.FinalResponse("unexpected extra turn") })
	r = newReminderRig(t, withAdapter(adapter))
	jobID := r.startShell(t)
	r.sess.registerTool("age_jobs", "test-only: let the running job pass the reminder age",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			r.advance(longRunningJobReminderInterval)
			return "aged", nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter; fires only on a genuine hang.
	defer cancel()
	if _, err := r.sess.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2 (the tool round, then one carrying the reminder)", len(requests))
	}
	if !requestsContain(requests[1:], "<job-notification", jobID) {
		t.Fatal("the request after the tool round does not carry the reminder naming the job")
	}
	if pending := r.sess.peekNotifications(); pending != 0 {
		t.Fatalf("pending job notifications after the turn = %d, want 0: the reminder must be consumed in-turn", pending)
	}
}

func TestNoReminderAfterLongJobsFinish(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	first, second := r.startShell(t), r.startShell(t)

	r.advance(longRunningJobReminderInterval)
	if reminders := r.reminders(); len(reminders) != 1 || !listed(reminders[0], first) || !listed(reminders[0], second) {
		t.Fatalf("first tick reminders = %+v, want one naming both jobs", reminders)
	}

	if _, err := r.sess.jobManager.stop(first); err != nil {
		t.Fatalf("stop %s: %v", first, err)
	}
	waitForShellDone(t, r.sess.jobManager, first)
	r.advance(longRunningJobReminderInterval)
	reminders := r.reminders()
	if len(reminders) != 1 || listed(reminders[0], first) || !listed(reminders[0], second) {
		t.Fatalf("second tick reminders = %+v, want one naming only %s", reminders, second)
	}

	if _, err := r.sess.jobManager.stop(second); err != nil {
		t.Fatalf("stop %s: %v", second, err)
	}
	waitForShellDone(t, r.sess.jobManager, second)
	r.sess.drainJobNotifications()
	for len(r.woken) > 0 {
		<-r.woken
	}
	r.advance(longRunningJobReminderInterval)
	r.advance(longRunningJobReminderInterval)
	if got := r.reminders(); len(got) != 0 {
		t.Fatalf("reminders after every job finished = %+v, want none", got)
	}
	if got := len(r.woken); got != 0 {
		t.Fatalf("wakes after every job finished = %d, want 0", got)
	}
}

func TestPendingLongRunningJobReminderIsReplacedNotQueued(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	first, second := r.startShell(t), r.startShell(t)

	// Nothing drains the queue, as when an open question parks the session.
	r.advance(longRunningJobReminderInterval)
	if _, err := r.sess.jobManager.stop(first); err != nil {
		t.Fatalf("stop %s: %v", first, err)
	}
	waitForShellDone(t, r.sess.jobManager, first)
	r.advance(longRunningJobReminderInterval)
	r.advance(longRunningJobReminderInterval)

	reminders := r.reminders()
	if len(reminders) != 1 {
		t.Fatalf("queued reminders = %d, want 1", len(reminders))
	}
	if listed(reminders[0], first) || !listed(reminders[0], second) {
		t.Fatalf("the surviving reminder must list only %s", second)
	}
}

func TestLongRunningJobReminderListsShellJobsOnly(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	shell := r.startShell(t)
	jm := r.sess.jobManager
	const delegateID = "job_delegate_not_listed"
	jm.mu.Lock()
	jm.running[delegateID] = &runningJob{rec: &jobstore.JobRecord{JobID: delegateID, Type: jobstore.JobType(delegateResourceType), StartedAt: jm.now()}}
	jm.mu.Unlock()
	defer func() {
		jm.mu.Lock()
		delete(jm.running, delegateID)
		jm.mu.Unlock()
	}()

	r.advance(longRunningJobReminderInterval)

	reminders := r.reminders()
	if len(reminders) != 1 || !listed(reminders[0], shell) || listed(reminders[0], delegateID) {
		t.Fatalf("reminders = %+v, want one naming only the shell job", reminders)
	}
}

func TestNoLongRunningJobReminderAfterClose(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	r.startShell(t)

	r.sess.Close()
	for len(r.woken) > 0 {
		<-r.woken
	}
	r.sess.drainJobNotifications()
	r.advance(longRunningJobReminderInterval)

	if got := r.reminders(); len(got) != 0 {
		t.Fatalf("reminders after close = %+v, want none", got)
	}
	if got := len(r.woken); got != 0 {
		t.Fatalf("wakes after close = %d, want 0", got)
	}
}

func TestForegroundShellThatCompletesNeverArmsTheReminder(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	before := r.clock.PendingCallbacks()

	res := r.sess.reg.ExecuteCall(context.Background(), r.sess.env, llm.ToolCallData{
		ID: "fg", Name: "shell",
		Arguments: json.RawMessage(`{"command":"true"}`),
	})
	if res.IsError {
		t.Fatalf("shell: %s", res.Output)
	}

	if got := r.clock.PendingCallbacks(); got != before {
		t.Fatalf("armed clock callbacks after a completed foreground shell = %d, want %d", got, before)
	}
}

// A watch notice whose durable attention append failed is an obligation until
// the retry lands.
//
// The retry lives in a clock closure, so a nonterminal idle-runtime release
// that runs before it has nothing to observe: the notice has no queue entry, no
// pending watch send and no timer. Releasing the runtime then loses the only
// firing a one-shot will ever have, and the next runtime finds neither a timer
// nor an attention row (#2340 review M5).
//
// The oracle is the release precheck itself, read while the obligation is owed
// and again after the retry discharges it. That is the wait the finding is
// about: the counter must hold the release BEFORE the append succeeds, not
// merely take a value afterwards.
func TestPendingAttentionAppendHoldsIdleRuntimeReleaseUntilItLands(t *testing.T) {
	t.Parallel()
	// A BOUND stable owner, so the retry below takes the production persist
	// path. Without one, stableWatchOwner returns nil and the retry releases
	// the obligation without ever appending, which made this test's green arm
	// prove only that the counter decremented on the branch where nothing was
	// owed an append (#2340 review).
	sess, _ := newCompactableSession(t)
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_reminder", "")
	sess.mu.Lock()
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_reminder"
	sess.mu.Unlock()
	controller.mu.Lock()
	controller.live["dlg_reminder"].binding.runtime = sess
	controller.mu.Unlock()
	sess.jobManager.bindDelegateParent(controller, delegateLease{delegateID: "dlg_reminder", generation: 1}, nil, sess)
	clock := agenttest.NewFakeClock()
	sess.clock = clock
	rig := &reminderRig{sess: sess, clock: clock, woken: make(chan struct{}, 16)}

	// The fixture must take the OWNABLE branch, or the whole test is vacuous.
	if owner, _ := sess.jobManager.stableWatchOwner(); owner == nil || owner.owningDelegateID == "" {
		t.Fatal("setup: the retry fixture has no bound stable owner, so the append could never run")
	}

	// Non-vacuity: with nothing owed the precheck is clear, so a false below is
	// this obligation and not some other residue of the fixture.
	if !sess.idleReleasePregatesClear() {
		t.Fatal("setup: the precheck is not clear before anything is owed")
	}
	if sess.hasPendingAttentionAppend() {
		t.Fatal("setup: an append is owed before the fixture owes one")
	}

	// One notice that cannot be persisted: detaching the transcript writer is
	// the same refusal a failed attention append takes, without a fake writer.
	sess.mu.Lock()
	writer := sess.transcript
	sess.transcript = nil
	sess.mu.Unlock()
	notif := watchSendDiagnosticNotification("watch:m5", "job:m5", "watch send failed: delivery_id=m5")
	notif.Occurrence = "diagnostic:m5"
	sess.scheduleStableWatchAttentionRetry([]jobNotification{notif})

	// The obligation exists even though the append has not been retried.
	if !sess.hasPendingAttentionAppend() {
		t.Fatal("a failed attention append left no obligation: a runtime release could outrun the retry")
	}
	if sess.idleReleasePregatesClear() {
		t.Fatal("the idle-runtime release precheck cleared while a notice was still owed its durable append")
	}

	// Restore the writer and let the retry land. The obligation discharges, and
	// only then may a release proceed.
	sess.mu.Lock()
	sess.transcript = writer
	sess.mu.Unlock()
	rig.advance(jobNotificationRetryInitialDelay)
	if sess.hasPendingAttentionAppend() {
		t.Fatal("the retry landed the append but the obligation was never discharged: the release would wait forever")
	}
	if !sess.idleReleasePregatesClear() {
		t.Fatal("the precheck still refuses after the owed append landed")
	}
	// And the append actually LANDED, through the production persist path: the
	// row is durable in the transcript under the notice's own identity. Without
	// this assertion the green arm would pass on the no-owner early-out that
	// discharges the counter without writing anything.
	if _, ok := sess.pendingStableWatchNotice(stableWatchDiagnosticAttentionID(notif)); !ok {
		t.Fatal("the retry discharged the obligation without making the notice durable: the release was bought with nothing")
	}
}
