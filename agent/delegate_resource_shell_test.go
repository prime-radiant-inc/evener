package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
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

	childNotices := stableShellNotifications(tree.child)
	if len(childNotices) != 1 || childNotices[0].JobID != rec.JobID {
		t.Fatalf("direct owner notifications = %+v, want exactly shell %q", childNotices, rec.JobID)
	}
	if rootNotices := stableShellNotifications(tree.root); len(rootNotices) != 0 {
		t.Fatalf("ancestor notification rail = %+v, want no child-owned shell attention", rootNotices)
	}
	block := formatJobNotificationBlock(childNotices[0], notificationExcerpt{}, true)
	if !strings.Contains(block, `<job-notification job_id="`+rec.JobID+`"`) ||
		!strings.Contains(block, `job_type="shell"`) || strings.Contains(block, "delegate-notification") {
		t.Fatalf("shell completion attention = %q", block)
	}
	stored := loadStableShellRecord(t, tree.childJM, rec.JobID)
	if stored.TerminalGen == "" || stored.NotifyState != jobstore.NotifyPending {
		t.Fatalf("terminal shell = %+v, want durable generation and pending direct-owner attention", stored)
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
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &payload); err != nil {
		t.Fatalf("decode descendant list: %v", err)
	}
	row := stableShellJobRow(payload.Jobs, rec.JobID)
	if row == nil {
		t.Fatalf("ancestor jobs = %+v, want descendant shell %q", payload.Jobs, rec.JobID)
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

	status, err := jobStatusTool(tree.root, map[string]any{"job_id": rec.JobID}, 1<<20)
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

	stopped, err := jobStopTool(context.Background(), tree.root, map[string]any{"job_id": rec.JobID}, 1<<20)
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
	for i := 0; i < 2; i++ {
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
	notices := stableShellNotifications(tree.child)
	if len(notices) != 1 || notices[0].JobID != rec.JobID {
		t.Fatalf("repaired direct-owner attention = %+v, want exactly one", notices)
	}
	block := formatJobNotificationBlock(notices[0], notificationExcerpt{}, true)
	if !strings.Contains(block, `<job-notification job_id="`+rec.JobID+`"`) || !strings.Contains(block, `job_type="shell"`) {
		t.Fatalf("repaired shell completion markup = %q", block)
	}
	forwarded := loadStableShellRecord(t, tree.rootJM, rec.JobID)
	if !forwarded.Status.IsTerminal() || forwarded.TerminalGen == "" {
		t.Fatalf("ancestor repaired shell visibility = %+v, want terminal generation", forwarded)
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

func stableShellNotifications(s *Session) []jobNotification {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	return append([]jobNotification(nil), s.pendingJobNotifs...)
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
