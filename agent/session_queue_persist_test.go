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
	"bytes"
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

func TestQueuePersist_DaemonAndClientSteeringUseDistinctAuthorities(t *testing.T) {
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
	want := []SteeringEntry{
		{Text: "<SYSTEM-REMINDER>daemon nudge</SYSTEM-REMINDER>"},
		{Text: "please check the tests"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored SteeringQueueSnapshot = %#v, want %#v", got, want)
	}
}

func TestQueuePersist_ClientSteeringDoesNotTouchLegacyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	path := queuePersistFilePath(dir, id)
	defer sess.Close()

	// The legacy file contains daemon-authored steering.
	sess.Steer("<SYSTEM-REMINDER>vision description</SYSTEM-REMINDER>")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue file after daemon steer: %v", err)
	}
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat queue file after user-sourced steer: %v", err)
	}

	sess.SteerFromUser("please check the tests")

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue file after client steer: %v", err)
	}
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat queue file after client steer: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("legacy queue file changed after client steer:\nbefore=%s\nafter=%s", before, after)
	}
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatalf("legacy queue file mtime changed after client steer: before=%v after=%v", infoBefore.ModTime(), infoAfter.ModTime())
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
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy queue file created by client enqueue: stat err=%v", err)
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
	if depth := restored.QueueDepth(); depth != 1 {
		t.Fatalf("restored QueueDepth = %d, want 1 (claimed input returns runnable)", depth)
	}
}

func TestQueuePersist_CrashBetweenClaimAndConsume_ReturnsRunnable(t *testing.T) {
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
	claimedRevision := sess.clientMutations.snapshot().QueueRevision

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	gotTexts := restored.QueueTexts()
	if !reflect.DeepEqual(gotTexts, []string{"alpha", "bravo"}) {
		t.Fatalf("restored QueueTexts = %#v, want [alpha bravo]", gotTexts)
	}
	if got := restored.clientMutations.snapshot().QueueRevision; got != claimedRevision+1 {
		t.Fatalf("recovery queue revision = %d, want %d", got, claimedRevision+1)
	}
}

// TestQueuePersist_DrainSteering_CrashLosesAtMostInFlightItem pins the
// at-most-one crash-window bound for the steering queue (kata 5em1, closing
// design review Important-1's wider window). injectDrainedSteering consumes the
// steering batch pop-one/persist/consume per message (peekSteeringForTurn to
// fold provenance upfront, then popSteeringHead + consumeSteeringMessage per
// item), so the persisted queue shrinks as each message is durably recorded —
// exactly like popQueueHead for the input queue. A crash between a message's
// pop (persist-shrunk) and its durable consume loses only that single
// in-flight message; every message not yet popped stays durably queued (never
// duplicated, never resurrected past the persisted state), and every message
// already consumed survives in the transcript.
func TestQueuePersist_DrainSteering_CrashLosesAtMostInFlightItem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()

	sess.SteerFromUser("alpha")
	sess.SteerFromUser("bravo")
	sess.SteerFromUser("charlie")

	// Drive the exact primitives injectDrainedSteering's loop runs — not a
	// hand-rolled imitation. peekSteeringForTurn folds the whole batch's
	// provenance upfront without removing anything; then each iteration pops the
	// head (persisting the shrunk queue) and durably records it.
	peeked := sess.peekSteeringForTurn()
	if len(peeked) != 3 {
		t.Fatalf("peeked = %d entries, want 3", len(peeked))
	}

	// Iteration 1 completes: pop alpha (persists queue as [bravo charlie]) and
	// durably consume it.
	first, ok := sess.popSteeringHead()
	if !ok || first.Text != "alpha" {
		t.Fatalf("first pop = %q ok=%v, want alpha", first.Text, ok)
	}
	sess.consumeSteeringMessage(first)

	// Iteration 2 crashes mid-window: pop bravo (persists queue as [charlie]),
	// then the daemon dies BEFORE consumeSteeringMessage durably records it.
	// Deliberately NOT consuming bravo and NOT calling Close().
	second, ok := sess.popSteeringHead()
	if !ok || second.Text != "bravo" {
		t.Fatalf("second pop = %q ok=%v, want bravo", second.Text, ok)
	}

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	// Claimed but unincorporated bravo returns to runnable state; charlie was
	// never claimed and remains behind it.
	got := restored.SteeringQueueSnapshot()
	if len(got) != 2 || got[0].Text != "bravo" || got[1].Text != "charlie" {
		t.Fatalf("restored SteeringQueueSnapshot = %#v, want [bravo charlie]", got)
	}
	// alpha survived because consumeSteeringMessage durably appended it to the
	// transcript/history before the crash; bravo did not and remains runnable.
	foundAlpha := false
	for _, turn := range restored.history {
		if turn.Kind == schema.TurnSteering && turn.Message.Text() == "alpha" {
			foundAlpha = true
		}
		if turn.Kind == schema.TurnSteering && turn.Message.Text() == "bravo" {
			t.Fatalf("restored history unexpectedly contains unincorporated bravo; kinds=%v", restored.history)
		}
	}
	if !foundAlpha {
		t.Fatalf("restored history missing alpha's steering turn (should have been durably recorded before the crash); kinds=%v", restored.history)
	}
}
