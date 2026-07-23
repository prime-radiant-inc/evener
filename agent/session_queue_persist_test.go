package agent

// Tests for queue persistence: Session.steeringQueue and Session.inputQueue
// must survive a daemon crash/restart exactly like every other piece of
// session state (Jesse's restart-parity ruling). Before this feature, both
// queues lived only in memory (session.go's own field-grouping comment listed
// them among the mu-guarded in-memory fields) and evaporated silently on any
// crash. Each test below drives the queue through its real public mutator,
// then reconstructs a fresh *Session from the same on-disk state dir via the
// production resume path (RestoreSessionFromMetaWithConfig, exactly what
// `serf serve --resume` calls per cmdutil.ResolveSessionMeta) rather than a
// hand-rolled imitation, and asserts the restored queue matches.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// newQueuePersistTestSession builds a session rooted at dir for both its
// working directory and its state dir, so meta.json/transcript/the new queue
// file all land under the same tree a real daemon would use.
func newQueuePersistTestSession(t *testing.T, dir string) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// restoreQueuePersistTestSession reconstructs the session with id from dir the
// way a real daemon resume does: load the persisted SessionMeta, then call
// RestoreSessionFromMetaWithConfig (agent/session_init.go), the exact function
// `serf serve --resume` uses.
func restoreQueuePersistTestSession(t *testing.T, dir, id string) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	return restored
}

// queuePersistFilePath is the on-disk path the persistence design is expected
// to use, mirroring the task store's <stateDir>/tasks/<id>.json convention
// (agent/task.NewTaskStore) with "queues" as the per-concern subdirectory.
// Asserting the exact path is deliberate (session_identifier_test.go does the
// same for meta.json/transcript.jsonl): the path convention is part of the
// persistence contract, not an implementation detail to paper over.
func queuePersistFilePath(dir, id string) string {
	return filepath.Join(dir, "queues", id+".json")
}

func TestQueuePersist_EnqueueMixedItems_SurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	ctx := context.Background()

	if err := sess.Enqueue(ctx, "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	img := ImageAttachment{MediaType: "image/png", Data: []byte("fake-png-bytes"), Name: "shot.png"}
	if err := sess.EnqueueWithImages(ctx, "", []ImageAttachment{img}); err != nil {
		t.Fatalf("Enqueue image-only: %v", err)
	}
	if err := sess.EnqueueWithImages(ctx, "bravo with pic", []ImageAttachment{img}); err != nil {
		t.Fatalf("Enqueue mixed text+image: %v", err)
	}
	// A steering message queued mid-turn (the other queue in scope) must
	// survive restart too, distinctly from the input queue (steer-vs-input
	// classification preserved).
	markProcessing(sess)
	sess.SteerFromUser("mid-turn nudge")

	wantPreview := sess.QueuePreview()
	wantIDs := sess.QueueIDs()
	wantTexts := sess.QueueTexts()
	wantSteering := sess.SteeringQueueSnapshot()
	if len(wantIDs) != 3 {
		t.Fatalf("test setup: QueueIDs = %#v, want 3 entries", wantIDs)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if got := restored.QueuePreview(); !reflect.DeepEqual(got, wantPreview) {
		t.Fatalf("restored QueuePreview = %#v, want %#v", got, wantPreview)
	}
	if got := restored.QueueIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("restored QueueIDs = %#v, want %#v (order/identity must survive)", got, wantIDs)
	}
	if got := restored.QueueTexts(); !reflect.DeepEqual(got, wantTexts) {
		t.Fatalf("restored QueueTexts = %#v, want %#v", got, wantTexts)
	}
	if got := restored.SteeringQueueSnapshot(); !reflect.DeepEqual(got, wantSteering) {
		t.Fatalf("restored SteeringQueueSnapshot = %#v, want %#v", got, wantSteering)
	}
}

// TestQueuePersist_HookSourcedSteering_DoesNotPersist locks in a real
// regression this feature discovered: a plain Steer() (Source=="", the daemon
// /hook nudge path — SessionStart hook context, task/compaction reminders,
// vision descriptions) must NOT survive restart via queue persistence.
// Resume replays SessionStart hook output through its own dedicated,
// matcher-aware path (drainPendingSessionStartHooksForUserTurn); resurrecting
// a stale undrained hook message from the old process would silently
// reintroduce the exact re-injection that mechanism exists to prevent
// (TestResume_DualFlavorPlugin_DoesNotReinject, plugin_integration_test.go).
// Only user-sent steering (turn/steer, DrainAsSteer, PromoteQueuedAsSteer) is
// in scope for restart parity.
func TestQueuePersist_HookSourcedSteering_DoesNotPersist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()

	sess.Steer("<SYSTEM-REMINDER>daemon nudge</SYSTEM-REMINDER>") // Source == "" (system-authored)
	sess.SteerFromUser("please check the tests")                  // Source == user
	if got := sess.SteeringQueueSnapshot(); len(got) != 2 {
		t.Fatalf("test setup: SteeringQueueSnapshot = %#v, want 2 entries before restart", got)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	got := restored.SteeringQueueSnapshot()
	want := []SteeringEntry{{Text: "please check the tests"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored SteeringQueueSnapshot = %#v, want %#v (only the user-sent entry should survive)", got, want)
	}
}

func TestQueuePersist_DrainAsSteer_SurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	ctx := context.Background()

	if err := sess.Enqueue(ctx, "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(ctx, "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	markProcessing(sess)
	if err := sess.DrainAsSteer(ctx); err != nil {
		t.Fatalf("DrainAsSteer: %v", err)
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("test setup: QueueDepth after drain = %d, want 0", depth)
	}
	wantSteering := sess.SteeringQueueSnapshot()
	if len(wantSteering) != 1 {
		t.Fatalf("test setup: SteeringQueueSnapshot = %#v, want 1 combined entry", wantSteering)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if depth := restored.QueueDepth(); depth != 0 {
		t.Fatalf("restored QueueDepth = %d, want 0 (drained before restart)", depth)
	}
	if got := restored.SteeringQueueSnapshot(); !reflect.DeepEqual(got, wantSteering) {
		t.Fatalf("restored SteeringQueueSnapshot = %#v, want %#v", got, wantSteering)
	}
}

func TestQueuePersist_PromoteQueuedAsSteer_SurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	ctx := context.Background()

	if err := sess.Enqueue(ctx, "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(ctx, "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	markProcessing(sess)
	ids := sess.QueueIDs()
	if err := sess.PromoteQueuedAsSteer(ctx, 0, ids[0]); err != nil {
		t.Fatalf("PromoteQueuedAsSteer: %v", err)
	}

	wantPreview := sess.QueuePreview()
	wantIDs := sess.QueueIDs()
	wantSteering := sess.SteeringQueueSnapshot()
	if len(wantPreview) != 1 || wantPreview[0] != "bravo" {
		t.Fatalf("test setup: QueuePreview after promote = %#v, want [bravo]", wantPreview)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if got := restored.QueuePreview(); !reflect.DeepEqual(got, wantPreview) {
		t.Fatalf("restored QueuePreview = %#v, want %#v", got, wantPreview)
	}
	if got := restored.QueueIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("restored QueueIDs = %#v, want %#v", got, wantIDs)
	}
	if got := restored.SteeringQueueSnapshot(); !reflect.DeepEqual(got, wantSteering) {
		t.Fatalf("restored SteeringQueueSnapshot = %#v, want %#v (promoted entry)", got, wantSteering)
	}
}

func TestQueuePersist_CancelQueued_SurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	ctx := context.Background()

	if err := sess.Enqueue(ctx, "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(ctx, "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	ids := sess.QueueIDs()
	if _, _, err := sess.CancelQueued(ctx, 0, ids[0]); err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}

	wantPreview := sess.QueuePreview()
	wantIDs := sess.QueueIDs()
	if len(wantPreview) != 1 || wantPreview[0] != "bravo" {
		t.Fatalf("test setup: QueuePreview after cancel = %#v, want [bravo]", wantPreview)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if got := restored.QueuePreview(); !reflect.DeepEqual(got, wantPreview) {
		t.Fatalf("restored QueuePreview = %#v, want %#v", got, wantPreview)
	}
	if got := restored.QueueIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("restored QueueIDs = %#v, want %#v", got, wantIDs)
	}
}

// TestQueuePersist_EmptyQueue_NoResidueOnDisk proves an empty queue leaves no
// trace on disk: absent entirely before any mutation, and removed (not
// rewritten as an empty array) once drained back to empty.
func TestQueuePersist_EmptyQueue_NoResidueOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	ctx := context.Background()
	path := queuePersistFilePath(dir, id)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("queue file present before any queue mutation: stat err=%v", err)
	}

	if err := sess.Enqueue(ctx, "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("queue file missing after enqueue: %v", err)
	}

	popped := sess.popQueueHead()
	if popped.Text != "alpha" {
		t.Fatalf("popped = %q, want alpha", popped.Text)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("queue file still present after draining back to empty (want removed, not an empty-array residue): stat err=%v", err)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	if depth := restored.QueueDepth(); depth != 0 {
		t.Fatalf("restored QueueDepth = %d, want 0", depth)
	}
}

// TestQueuePersist_CrashBetweenDequeueAndConsume_ItemNotDuplicated exercises
// the crash-window semantics decision (report §4): popQueueHead persists the
// shrunk queue synchronously, before the popped item is ever handed to
// processOneInput/acceptUserInput for its durable transcript append. A crash
// in that narrow, I/O-free window loses the just-popped item at most once; it
// must NEVER resurrect it (which would risk re-running tools twice), matching
// the orphaned-tool-call precedent in history_repair.go (never re-execute,
// accept a gap instead).
func TestQueuePersist_CrashBetweenDequeueAndConsume_ItemNotDuplicated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	ctx := context.Background()

	if err := sess.Enqueue(ctx, "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(ctx, "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}

	// popQueueHead is the exact primitive the drain loop (session_lifecycle.go)
	// uses to dequeue the next queued message before feeding it to
	// processOneInput. Deliberately NOT calling Close() afterward and NOT
	// running any turn: this simulates the daemon dying in the gap between
	// "dequeued, persisted" and "durably recorded as a transcript turn."
	popped := sess.popQueueHead()
	if popped.Text != "alpha" {
		t.Fatalf("popped = %q, want alpha", popped.Text)
	}

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	gotTexts := restored.QueueTexts()
	if !reflect.DeepEqual(gotTexts, []string{"bravo"}) {
		t.Fatalf("restored QueueTexts = %#v, want [bravo] (alpha lost in the dequeue-to-consume window, never duplicated)", gotTexts)
	}
}
