package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// ---- session_tools_worktree.go ----

// TestCovWriteWorktreeSidecar covers writeWorktreeSidecar
// (session_tools_worktree.go lines 55-60): the hook-seam path and the
// production path.
func TestCovWriteWorktreeSidecar(t *testing.T) {
	dir := t.TempDir()
	s := &Session{}
	sidecar := worktree.Sidecar{Name: "test_sidecar", Branch: "test-branch", BaseSHA: "abc123", CreatorSession: "session_1"}
	if err := s.writeWorktreeSidecar(dir, "test_sidecar", sidecar); err != nil {
		t.Fatalf("writeWorktreeSidecar: %v", err)
	}
	got, err := worktree.ReadSidecar(dir, "test_sidecar")
	if err != nil {
		t.Fatalf("ReadSidecar: %v", err)
	}
	if !reflect.DeepEqual(got, sidecar) {
		t.Fatalf("written sidecar = %+v, want %+v", got, sidecar)
	}
}

// TestCovDeleteWorktreeSidecar covers deleteWorktreeSidecar
// (session_tools_worktree.go lines 62-67).
func TestCovDeleteWorktreeSidecar(t *testing.T) {
	dir := t.TempDir()
	s := &Session{}
	// Write then delete.
	sidecar := worktree.Sidecar{Branch: "test"}
	if err := s.writeWorktreeSidecar(dir, "to_delete", sidecar); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteWorktreeSidecar(dir, "to_delete"); err != nil {
		t.Fatalf("deleteWorktreeSidecar: %v", err)
	}
	if _, err := worktree.ReadSidecar(dir, "to_delete"); !os.IsNotExist(err) {
		t.Fatalf("deleted sidecar is still readable: %v", err)
	}
}

// TestCovUpdateWorktreeSidecar covers updateWorktreeSidecar
// (session_tools_worktree.go lines 69-74): the hook-seam path and the
// production path.
func TestCovUpdateWorktreeSidecar(t *testing.T) {
	dir := t.TempDir()
	s := &Session{}
	// Write initial.
	want := worktree.Sidecar{Name: "update_test", Branch: "initial", BaseSHA: "base123", CreatorSession: "session_1"}
	if err := s.writeWorktreeSidecar(dir, "update_test", want); err != nil {
		t.Fatal(err)
	}
	// Update.
	if err := s.updateWorktreeSidecar(dir, "update_test", func(sc *worktree.Sidecar) {
		sc.Branch = "updated"
	}); err != nil {
		t.Fatalf("updateWorktreeSidecar: %v", err)
	}
	want.Branch = "updated"
	got, err := worktree.ReadSidecar(dir, "update_test")
	if err != nil {
		t.Fatalf("ReadSidecar after update: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updated sidecar = %+v, want %+v", got, want)
	}
}

// TestCovProjectIsGitCheckout covers projectIsGitCheckout
// (session_tools_worktree.go lines 701-707).
func TestCovProjectIsGitCheckout(t *testing.T) {
	// Empty canonical path — false.
	if projectIsGitCheckout(identifier.Project{}) {
		t.Fatal("empty project should return false")
	}

	// Non-git directory — false.
	dir := t.TempDir()
	if projectIsGitCheckout(identifier.Project{CanonicalPath: dir}) {
		t.Fatal("non-git dir should return false")
	}

	// Git directory — true.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !projectIsGitCheckout(identifier.Project{CanonicalPath: dir}) {
		t.Fatal("git dir should return true")
	}
}

// TestCovWorktreeRootForProject covers worktreeRootForProject
// (session_tools_worktree.go lines 683-691).
func TestCovWorktreeRootForProject(t *testing.T) {
	s := &Session{}
	// Empty project — error.
	_, err := s.worktreeRootForProject("", identifier.Project{})
	if err == nil {
		t.Fatal("empty project should return error")
	}

	// Valid project with stateDir.
	got, err := s.worktreeRootForProject("/state", identifier.Project{
		ID: "proj_1", CanonicalPath: "/path",
	})
	if err != nil {
		t.Fatalf("with stateDir: %v", err)
	}
	if want := filepath.Join("/state", "worktrees"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Valid project without stateDir — uses RuntimeDirForProjectWithStateHome.
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	got, err = s.worktreeRootForProject("", identifier.Project{
		ID: "proj_1", CanonicalPath: "/path",
	})
	if err != nil {
		t.Fatalf("without stateDir: %v", err)
	}
	if want := filepath.Join(stateHome, "evener", "projects", "proj_1", "worktrees"); got != want {
		t.Fatalf("runtime-derived root = %q, want %q", got, want)
	}
}

// TestCovManagedWorktreeExists covers managedWorktreeExists
// (session_tools_worktree.go lines 2983-2986).
func TestCovManagedWorktreeExists(t *testing.T) {
	dir := t.TempDir()
	// No .git — false.
	if managedWorktreeExists(dir) {
		t.Fatal("no .git should return false")
	}
	// With .git — true.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !managedWorktreeExists(dir) {
		t.Fatal("with .git should return true")
	}
}

// TestCovBranchExists covers branchExists
// (session_tools_worktree.go lines 2975-2978): using a scripted git runner.
func TestCovBranchExists(t *testing.T) {
	// Scripted runner that simulates branch not found (error).
	run := func(args ...string) (string, error) {
		want := []string{"show-ref", "--verify", "--quiet", "refs/heads/nonexistent"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("git args = %v, want %v", args, want)
		}
		return "", &os.PathError{Op: "git", Path: ".", Err: os.ErrNotExist}
	}
	if branchExists(run, "nonexistent") {
		t.Fatal("nonexistent branch should return false")
	}

	// Scripted runner that simulates branch found (no error).
	runOK := func(args ...string) (string, error) {
		want := []string{"show-ref", "--verify", "--quiet", "refs/heads/test-branch"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("git args = %v, want %v", args, want)
		}
		return "refs/heads/test-branch", nil
	}
	if !branchExists(runOK, "test-branch") {
		t.Fatal("existing branch should return true")
	}
}

// TestCovBranchAtRoot covers branchAtRoot
// (session_tools_worktree.go lines 2990-2996).
func TestCovBranchAtRoot(t *testing.T) {
	// Error case — returns empty string.
	runErr := func(args ...string) (string, error) {
		want := []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("git args = %v, want %v", args, want)
		}
		return "", &os.PathError{Op: "git", Path: ".", Err: os.ErrNotExist}
	}
	if got := branchAtRoot(runErr, "/repo"); got != "" {
		t.Fatalf("error case: got %q", got)
	}

	// Success case.
	runOK := func(args ...string) (string, error) {
		want := []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("git args = %v, want %v", args, want)
		}
		return "main\n", nil
	}
	if got := branchAtRoot(runOK, "/repo"); got != "main" {
		t.Fatalf("success: got %q, want main", got)
	}

	// Detached HEAD (empty output but no error).
	runDetached := func(args ...string) (string, error) {
		want := []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("git args = %v, want %v", args, want)
		}
		return "", nil
	}
	if got := branchAtRoot(runDetached, "/repo"); got != "" {
		t.Fatalf("detached: got %q, want empty", got)
	}
}

// ---- jobs_nested.go ----

// TestCovShouldRecoverForwardedTerminalRecord covers
// shouldRecoverForwardedTerminalRecord (jobs_nested.go lines 615-624).
func TestCovShouldRecoverForwardedTerminalRecord(t *testing.T) {
	jm := newTestJM(t)

	// nil rec — false.
	if jm.shouldRecoverForwardedTerminalRecord(nil, "parent_1", "") {
		t.Fatal("nil rec should return false")
	}

	// Wrong parent — false.
	rec := &jobstore.JobRecord{
		ParentJobID:    "other_parent",
		OwnerSessionID: jm.sessionID,
		Status:         jobstore.StatusCompleted,
		TerminalGen:    "gen_1",
	}
	if jm.shouldRecoverForwardedTerminalRecord(rec, "parent_1", "") {
		t.Fatal("wrong parent should return false")
	}

	// Wrong owner — false.
	rec.ParentJobID = "parent_1"
	rec.OwnerSessionID = "other_session"
	if jm.shouldRecoverForwardedTerminalRecord(rec, "parent_1", "") {
		t.Fatal("wrong owner should return false")
	}

	// Non-terminal — false.
	rec.OwnerSessionID = jm.sessionID
	rec.Status = jobstore.StatusRunning
	if jm.shouldRecoverForwardedTerminalRecord(rec, "parent_1", "") {
		t.Fatal("non-terminal should return false")
	}

	// No TerminalGen — false.
	rec.Status = jobstore.StatusCompleted
	rec.TerminalGen = ""
	if jm.shouldRecoverForwardedTerminalRecord(rec, "parent_1", "") {
		t.Fatal("no gen should return false")
	}

	// Valid terminal — true.
	rec.TerminalGen = "gen_1"
	if !jm.shouldRecoverForwardedTerminalRecord(rec, "parent_1", "") {
		t.Fatal("valid terminal should return true")
	}

	// forward_failed status — false.
	rec.Status = jobstore.StatusFailed
	rec.Reason = "forward_failed"
	if jm.shouldRecoverForwardedTerminalRecord(rec, "parent_1", "") {
		t.Fatal("forward_failed should return false")
	}

	// Other failed status — true.
	rec.Reason = "other_reason"
	if !jm.shouldRecoverForwardedTerminalRecord(rec, "parent_1", "") {
		t.Fatal("other failed should return true")
	}
}

// TestCovRecoveredEventTime covers recoveredEventTime
// (jobs_nested.go lines 626-631).
func TestCovRecoveredEventTime(t *testing.T) {
	jm := newTestJM(t)

	// With EndedAt — returns EndedAt.
	endedAt := frozenTestTime.Add(5 * time.Second)
	rec := &jobstore.JobRecord{EndedAt: &endedAt}
	if got := jm.recoveredEventTime(rec); !got.Equal(endedAt) {
		t.Fatalf("got %v, want %v", got, endedAt)
	}

	// Without EndedAt — returns jm.now().
	rec.EndedAt = nil
	got := jm.recoveredEventTime(rec)
	if !got.Equal(frozenTestTime) {
		t.Fatalf("got %v, want %v", got, frozenTestTime)
	}
}

// TestCovLiveSubagentSession covers liveSubagentSession
// (jobs_nested.go lines 256-271).
func TestCovLiveSubagentSession(t *testing.T) {
	// nil manager — nil.
	if liveSubagentSession(nil, "child_1") != nil {
		t.Fatal("nil manager should return nil")
	}

	// Non-existent child — nil.
	mgr := newSubagentManager(nil, 10)
	if liveSubagentSession(mgr, "nonexistent") != nil {
		t.Fatal("non-existent child should return nil")
	}
}

// TestCovForwardEvent covers forwardEvent (jobs_nested.go lines 635-662).
func TestCovForwardEvent(t *testing.T) {
	jm := newTestJM(t)

	// Simple event append — should succeed.
	e := jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		TS:             jm.now(),
		JobID:          "job_test",
		Type:           jobstore.JobShell,
		OwnerSessionID: "child_session",
		Description:    "forwarded shell",
	}
	if err := jm.forwardEvent(e); err != nil {
		t.Fatalf("forwardEvent: %v", err)
	}
	records, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load forwarded start: %v", err)
	}
	forwarded := records["job_test"]
	if forwarded == nil || forwarded.VisibleToSession != jm.sessionID || forwarded.OwnerSessionID != "child_session" || forwarded.Type != jobstore.JobShell || forwarded.Description != "forwarded shell" {
		t.Fatalf("forwarded start record = %+v", forwarded)
	}

	// NotificationPending event with nil enqueue — should append and not enqueue.
	jm.enqueue = nil
	if err := jm.forwardEvent(jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		TS:             jm.now(),
		JobID:          "job_pending",
		Type:           jobstore.JobShell,
		OwnerSessionID: jm.sessionID,
		Description:    "pending shell",
	}); err != nil {
		t.Fatalf("forward pending start: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          jm.now(),
		JobID:       "job_pending",
		Status:      jobstore.StatusCompleted,
		TerminalGen: "gen_1",
	}); err != nil {
		t.Fatalf("finish pending job: %v", err)
	}
	e2 := jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          jm.now(),
		JobID:       "job_pending",
		TerminalGen: "gen_1",
	}
	if err := jm.forwardEvent(e2); err != nil {
		t.Fatalf("forwardEvent pending: %v", err)
	}
	records, err = jm.store.Load()
	if err != nil {
		t.Fatalf("load forwarded pending event: %v", err)
	}
	if rec := records["job_pending"]; rec == nil || rec.VisibleToSession != jm.sessionID || rec.TerminalGen != "gen_1" || rec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("forwarded pending record = %+v", rec)
	}

	// NotificationPending event with enqueue — should append and enqueue.
	var queued []jobNotification
	jm.enqueue = func(n jobNotification) { queued = append(queued, n) }
	e3 := jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          jm.now(),
		JobID:       "job_enqueue",
		TerminalGen: "gen_2",
	}
	// First, create an owned terminal record so the enqueue finds it.
	if err := jm.forwardEvent(jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		TS:             jm.now(),
		JobID:          "job_enqueue",
		Type:           jobstore.JobType(delegateResourceType),
		OwnerSessionID: jm.sessionID,
		Description:    "owned delegate",
	}); err != nil {
		t.Fatal(err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          jm.now(),
		JobID:       "job_enqueue",
		Status:      jobstore.StatusCompleted,
		TerminalGen: "gen_2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := jm.forwardEvent(e3); err != nil {
		t.Fatalf("forwardEvent with enqueue: %v", err)
	}
	if len(queued) != 1 || queued[0].JobID != "job_enqueue" || queued[0].Status != string(jobstore.StatusCompleted) || queued[0].Description != "owned delegate" {
		t.Fatalf("queued notifications = %+v, want one completed job_enqueue", queued)
	}
	records, err = jm.store.Load()
	if err != nil {
		t.Fatalf("load enqueued forwarding: %v", err)
	}
	if rec := records["job_enqueue"]; rec == nil || rec.VisibleToSession != jm.sessionID || rec.NotifyState != jobstore.NotifyPending || rec.TerminalGen != "gen_2" {
		t.Fatalf("forwarded terminal record = %+v", rec)
	}
}

// ---- session_queue.go ----

// TestCovTrySteerEnqueue_Closed covers trySteerEnqueue
// (session_queue.go lines 252-274): closed session returns false.
func TestCovTrySteerEnqueue_Closed(t *testing.T) {
	s := &Session{}
	s.mu.Lock()
	s.state = SessionClosed
	s.mu.Unlock()
	if s.trySteerEnqueue("msg", nil, nil, "", "") {
		t.Fatal("closed session should return false")
	}
}

// TestCovTrySteerEnqueue_EmptyMsg covers empty message + no images.
func TestCovTrySteerEnqueue_EmptyMsg(t *testing.T) {
	s := &Session{}
	if s.trySteerEnqueue("", nil, nil, "", "") {
		t.Fatal("empty msg + no images should return false")
	}
	if s.trySteerEnqueue("  ", nil, nil, "", "") {
		t.Fatal("whitespace msg should return false")
	}
}

// TestCovTrySteerEnqueue_Success covers a successful enqueue.
func TestCovTrySteerEnqueue_Success(t *testing.T) {
	s := &Session{}
	if !s.trySteerEnqueue("hello", nil, nil, "", "") {
		t.Fatal("valid msg should return true")
	}
	s.mu.Lock()
	if len(s.steeringQueue) != 1 || s.steeringQueue[0].Text != "hello" {
		t.Fatalf("steering queue: %+v", s.steeringQueue)
	}
	s.mu.Unlock()
}

// TestCovTrySteerEnqueue_UserSource covers the user-source path (skips persist).
func TestCovTrySteerEnqueue_UserSource(t *testing.T) {
	s := &Session{}
	if !s.trySteerEnqueue("user msg", nil, nil, events.SteeringSourceUser, "") {
		t.Fatal("user source should return true")
	}
	// User-sourced steering is not persisted by the snapshot; verify it's queued.
	s.mu.Lock()
	if len(s.steeringQueue) != 1 || s.steeringQueue[0].Text != "user msg" || s.steeringQueue[0].Source != events.SteeringSourceUser {
		t.Fatalf("user steering queue = %+v", s.steeringQueue)
	}
	s.mu.Unlock()
}

// TestCovTrySteerWithProvenanceAndNotify covers
// trySteerWithProvenanceAndNotify (session_queue.go lines 202-208).
func TestCovTrySteerWithProvenanceAndNotify(t *testing.T) {
	s := &Session{}
	// Empty message — trySteer returns false, so should return false.
	if s.trySteerWithProvenanceAndNotify("", nil, "") {
		t.Fatal("empty msg should return false")
	}
}

// TestCovDrainAsSteerWithInput_Closed covers DrainAsSteerWithInput
// (session_queue.go lines 393-424): closed session.
func TestCovDrainAsSteerWithInput_Closed(t *testing.T) {
	s := &Session{}
	s.mu.Lock()
	s.state = SessionClosed
	s.mu.Unlock()
	if err := s.DrainAsSteerWithInput(context.Background(), "", nil); err == nil {
		t.Fatal("closed session should return error")
	}
}

// TestCovDrainAsSteerWithInput_NotProcessing covers no active turn.
func TestCovDrainAsSteerWithInput_NotProcessing(t *testing.T) {
	s := &Session{}
	if err := s.DrainAsSteerWithInput(context.Background(), "", nil); err == nil {
		t.Fatal("not processing should return error")
	}
}

// TestCovDrainAsSteerWithInput_CanceledCtx covers canceled context.
func TestCovDrainAsSteerWithInput_CanceledCtx(t *testing.T) {
	s := &Session{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.DrainAsSteerWithInput(ctx, "", nil); err == nil {
		t.Fatal("canceled context should return error")
	}
}

// TestCovPopSteeringHead_Empty covers popSteeringHead
// (session_queue.go lines 810-844): empty queue.
func TestCovPopSteeringHead_Empty2(t *testing.T) {
	s := &Session{}
	_, ok := s.popSteeringHead()
	if ok {
		t.Fatal("empty queue should return false")
	}
}

// TestCovPopSteeringHead_Success covers a successful pop.
func TestCovPopSteeringHead_Success(t *testing.T) {
	s := &Session{}
	s.mu.Lock()
	s.steeringQueue = []steeringMessage{{Text: "msg1"}}
	s.mu.Unlock()
	msg, ok := s.popSteeringHead()
	if !ok || msg.Text != "msg1" {
		t.Fatalf("got msg=%q ok=%v", msg.Text, ok)
	}
	// Queue should now be empty.
	s.mu.Lock()
	if len(s.steeringQueue) != 0 {
		t.Fatalf("queue should be empty, got %d", len(s.steeringQueue))
	}
	s.mu.Unlock()
}

// TestCovPushQueueHead_Empty covers pushQueueHead
// (session_queue.go lines 635-683): empty text + no images — no-op.
func TestCovPushQueueHead_Empty(t *testing.T) {
	s := &Session{}
	s.pushQueueHead(queuedInput{})
	s.mu.Lock()
	if len(s.inputQueue) != 0 {
		t.Fatal("empty entry should be no-op")
	}
	s.mu.Unlock()
}

// TestCovPushQueueHead_NoClientMutation covers pushQueueHead without
// a client mutation ID — the direct-input-queue path.
func TestCovPushQueueHead_NoClientMutation(t *testing.T) {
	s := &Session{}
	s.pushQueueHead(queuedInput{ID: "q1", Text: "hello"})
	s.mu.Lock()
	if len(s.inputQueue) != 1 || s.inputQueue[0].Text != "hello" {
		t.Fatalf("input queue: %+v", s.inputQueue)
	}
	s.mu.Unlock()
}

// ---- session_attention.go ----

// TestCovHasPendingDelegateAttentionArmRetry_WithArms covers
// hasPendingDelegateAttentionArmRetry (session_attention.go lines 485-493)
// with arms set.
func TestCovHasPendingDelegateAttentionArmRetry_WithArms(t *testing.T) {
	s := &Session{}
	s.attentionMu.Lock()
	s.delegateAttentionArmIDs = map[string]struct{}{"arm_1": {}}
	s.attentionMu.Unlock()
	if !s.hasPendingDelegateAttentionArmRetry() {
		t.Fatal("with arms should return true")
	}
}

// TestCovResetDelegateAttentionArmRetryLocked covers
// resetDelegateAttentionArmRetryLocked (session_attention.go lines 734-738).
func TestCovResetDelegateAttentionArmRetryLocked(t *testing.T) {
	s := &Session{}
	s.attentionMu.Lock()
	s.delegateAttentionArmRetry.active = true
	s.delegateAttentionArmRetry.generation = 8
	s.delegateAttentionArmRetry.delay = 5 * time.Second
	s.attentionMu.Unlock()
	s.attentionMu.Lock()
	s.resetDelegateAttentionArmRetryLocked()
	if s.delegateAttentionArmRetry.active {
		t.Fatal("active should be false")
	}
	if s.delegateAttentionArmRetry.delay != jobNotificationRetryInitialDelay {
		t.Fatalf("delay = %v, want %v", s.delegateAttentionArmRetry.delay, jobNotificationRetryInitialDelay)
	}
	if s.delegateAttentionArmRetry.generation != 9 {
		t.Fatalf("generation = %d, want 9", s.delegateAttentionArmRetry.generation)
	}
	s.attentionMu.Unlock()
}

// TestCovIsRootDelegateAttentionReceiver_NoController covers
// isRootDelegateAttentionReceiver (session_attention.go lines 552-559).
func TestCovIsRootDelegateAttentionReceiver_NoController2(t *testing.T) {
	s := &Session{}
	if s.isRootDelegateAttentionReceiver() {
		t.Fatal("no controller should return false")
	}
}

// TestCovIsRootDelegateAttentionReceiver_RootRuntime covers the root path.
func TestCovIsRootDelegateAttentionReceiver_RootRuntime(t *testing.T) {
	s := &Session{}
	s.delegateController = &delegateTreeController{rootRuntime: s}
	if !s.isRootDelegateAttentionReceiver() {
		t.Fatal("root runtime should return true")
	}
}

// TestCovResetStableDelegateAttentionRetry covers
// resetStableDelegateAttentionRetry (session_attention.go lines 784+).
func TestCovResetStableDelegateAttentionRetry(t *testing.T) {
	s := &Session{}
	s.attentionMu.Lock()
	s.stableAttentionRetry.active = true
	s.stableAttentionRetry.generation = 4
	s.stableAttentionRetry.delay = 5 * time.Second
	s.attentionMu.Unlock()
	s.resetStableDelegateAttentionRetry()
	s.attentionMu.Lock()
	if s.stableAttentionRetry.active {
		t.Fatal("should be inactive after reset")
	}
	if s.stableAttentionRetry.generation != 5 {
		t.Fatalf("generation = %d, want 5", s.stableAttentionRetry.generation)
	}
	if s.stableAttentionRetry.delay != jobNotificationRetryInitialDelay {
		t.Fatalf("delay = %v, want %v", s.stableAttentionRetry.delay, jobNotificationRetryInitialDelay)
	}
	s.attentionMu.Unlock()
}

// TestCovAcceptDelegateAttention_NilReservation covers
// acceptDelegateAttention (session_attention.go lines 527+).
func TestCovAcceptDelegateAttention_NilReservation2(t *testing.T) {
	s := &Session{}
	err := s.acceptDelegateAttention(nil)
	if err == nil {
		t.Fatal("nil reservation should return error")
	}
}

// TestCovAcceptDelegateAttention_NoController covers with no controller.
func TestCovAcceptDelegateAttention_NoController2(t *testing.T) {
	s := &Session{}
	err := s.acceptDelegateAttention(nil)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("no controller: %v", err)
	}
}
