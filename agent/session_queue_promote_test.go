package agent

// Tests for Session.PromoteQueuedAsSteer (issue #22): a single queued
// follow-up can be promoted to a steering injection on the in-flight turn.
// The promoted entry leaves the FIFO queue (other entries keep their order)
// and lands on the steering queue marked Source="user" so UIs render it as
// user speech, exactly like a DrainAsSteer collapse (issue #24).

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

func newPromoteTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func markProcessing(sess *Session) {
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()
}

func TestSession_PromoteQueuedAsSteer_RemovesOnlyThatEntry(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	for _, msg := range []string{"alpha", "bravo", "charlie"} {
		if err := sess.Enqueue(context.Background(), msg); err != nil {
			t.Fatalf("Enqueue %s: %v", msg, err)
		}
	}
	markProcessing(sess)

	if err := sess.PromoteQueuedAsSteer(context.Background(), 1, ""); err != nil {
		t.Fatalf("PromoteQueuedAsSteer: %v", err)
	}

	// The other queued messages stay queued, in order.
	preview := sess.QueuePreview()
	if len(preview) != 2 || preview[0] != "alpha" || preview[1] != "charlie" {
		t.Fatalf("QueuePreview after promote: got %#v, want [alpha charlie]", preview)
	}

	// The promoted message — and only it — lands on the steering queue.
	sess.mu.Lock()
	var steering []string
	for _, m := range sess.steeringQueue {
		steering = append(steering, m.Text)
	}
	sess.mu.Unlock()
	if len(steering) != 1 || steering[0] != "bravo" {
		t.Fatalf("steeringQueue after promote: got %#v, want [bravo]", steering)
	}
}

func TestSession_PromoteQueuedAsSteer_MarksUserSource(t *testing.T) {
	t.Parallel()
	s := &Session{state: SessionProcessing}
	if err := s.Enqueue(context.Background(), "human queued text"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := s.PromoteQueuedAsSteer(context.Background(), 0, ""); err != nil {
		t.Fatalf("PromoteQueuedAsSteer: %v", err)
	}
	got := s.drainSteeringForTurn()
	if len(got) != 1 {
		t.Fatalf("drained = %d, want 1", len(got))
	}
	if got[0].Source != events.SteeringSourceUser {
		t.Fatalf("drained steering Source = %q, want %q", got[0].Source, events.SteeringSourceUser)
	}
	if got[0].Text != "human queued text" {
		t.Fatalf("drained steering Text = %q, want %q", got[0].Text, "human queued text")
	}
}

func TestSession_PromoteQueuedAsSteer_RejectsIdleWithoutMutatingQueue(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// No turn in flight: the promote must fail honestly and leave the queued
	// message in place so it is still processed as a normal follow-up.
	err := sess.PromoteQueuedAsSteer(context.Background(), 0, "")
	if err == nil || !strings.Contains(err.Error(), "no active turn") {
		t.Fatalf("PromoteQueuedAsSteer idle err=%v, want no active turn", err)
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "alpha" {
		t.Fatalf("QueuePreview after rejected promote: got %#v, want [alpha]", preview)
	}
	sess.mu.Lock()
	steeringDepth := len(sess.steeringQueue)
	sess.mu.Unlock()
	if steeringDepth != 0 {
		t.Fatalf("steeringQueue after rejected promote: got %d, want 0", steeringDepth)
	}
}

func TestSession_PromoteQueuedAsSteer_IndexOutOfRange(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	markProcessing(sess)
	for _, idx := range []int{-1, 1, 42} {
		err := sess.PromoteQueuedAsSteer(context.Background(), idx, "")
		if err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Fatalf("PromoteQueuedAsSteer(%d) err=%v, want out of range", idx, err)
		}
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "alpha" {
		t.Fatalf("QueuePreview after out-of-range promotes: got %#v, want [alpha]", preview)
	}
}

func TestSession_PromoteQueuedAsSteer_EmitsQueueChanged(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	markProcessing(sess)
	if err := sess.PromoteQueuedAsSteer(context.Background(), 0, ""); err != nil {
		t.Fatalf("PromoteQueuedAsSteer: %v", err)
	}
	sess.Close()

	var last *events.QueueChangedData
	for ev := range sess.Events() {
		if d, ok := ev.Data.(events.QueueChangedData); ok {
			d := d
			last = &d
		}
	}
	if last == nil {
		t.Fatal("expected at least one QueueChanged event")
	}
	if last.Depth != 1 || len(last.Preview) != 1 || last.Preview[0] != "bravo" {
		t.Fatalf("final QueueChanged = %+v, want depth 1 preview [bravo]", *last)
	}
}

// TestSession_PromoteQueuedAsSteer_ExpectedIDMismatch covers review F1: the
// queue head can be consumed while a turn is in flight, so an index that was
// valid when the UI snapshotted the preview may now point at a DIFFERENT
// queued message. The promote must refuse — leaving the queue untouched —
// when the caller's expected entry id no longer matches the entry at index.
func TestSession_PromoteQueuedAsSteer_ExpectedIDMismatch(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	ids := sess.QueueIDs()
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("QueueIDs = %#v, want two distinct non-empty ids", ids)
	}
	markProcessing(sess)

	// Simulate the shift: the head (alpha) is consumed into a fresh user
	// turn, so index 0 now holds bravo. A promote carrying alpha's id must
	// fail honestly instead of steering bravo into the running turn.
	_ = sess.popQueueHead()
	err := sess.PromoteQueuedAsSteer(context.Background(), 0, ids[0])
	if err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("PromoteQueuedAsSteer shifted err=%v, want mismatch", err)
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "bravo" {
		t.Fatalf("QueuePreview after mismatch: got %#v, want [bravo]", preview)
	}
	sess.mu.Lock()
	steeringDepth := len(sess.steeringQueue)
	sess.mu.Unlock()
	if steeringDepth != 0 {
		t.Fatalf("steeringQueue after mismatch: got %d, want 0", steeringDepth)
	}

	// The correct id still promotes.
	if err := sess.PromoteQueuedAsSteer(context.Background(), 0, ids[1]); err != nil {
		t.Fatalf("PromoteQueuedAsSteer with correct id: %v", err)
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth after promote: got %d, want 0", depth)
	}
}

func TestSession_PromoteQueuedAsSteer_QueueChangedCarriesIDs(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	markProcessing(sess)
	ids := sess.QueueIDs()
	if err := sess.PromoteQueuedAsSteer(context.Background(), 0, ids[0]); err != nil {
		t.Fatalf("PromoteQueuedAsSteer: %v", err)
	}
	sess.Close()
	var last *events.QueueChangedData
	for ev := range sess.Events() {
		if d, ok := ev.Data.(events.QueueChangedData); ok {
			d := d
			last = &d
		}
	}
	if last == nil {
		t.Fatal("expected at least one QueueChanged event")
	}
	if len(last.IDs) != len(last.Preview) {
		t.Fatalf("QueueChanged IDs=%#v misaligned with Preview=%#v", last.IDs, last.Preview)
	}
	// The enqueue-time event carried the minted id.
	if len(last.IDs) != 0 {
		t.Fatalf("final QueueChanged IDs=%#v, want empty after promote", last.IDs)
	}
}

func TestSession_PromoteQueuedAsSteer_ClosedSession(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	sess.Close()
	err := sess.PromoteQueuedAsSteer(context.Background(), 0, "")
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("PromoteQueuedAsSteer closed err=%v, want closed", err)
	}
}
