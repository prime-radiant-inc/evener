package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type stableDelegateShellTree struct {
	controller *delegateTreeController
	root       *Session
	child      *Session
	grandchild *Session
	rootJM     *jobManager
	childJM    *jobManager
	grandJM    *jobManager
}

func TestStableDelegateShell_ParentDelegateIDReplacesSyntheticParentJob(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	rec := createStableDelegateShell(t, tree.childJM, "typed parent")

	stored := loadStableShellRecord(t, tree.childJM, rec.JobID)
	fields := stableShellRecordFields(t, stored)
	if got := fields["parent_delegate_id"]; got != "dlg_child" {
		t.Fatalf("shell parent_delegate_id = %v, want dlg_child", got)
	}
	if got := fields["parent_job_id"]; got != nil {
		t.Fatalf("shell retained synthetic parent_job_id = %v", got)
	}
	if !strings.HasPrefix(rec.JobID, "job_") || rec.Type != jobstore.JobShell {
		t.Fatalf("stable delegate shell identity = %q/%q, want job_*/shell", rec.JobID, rec.Type)
	}
}

func TestStableDelegateShell_CompletionAttentionReachesDirectOwner(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	rec := createStableDelegateShell(t, tree.childJM, "notify direct owner")
	finishStableDelegateShell(t, tree.childJM, rec.JobID)

	stored := loadStableShellRecord(t, tree.childJM, rec.JobID)
	attentionID := stableShellAttentionID(rec.JobID, stored.TerminalGen)
	childFold, err := readDelegateAttentionFold(transcriptPath(tree.controller.stateDir, tree.child.ID()), tree.child.ID())
	if err != nil {
		t.Fatalf("read direct owner attention: %v", err)
	}
	message, pending := childFold.content[attentionID]
	if !pending {
		t.Fatalf("direct owner attention missing exact shell identity %q", attentionID)
	}
	rootFold, err := readDelegateAttentionFold(transcriptPath(tree.controller.stateDir, tree.root.ID()), tree.root.ID())
	if err != nil {
		t.Fatalf("read ancestor attention: %v", err)
	}
	if _, leaked := rootFold.content[attentionID]; leaked {
		t.Fatalf("ancestor transcript received child-owned shell attention %q", attentionID)
	}
	if !strings.Contains(message.Text(), `<job-notification job_id="`+rec.JobID+`"`) ||
		!strings.Contains(message.Text(), `job_type="shell"`) || strings.Contains(message.Text(), "delegate-notification") {
		t.Fatalf("shell completion attention = %q", message.Text())
	}
	if stored.TerminalGen == "" || stored.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("terminal shell = %+v, want durable generation and acknowledged direct-owner attention", stored)
	}
	if got := tree.child.peekNotifications(); got != 0 {
		t.Fatalf("direct owner retained %d legacy shell notifications", got)
	}

	retryTree := newStableDelegateShellTree(t)
	retryRec := createStableDelegateShell(t, retryTree.childJM, "retry direct owner arm")
	readCalls := 0
	injected := errors.New("injected stable shell arm failure")
	retryTree.child.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		readCalls++
		if readCalls == 3 {
			return delegateAttentionFold{}, injected
		}
		return readDelegateAttentionFold(path, sessionID)
	}
	code := 0
	if err := retryTree.childJM.finalize(retryRec.JobID, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, injected) {
		t.Fatalf("first shell finalization error = %v, want %v", err, injected)
	}
	retryStored := loadStableShellRecord(t, retryTree.childJM, retryRec.JobID)
	if retryStored.NotifyState != jobstore.NotifyPending {
		t.Fatalf("failed shell arm notify state = %q, want pending", retryStored.NotifyState)
	}
	retryTree.childJM.mu.Lock()
	_, running := retryTree.childJM.running[retryRec.JobID]
	retryTree.childJM.mu.Unlock()
	if !running {
		t.Fatal("failed shell arm removed its finalization source")
	}
	retryTree.child.cfg.testOnly.delegateAttentionReadFold = nil
	if err := retryTree.childJM.finalize(retryRec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("retry shell finalization: %v", err)
	}
	retryStored = loadStableShellRecord(t, retryTree.childJM, retryRec.JobID)
	retryAttentionID := stableShellAttentionID(retryRec.JobID, retryStored.TerminalGen)
	if retryStored.NotifyState != jobstore.NotifyDelivered || countAttentionEntries(t, transcriptPath(retryTree.controller.stateDir, retryTree.child.ID()), retryAttentionID) != 1 {
		t.Fatalf("retried shell source = state:%q attention:%d, want delivered/1", retryStored.NotifyState, countAttentionEntries(t, transcriptPath(retryTree.controller.stateDir, retryTree.child.ID()), retryAttentionID))
	}
}

func TestStableDelegateShell_CompletionUsesExactDurableAttention(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	path := transcriptPath(tree.controller.stateDir, tree.child.ID())
	rec := createStableDelegateShell(t, tree.childJM, "exact durable attention")
	lease := delegateLease{delegateID: "dlg_child", generation: 1}
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "owner idle", communicated: true})
	continued, _, err := tree.controller.prepareSettlementForTest(lease, finish.packet)
	if err != nil || continued {
		t.Fatalf("settle owning delegate generation = continued:%t err:%v", continued, err)
	}
	_, err = tree.controller.FinishGeneration(lease, finish)
	if err != nil {
		t.Fatalf("finish owning delegate generation: %v", err)
	}
	finishStableDelegateShell(t, tree.childJM, rec.JobID)

	stored := loadStableShellRecord(t, tree.childJM, rec.JobID)
	attentionID := "shell:" + rec.JobID + ":" + stored.TerminalGen
	fold, err := readDelegateAttentionFold(path, tree.child.ID())
	if err != nil {
		t.Fatalf("read shell attention: %v", err)
	}
	message, pending := fold.content[attentionID]
	if !pending || !strings.Contains(message.Text(), `<job-notification job_id="`+rec.JobID+`"`) {
		t.Fatalf("shell attention %q = %#v, want exact durable terminal block", attentionID, message)
	}
	if stored.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("terminal shell notify state = %q, want delivered after receiver fsync", stored.NotifyState)
	}
	if got := tree.child.peekNotifications(); got != 0 {
		t.Fatalf("stable shell left %d legacy notification tokens", got)
	}
}

func TestStableDelegateShell_AncestorCanSeeDescendantShell(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	rec := createStableDelegateShell(t, tree.grandJM, "visible descendant")

	out, err := jobListTool(tree.root, map[string]any{"include_descendants": true}, 1<<20)
	if err != nil {
		t.Fatalf("job_list(include_descendants): %v", err)
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &payload); err != nil {
		t.Fatalf("decode descendant list: %v", err)
	}
	row := stableShellJobRow(payload.Items, rec.JobID)
	if row == nil {
		t.Fatalf("ancestor items = %+v, want descendant shell %q", payload.Items, rec.JobID)
	}
	if got := row["parent_delegate_id"]; got != "dlg_grandchild" {
		t.Fatalf("descendant public parent_delegate_id = %v, want dlg_grandchild", got)
	}
	if got := row["parent_job_id"]; got != nil {
		t.Fatalf("descendant public row retained synthetic parent_job_id = %v", got)
	}
}

func TestStableDelegateShell_AncestorCannotControlWithoutDirectDelegateHandle(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	rec := createStableDelegateShell(t, tree.grandJM, "direct edge only")
	var signals atomic.Int32
	tree.grandJM.setShellSignal(tree.grandJM.running[rec.JobID], func() { signals.Add(1) })

	if _, err := tree.root.stopNestedOrLocal(rec.JobID); err == nil || !strings.Contains(err.Error(), `direct delegate "dlg_child"`) {
		t.Fatalf("ancestor stop error = %v, want direct delegate dlg_child guidance", err)
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("ancestor stop signalled descendant shell %d times, want 0", got)
	}
	if _, err := tree.child.stopNestedOrLocal(rec.JobID); err != nil {
		t.Fatalf("direct stable delegate owner could not stop job-addressed shell: %v", err)
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("direct owner stop signals = %d, want 1", got)
	}
}

func TestStableDelegateShell_OutputStatusWatchAndStopRemainJobAddressed(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	rec := createStableDelegateShell(t, tree.childJM, "job controls")
	var signals atomic.Int32
	tree.childJM.setShellSignal(tree.childJM.running[rec.JobID], func() { signals.Add(1) })
	output := tree.childJM.running[rec.JobID].output
	if _, err := tree.childJM.appendJobOutput(rec.JobID, output, []byte("stable shell output\n")); err != nil {
		t.Fatalf("append shell output: %v", err)
	}

	status, err := jobStatusTool(tree.root, map[string]any{"target": rec.JobID}, 1<<20)
	if err != nil {
		t.Fatalf("job_status(%s): %v", rec.JobID, err)
	}
	var statusState map[string]any
	if err := json.Unmarshal(handlerJSON(t, status), &statusState); err != nil {
		t.Fatalf("decode job_status: %v", err)
	}
	if statusState["job_id"] != rec.JobID || statusState["kind"] != "shell" {
		t.Fatalf("job_status state = %+v, want job-addressed shell", statusState)
	}

	ownerJM, ownerRec, err := tree.root.nestedOrLocalJobManager(rec.JobID)
	if err != nil || ownerJM != tree.childJM || ownerRec == nil {
		t.Fatalf("job output route = manager %p record %+v err %v, want direct child manager", ownerJM, ownerRec, err)
	}
	text, _, _, err := ownerJM.readOutput(rec.JobID, 1024)
	if err != nil || text != "stable shell output\n" {
		t.Fatalf("job output = %q, %v", text, err)
	}

	watch, err := jobWatchTool(tree.root, map[string]any{
		"operation":    "create",
		"source":       rec.JobID,
		"output_match": "next line",
	}, 1<<20)
	if err != nil {
		t.Fatalf("job_watch(%s): %v", rec.JobID, err)
	}
	if !strings.Contains(string(handlerJSON(t, watch)), rec.JobID) {
		t.Fatalf("job watch state = %s, want job source %q", handlerJSON(t, watch), rec.JobID)
	}

	stopped, err := jobStopTool(context.Background(), tree.root, map[string]any{"target": rec.JobID}, 1<<20)
	if err != nil {
		t.Fatalf("job_stop(%s): %v", rec.JobID, err)
	}
	if !strings.Contains(string(handlerJSON(t, stopped)), rec.JobID) || signals.Load() != 1 {
		t.Fatalf("job stop = %s signals=%d, want job-addressed stop", handlerJSON(t, stopped), signals.Load())
	}
}

func TestStableDelegateShell_RestartRepairsCompletionAttentionOnce(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	rec := createStableDelegateShell(t, tree.childJM, "repair after restart")
	run := tree.childJM.running[rec.JobID]
	if err := run.output.Close(); err != nil {
		t.Fatalf("close crashed shell output: %v", err)
	}
	if err := tree.childJM.closeStoreOnly(); err != nil {
		t.Fatalf("close crashed shell store: %v", err)
	}

	plan := delegateShellRepairPlan{
		delegateID:    "dlg_child",
		storePath:     filepath.Join(jobsDir(tree.controller.stateDir, tree.child.ID()), "jobs.jsonl"),
		runningJobIDs: []string{rec.JobID},
	}
	for i := range 2 {
		if err := executeDelegateShellRepair(plan, time.Unix(200+int64(i), 0).UTC()); err != nil {
			t.Fatalf("restart shell repair %d: %v", i+1, err)
		}
	}

	restoredJM, err := newJobManagerNoSync(tree.controller.stateDir, tree.child.ID(), tree.child.enqueueJobNotificationAndNotify)
	if err != nil {
		t.Fatalf("reopen child job manager: %v", err)
	}
	t.Cleanup(func() { _ = restoredJM.closeStoreOnly() })
	restoredJM.delegateController = tree.controller
	tree.child.jobManager = restoredJM
	bindStableDelegateActivity(tree.child, tree.controller, delegateLease{delegateID: "dlg_child", generation: 1})
	if err := restoredJM.recoverForwardedTerminalEvents(); err != nil {
		t.Fatalf("recover forwarded terminal shell: %v", err)
	}
	if err := restoredJM.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm repaired shell completion: %v", err)
	}
	if err := restoredJM.recoverForwardedPendingNotifications(); err != nil {
		t.Fatalf("recover forwarded shell attention: %v", err)
	}

	events, err := restoredJM.store.LoadEvents()
	if err != nil {
		t.Fatalf("load repaired shell events: %v", err)
	}
	finished, pending := 0, 0
	for _, event := range events {
		if event.JobID != rec.JobID {
			continue
		}
		switch event.Kind {
		case jobstore.EventJobFinished:
			finished++
		case jobstore.EventJobNotificationPending:
			pending++
		}
	}
	if finished != 1 || pending != 1 {
		t.Fatalf("repaired shell durable events = finished:%d pending:%d, want 1/1", finished, pending)
	}
	attentionID := stableShellAttentionID(rec.JobID, loadStableShellRecord(t, restoredJM, rec.JobID).TerminalGen)
	fold, err := readDelegateAttentionFold(transcriptPath(tree.controller.stateDir, tree.child.ID()), tree.child.ID())
	if err != nil {
		t.Fatalf("read repaired shell attention: %v", err)
	}
	message, pendingAttention := fold.content[attentionID]
	if !pendingAttention || !strings.Contains(message.Text(), `<job-notification job_id="`+rec.JobID+`"`) || !strings.Contains(message.Text(), `job_type="shell"`) {
		t.Fatalf("repaired shell completion attention %q = %#v", attentionID, message)
	}
	if got := tree.child.peekNotifications(); got != 0 {
		t.Fatalf("repaired shell queued %d legacy notification tokens", got)
	}
	forwarded := loadStableShellRecord(t, tree.rootJM, rec.JobID)
	if !forwarded.Status.IsTerminal() || forwarded.TerminalGen == "" {
		t.Fatalf("ancestor repaired shell visibility = %+v, want terminal generation", forwarded)
	}
}

func TestStableDelegateShell_ColdRestartRearmsExactCompletionAttention(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	childJM, err := newJobManagerNoSync(fixture.stateDir, fixture.childID, nil)
	if err != nil {
		t.Fatalf("open cold child job manager: %v", err)
	}
	childJM.parentDelegateID = fixture.delegateID
	childJM.now = func() time.Time { return time.Unix(300, 0).UTC() }
	causal := provenance.WithWatch(nil, "opaque-cold-watch-681c", "opaque-cold-generation-923d", "opaque-cold-delivery-c549", fixture.childID, "opaque-cold-origin-7f25")
	childJM.currentProvenance = func() *provenance.Causal { return causal }
	rec, err := childJM.createShell(createShellOpts{Command: "true", Description: "cold shell attention"})
	if err != nil {
		_ = childJM.closeStoreOnly()
		t.Fatalf("create cold stable shell: %v", err)
	}
	code := 0
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		_ = childJM.closeStoreOnly()
		t.Fatalf("finish cold stable shell: %v", err)
	}
	attentionID := stableShellAttentionID(rec.JobID, loadStableShellRecord(t, childJM, rec.JobID).TerminalGen)
	if err := childJM.closeStoreOnly(); err != nil {
		t.Fatalf("close cold child job manager: %v", err)
	}

	attentionSeen := make(chan struct{})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(request llm.Request) llm.Response {
			if !requestContainsText(request, `<job-notification job_id="`+rec.JobID+`"`) {
				t.Error("cold child request omitted the exact durable terminal block")
			}
			close(attentionSeen)
			return communicateResponse(true, "cold shell completion handled")
		},
		func(llm.Request) llm.Response {
			select {
			case <-attentionSeen:
			default:
				t.Error("root completion request preceded child shell observation")
			}
			return communicateResponse(true, "root drained cold shell delegate completion")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	if got := len(fixture.adapter.Requests()); got != 0 {
		t.Fatalf("provider requests during cold shell repair = %d, want 0", got)
	}
	for range 2 {
		matches := 0
		for _, entry := range notificationRawEntries(t, transcriptPath(fixture.stateDir, fixture.childID)) {
			if entry.Turn.AttentionID == attentionID {
				matches++
				if !reflect.DeepEqual(entry.Turn.NotificationProvenance, causal) {
					t.Fatal("cold first custody/replay changed persisted notification provenance")
				}
			}
		}
		if matches != 1 {
			t.Fatalf("cold shell canonical identities=%d want1", matches)
		}
		if err := repairStableShellAttentionForBootstrap(root.delegateController); err != nil {
			t.Fatal(err)
		}
	}
	// The child owns this notification. Root readiness is not permission for
	// selecting its executor, and the parent's model need not run first.
	wake := drainWakeForTest(root)
	if _, err := root.DrainJobTree(context.Background(), wake); err != nil {
		t.Fatalf("drain cold shell attention: %v", err)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	select {
	case <-attentionSeen:
	default:
		t.Fatal("cold shell attention generation omitted the exact durable terminal block")
	}
	restoredJM, err := newJobManagerNoSync(fixture.stateDir, fixture.childID, nil)
	if err != nil {
		t.Fatalf("reopen cold child job manager: %v", err)
	}
	t.Cleanup(func() { _ = restoredJM.closeStoreOnly() })
	stored := loadStableShellRecord(t, restoredJM, rec.JobID)
	attentionID = stableShellAttentionID(rec.JobID, stored.TerminalGen)
	fold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.childID), fixture.childID)
	if err != nil {
		t.Fatalf("read cold shell attention: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("cold shell attention resolution = %q, want consumed", got)
	}
	if stored.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("cold shell notification state = %q, want delivered", stored.NotifyState)
	}
}

func newStableDelegateShellTree(t *testing.T) *stableDelegateShellTree {
	t.Helper()
	controller, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerRunning(t, controller, "dlg_child", "")
	seedDelegateControllerRunning(t, controller, "dlg_grandchild", "dlg_child")

	newManager := func(sessionID string) *jobManager {
		jm, err := newJobManagerNoSync(controller.stateDir, sessionID, nil)
		if err != nil {
			t.Fatalf("new job manager %s: %v", sessionID, err)
		}
		jm.now = func() time.Time { return time.Unix(100, 0).UTC() }
		jm.newJobID = func(string) (string, error) {
			return "job_" + strings.ReplaceAll(sessionID, "-", "_"), nil
		}
		jm.delegateController = controller
		return jm
	}
	rootJM := newManager("root-session")
	childJM := newManager("child-dlg_child")
	grandJM := newManager("child-dlg_grandchild")
	root := &Session{
		id: "root-session", stateDir: controller.stateDir, jobManager: rootJM,
		delegateController: controller, delegateRootSessionID: "root-session",
		subagents: newSubagentManager(nil, 0), state: SessionIdle,
	}
	child := &Session{
		id: "child-dlg_child", stateDir: controller.stateDir, jobManager: childJM,
		delegateController: controller, delegateRootSessionID: "root-session", owningDelegateID: "dlg_child",
		subagents: newSubagentManager(nil, 0), state: SessionIdle,
	}
	grandchild := &Session{
		id: "child-dlg_grandchild", stateDir: controller.stateDir, jobManager: grandJM,
		delegateController: controller, delegateRootSessionID: "root-session", owningDelegateID: "dlg_grandchild",
		subagents: newSubagentManager(nil, 0), state: SessionIdle,
	}
	for _, session := range []*Session{root, child, grandchild} {
		writer, err := transcript.NewWriter(transcriptPath(controller.stateDir, session.ID()), transcript.Header{SessionID: session.ID()})
		if err != nil {
			t.Fatalf("create %s transcript: %v", session.ID(), err)
		}
		session.attachTranscript(writer)
		t.Cleanup(func() { _ = session.closeAttachedTranscript() })
	}
	rootJM.enqueue = root.enqueueJobNotificationAndNotify
	childJM.enqueue = child.enqueueJobNotificationAndNotify
	grandJM.enqueue = grandchild.enqueueJobNotificationAndNotify
	root.subagents.track(&subagent{id: child.id, sess: child, status: SubagentRunning})
	child.subagents.track(&subagent{id: grandchild.id, sess: grandchild, status: SubagentRunning})
	controller.mu.Lock()
	controller.rootRuntime = root
	controller.live["dlg_child"].runtime = child
	controller.live["dlg_child"].binding.runtime = child
	controller.live["dlg_grandchild"].runtime = grandchild
	controller.live["dlg_grandchild"].binding.runtime = grandchild
	controller.mu.Unlock()
	bindStableDelegateActivity(child, controller, delegateLease{delegateID: "dlg_child", generation: 1})
	bindStableDelegateActivity(grandchild, controller, delegateLease{delegateID: "dlg_grandchild", generation: 1})

	tree := &stableDelegateShellTree{
		controller: controller, root: root, child: child, grandchild: grandchild,
		rootJM: rootJM, childJM: childJM, grandJM: grandJM,
	}
	t.Cleanup(func() {
		for _, jm := range []*jobManager{grandJM, childJM, rootJM} {
			jm.mu.Lock()
			ids := make([]string, 0, len(jm.running))
			for id := range jm.running {
				ids = append(ids, id)
			}
			jm.mu.Unlock()
			for _, id := range ids {
				code := 0
				_ = jm.finalize(id, jobstore.StatusCompleted, "test_cleanup", &code)
			}
		}
		_ = grandJM.closeStoreOnly()
		_ = childJM.closeStoreOnly()
		_ = rootJM.closeStoreOnly()
	})
	return tree
}

func createStableDelegateShell(t *testing.T, jm *jobManager, description string) *jobstore.JobRecord {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30", Description: description})
	if err != nil {
		t.Fatalf("create stable delegate shell: %v", err)
	}
	return rec
}

func finishStableDelegateShell(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	code := 0
	if err := jm.finalize(jobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize stable delegate shell: %v", err)
	}
}

func stableShellRecordFields(t *testing.T, rec *jobstore.JobRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal shell record: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode shell record: %v", err)
	}
	return fields
}

func stableShellJobRow(rows []map[string]any, jobID string) map[string]any {
	for _, row := range rows {
		if row["job_id"] == jobID {
			return row
		}
	}
	return nil
}

func loadStableShellRecord(t *testing.T, jm *jobManager, jobID string) *jobstore.JobRecord {
	t.Helper()
	records, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load shell records: %v", err)
	}
	rec := records[jobID]
	if rec == nil {
		t.Fatalf("shell record %q missing", jobID)
	}
	return rec
}

// A receiver wake closes the real source descriptor after custody, before ack.
func TestShellReplayPreservesAcceptedMachineryMessage(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	causal := provenance.WithWatch(nil, "opaque-shell-watch-59bf", "opaque-shell-generation-723a", "opaque-shell-delivery-b453", tree.child.ID(), "opaque-origin-job-26ea")
	tree.childJM.currentProvenance = func() *provenance.Causal { return causal }
	rec := createStableDelegateShell(t, tree.childJM, "opaque-shell-4c72")
	tree.child.SetNotifyFunc(func() { _ = tree.childJM.store.Close() })
	code := 0
	if err := tree.childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err == nil {
		t.Fatal("closed source acknowledged custody")
	}
	tree.child.SetNotifyFunc(nil)
	path := transcriptPath(tree.controller.stateDir, tree.child.ID())
	entries := notificationRawEntries(t, path)
	if len(entries) != 1 || !entries[0].Turn.Message.Content[0].Machinery {
		t.Fatalf("live canonical entries=%+v", entries)
	}
	if causal == nil || !reflect.DeepEqual(entries[0].Turn.NotificationProvenance, causal) {
		t.Fatalf("first shell custody provenance=%+v want=%+v", entries[0].Turn.NotificationProvenance, causal)
	}
	accepted := entries[0].Turn.Message
	id := entries[0].Turn.AttentionID
	storePath := filepath.Join(jobsDir(tree.controller.stateDir, tree.child.ID()), "jobs.jsonl")
	records, _ := readDelegateShellStore(t, storePath)
	if records[rec.JobID].NotifyState != jobstore.NotifyPending {
		t.Fatal("source debt lost before replay")
	}
	if err := tree.child.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	// Crash teardown does not issue explicit Stop or turn accepted work into observation.
	tree.controller.mu.Lock()
	descriptor := cloneDelegateStartDescriptor(tree.controller.durable["dlg_child"].Descriptor)
	tree.controller.rootRuntime = nil
	tree.controller.live = nil
	tree.controller.mu.Unlock()
	if err := tree.controller.store.Close(); err != nil {
		t.Fatal(err)
	}
	if run := tree.childJM.running[rec.JobID]; run != nil {
		_ = run.output.Close()
	}
	plan := stableShellAttentionBootstrapTarget{delegateID: "dlg_child", descriptor: descriptor, storePath: storePath}
	held := errors.New("shell replay receiver barrier held")
	failedOpen := func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(producerQueryFaultFS{Fs: afero.NewOsFs(), syncErr: held}, path, id)
	}
	if err := repairStableShellAttentionTarget(plan, tree.controller.stateDir, time.Now, failedOpen); !errors.Is(err, held) {
		t.Fatalf("held receiver barrier error=%v", err)
	}
	records, _ = readDelegateShellStore(t, storePath)
	if records[rec.JobID].NotifyState != jobstore.NotifyPending {
		t.Fatal("failed custody barrier acknowledged shell source")
	}
	for range 2 {
		if err := repairStableShellAttentionTarget(plan, tree.controller.stateDir, func() time.Time { return time.Unix(400, 0).UTC() }, transcript.OpenWriterForSession); err != nil {
			t.Fatalf("cold replay: %v", err)
		}
	}
	entries = notificationRawEntries(t, path)
	if len(entries) != 1 || entries[0].Turn.AttentionID != id || !reflect.DeepEqual(entries[0].Turn.Message, accepted) {
		t.Fatal("replay changed complete accepted message or canonical identity")
	}
	if !reflect.DeepEqual(entries[0].Turn.NotificationProvenance, causal) {
		t.Fatal("cold shell replay changed persisted notification provenance")
	}
	records, _ = readDelegateShellStore(t, storePath)
	if records[rec.JobID].NotifyState != jobstore.NotifyDelivered {
		t.Fatal("cold custody did not settle exact source")
	}
	mismatched := accepted
	mismatched.Content = append([]llm.ContentPart(nil), accepted.Content...)
	mismatched.Content[0].Machinery = false
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen(path, tree.child.ID(), id, mismatched, time.Unix(401, 0).UTC(), transcript.OpenWriterForSession, nil); err == nil {
		t.Fatal("same ID with different metadata accepted")
	}
	if countAttentionEntries(t, path, id) != 1 {
		t.Fatal("integrity refusal appended duplicate")
	}
}

func TestShellBootstrapReplaysHistoricalReceiptAfterInterruptedAck(t *testing.T) {
	setup := func(t *testing.T, withProvenance bool) (coldStableDelegateFixture, stableShellAttentionBootstrapTarget, *jobstore.JobRecord, string) {
		t.Helper()
		var descriptor delegatestore.Descriptor
		fixture := newColdStableDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
			descriptor = cloneDelegateStartDescriptor(*d)
		})
		jm, err := newJobManagerNoSync(fixture.stateDir, fixture.childID, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = jm.closeStoreOnly() })
		jm.parentDelegateID = fixture.delegateID
		jm.now = func() time.Time { return time.Unix(300, 0).UTC() }
		if withProvenance {
			causal := provenance.WithWatch(nil, "opaque-historical-watch-a27f", "opaque-historical-generation-76cb", "opaque-historical-delivery-52a1", fixture.childID, "opaque-historical-origin-038d")
			jm.currentProvenance = func() *provenance.Causal { return causal }
		}
		record, err := jm.createShell(createShellOpts{Command: "true", Description: "opaque-historical-shell-68cf"})
		if err != nil {
			t.Fatal(err)
		}
		code := 0
		if err := jm.finalize(record.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
			t.Fatal(err)
		}
		record = loadStableShellRecord(t, jm, record.JobID)
		if !record.Status.IsTerminal() || record.TerminalGen == "" || record.NotifyState != jobstore.NotifyPending || (record.Provenance != nil) != withProvenance {
			t.Fatalf("historical source before interrupted ack = %+v", record)
		}
		if err := jm.closeStoreOnly(); err != nil {
			t.Fatal(err)
		}
		target := stableShellAttentionBootstrapTarget{delegateID: fixture.delegateID, descriptor: descriptor, storePath: filepath.Join(jobsDir(fixture.stateDir, fixture.childID), "jobs.jsonl")}
		return fixture, target, record, stableShellAttentionContent(target.storePath, descriptor, record)
	}
	assertSource := func(t *testing.T, target stableShellAttentionBootstrapTarget, record *jobstore.JobRecord, want jobstore.NotifyState) {
		t.Helper()
		events, err := jobstore.ReadEvents(target.storePath)
		if err != nil {
			t.Fatal(err)
		}
		stored := jobstore.Fold(events)[record.JobID]
		if stored == nil || stored.TerminalGen != record.TerminalGen || stored.NotifyState != want {
			t.Fatalf("exact historical source = %+v, want %s/%s", stored, record.TerminalGen, want)
		}
		delivered := 0
		for _, event := range events {
			if event.Kind == jobstore.EventJobNotificationDelivered {
				if event.JobID != record.JobID || event.TerminalGen != record.TerminalGen {
					t.Fatalf("acknowledged another source: %+v", event)
				}
				delivered++
			}
		}
		if (want == jobstore.NotifyPending && delivered != 0) || (want == jobstore.NotifyDelivered && delivered != 1) {
			t.Fatalf("source delivered events = %d, want state %s", delivered, want)
		}
	}
	for _, withProvenance := range []bool{false, true} {
		name := "nil_source_provenance"
		if withProvenance {
			name = "watch_source_provenance"
		}
		t.Run(name, func(t *testing.T) {
			fixture, target, record, content := setup(t, withProvenance)
			path := transcriptPath(fixture.stateDir, fixture.childID)
			id := stableShellAttentionID(record.JobID, record.TerminalGen)
			// Execute the old writer operation, stopping before its separate source ack.
			if appended, err := appendColdDelegateNotificationDurablyWithOpen(path, fixture.childID, id, content, time.Unix(301, 0).UTC(), transcript.OpenWriterForSession); err != nil || !appended {
				t.Fatalf("old receiver append = %t, %v", appended, err)
			}
			accepted := notificationRawEntries(t, path)
			if len(accepted) != 1 || !accepted[0].MachineryFlagged || !reflect.DeepEqual(accepted[0].Turn.Message, llm.User(content)) || accepted[0].Turn.NotificationProvenance != nil {
				t.Fatalf("old receipt representation = %+v", accepted)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertSource(t, target, record, jobstore.NotifyPending)
			seen := make(chan struct{})
			fixture.adapter.steps = []func(llm.Request) llm.Response{
				func(request llm.Request) llm.Response {
					if !slices.ContainsFunc(request.Messages, func(message llm.Message) bool {
						return reflect.DeepEqual(message, llm.User(content))
					}) {
						t.Error("child request omitted complete original historical receipt")
					}
					if !requestContainsText(request, `<job-notification job_id="`+record.JobID+`"`) || !requestContainsText(request, record.Description) {
						t.Error("child request omitted input-derived shell identity or payload")
					}
					close(seen)
					return communicateResponse(true, "historical shell completion handled")
				},
				func(llm.Request) llm.Response {
					select {
					case <-seen:
					default:
						t.Error("root completion preceded historical shell observation")
					}
					return communicateResponse(true, "historical shell completion drained")
				},
			}
			root := restoreSupervisionRoot(t, fixture, nil)
			if len(fixture.adapter.Requests()) != 0 {
				t.Fatal("bootstrap called provider")
			}
			for range 2 {
				assertSource(t, target, record, jobstore.NotifyDelivered)
				if err := repairStableShellAttentionForBootstrap(root.delegateController); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, before) {
					t.Fatalf("bootstrap changed historical receiver bytes: %v", err)
				}
			}
			if _, err := root.DrainJobTree(context.Background(), drainWakeForTest(root)); err != nil {
				t.Fatal(err)
			}
			waitForStableSupervisionRun(t, root, fixture.childID)
			assertNotificationObservedOnce(t, path, id)
			fold, err := readDelegateAttentionFold(path, fixture.childID)
			if err != nil || fold.resolutions[id] != delegateAttentionConsumed {
				t.Fatalf("historical receipt resolution = %q, %v", fold.resolutions[id], err)
			}
			entries := notificationRawEntries(t, path)
			if len(entries) == 0 || !reflect.DeepEqual(entries[0], accepted[0]) || countAttentionEntries(t, path, id) != 1 {
				t.Fatal("continuation rewrote or duplicated historical receipt")
			}
			root.delegateController.mu.Lock()
			aggregate := root.delegateController.durable[fixture.delegateID]
			completed := aggregate != nil && aggregate.Generation == 1 && !aggregate.CurrentRunOpen && aggregate.LatestOutcome != nil && aggregate.LatestOutcome.Status == delegatestore.OutcomeCompleted && aggregate.LatestPacket != nil && aggregate.LatestPacket.Kind == delegatestore.PacketReported
			root.delegateController.mu.Unlock()
			if !completed {
				t.Fatal("historical shell owner did not complete and report its generation")
			}
			assertSource(t, target, record, jobstore.NotifyDelivered)
		})
	}
	seedHistorical := func(t *testing.T, fixture coldStableDelegateFixture, record *jobstore.JobRecord, content string) (string, string, []byte) {
		t.Helper()
		path := transcriptPath(fixture.stateDir, fixture.childID)
		id := stableShellAttentionID(record.JobID, record.TerminalGen)
		if appended, err := appendColdDelegateNotificationDurablyWithOpen(path, fixture.childID, id, content, time.Unix(301, 0).UTC(), transcript.OpenWriterForSession); err != nil || !appended {
			t.Fatalf("old receiver append = %t, %v", appended, err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return path, id, before
	}
	assertPendingUnchanged := func(t *testing.T, fixture coldStableDelegateFixture, target stableShellAttentionBootstrapTarget, record *jobstore.JobRecord, path string, before []byte) {
		t.Helper()
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("refused recovery changed receiver bytes: %v", err)
		}
		assertSource(t, target, record, jobstore.NotifyPending)
		if len(fixture.adapter.Requests()) != 0 {
			t.Fatal("refused recovery called provider")
		}
	}
	wrongProvenance := provenance.WithWatch(nil, "opaque-wrong-watch-174c", "opaque-wrong-generation-f2a9", "opaque-wrong-delivery-0bc8", "opaque-wrong-receiver-a5d1", "opaque-wrong-origin-e34b")
	for _, control := range []struct {
		name   string
		mutate func(*transcript.Entry)
	}{
		{"wrong_payload", func(entry *transcript.Entry) { entry.Turn.Message.Content[0].Text += "x" }},
		{"extra_content", func(entry *transcript.Entry) {
			entry.Turn.Message.Content = append(entry.Turn.Message.Content, llm.ContentPart{Kind: llm.ContentText})
		}},
		{"message_metadata", func(entry *transcript.Entry) { entry.Turn.Message.Name = "opaque-wrong-name-7a39" }},
		{"wrong_role", func(entry *transcript.Entry) { entry.Turn.Message.Role = llm.RoleAssistant }},
		{"wrong_provenance", func(entry *transcript.Entry) { entry.Turn.NotificationProvenance = wrongProvenance }},
		{"current_missing_provenance", func(entry *transcript.Entry) { entry.Turn.Message.Content[0].Machinery = true }},
		{"current_wrong_provenance", func(entry *transcript.Entry) {
			entry.Turn.Message.Content[0].Machinery = true
			entry.Turn.NotificationProvenance = wrongProvenance
		}},
		{"wrong_receiver_header", nil},
		{"malformed_transcript", nil},
	} {
		t.Run("refuse_"+control.name, func(t *testing.T) {
			fixture, target, record, content := setup(t, true)
			path, id, before := seedHistorical(t, fixture, record, content)
			lines := bytes.Split(before, []byte("\n"))
			switch control.name {
			case "wrong_receiver_header":
				var header transcript.Header
				if err := json.Unmarshal(lines[0], &header); err != nil {
					t.Fatal(err)
				}
				header.SessionID = "opaque-wrong-receiver-f0c2"
				var err error
				lines[0], err = json.Marshal(header)
				if err != nil {
					t.Fatal(err)
				}
			case "malformed_transcript":
				lines = append(lines[:len(lines)-1], []byte("{malformed"), nil)
			default:
				entry := notificationRawEntries(t, path)[0]
				control.mutate(&entry)
				var err error
				lines[1], err = json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
			}
			before = bytes.Join(lines, []byte("\n"))
			if err := os.WriteFile(path, before, 0o644); err != nil {
				t.Fatal(err)
			}
			root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
			if root != nil {
				t.Cleanup(root.Close)
			}
			if err == nil {
				t.Fatal("root restore accepted conflicting receiver evidence")
			}
			assertPendingUnchanged(t, fixture, target, record, path, before)
			if _, err := appendColdDelegateAttentionMessageDurablyWithOpen(path, fixture.childID, id, llm.User(content), time.Unix(302, 0).UTC(), transcript.OpenWriterForSession); err == nil {
				t.Fatal("generic validation accepted conflicting receiver evidence")
			}
			assertPendingUnchanged(t, fixture, target, record, path, before)
		})
	}
	for _, change := range []string{"absent", "message", "provenance"} {
		t.Run("changed_at_writer_open_"+change, func(t *testing.T) {
			fixture, target, record, content := setup(t, true)
			path, _, before := seedHistorical(t, fixture, record, content)
			lines := bytes.Split(before, []byte("\n"))
			if change == "absent" {
				before = append(append([]byte(nil), lines[0]...), '\n')
			} else {
				entry := notificationRawEntries(t, path)[0]
				if change == "message" {
					entry.Turn.Message.Name = "opaque-changed-at-open-5c7e"
				} else {
					entry.Turn.NotificationProvenance = wrongProvenance
				}
				var err error
				lines[1], err = json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
				before = bytes.Join(lines, []byte("\n"))
			}
			opened := false
			open := func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				opened = true
				if err := os.WriteFile(path, before, 0o644); err != nil {
					return nil, nil, err
				}
				return transcript.OpenWriterForSession(path, id)
			}
			if err := repairStableShellAttentionTarget(target, fixture.stateDir, time.Now, open); err == nil || !opened {
				t.Fatalf("changed historical receipt at writer open = opened:%t err:%v", opened, err)
			}
			assertPendingUnchanged(t, fixture, target, record, path, before)
		})
	}
	for _, boundary := range []string{"sync", "close"} {
		t.Run("receiver_"+boundary+"_failure_then_retry", func(t *testing.T) {
			fixture, target, record, content := setup(t, true)
			path, id, before := seedHistorical(t, fixture, record, content)
			held := errors.New("historical receiver boundary held")
			syncs := 0
			fs := producerQueryFaultFS{Fs: afero.NewOsFs(), syncCount: &syncs}
			if boundary == "sync" {
				fs.syncErr = held
			} else {
				fs.closeErr = held
			}
			open := func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				return transcript.OpenWriterForSessionWithFS(fs, path, id)
			}
			if err := repairStableShellAttentionTarget(target, fixture.stateDir, time.Now, open); !errors.Is(err, held) || syncs == 0 {
				t.Fatalf("historical receiver boundary = syncs:%d err:%v", syncs, err)
			}
			assertPendingUnchanged(t, fixture, target, record, path, before)
			for range 2 {
				if err := repairStableShellAttentionTarget(target, fixture.stateDir, time.Now, transcript.OpenWriterForSession); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) || countAttentionEntries(t, path, id) != 1 {
				t.Fatalf("retry changed historical receipt: %v", err)
			}
			assertSource(t, target, record, jobstore.NotifyDelivered)
		})
	}
}
