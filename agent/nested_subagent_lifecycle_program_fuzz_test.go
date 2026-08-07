//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/llm"
)

// FuzzNslpNestedLifecycleProgram exercises the real delegate tree below a
// scripted provider boundary. It never invokes a shell, network, or external
// provider: DenyEnv supplies the child environments, the FakeClock owns all
// in-session timing, and the only shell-shaped record is created directly in a
// job manager without an executor.
//
// The program has three linked oracles:
//   - exact-once: every terminal delegate has one durable finish, pending, and
//     delivered notification event in its owning store;
//   - routing: a descendant walk projects each job once from its owner, while
//     direct and deep owner lookup choose the correct manager/session pair;
//   - callback delivery: a watch-delivery communicate call routes at most once
//     after acceptance and does not latch a rejected route.
//
// The lifecycle is deliberately small but real: root delegates to a coordinator;
// the coordinator delegates to a leaf; the leaf's pending notification is driven
// by its immediate parent; then DrainJobTree settles the root notification.
// serf:fuzz native
func FuzzNslpNestedLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0, 0},
		{1, 1, 1, 1},
		{2, 3, 1, 0, 2, 3, 4},
		{0, 1, 0, 2, 3, 0, 1}, // deep record + owner-routing branch
		{255, 254, 253, 252, 251, 250},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		program := nslpDecode(data)
		root, rootAdapter := nslpNewRoot(t, program)

		rootJobID, coordinator, coordinatorSub := nslpStartCoordinator(t, root, program)
		nslpWaitDone(t, coordinatorSub.done, "coordinator run")

		worker, workerSub, workerJobID := nslpFindWorker(t, coordinator)
		nslpWaitDone(t, workerSub.done, "worker run")
		nslpWaitJobTerminal(t, coordinator.jobManager, workerJobID, "worker delegate")

		// A leaf completion can land while its coordinator is still running. Once
		// the coordinator is idle, drive its own notification rail exactly as the
		// parent-side notify hook does, then join the deterministic drive goroutine.
		nslpDriveChildNotifications(t, coordinator, workerSub)

		result, err := nslpDrain(root)
		if err != nil {
			t.Fatalf("DrainJobTree: %v", err)
		}
		if result != nslpRootNotice {
			t.Fatalf("DrainJobTree result = %q, want %q", result, nslpRootNotice)
		}
		if got := len(rootAdapter.Requests()); got != 1 {
			t.Fatalf("root notification requests = %d, want exactly one", got)
		}
		nslpWaitJobTerminal(t, root.jobManager, rootJobID, "root delegate")

		nslpAssertDelegateLedger(t, root, coordinator, rootJobID, workerJobID)
		nslpAssertDescendantRouting(t, root, coordinator, worker, rootJobID, workerJobID)

		if program.resumeCoordinator {
			if _, err := root.sendInput(context.Background(), coordinator.ID(), "nslp resume "+program.task); err != nil {
				t.Fatalf("sendInput(resume): %v", err)
			}
			nslpWaitDone(t, coordinatorSub.done, "coordinator resume")
			if got := len(rootAdapter.Requests()); got != 1 {
				t.Fatalf("resume unexpectedly drove root notification: requests=%d", got)
			}
		}

		if program.addNestedRecord {
			nslpAddNestedRecord(t, root, coordinator, worker, workerSub, rootJobID, workerJobID, program)
		}

		nslpManagerOwnershipProgram(t, "nslp-child-"+program.task)
		nslpForwardRecoveryProgram(t)
		nslpWatchCallbackProgram(t, program)
	})
}

const nslpRootNotice = "nslp root settled"

type nslpProgram struct {
	task              string
	callbackMessage   string
	acceptCallback    bool
	resumeCoordinator bool
	addNestedRecord   bool
	seed              uint64
}

type nslpReader struct {
	data []byte
	pos  int
}

func (r *nslpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *nslpReader) pick(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[int(r.next())%len(values)]
}

func nslpDecode(data []byte) nslpProgram {
	r := &nslpReader{data: data}
	program := nslpProgram{
		task:            r.pick([]string{"alpha", "beta", "edge-case", "routing"}),
		callbackMessage: r.pick([]string{"callback", "final report", "nested status", "route me"}),
		acceptCallback:  r.next()&1 == 0,
		seed:            uint64(r.next())<<8 | uint64(r.next()),
	}
	program.resumeCoordinator = r.next()&1 == 1
	program.addNestedRecord = r.next()&1 == 1
	return program
}

func nslpNewRoot(t *testing.T, program nslpProgram) (*Session, *agenttest.ScriptedAdapter) {
	t.Helper()
	clock := agenttest.NewFakeClock()
	rootAdapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return agenttest.FinalResponse(nslpRootNotice)
		},
	}
	rootClient := llm.NewClient()
	rootClient.Register(rootAdapter)

	var factoryMu sync.Mutex
	factoryIndex := 0
	cfg := SessionConfig{
		StateDir:              t.TempDir(),
		MaxSubagentDepth:      2,
		MaxToolRoundsPerInput: 8,
		NoProjectPrompts:      true,
		LLMSleep:              func(context.Context, time.Duration) error { return nil },
		clock:                 clock,
	}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
	}
	cfg.testOnly.childClientFactory = func() *llm.Client {
		factoryMu.Lock()
		index := factoryIndex
		factoryIndex++
		factoryMu.Unlock()

		client := llm.NewClient()
		switch index {
		case 0:
			client.Register(nslpCoordinatorAdapter(program))
		case 1:
			client.Register(&agenttest.ScriptedAdapter{
				Provider: "openai",
				Responder: func(llm.Request) llm.Response {
					return agenttest.FinalResponse("nslp worker complete")
				},
			})
		default:
			// A replay that reaches an unexpected extra child remains offline and
			// terminal rather than sharing a responder with another goroutine.
			client.Register(&agenttest.ScriptedAdapter{
				Provider: "openai",
				Responder: func(llm.Request) llm.Response {
					return agenttest.FinalResponse("nslp spare child complete")
				},
			})
		}
		return client
	}

	root, err := NewSession(rootClient, NewOpenAIProfile("gpt-5.2"), &agenttest.DenyEnv{
		WorkDir: t.TempDir(),
		Seed:    program.seed,
	}, cfg)
	if err != nil {
		t.Fatalf("NewSession(root): %v", err)
	}
	t.Cleanup(root.Close)
	return root, rootAdapter
}

func nslpCoordinatorAdapter(program nslpProgram) *agenttest.ScriptedAdapter {
	var mu sync.Mutex
	call := 0
	return &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			mu.Lock()
			call++
			current := call
			mu.Unlock()
			if current == 1 {
				args, err := json.Marshal(map[string]any{
					"task":                 "nslp worker " + program.task,
					"delegation_allowance": float64(0),
				})
				if err != nil {
					panic(err)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "nslp-delegate-worker",
					Name:      "delegate",
					Arguments: args,
					Type:      "function",
				})
			}
			return agenttest.FinalResponse("nslp coordinator complete")
		},
	}
}

func nslpStartCoordinator(t *testing.T, root *Session, program nslpProgram) (string, *Session, *subagent) {
	t.Helper()
	res := root.createDelegate(context.Background(), delegateArgs{
		Task:                "nslp coordinator " + program.task,
		DelegationAllowance: 1,
		Background:          true,
	})
	if res.Err != nil {
		t.Fatalf("create coordinator delegate: %v", res.Err)
	}
	if !res.RunningInBackground || res.JobID == "" || res.TranscriptRef == "" {
		t.Fatalf("coordinator delegate result = %+v", res)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil || childID == "" {
		t.Fatalf("decode coordinator transcript %q: %v", res.TranscriptRef, err)
	}
	sub := root.getSub(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("coordinator %q was not tracked", childID)
	}
	return res.JobID, sub.sess, sub
}

func nslpFindWorker(t *testing.T, coordinator *Session) (*Session, *subagent, string) {
	t.Helper()
	var workerJob *jobstore.JobRecord
	for _, rec := range coordinator.jobManager.list(listFilter{IncludeNested: true}) {
		if rec.Type == jobstore.JobDelegate && rec.OwnerSessionID == coordinator.ID() {
			if workerJob != nil {
				t.Fatalf("coordinator has multiple worker delegates: %q and %q", workerJob.JobID, rec.JobID)
			}
			workerJob = rec
		}
	}
	if workerJob == nil {
		t.Fatal("coordinator did not create a worker delegate")
	}
	_, childID, err := decodeRef(workerJob.TranscriptRef)
	if err != nil || childID == "" {
		t.Fatalf("decode worker transcript %q: %v", workerJob.TranscriptRef, err)
	}
	sub := coordinator.getSub(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("worker %q was not tracked", childID)
	}
	return sub.sess, sub, workerJob.JobID
}

func nslpWaitDone(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not complete", label)
	}
}

func nslpWaitJobTerminal(t *testing.T, jm *jobManager, jobID, label string) {
	t.Helper()
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		nslpWaitDone(t, run.done, label+" finalization")
	}
	rec, err := findJobRecord(jm, jobID)
	if err != nil {
		t.Fatalf("%s record: %v", label, err)
	}
	if !rec.Status.IsTerminal() {
		t.Fatalf("%s status = %q, want terminal", label, rec.Status)
	}
}

func nslpDriveChildNotifications(t *testing.T, parent *Session, child *subagent) {
	t.Helper()
	if child == nil || child.sess == nil {
		t.Fatal("notification child is nil")
	}
	if child.sess.peekNotifications() > 0 && !parent.driveSubagentNotificationTurn(child) {
		// The child notify hook can already have claimed the same drive turn. A
		// false handoff is valid only while that existing run is active; otherwise
		// pending attention would be stranded.
		child.mu.Lock()
		active := child.running || child.driving
		child.mu.Unlock()
		if !active {
			t.Fatal("idle child notification was not handed to its parent drive loop")
		}
	}
	// A child might already have been driven by its notify hook. Its add happens
	// before finalization returns, so this is a deterministic join, not polling.
	parent.sendersWG.Wait()
	if got := child.sess.peekNotifications(); got != 0 {
		t.Fatalf("child notification queue = %d after drive, want 0", got)
	}
}

func nslpDrain(root *Session) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// DrainJobTree's production lost-wake backstop is a clock ticker. This
	// fixture deliberately freezes its shared FakeClock, so drive the extracted
	// recheck seam directly instead of depending on wall time or advancing every
	// unrelated timer in the delegate tree.
	recheck := make(chan time.Time)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case recheck <- time.Time{}:
			case <-stop:
				return
			}
		}
	}()
	return root.drainJobTree(ctx, recheck)
}

func nslpAssertDelegateLedger(t *testing.T, root, coordinator *Session, rootJobID, workerJobID string) {
	t.Helper()
	for _, check := range []struct {
		name  string
		jm    *jobManager
		jobID string
	}{
		{name: "root delegate", jm: root.jobManager, jobID: rootJobID},
		{name: "coordinator worker", jm: coordinator.jobManager, jobID: workerJobID},
	} {
		if got := nslpEventCount(t, check.jm, jobstore.EventJobFinished, check.jobID); got != 1 {
			t.Fatalf("%s finished events = %d, want exactly one", check.name, got)
		}
		if got := nslpEventCount(t, check.jm, jobstore.EventJobNotificationPending, check.jobID); got != 1 {
			t.Fatalf("%s pending notification events = %d, want exactly one", check.name, got)
		}
		if got := nslpEventCount(t, check.jm, jobstore.EventJobNotificationDelivered, check.jobID); got != 1 {
			t.Fatalf("%s delivered notification events = %d, want exactly one", check.name, got)
		}
	}
	// The worker is owned by the coordinator, but its terminal lifecycle is
	// forwarded to the root exactly once for parent-visible job history.
	if got := nslpEventCount(t, root.jobManager, jobstore.EventJobFinished, workerJobID); got != 1 {
		t.Fatalf("root forwarded worker finished events = %d, want exactly one", got)
	}
	if got := nslpEventCount(t, root.jobManager, jobstore.EventJobNotificationPending, workerJobID); got != 1 {
		t.Fatalf("root forwarded worker pending events = %d, want exactly one", got)
	}
	if outstanding, err := root.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("tree outstanding after drain = %v, err=%v", outstanding, err)
	}
}

func nslpEventCount(t *testing.T, jm *jobManager, kind jobstore.EventKind, jobID string) int {
	t.Helper()
	events, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("load events for %q: %v", jobID, err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == kind && event.JobID == jobID {
			count++
		}
	}
	return count
}

func nslpAssertDescendantRouting(t *testing.T, root, coordinator, worker *Session, rootJobID, workerJobID string) {
	t.Helper()
	owner, rec := root.ownerJobManagerFor(workerJobID)
	if owner != coordinator.jobManager || rec == nil || rec.OwnerSessionID != coordinator.ID() {
		t.Fatalf("direct owner route = (%p, %+v), want coordinator %p", owner, rec, coordinator.jobManager)
	}
	resolvedJM, resolvedRec, err := root.nestedOrLocalJobManager(workerJobID)
	if err != nil || resolvedJM != coordinator.jobManager || resolvedRec == nil {
		t.Fatalf("nested owner route = (%p, %+v, %v), want coordinator manager", resolvedJM, resolvedRec, err)
	}

	rows, _, err := root.walkDescendantJobs(listFilter{IncludeDescendants: true})
	if err != nil {
		t.Fatalf("walkDescendantJobs: %v", err)
	}
	nslpAssertRows(t, rows, map[string]nslpRowWant{
		rootJobID:   {owner: root.ID(), depth: 0},
		workerJobID: {owner: coordinator.ID(), depth: 1},
	})
	if worker.jobManager.currentParentJobID() != workerJobID {
		t.Fatalf("worker parent job = %q, want %q", worker.jobManager.currentParentJobID(), workerJobID)
	}
}

type nslpRowWant struct {
	owner string
	depth int
}

func nslpAssertRows(t *testing.T, rows []jobListEntry, wants map[string]nslpRowWant) {
	t.Helper()
	seen := make(map[string]int, len(rows))
	for _, row := range rows {
		seen[row.JobID]++
	}
	for id, want := range wants {
		if seen[id] != 1 {
			t.Fatalf("descendant row count for %q = %d, want exactly one; rows=%+v", id, seen[id], rows)
		}
		for _, row := range rows {
			if row.JobID != id {
				continue
			}
			if row.OwnerSessionID != want.owner || row.Depth != want.depth {
				t.Fatalf("descendant row %q = owner %q depth %d, want owner %q depth %d", id, row.OwnerSessionID, row.Depth, want.owner, want.depth)
			}
		}
	}
}

func nslpAddNestedRecord(t *testing.T, root, coordinator, worker *Session, workerSub *subagent, rootJobID, workerJobID string, program nslpProgram) {
	t.Helper()
	// createShell only opens the job record/output store. It never calls an
	// ExecutionEnvironment or starts a process; finalizing directly keeps this
	// branch inside the no-subprocess fuzz safety boundary.
	virtual, err := worker.jobManager.createShell(createShellOpts{
		Command:     "nslp virtual " + program.task,
		Description: "nslp nested record",
	})
	if err != nil {
		t.Fatalf("create nested record: %v", err)
	}
	// The root's direct delegate is already terminal, but it still owns a live
	// coordinator subtree. Stopping that handle must find the coordinator and
	// cascade the stop into this grandchild record. include_children also drives
	// the parent-side child scan; every job it sees here is already terminal.
	if _, err := jobStopTool(context.Background(), root, map[string]any{
		"job_id":           rootJobID,
		"include_children": true,
	}, jobToolResultDefaultMaxChar); err != nil {
		t.Fatalf("cascade stop through root delegate: %v", err)
	}
	worker.jobManager.mu.Lock()
	run := worker.jobManager.running[virtual.JobID]
	stopStatus := jobstore.Status("")
	if run != nil {
		stopStatus = run.stopStatus
	}
	worker.jobManager.mu.Unlock()
	if stopStatus != jobstore.StatusCancelled {
		t.Fatalf("nested record stop status = %q, want %q", stopStatus, jobstore.StatusCancelled)
	}
	if err := worker.jobManager.finalize(virtual.JobID, jobstore.StatusCancelled, "stopped_by_parent", nil); err != nil {
		t.Fatalf("finalize cancelled nested record: %v", err)
	}
	nslpDriveChildNotifications(t, coordinator, workerSub)

	owner, ownerSession, ownerParent, rec, ok := root.resolveDescendantJobOwner(virtual.JobID)
	if !ok || owner != worker.jobManager || ownerSession != worker || ownerParent != coordinator || rec == nil {
		t.Fatalf("deep owner route = (%p, %p, %p, %+v, %v)", owner, ownerSession, ownerParent, rec, ok)
	}
	if _, err := root.stopNestedOrLocal(virtual.JobID); err == nil || !strings.Contains(err.Error(), "not_controllable") || !strings.Contains(err.Error(), rootJobID) {
		t.Fatalf("deep stop guidance = %v, want not_controllable naming %q", err, rootJobID)
	}

	rows, _, err := root.walkDescendantJobs(listFilter{IncludeDescendants: true})
	if err != nil {
		t.Fatalf("walk descendants after nested record: %v", err)
	}
	nslpAssertRows(t, rows, map[string]nslpRowWant{
		rootJobID:     {owner: root.ID(), depth: 0},
		workerJobID:   {owner: coordinator.ID(), depth: 1},
		virtual.JobID: {owner: worker.ID(), depth: 2},
	})
	if got := nslpEventCount(t, worker.jobManager, jobstore.EventJobFinished, virtual.JobID); got != 1 {
		t.Fatalf("nested record finished events = %d, want exactly one", got)
	}
	if got := nslpEventCount(t, worker.jobManager, jobstore.EventJobNotificationDelivered, virtual.JobID); got != 1 {
		t.Fatalf("nested record delivered notification events = %d, want exactly one", got)
	}
	if outstanding, err := root.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("tree outstanding after nested record drive = %v, err=%v", outstanding, err)
	}
}

func nslpManagerOwnershipProgram(t *testing.T, childID string) {
	t.Helper()
	manager := newSubagentManager(nil, 0)
	first, pending, leader, err := manager.beginReconstruction(childID)
	if err != nil || first != nil || pending == nil || !leader {
		t.Fatalf("first reconstruction = (%+v, %p, %v, %v)", first, pending, leader, err)
	}
	second, samePending, secondLeader, err := manager.beginReconstruction(childID)
	if err != nil || second != nil || samePending != pending || secondLeader {
		t.Fatalf("coalesced reconstruction = (%+v, %p, %v, %v)", second, samePending, secondLeader, err)
	}

	recovered := &subagent{id: childID}
	owned, inserted, err := manager.trackIfAbsent(recovered)
	if err != nil || !inserted || owned != recovered {
		t.Fatalf("track reconstructed child = (%+v, %v, %v)", owned, inserted, err)
	}
	manager.finishReconstruction(childID, pending, recovered, nil)
	if got, err := pending.wait(); err != nil || got != recovered {
		t.Fatalf("reconstruction wait = (%+v, %v), want recovered child", got, err)
	}
	contender := &subagent{id: childID}
	owned, inserted, err = manager.trackIfAbsent(contender)
	if err != nil || inserted || owned != recovered {
		t.Fatalf("duplicate runtime ownership = (%+v, %v, %v)", owned, inserted, err)
	}

	finishSideEffects, err := manager.beginReconstructionSideEffects(childID, recovered)
	if err != nil {
		t.Fatalf("begin reconstruction side effects: %v", err)
	}
	finishSideEffects()
	manager.waitForReconstructionSideEffects()

	_, pendingAgain, leaderAgain, err := manager.beginReconstruction(childID + "-pending")
	if err != nil || pendingAgain == nil || !leaderAgain {
		t.Fatalf("second reconstruction setup = (%p, %v, %v)", pendingAgain, leaderAgain, err)
	}
	waited := make(chan struct{})
	go func() {
		manager.waitForReconstructions()
		close(waited)
	}()
	manager.finishReconstruction(childID+"-pending", pendingAgain, nil, nil)
	nslpWaitDone(t, waited, "reconstruction waiters")

	manager.remove(childID)
	if got := manager.get(childID); got != nil {
		t.Fatalf("remove retained child %q", childID)
	}
	manager.drainForClose()
	if _, _, _, err := manager.beginReconstruction("after-close"); err != errSubagentManagerClosing {
		t.Fatalf("begin reconstruction after close = %v, want %v", err, errSubagentManagerClosing)
	}
	if _, _, err := manager.trackIfAbsent(&subagent{id: "after-close"}); err != errSubagentManagerClosing {
		t.Fatalf("track after close = %v, want %v", err, errSubagentManagerClosing)
	}
}

// nslpForwardRecoveryProgram models a child that reached a durable terminal
// state in isolated test storage while its parent was unavailable. Re-opening
// the forwarding link must replay exactly one started/finished/pending triplet
// to the parent; it must not
// invent another terminal generation or enqueue the non-owner parent's rail.
func nslpForwardRecoveryProgram(t *testing.T) {
	t.Helper()
	clock := agenttest.NewFakeClock()
	parent, err := newJobManagerNoSync(t.TempDir(), testParentSessionID, nil)
	if err != nil {
		t.Fatalf("new recovery parent manager: %v", err)
	}
	child, err := newJobManagerNoSync(t.TempDir(), testChildSessionID, nil)
	if err != nil {
		_ = parent.closeStoreOnly()
		t.Fatalf("new recovery child manager: %v", err)
	}
	t.Cleanup(func() {
		_ = child.closeStoreOnly()
		_ = parent.closeStoreOnly()
	})
	child.clock = clock
	child.now = clock.Now
	child.parentJobID = "nslp-recovery-parent-job"

	// createShell only creates the durable record/output holder; no executor is
	// attached and no command runs. Leaving forward nil models the lost parent
	// link until the record is terminal.
	rec, err := child.createShell(createShellOpts{Command: "nslp recovery record"})
	if err != nil {
		t.Fatalf("create recovery record: %v", err)
	}
	if err := child.finalize(rec.JobID, jobstore.StatusCompleted, "nslp_complete", nil); err != nil {
		t.Fatalf("finalize recovery record: %v", err)
	}
	child.forward = parent.forwardEvent
	if err := child.recoverForwardedTerminalEvents(); err != nil {
		t.Fatalf("recover forwarded terminal events: %v", err)
	}
	if err := child.recoverForwardedPendingNotifications(); err != nil {
		t.Fatalf("recover forwarded pending notification: %v", err)
	}
	for _, kind := range []jobstore.EventKind{
		jobstore.EventJobStarted,
		jobstore.EventJobFinished,
		jobstore.EventJobNotificationPending,
	} {
		if got := nslpEventCount(t, parent, kind, rec.JobID); got != 1 {
			t.Fatalf("recovered %s events = %d, want exactly one", kind, got)
		}
	}
	if got := nslpEventCount(t, parent, jobstore.EventJobNotificationDelivered, rec.JobID); got != 0 {
		t.Fatalf("non-owner parent delivered recovered notification %d times", got)
	}
}

func nslpWatchCallbackProgram(t *testing.T, program nslpProgram) {
	t.Helper()
	clock := agenttest.NewFakeClock()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return agenttest.FinalResponse("unused callback provider")
		},
	})
	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		NoProjectPrompts: true,
		clock:            clock,
	}
	cfg.testOnly = testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}
	callbackSession, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), &agenttest.DenyEnv{
		WorkDir: t.TempDir(),
		Seed:    program.seed + 1,
	}, cfg)
	if err != nil {
		t.Fatalf("NewSession(callback): %v", err)
	}
	defer callbackSession.Close()

	var (
		mu       sync.Mutex
		routes   []string
		marked   int
		attempts int
	)
	callbackSession.cfg.spawn.parentJobID = "nslp-callback-job"
	callbackSession.cfg.spawn.parentSteerDelivered = func(message string, _ *provenance.Causal, _ string) bool {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		routes = append(routes, message)
		return program.acceptCallback
	}
	callbackSession.cfg.spawn.parentMarkCallerCallbackDelivered = func(jobID string) {
		mu.Lock()
		defer mu.Unlock()
		if jobID != "nslp-callback-job" {
			t.Fatalf("callback marked job %q", jobID)
		}
		marked++
	}
	callbackSession.setActiveEntryKind(EntryWatchDelivery)

	args := map[string]any{
		"message":  program.callbackMessage,
		"end_turn": true,
		"output": map[string]any{
			"message":   program.callbackMessage,
			"data":      map[string]any{"task": program.task},
			"artifacts": []any{},
		},
	}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal callback communicate args: %v", err)
	}
	for i := 0; i < 2; i++ {
		res := callbackSession.reg.ExecuteCall(context.Background(), callbackSession.env, llm.ToolCallData{
			ID:        fmt.Sprintf("nslp-callback-%d", i),
			Name:      "communicate",
			Arguments: b,
			Type:      "function",
		})
		if res.IsError {
			t.Fatalf("callback communicate %d: %s", i, res.Output)
		}
	}

	mu.Lock()
	gotRoutes := append([]string(nil), routes...)
	gotAttempts := attempts
	gotMarked := marked
	mu.Unlock()
	wantRoutes := 2
	wantMarked := 0
	if program.acceptCallback {
		wantRoutes = 1
		wantMarked = 1
	}
	if gotAttempts != wantRoutes || len(gotRoutes) != wantRoutes || gotMarked != wantMarked {
		t.Fatalf("callback routing attempts=%d routes=%q marked=%d, want %d/%d/%d", gotAttempts, gotRoutes, gotMarked, wantRoutes, wantRoutes, wantMarked)
	}
	if len(gotRoutes) > 0 && !strings.Contains(gotRoutes[0], program.callbackMessage) {
		t.Fatalf("callback text %q does not contain %q", gotRoutes[0], program.callbackMessage)
	}
	callbackSession.setActiveEntryKind(EntryUserInput)
	callbackSession.deliverWatchCommunicateCallback("must not route outside a watch delivery")
	mu.Lock()
	deferredAttempts := attempts
	mu.Unlock()
	if deferredAttempts != gotAttempts {
		t.Fatalf("non-watch callback changed route count %d -> %d", gotAttempts, deferredAttempts)
	}
	var nilSession *Session
	nilSession.deliverWatchCommunicateCallback("nil is a no-op")
}
