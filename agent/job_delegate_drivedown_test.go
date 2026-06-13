package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
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
