package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// TestDriveDownDeafCoordinator is the spec §9 headline regression: a
// deaf-coordinator drive-down test.
//
// A root session backgrounds a coordinator delegate (depth=1). In the full
// drive-down scenario the coordinator would background two worker delegates,
// the workers would finish, and the PARENT would detect undelivered notifications
// on the coordinator and DRIVE it — submit an EntryNotification turn to the
// coordinator's own drain loop so the coordinator's model receives the workers'
// completion notifications.
//
// TODAY the depth>0 gate (subagents.go:296) also prevents a depth-1 session from
// calling createDelegate, so we use option (a): inject two completed worker
// notifications directly into the coordinator's pendingJobNotifs via
// enqueueCompletedDelegateNotification. This places the coordinator in exactly
// the state it would be in if workers had finished — two queued notifications
// awaiting a coordinator notification turn.
//
// The missing machinery is drive-down (spec §3): after the coordinator ends its
// own turn and becomes idle, the parent should detect undelivered notifications
// and submit an EntryNotification turn to the coordinator's drain loop. TODAY
// nobody does this: idle children drain nothing (dossier §4 "idle children drain
// nothing"; child jm wake = no-op unless SetNotifyFunc, which only serve.go
// wires for the root). The coordinator's queued worker-completions sit
// undelivered; the coordinator's model never gets a notification turn for them.
//
// This test asserts the DESIRED post-Task-14 behaviour; it is RED against today's
// code because that behaviour is absent.
//
// Assertion 1 — the parent drives the coordinator:
//
//	After workers' notifications are pending on the coordinator, the parent must
//	detect this and drive the coordinator's drain loop. Evidence: the coordinator's
//	model adapter receives a second request (the notification turn). Today: only
//	one request fires (the coordinator's initial turn). The assertion fails.
//
// Assertion 2 — the root's notification queue carries only the coordinator's
// terminal, never a worker's terminal:
//
//	Worker job IDs must not appear in root's per-session pendingJobNotifs
//	queue. Workers' terminals are enqueued on the coordinator's queue (via
//	enqueueCompletedDelegateNotification in this test; via finalizeDelegate in
//	production). Root's queue receives only the coordinator's own job
//	completion (via finalizeDelegate → root.enqueueJobNotification when the
//	coordinator job finishes). This is observable today: pendingJobNotifs is a
//	per-session queue, independent of the shared adapter, populated upstream of
//	any driving. If drive-down ever forwarded worker terminals onto root's queue
//	(violating spec §3), this assertion would catch it.
//
// Construction: option (a) — worker state injected after coordinator ends its
// turn (simulating workers that finish while coordinator is idle). The red is
// squarely about the drive mechanism being absent: nothing drives the coordinator
// to drain those notifications.
//
// RED until Task 14 — drive-down; tracks spec §9 headline.
func TestDriveDownDeafCoordinator(t *testing.T) {
	// Single client/adapter shared by root and coordinator. The coordinator session
	// inherits root's LLM client via prepareSubagentRun → NewSession(s.client, ...).
	//
	// Steps:
	//   0 — coordinator's initial model turn: returns communicate("coordinator done").
	//   1 — coordinator's notification turn (drive-down): returns communicate("ack workers").
	//       Today step 1 never fires; Task 14 will make it fire.
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Step 0: coordinator's initial turn. Ends cleanly via communicate.
			func(_ llm.Request) llm.Response {
				return finalResponse("coordinator done")
			},
			// Step 1: coordinator's notification turn (drive-down, Task 14).
			// Today this step never runs. Post-Task-14 it receives the <job-notification>
			// reminder for worker-alpha and worker-beta and must ack them.
			func(_ llm.Request) llm.Response {
				return finalResponse("ack workers")
			},
		},
	}
	c.Register(adapter)

	root, err := NewSession(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 3,
			NoProjectPrompts: true,
		},
	)
	if err != nil {
		t.Fatalf("NewSession (root): %v", err)
	}
	t.Cleanup(func() { root.Close() })

	// Background a coordinator delegate from root. The coordinator's session runs
	// in a goroutine: subagent.run → sess.ProcessInput → adapter step 0.
	delegate := root.createDelegate(context.Background(), delegateArgs{
		Task:       "coordinate workers",
		Background: true,
	})
	if delegate.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", delegate.Err)
	}

	_, coordID, err := decodeRef(delegate.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(coordinator): %v", err)
	}
	coordSub := root.subagents.get(coordID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent not found: %q", coordID)
	}
	coordSess := coordSub.sess

	// Wait for the coordinator's delegate job to finish on root's jobManager.
	// This confirms the coordinator has ended its own turn and is now idle.
	waitForShellDone(t, root.jobManager, delegate.JobID)

	// Inject two completed worker delegate notifications into the coordinator's
	// session. This simulates the state that would exist if the coordinator had
	// backgrounded two workers that subsequently finished — their completions queue
	// notifications on the coordinator's pendingJobNotifs awaiting a notification turn.
	//
	// The coordinator is now IDLE with two undelivered worker notifications.
	// Drive-down machinery (Task 14) detects this and submits an
	// EntryNotification turn to the coordinator's drain loop.
	enqueueCompletedDelegateNotification(t, coordSess, "worker-alpha")
	enqueueCompletedDelegateNotification(t, coordSess, "worker-beta")

	// Model the worker-finish WAKE EDGE that production produces. When a real
	// worker delegate finishes, the coordinator's jobManager.enqueue (=
	// enqueueJobNotificationAndNotify, session_init.go) fires coordSess.notify().
	// Drive-down (Task 14) wires the coordinator's notify to its parent (root),
	// which then drives the coordinator's own drain loop — the agent-internal
	// analog of serve.go's SetNotifyFunc on the root. The enqueueCompletedDelegate-
	// Notification helper arms the durable record + queue but uses the non-notify
	// enqueue, so this call supplies the wake edge the helper omits. (Without a
	// notify, an idle root never reaches a loop boundary and cannot read the drive
	// signal — there is no autonomous poll; spec §3 defers continuous driving.)
	coordSess.notify()

	// Allow time for the async drive turn to run: notify → parent drives the
	// coordinator → coordinator's EntryNotification turn drains both worker
	// notifications and makes its model request within this window.
	time.Sleep(100 * time.Millisecond)

	// === ASSERTION 1 — parent drives the coordinator (RED today) ===
	//
	// The coordinator's model adapter must have received at least 2 requests:
	//   request 0 — initial turn (always fires)
	//   request 1 — notification turn for worker-alpha and worker-beta (drive-down)
	//
	// Today only request 0 fires. The drive mechanism does not exist. FAILS.
	// After Task 14: the parent detects pending coordinator notifications and drives
	// an EntryNotification turn, making request 1 fire. PASSES.
	coordRequests := adapter.Requests()
	if len(coordRequests) < 2 {
		t.Fatalf("coordinator's model received %d request(s), want >= 2 (initial turn + "+
			"at least one notification turn for worker-alpha and worker-beta); "+
			"the parent did not drive the coordinator's drain loop after workers completed "+
			"(dossier §4: idle children drain nothing — drive-down machinery absent today)",
			len(coordRequests))
	}

	// The notification-turn request (index >= 1) must carry the <job-notification>
	// block for both workers — confirming the coordinator's model received the worker
	// completions via its own notification turn, not the root's.
	notifReqs := coordRequests[1:]
	foundAlpha := false
	foundBeta := false
	for _, req := range notifReqs {
		for _, msg := range req.Messages {
			text := msg.Text()
			if strings.Contains(text, "worker-alpha") {
				foundAlpha = true
			}
			if strings.Contains(text, "worker-beta") {
				foundBeta = true
			}
		}
	}
	if !foundAlpha || !foundBeta {
		t.Fatalf("coordinator's notification-turn request(s) did not carry <job-notification> blocks for both workers "+
			"(foundAlpha=%v foundBeta=%v); the coordinator's model must receive worker completions via its own notification turn",
			foundAlpha, foundBeta)
	}

	// === ASSERTION 2 — root's notification queue carries only the coordinator's
	// terminal, never a worker's (the discriminating half of the spec §9 headline) ===
	//
	// Worker terminals are driven down to the coordinator; the root's per-session
	// notification queue must contain only the coordinator's own job completion,
	// never a worker job ID. This is observable today: pendingJobNotifs is a
	// per-session queue, independent of the shared adapter, and is populated
	// upstream of any driving. If drive-down ever forwarded worker terminals onto
	// the root's queue (violating spec §3 drive-down), this assertion would catch it.
	root.pendingJobNotifsMu.Lock()
	rootPending := append([]jobNotification(nil), root.pendingJobNotifs...)
	root.pendingJobNotifsMu.Unlock()
	for _, n := range rootPending {
		if n.JobID == "worker-alpha" || n.JobID == "worker-beta" {
			t.Fatalf("root's notification queue contains worker job %q; worker "+
				"terminals must be driven down to the coordinator, never leaked to "+
				"the root's rail (spec §3 drive-down)", n.JobID)
		}
	}
}

// alwaysAckAdapter records every request and answers every model call with a
// clean communicate (so each session ends its turn without a nudge). It is used
// by the depth-3 drive test, where the number and ordering of model calls across
// three concurrently-driven sessions is not statically scriptable.
type alwaysAckAdapter struct {
	name string

	mu       sync.Mutex
	requests []llm.Request
}

func (a *alwaysAckAdapter) Name() string { return a.name }

func (a *alwaysAckAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	resp := finalResponse("ack")
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *alwaysAckAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *alwaysAckAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// TestDriveAtDepth3WithIdleMiddle proves drive-down is recursive (spec §3 / §9):
// root drives the mid; the mid, once driven, drives its OWN idle child at its own
// loop boundary. The tree is eventually-driven level by level.
//
// Tree: root → mid (depth 1) → grandchild (depth 2). Both the mid and the
// grandchild end their initial turns and go idle. A completed-delegate
// notification is injected onto EACH idle session's own queue (option (a), as in
// the headline test): mid-job onto the mid, gc-job onto the grandchild —
// simulating that each had a delegate finish while it was idle.
//
// The drive cascade:
//   - root drives the mid (the mid's notify is wired to root, the §3 parent-drive
//     analog of serve.go's root wiring): root launches the mid's EntryNotification
//     turn, which drains the mid's own queue (mid-job) and makes a model request.
//   - that drive turn runs the mid's drain loop, hitting the same loop boundaries
//     that drain watch sends today (session_tool_round.go:327 / session_state.go:122,
//     spec §3). At those boundaries the mid reads ITS children's drive signals and
//     drives the grandchild's EntryNotification turn, which drains gc-job.
//
// Assertion: BOTH mid-job and gc-job notification blocks reach the model — proving
// the mid was driven by the root AND the grandchild was driven by the mid at the
// mid's own boundary. The grandchild reaching the model requires the recursion;
// a single-level drive would deliver only the mid's notification.
func TestDriveAtDepth3WithIdleMiddle(t *testing.T) {
	c := llm.NewClient()
	adapter := &alwaysAckAdapter{name: "openai"}
	c.Register(adapter)

	root, err := NewSession(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 3,
			NoProjectPrompts: true,
		},
	)
	if err != nil {
		t.Fatalf("NewSession (root): %v", err)
	}
	t.Cleanup(func() { root.Close() })

	// Background the mid delegate (depth 1). It communicates ("ack") and idles.
	midRes := root.createDelegate(context.Background(), delegateArgs{
		Task:       "mid coordinator",
		Background: true,
	})
	if midRes.Err != nil {
		t.Fatalf("createDelegate (mid): %v", midRes.Err)
	}
	_, midID, err := decodeRef(midRes.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(mid): %v", err)
	}
	midSub := root.subagents.get(midID)
	if midSub == nil || midSub.sess == nil {
		t.Fatalf("mid subagent not found: %q", midID)
	}
	midSess := midSub.sess
	waitForShellDone(t, root.jobManager, midRes.JobID)

	// Open the mid's allowance so it can background a grandchild delegate (the
	// depth gate is allowance-keyed; this is the fixture used by the depth-2 nested
	// tests). The grandchild gets allowance 0 (a leaf).
	midSess.mu.Lock()
	midSess.delegationAllowance = 1
	midSess.mu.Unlock()

	gcRes := midSess.createDelegate(context.Background(), delegateArgs{
		Task:       "grandchild worker",
		Background: true,
	})
	if gcRes.Err != nil {
		t.Fatalf("createDelegate (grandchild): %v", gcRes.Err)
	}
	_, gcID, err := decodeRef(gcRes.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(grandchild): %v", err)
	}
	gcSub := midSess.subagents.get(gcID)
	if gcSub == nil || gcSub.sess == nil {
		t.Fatalf("grandchild subagent not found: %q", gcID)
	}
	gcSess := gcSub.sess
	waitForShellDone(t, midSess.jobManager, gcRes.JobID)

	// Both the mid and the grandchild are now IDLE. Inject one completed-delegate
	// notification on each (its own queue), as if a worker finished under each.
	enqueueCompletedDelegateNotification(t, midSess, "mid-job")
	enqueueCompletedDelegateNotification(t, gcSess, "gc-job")

	// Fire the mid's wake edge (the worker-finish notify production produces). The
	// grandchild is NOT woken directly — the mid must drive it at the mid's own
	// boundary during the mid's drive turn.
	midSess.notify()

	// Allow the recursive drive cascade to run: root → mid → grandchild.
	time.Sleep(300 * time.Millisecond)

	reqs := adapter.Requests()
	foundMidJob := false
	foundGCJob := false
	for _, req := range reqs {
		for _, msg := range req.Messages {
			text := msg.Text()
			if strings.Contains(text, "mid-job") {
				foundMidJob = true
			}
			if strings.Contains(text, "gc-job") {
				foundGCJob = true
			}
		}
	}
	if !foundMidJob {
		t.Fatalf("the mid's notification (mid-job) never reached the model; the root did not drive the mid")
	}
	if !foundGCJob {
		t.Fatalf("the grandchild's notification (gc-job) never reached the model; the mid, once driven, " +
			"did not drive its own idle child at its own loop boundary (drive-down is not recursive)")
	}
}

// TestMidOwnerCallerFramesRenderMidSide proves spec §3 "mid-owner caller sends":
// a mid-level watch owner's send.to="caller" frame renders in the MID's OWN
// drive turn, NOT re-routed onto the parent's notification rail.
//
// Setup: root → mid delegate (depth 1). The mid owns a caller-targeted watch
// send (the restored/observed shape). Under the v2 re-route the parent's drain
// re-tokened that caller send onto the PARENT's rail (ChildSessionID="mid"); T15
// deletes that re-route and adds hasPendingWatchSends as drive signal (b), so the
// parent DRIVES the mid and the mid renders the caller frame on its own rail.
//
// Assertion 1 — the mid's model receives the watch_send frame in its drive turn
// (its delivery_id appears in a request to the mid's adapter).
// Assertion 2 — root's notification rail never carries a child caller token: the
// frame renders mid-side, not on the parent.
//
// RED before T15: the re-route puts the token on the parent's rail and the mid is
// never driven on the watch-send signal, so the mid's model never sees the frame.
func TestMidOwnerCallerFramesRenderMidSide(t *testing.T) {
	c := llm.NewClient()
	adapter := &alwaysAckAdapter{name: "openai"}
	c.Register(adapter)

	root, err := NewSession(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 3,
			NoProjectPrompts: true,
		},
	)
	if err != nil {
		t.Fatalf("NewSession (root): %v", err)
	}
	t.Cleanup(func() { root.Close() })

	midRes := root.createDelegate(context.Background(), delegateArgs{
		Task:       "mid watch owner",
		Background: true,
	})
	if midRes.Err != nil {
		t.Fatalf("createDelegate (mid): %v", midRes.Err)
	}
	_, midID, err := decodeRef(midRes.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(mid): %v", err)
	}
	midSub := root.subagents.get(midID)
	if midSub == nil || midSub.sess == nil {
		t.Fatalf("mid subagent not found: %q", midID)
	}
	midSess := midSub.sess
	waitForShellDone(t, root.jobManager, midRes.JobID)

	// Give the mid a caller-targeted pending watch send (the restored shape the
	// observed delivery produces). It is owned by the mid's jobManager.
	now := time.Unix(7000, 0).UTC()
	for _, event := range restoredWatchSendPendingEvents(midID, "job_mid_watched", runtimeMessageAliasCaller, now) {
		if err := midSess.jobManager.appendEvent(event); err != nil {
			t.Fatalf("append mid caller pending: %v", err)
		}
	}
	if err := midSess.jobManager.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore mid caller pending: %v", err)
	}

	// Fire the mid's wake edge (a real caller-send observation kicks the mid's
	// notify, which drive-down wires to the parent). The parent must drive the mid
	// on the pending-watch-send signal.
	midSess.notify()

	time.Sleep(300 * time.Millisecond)

	// === ASSERTION 1 — the caller frame reaches the mid's own model turn. ===
	foundFrame := false
	for _, req := range adapter.Requests() {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Text(), "delivery_restore_pending") {
				foundFrame = true
			}
		}
	}
	if !foundFrame {
		t.Fatalf("the mid's caller watch-send frame (delivery_restore_pending) never reached the mid's own " +
			"model turn; the mid was not driven on the pending-watch-send signal (spec §3 mid-owner caller sends)")
	}

	// === ASSERTION 2 — root's rail never carries a child caller token. ===
	root.pendingJobNotifsMu.Lock()
	rootPending := append([]jobNotification(nil), root.pendingJobNotifs...)
	root.pendingJobNotifsMu.Unlock()
	for _, n := range rootPending {
		if n.WatchSend != nil && n.WatchSend.ChildSessionID != "" {
			t.Fatalf("root's notification rail carries a child caller token (ChildSessionID=%q); the "+
				"v2 re-route must be deleted — mid-owner caller frames render in the mid's own drive turn",
				n.WatchSend.ChildSessionID)
		}
	}
}

// TestDrainDoesNotReRouteChildCallerPendings is the replacement for the deleted
// TestDrainEnqueuesTokensForChildCallerPendings. The deleted test asserted the v2
// behavior — the parent's drain re-tokens a child's caller-targeted pending onto
// the PARENT's rail with ChildSessionID set. Spec §3 deletes that re-route: a
// mid-owner caller send renders in the mid's OWN drive turn, never on the parent's
// rail. This test pins the NEW behavior at the drain level: the parent's drain
// leaves a child's caller pending alone (no token on the parent's rail), and the
// child's own drain renders it on the child's own rail.
func TestDrainDoesNotReRouteChildCallerPendings(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	var parentEnqueued []jobNotification
	parentJM.enqueue = func(n jobNotification) { parentEnqueued = append(parentEnqueued, n) }
	var childEnqueued []jobNotification
	childJM.enqueue = func(n jobNotification) { childEnqueued = append(childEnqueued, n) }

	now := time.Unix(4000, 0).UTC()
	for _, event := range restoredWatchSendPendingEvents("CHILD", "job_child_watched", runtimeMessageAliasCaller, now) {
		if err := childJM.appendEvent(event); err != nil {
			t.Fatalf("append child pending: %v", err)
		}
	}
	if err := childJM.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore child pending: %v", err)
	}

	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil),
	}
	child := &Session{id: "CHILD", jobManager: childJM}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   child,
		status: SubagentRunning,
	})

	// The parent's drain (iterating the child's jm with childSessionID="CHILD")
	// must NOT re-token the child's caller pending onto the parent's rail.
	if err := parent.drainJobManagerWatchSends(context.Background(), childJM, "CHILD"); err != nil {
		t.Fatalf("drainJobManagerWatchSends(child): %v", err)
	}
	for _, n := range parentEnqueued {
		if n.WatchSend != nil {
			t.Fatalf("parent's drain re-routed a child caller pending onto the parent's rail "+
				"(ChildSessionID=%q); the v2 re-route must be deleted (spec §3 mid-owner caller sends)",
				n.WatchSend.ChildSessionID)
		}
	}

	// The CHILD's own drain (childSessionID="") renders the caller pending on the
	// child's own rail — the mid-side render the drive turn performs.
	if err := child.drainJobManagerWatchSends(context.Background(), childJM, ""); err != nil {
		t.Fatalf("drainJobManagerWatchSends(own): %v", err)
	}
	var childTokens []jobNotification
	for _, n := range childEnqueued {
		if n.WatchSend != nil {
			childTokens = append(childTokens, n)
		}
	}
	if len(childTokens) != 1 {
		t.Fatalf("child's own rail tokens = %d, want exactly one self-rail caller token: %+v",
			len(childTokens), childEnqueued)
	}
	if childTokens[0].WatchSend.ChildSessionID != "" {
		t.Fatalf("child self-rail token ChildSessionID = %q, want \"\" (renders on the child's own rail)",
			childTokens[0].WatchSend.ChildSessionID)
	}
	if childTokens[0].WatchSend.Key.ResolvedSendTo != runtimeMessageAliasCaller {
		t.Fatalf("child token send-to = %q, want caller", childTokens[0].WatchSend.Key.ResolvedSendTo)
	}
}

// appendDelegateRecordForChild appends a delegate job record (start + terminal)
// to jm's store, owned by jm's session and pointing at childSessionID via its
// transcript ref — the shape the parent's store holds for a delegate it created.
// status/reason set the terminal; an empty status leaves the job running.
func appendDelegateRecordForChild(t *testing.T, jm *jobManager, jobID, childSessionID string, status jobstore.Status, reason string) {
	t.Helper()
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		TranscriptRef:    encodeRef("", childSessionID),
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("append delegate start %q: %v", jobID, err)
	}
	if status == "" {
		return
	}
	ended := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          ended,
		JobID:       jobID,
		Status:      status,
		Reason:      reason,
		EndedAt:     &ended,
		TerminalGen: "gen-" + jobID,
	}); err != nil {
		t.Fatalf("append delegate finish %q: %v", jobID, err)
	}
}

// TestStopGatingNoResurrection proves spec §3 stop-gating: a child whose LATEST
// delegate record (durable append order) terminated by deliberate stop
// (Cancelled/stopped_by_parent) is NOT driven for attention that predates the
// stop; a fresh send/spawn (a newer record) clears the gate.
//
// RED before T15: there is no gate, so a stopped child with queued pre-stop
// attention is still selected for a drive.
func TestStopGatingNoResurrection(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	t.Cleanup(func() { _ = parentJM.store.Close() })

	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil),
	}

	// Latest record for child CHILD is a deliberate stop.
	appendDelegateRecordForChild(t, parentJM, "job_stop_1", "CHILD", jobstore.StatusCancelled, "stopped_by_parent")

	if !parent.childStopGated("CHILD") {
		t.Fatalf("child whose latest record is Cancelled/stopped_by_parent must be stop-gated; gate is open")
	}

	// A fresh send/spawn appends a NEWER record for the same child — the gate clears.
	appendDelegateRecordForChild(t, parentJM, "job_resume_2", "CHILD", "", "")
	if parent.childStopGated("CHILD") {
		t.Fatalf("a fresh send/spawn (a newer running record) must clear the stop gate; gate still closed")
	}

	// A child whose latest record completed normally is never gated.
	appendDelegateRecordForChild(t, parentJM, "job_done_3", "OTHER", jobstore.StatusCompleted, "communicated")
	if parent.childStopGated("OTHER") {
		t.Fatalf("a child whose latest record completed normally must not be stop-gated")
	}

	// A child with no record at all is not gated.
	if parent.childStopGated("UNKNOWN") {
		t.Fatalf("a child with no delegate record must not be stop-gated")
	}
}

// TestStopGateUsesAppendOrderNotWallClock proves the "latest record" key is
// durable APPEND ORDER (resolved decision #3), not wall-clock: a fresh resume
// record appended AFTER a stop clears the gate even when its wall-clock stamp is
// earlier than the stop's (a clock-skew / frozen-clock race).
func TestStopGateUsesAppendOrderNotWallClock(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	t.Cleanup(func() { _ = parentJM.store.Close() })
	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}

	// Freeze the clock so wall-clock CANNOT disambiguate: the stop and the later
	// resume share an identical TS. Only append order separates them.
	frozen := time.Unix(9000, 0).UTC()
	parentJM.now = func() time.Time { return frozen }

	appendDelegateRecordForChild(t, parentJM, "job_stop_a", "CHILD", jobstore.StatusCancelled, "stopped_by_parent")
	appendDelegateRecordForChild(t, parentJM, "job_resume_b", "CHILD", "", "")

	if parent.childStopGated("CHILD") {
		t.Fatalf("the resume record appended AFTER the stop (same wall-clock) must clear the gate; " +
			"latest-record resolution must use durable append order, not wall-clock")
	}
}

// appendForwardedChildTerminalPending appends to jm's store the forwarded copy of
// a child-owned delegate terminal in NotifyPending state — the shape a parent's
// store holds for a direct child's own job (OwnerSessionID=child, visible=parent).
func appendForwardedChildTerminalPending(t *testing.T, jm *jobManager, jobID, childSessionID string) {
	t.Helper()
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   childSessionID,
		VisibleToSession: jm.sessionID,
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("append forwarded start %q: %v", jobID, err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		Reason:      "communicated",
		EndedAt:     &now,
		TerminalGen: "gen-" + jobID,
	}); err != nil {
		t.Fatalf("append forwarded finish %q: %v", jobID, err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          now,
		JobID:       jobID,
		TerminalGen: "gen-" + jobID,
	}); err != nil {
		t.Fatalf("append forwarded pending %q: %v", jobID, err)
	}
}

func childPendingNotifyState(t *testing.T, jm *jobManager, jobID string) jobstore.NotifyState {
	t.Helper()
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	rec := recs[jobID]
	if rec == nil {
		t.Fatalf("record %q not found", jobID)
	}
	return rec.NotifyState
}

// TestDriveHandoffSettleAndCrashReArm proves spec §3 settle + restore re-arm
// filtering:
//   - a successful drive handoff settles the parent's forwarded pending COPY
//     (marks it delivered) so the same stale signal does not re-drive forever;
//   - the child's OWN durable queue (its own store) is the ledger — untouched by
//     the parent's settle, it re-arms at the child's restore (nothing lost);
//   - on the parent's restore the re-arm filters to OWNED records only — a
//     forwarded child-owned terminal does NOT re-arm onto the parent's render
//     rail (no restart wake-storm).
//
// RED before T15: no settle-at-handoff (the parent's copy stays pending) and the
// restore re-arm enqueues every terminal record including forwarded child-owned
// ones (the wake-storm).
func TestDriveHandoffSettleAndCrashReArm(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()

	var parentEnqueued []jobNotification
	parentJM, err := newJobManager(parentDir, "PARENT", func(n jobNotification) {
		parentEnqueued = append(parentEnqueued, n)
	})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}

	// The parent holds a forwarded copy of CHILD's own delegate terminal (pending):
	// owner=CHILD, visible=PARENT. CHILD's OWN store holds the same job, also pending
	// (the ledger): owner=CHILD, visible=CHILD — the own-owned shape, since the
	// helper sets visible to its jm's own session id.
	appendForwardedChildTerminalPending(t, parentJM, "job_worker", "CHILD")
	appendForwardedChildTerminalPending(t, childJM, "job_worker", "CHILD")

	if got := childPendingNotifyState(t, parentJM, "job_worker"); got != jobstore.NotifyPending {
		t.Fatalf("parent forwarded copy starts NotifyState=%q, want pending", got)
	}

	// === Settle at handoff: the parent marks its forwarded COPY delivered. ===
	parent.settleDrivenChildForwardedPendings("CHILD")

	if got := childPendingNotifyState(t, parentJM, "job_worker"); got != jobstore.NotifyDelivered {
		t.Fatalf("after settle, parent forwarded copy NotifyState=%q, want delivered (settle marks the parent's copy)", got)
	}
	// The child's OWN ledger is untouched by the parent's settle.
	if got := childPendingNotifyState(t, childJM, "job_worker"); got != jobstore.NotifyPending {
		t.Fatalf("after the parent's settle, the child's OWN ledger NotifyState=%q, want still pending "+
			"(the parent must not touch the child's ledger)", got)
	}

	// === Restart: the parent re-arms. The settled forwarded copy must not wake. ===
	parentEnqueued = nil
	if err := parentJM.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("armPendingTerminalNotifications: %v", err)
	}
	for _, n := range parentEnqueued {
		if n.JobID == "job_worker" {
			t.Fatalf("restart re-armed the forwarded child-owned terminal onto the parent's rail "+
				"(job %q); restore re-arm must filter to OWNED records — forwarded child-owned terminals "+
				"are the child's ledger, driven down, never the parent's render (restart wake-storm)", n.JobID)
		}
	}

	// === No-loss: the child's own ledger re-arms at the child's restore. ===
	childRestored, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen child jobManager: %v", err)
	}
	t.Cleanup(func() { _ = childRestored.store.Close() })
	var childRearmed []jobNotification
	childRestored.enqueue = func(n jobNotification) { childRearmed = append(childRearmed, n) }
	if err := childRestored.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("child armPendingTerminalNotifications: %v", err)
	}
	found := false
	for _, n := range childRearmed {
		if n.JobID == "job_worker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the child's own ledger did not re-arm its pending terminal at restore; nothing must be lost")
	}
}

// TestRestoreReArmKeepsOwnedTerminals proves the restore re-arm filter keeps the
// parent's OWN jobs (its direct delegates' terminals still render on the parent's
// rail — spec §3 / contract :1054 "still renders its OWN direct delegates'
// terminals"), while filtering out forwarded child-owned terminals.
func TestRestoreReArmKeepsOwnedTerminals(t *testing.T) {
	var enqueued []jobNotification
	jm, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		enqueued = append(enqueued, n)
	})
	if err != nil {
		t.Fatalf("new jobManager: %v", err)
	}
	t.Cleanup(func() { _ = jm.store.Close() })

	// PARENT's own delegate terminal (owned by PARENT) — must re-arm and render.
	appendDelegateRecordForChild(t, jm, "job_own_coord", "COORD", jobstore.StatusCompleted, "communicated")
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          jm.now(),
		JobID:       "job_own_coord",
		TerminalGen: "gen-job_own_coord",
	}); err != nil {
		t.Fatalf("append own pending: %v", err)
	}
	// A forwarded child-owned terminal (owned by COORD) — must NOT re-arm.
	appendForwardedChildTerminalPending(t, jm, "job_coord_worker", "COORD")

	enqueued = nil
	if err := jm.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("armPendingTerminalNotifications: %v", err)
	}
	foundOwn := false
	for _, n := range enqueued {
		if n.JobID == "job_coord_worker" {
			t.Fatalf("restore re-armed a forwarded child-owned terminal %q onto the parent's rail", n.JobID)
		}
		if n.JobID == "job_own_coord" {
			foundOwn = true
		}
	}
	if !foundOwn {
		t.Fatalf("restore re-arm dropped the parent's OWN direct-delegate terminal; the parent must still " +
			"render its own jobs' terminals (contract :1054)")
	}
}

// TestFallbackRenderNonResumableChild proves spec §3 failure fallback: when the
// parent has a forwarded pending for a child-owned job but the child is
// non-resumable (closed / descriptor-less / validation failure) at drive time,
// the parent RENDERS the pending itself, prefixed "child unreachable:" — attention
// escalates one honest level instead of vanishing.
//
// RED before T15: there is no fallback; an unreachable child's pending vanishes
// (the parent does not render it).
func TestFallbackRenderNonResumableChild(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	t.Cleanup(func() { _ = parentJM.store.Close() })

	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}

	// The parent holds a forwarded pending for GONE's own job, but GONE has no live
	// subagent session (closed / descriptor-less) — it cannot be driven.
	appendForwardedChildTerminalPending(t, parentJM, "job_gone_work", "GONE")

	parent.driveChildrenWithUndeliveredAttention()

	parent.pendingJobNotifsMu.Lock()
	pending := append([]jobNotification(nil), parent.pendingJobNotifs...)
	parent.pendingJobNotifsMu.Unlock()

	var rendered *jobNotification
	for i := range pending {
		if pending[i].JobID == "job_gone_work" {
			rendered = &pending[i]
		}
	}
	if rendered == nil {
		t.Fatalf("the unreachable child's forwarded pending vanished; the parent must render it itself " +
			"(spec §3 failure fallback)")
	}
	if !strings.HasPrefix(rendered.Reason, "child unreachable:") {
		t.Fatalf("fallback render reason = %q, want a \"child unreachable:\" prefix (attention escalates "+
			"one honest level)", rendered.Reason)
	}
}

// TestFallbackRenderNonResumableChildSurvivesDeliveryFilter proves the
// unreachable-child fallback's notification actually reaches the model (spec §3
// failure fallback). TestFallbackRenderNonResumableChild inspects
// pendingJobNotifs directly right after the drive, where the enqueue has
// happened — it never runs the delivery filter. The notification turn DOES run
// the filter (filterDeliverableJobNotifications), which gates each durable
// notification on jobstore.ShouldDeliver(rec) (NotifyState == NotifyPending). If
// the fallback settles the record to NotifyDelivered before any render, the
// filter drops the notification and the model never sees it.
//
// RED at HEAD: renderUnreachableChildPendings marks the record delivered inline
// right after enqueuing, so ShouldDeliver returns false and the deliverable set
// is empty. After removing the premature mark the record stays NotifyPending,
// survives the filter, and is rendered (then settled post-render by the normal
// markJobNotificationsDelivered path).
func TestFallbackRenderNonResumableChildSurvivesDeliveryFilter(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	t.Cleanup(func() { _ = parentJM.store.Close() })

	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}

	// The parent holds a forwarded pending for GONE's own job, but GONE has no live
	// subagent session (closed / descriptor-less) — it cannot be driven.
	appendForwardedChildTerminalPending(t, parentJM, "job_gone_work", "GONE")

	parent.driveChildrenWithUndeliveredAttention()

	// Corroboration: the record must remain NotifyPending after the drive (today it
	// is flipped to NotifyDelivered by the premature inline mark).
	if got := childPendingNotifyState(t, parentJM, "job_gone_work"); got != jobstore.NotifyPending {
		t.Fatalf("after fallback drive, record NotifyState=%q, want pending (the fallback must not settle "+
			"before any render)", got)
	}

	// DECISIVE: run the REAL delivery filter the notification turn runs. The
	// unreachable-child notification must survive into the deliverable set.
	deliverable, _, _ := parent.filterDeliverableJobNotifications(parent.drainJobNotifications())
	found := false
	for _, d := range deliverable {
		if d.notification.JobID == "job_gone_work" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unreachable-child notification was filtered out before the model saw it; the fallback "+
			"settled the record to NotifyDelivered before any render, so ShouldDeliver gated it out (deliverable=%+v)",
			deliverable)
	}
}

// TestSelfDeliverySettlesParentForwardedPending proves that a child settling its
// OWN delegate notification also settles the parent's FORWARDED COPY (spec §3).
// When a child completes/delivers a job within its own running turn,
// markJobNotificationsDelivered settles only the child's local store. The child
// forwards the Pending up (forwardPendingJobNotification) but nothing forwards
// the Delivered up, so the parent's forwarded copy stays NotifyPending forever.
// If the child later becomes non-live and non-resumable,
// renderUnreachableChildPendings falsely escalates "child unreachable:" for a
// job the child already delivered.
//
// RED at HEAD: the parent's forwarded copy stays NotifyPending after the child
// self-delivers, and the parent then falsely escalates it. After the fix
// markJobNotificationsDelivered also forwards the Delivered up, settling the
// parent's copy so no false escalation fires.
func TestSelfDeliverySettlesParentForwardedPending(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()

	var parentEnqueued []jobNotification
	parentJM, err := newJobManager(parentDir, "PARENT", func(n jobNotification) {
		parentEnqueued = append(parentEnqueued, n)
	})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	// Wire the child to forward up to the parent (the production shape: a child
	// with a real parent forwards its settle; root sessions and non-forwarded
	// terminals no-op in forwardSnapshot).
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}

	// The parent holds a forwarded copy of CHILD's own delegate terminal (pending),
	// and CHILD's OWN ledger holds the same job (also pending).
	appendForwardedChildTerminalPending(t, parentJM, "job_worker", "CHILD")
	appendForwardedChildTerminalPending(t, childJM, "job_worker", "CHILD")

	if got := childPendingNotifyState(t, parentJM, "job_worker"); got != jobstore.NotifyPending {
		t.Fatalf("parent forwarded copy starts NotifyState=%q, want pending", got)
	}
	if got := childPendingNotifyState(t, childJM, "job_worker"); got != jobstore.NotifyPending {
		t.Fatalf("child own ledger starts NotifyState=%q, want pending", got)
	}

	// ACT: the child self-delivers WITHOUT a parent drive — its own running turn
	// settles its own notification. The helper stamps the terminal_generation as
	// "gen-" + jobID, so the deliverable must carry that gen to match the record.
	child := &Session{id: "CHILD", jobManager: childJM}
	failed := child.markJobNotificationsDelivered([]deliverableJobNotification{{
		notification: jobNotification{JobID: "job_worker"},
		terminalGen:  "gen-job_worker",
	}})
	if len(failed) != 0 {
		t.Fatalf("markJobNotificationsDelivered reported failures: %+v", failed)
	}

	// The child's own ledger settles (this passes today).
	if got := childPendingNotifyState(t, childJM, "job_worker"); got != jobstore.NotifyDelivered {
		t.Fatalf("child own ledger NotifyState=%q, want delivered after self-delivery", got)
	}
	// The parent's forwarded copy must ALSO settle (FAILS today — stays pending
	// because the Delivered is never forwarded up).
	if got := childPendingNotifyState(t, parentJM, "job_worker"); got != jobstore.NotifyDelivered {
		t.Fatalf("parent forwarded copy NotifyState=%q, want delivered — the child's self-delivery must "+
			"forward the Delivered up so the parent's copy settles (else a false \"child unreachable:\" "+
			"escalation fires later)", got)
	}

	// User-visible consequence: with the child non-live/non-resumable, the parent's
	// drive must NOT falsely escalate "child unreachable:" for the already-delivered
	// job. CHILD is not tracked as a live subagent, so the fallback would escalate
	// any still-pending forwarded copy.
	parent.driveChildrenWithUndeliveredAttention()
	parent.pendingJobNotifsMu.Lock()
	pending := append([]jobNotification(nil), parent.pendingJobNotifs...)
	parent.pendingJobNotifsMu.Unlock()
	for _, n := range pending {
		if n.JobID == "job_worker" && strings.HasPrefix(n.Reason, "child unreachable:") {
			t.Fatalf("the parent falsely escalated \"child unreachable:\" for job %q that the child already "+
				"self-delivered; the forwarded copy must have settled", n.JobID)
		}
	}
}

// driveBlockingSecondTurnAdapter scripts a coordinator that ends its initial turn
// cleanly, then BLOCKS its drive turn (EntryNotification) on a release channel.
// Any THIRD model call — a second concurrent ProcessInputKind launched on the same
// child while the drive turn is still in flight — records secondTurnStarted. The
// A7 data race is exactly that second concurrent turn; the steer fix must route the
// mid-drive send into the in-flight drive turn instead of launching a fresh one.
type driveBlockingSecondTurnAdapter struct {
	name            string
	release         <-chan struct{}
	secondTurnStart chan struct{}

	mu       sync.Mutex
	requests []llm.Request
	calls    int
}

func (a *driveBlockingSecondTurnAdapter) Name() string { return a.name }

func (a *driveBlockingSecondTurnAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.calls++
	call := a.calls
	a.mu.Unlock()

	switch call {
	case 1:
		// Coordinator's initial turn ends cleanly → idle.
		resp := finalResponse("coordinator idle")
		resp.Provider = a.name
		if resp.Model == "" {
			resp.Model = req.Model
		}
		return resp, nil
	case 2:
		// The drive turn (EntryNotification). Block so the drive goroutine holds
		// sub.driving==true while the test sends a mid-drive message.
		select {
		case <-a.release:
		case <-ctx.Done():
			return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
		}
		resp := finalResponse("drive ack")
		resp.Provider = a.name
		if resp.Model == "" {
			resp.Model = req.Model
		}
		return resp, nil
	default:
		// A THIRD call means a SECOND concurrent ProcessInputKind started on the
		// same child while the drive turn was in flight — the A7 race.
		select {
		case <-a.secondTurnStart:
		default:
			close(a.secondTurnStart)
		}
		<-ctx.Done()
		return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
	}
}

func (a *driveBlockingSecondTurnAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *driveBlockingSecondTurnAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// TestSendMessageMidDriveSteersNoSecondTurn proves the A7 fix: a job_send_message
// arriving while a coordinator's drive turn is in flight (sub.driving==true,
// sub.running==false) STEERS into that single in-flight turn instead of launching
// a SECOND concurrent ProcessInputKind on the same child session (a data race on
// session history / sessionEndEmitted / goalInTurn — ProcessInputKind has no
// per-session mutual exclusion; serialization lives entirely in the running/driving
// flags).
//
// RED at HEAD: sendDelegateMessage reads only sub.running, so a driving-but-not-
// running coordinator falls into resumeOrFindRunningDelegate, which launches a
// fresh delegate turn → Action=="resumed", a second ProcessInputKind runs
// concurrently with the drive turn (-race reports a DATA RACE), and the adapter's
// third call records secondTurnStarted.
//
// GREEN after the fix: the send is steered into the drive turn → Action=="sent",
// no second turn, no race. Run under -race.
func TestSendMessageMidDriveSteersNoSecondTurn(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	adapter := &driveBlockingSecondTurnAdapter{
		name:            "openai",
		release:         release,
		secondTurnStart: make(chan struct{}),
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "coordinate",
		Background: true,
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForShellDone(t, sess.jobManager, coord.JobID)

	_, coordID, err := decodeRef(coord.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	coordSub := sess.subagents.get(coordID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coordID)
	}

	// Queue a worker completion so the drive turn has work, then launch the drive
	// turn. It blocks on release (adapter call 2) with sub.driving==true.
	enqueueCompletedDelegateNotification(t, coordSub.sess, "worker-1")
	if !sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("driveSubagentNotificationTurn returned false; expected a launched drive turn")
	}

	// Confirm the mid-drive state: driving==true, running==false.
	coordSub.mu.Lock()
	driving := coordSub.driving
	running := coordSub.running
	coordSub.mu.Unlock()
	if !driving || running {
		t.Fatalf("expected mid-drive state driving==true running==false, got driving=%v running=%v", driving, running)
	}

	// WHILE the drive turn is blocked in flight, send a message to the coordinator.
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:        coord.JobID,
		Message:       "steer me",
		Background:    true,
		BackgroundSet: true,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage during drive turn: %v", res.Err)
	}

	// The send must steer into the single in-flight drive turn (Action=="sent"),
	// NOT launch a fresh resumed ProcessInput (Action=="resumed") that runs a
	// SECOND concurrent ProcessInputKind on the same child session.
	if res.Action != "sent" {
		t.Fatalf("mid-drive send Action=%q, want \"sent\" (steered into the in-flight drive turn); "+
			"a \"resumed\" Action means a SECOND concurrent ProcessInputKind launched on the driving child", res.Action)
	}

	// A second concurrent turn must NEVER start on the child. At HEAD the resumed
	// turn fires its own model call (adapter call 3); give it a window so -race can
	// observe the concurrent turn and the adapter records secondTurnStarted.
	select {
	case <-adapter.secondTurnStart:
		t.Fatal("a second concurrent turn started on the driving child; the mid-drive send must steer, not launch a fresh turn")
	case <-time.After(200 * time.Millisecond):
	}

	// Release the drive turn and let it finish cleanly.
	releaseOnce.Do(func() { close(release) })
}

// TestWatchResumeMidDriveSteersNotDropped proves the A7 fix's FromWatch leg: a
// watch-send resume (FromWatch==true) arriving while a coordinator's drive turn is
// in flight (sub.driving==true, sub.running==false) must STEER into that single
// in-flight turn — delivered via trySteer — and MUST NOT be permanently dropped.
//
// A backgrounded coordinator that finished its run carries a TERMINAL job record
// (not StatusRunning), and a drive turn mints NO running runtime job. So a
// FromWatch send takes sendDelegateMessage's terminal path, hits the A7 driving
// intercept, and reaches sendRunningDelegateMessage with run==nil. The
// `fromWatch && run == nil` guard there would hard-fail (sendMessageFailed →
// watchSendHardFailure), and the live drain path (drainJobManagerWatchSends →
// deliverPendingWatchSend) classifies a hard failure as a PERMANENT dropWatchSend.
//
// This is the live drain path's reachability — drainJobManagerWatchSends applies
// NO classifyRestoredWatchSendTarget busy pre-filter (that pre-filter exists only
// on retryRestoredPendingWatchSends), so nothing upstream protects this send.
//
// RED at the A7 working-tree state: the FromWatch send returns watchSendHardFailure
// (a permanent drop), contradicting the A7 "steer into the drive turn" decision and
// silently losing the watch delivery.
//
// GREEN after the fix: a driving child's FromWatch send steers via trySteer
// (Delivered / Action=="sent", classifyWatchSendDelivery != watchSendHardFailure)
// — or, if trySteer ever declined, returns watchSendBusy so the frame stays pending
// for A6 re-delivery — never a hard failure.
func TestWatchResumeMidDriveSteersNotDropped(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	adapter := &driveBlockingSecondTurnAdapter{
		name:            "openai",
		release:         release,
		secondTurnStart: make(chan struct{}),
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "coordinate",
		Background: true,
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForShellDone(t, sess.jobManager, coord.JobID)

	_, coordID, err := decodeRef(coord.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	coordSub := sess.subagents.get(coordID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coordID)
	}

	// Queue a worker completion so the drive turn has work, then launch the drive
	// turn. It blocks on release (adapter call 2) with sub.driving==true.
	enqueueCompletedDelegateNotification(t, coordSub.sess, "worker-1")
	if !sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("driveSubagentNotificationTurn returned false; expected a launched drive turn")
	}

	// Confirm the mid-drive state: driving==true, running==false, terminal record,
	// no running runtime job — the exact preconditions for the drop.
	coordSub.mu.Lock()
	driving := coordSub.driving
	running := coordSub.running
	coordSub.mu.Unlock()
	if !driving || running {
		t.Fatalf("expected mid-drive state driving==true running==false, got driving=%v running=%v", driving, running)
	}
	rec, err := findJobRecord(sess.jobManager, coord.JobID)
	if err != nil {
		t.Fatalf("findJobRecord: %v", err)
	}
	if !rec.Status.IsTerminal() {
		t.Fatalf("coordinator record status=%q, want a terminal record (the drive-down precondition)", rec.Status)
	}

	// WHILE the drive turn is blocked in flight, deliver a WATCH-RESUME send to the
	// coordinator (FromWatch==true — the live-drain shape).
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:        coord.JobID,
		Message:       "watch steer me",
		Background:    true,
		BackgroundSet: true,
		FromWatch:     true,
	})

	// The watch send MUST NOT be a hard failure: a hard failure on the live drain
	// path means deliverPendingWatchSend permanently dropWatchSends the frame —
	// a silent watch-delivery loss that contradicts the A7 steer-into-drive decision.
	if classifyWatchSendDelivery(res) == watchSendHardFailure {
		t.Fatalf("mid-drive FromWatch send classified as watchSendHardFailure (Err=%v); a driving child's "+
			"watch-resume must STEER into the drive turn, never be permanently dropped (spec §3, A7)", res.Err)
	}

	// Steering into a live (drive) turn always succeeds, so the frame settles as
	// delivered (Action=="sent", classifyWatchSendDelivery==watchSendDelivered);
	// deliverPendingWatchSend then settleWatchSendDelivered-s the pending frame.
	if res.Action != "sent" {
		t.Fatalf("mid-drive FromWatch send Action=%q, want \"sent\" (steered into the in-flight drive turn)", res.Action)
	}
	if classifyWatchSendDelivery(res) != watchSendDelivered {
		t.Fatalf("mid-drive FromWatch send classified as %d, want watchSendDelivered (%d) — the steered frame "+
			"must settle delivered, not stay pending or drop", classifyWatchSendDelivery(res), watchSendDelivered)
	}

	// Release the drive turn and let it finish cleanly.
	releaseOnce.Do(func() { close(release) })
}

// driveReDriveAdapter records every request, ends the coordinator's initial turn
// cleanly, BLOCKS its first drive turn on a release channel so a late notification
// can be enqueued mid-drive (dropped at the driving==true guard), then answers all
// later turns cleanly. The A6 re-check must re-drive after the first turn ends so
// the late notification still reaches the model.
type driveReDriveAdapter struct {
	name        string
	firstDrive  chan struct{} // closed when the first drive turn's model call starts
	release     <-chan struct{}
	firstDriveO sync.Once

	mu       sync.Mutex
	requests []llm.Request
	calls    int
}

func (a *driveReDriveAdapter) Name() string { return a.name }

func (a *driveReDriveAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.calls++
	call := a.calls
	a.mu.Unlock()

	if call == 2 {
		// The first drive turn (EntryNotification): signal it started, then block so
		// the test can enqueue a late notification while sub.driving==true.
		a.firstDriveO.Do(func() { close(a.firstDrive) })
		select {
		case <-a.release:
		case <-ctx.Done():
			return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
		}
	}

	resp := finalResponse("ack")
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *driveReDriveAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *driveReDriveAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// TestDriveWakeDuringInflightDriveReDrives proves the A6 fix: a notification that
// arms on a child DURING its drive turn — after the turn's notification drain but
// before the drive goroutine returns — is dropped at the driving==true guard, so
// the drive goroutine's deferred cleanup must re-evaluate the child's queue after
// clearing sub.driving and re-drive if attention remains (spec §3). Without the
// re-check the late notification strands permanently: the parent is idle, there is
// no autonomous poll, and nothing re-drives.
//
// Deterministic construction: the first drive turn drains notif-1 ("first-job") and
// then BLOCKS in its model call (sub.driving==true). While blocked, the test arms
// notif-2 ("late-job") and fires the child's notify() — its wake hits the guard and
// is dropped. The first turn is then released; its deferred cleanup clears
// sub.driving and (with the fix) re-checks the queue, finds notif-2 still pending,
// and re-drives so "late-job" reaches the model.
//
// RED at HEAD: the deferred cleanup only clears sub.driving with no re-check, so the
// re-drive never happens and "late-job" never appears in the adapter's requests.
func TestDriveWakeDuringInflightDriveReDrives(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	adapter := &driveReDriveAdapter{
		name:       "openai",
		firstDrive: make(chan struct{}),
		release:    release,
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "coordinate",
		Background: true,
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForShellDone(t, sess.jobManager, coord.JobID)

	_, coordID, err := decodeRef(coord.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	coordSub := sess.subagents.get(coordID)
	if coordSub == nil || coordSub.sess == nil {
		t.Fatalf("coordinator subagent %q not found", coordID)
	}
	childSess := coordSub.sess

	// Arm notif-1 and drive the coordinator's notification turn THROUGH the real
	// driveSubagentNotificationTurn so its deferred cleanup runs.
	enqueueCompletedDelegateNotification(t, childSess, "first-job")
	if !sess.driveSubagentNotificationTurn(coordSub) {
		t.Fatal("driveSubagentNotificationTurn returned false; expected a launched drive turn")
	}

	// Wait until the first drive turn is in flight and blocked in its model call.
	select {
	case <-adapter.firstDrive:
	case <-time.After(2 * time.Second):
		t.Fatal("first drive turn never started its model call")
	}

	// WHILE driving==true, arm a late notification and fire the wake. The wake hits
	// the driving==true guard in driveSubagentNotificationTurn and is dropped; the
	// late notification stays queued for the deferred re-check.
	enqueueCompletedDelegateNotification(t, childSess, "late-job")
	childSess.notify()

	// Release the first drive turn. Its deferred cleanup clears sub.driving and (with
	// the fix) re-checks the queue and re-drives so "late-job" reaches the model.
	releaseOnce.Do(func() { close(release) })

	// Poll the adapter's requests until the late notification reaches the model.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if requestsContain(adapter.Requests(), "late-job") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the late notification (late-job) never reached the model; the drive goroutine's deferred " +
		"cleanup cleared sub.driving without re-checking the queue, so the dropped mid-drive wake stranded")
}
