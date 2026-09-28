package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// borrowProbeStrategy parks at AfterAction entry so a test can run an
// asynchronous attention mutation against the history it was handed, then
// reads that history. It records the exact slice it received so the test can
// assert the snapshot AfterAction saw did not move underneath it; read is the
// total text width it observed, which must match the original snapshot.
type borrowProbeStrategy struct {
	spyStrategy
	entered chan struct{}
	proceed chan struct{}
	got     []schema.Turn
	read    int
}

func (b *borrowProbeStrategy) AfterAction(_ context.Context, history []schema.Turn, _ *llm.Client) error {
	b.got = history
	close(b.entered)
	<-b.proceed
	// Read every turn. Without the snapshot copy this read races a concurrent
	// attention replacement or removal on the same backing array.
	for _, turn := range history {
		b.read += len(turn.Message.Text())
	}
	return nil
}

// newBorrowProbeSession builds a session whose sole strategy is probe.
func newBorrowProbeSession(t *testing.T, probe *borrowProbeStrategy) *Session {
	t.Helper()
	return newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{contextStrategyOverride: probe},
	}))
}

// seedResidentAttention appends a durable attention turn directly to history,
// as if it had already been delivered.
func seedResidentAttention(t *testing.T, s *Session, attentionID, stableID, text string) schema.Turn {
	t.Helper()
	turn := schema.NewTurn(schema.TurnSteering, llm.User(text))
	turn.AttentionID = attentionID
	turn.StableTurnID = stableID
	s.mu.Lock()
	s.history = append(s.history, turn)
	s.mu.Unlock()
	return turn
}

// TestAfterAction_HistorySnapshot_AttentionReplacement proves the history an
// enabled strategy reads is a private snapshot: when asynchronous attention
// replaces a resident entry in place while AfterAction holds the slice, the
// strategy keeps seeing the original entry. Run under -race, the borrowed
// slice also races the in-place write before the snapshot copy.
func TestAfterAction_HistorySnapshot_AttentionReplacement(t *testing.T) {
	t.Parallel()
	probe := &borrowProbeStrategy{entered: make(chan struct{}), proceed: make(chan struct{})}
	sess := newBorrowProbeSession(t, probe)
	resident := seedResidentAttention(t, sess, "a1", "s1", "attention one")

	done := make(chan error, 1)
	go func() { done <- sess.notifyStrategyAfterAction(context.Background()) }()
	<-probe.entered

	// Attention delivery replaces the resident entry in place, exactly as
	// appendDelegateAttentionMessageDurably does when the durable content
	// matches a resident turn but its identity has moved on.
	//
	// Release the reader and then mutate deliberately without an intervening
	// synchronization point: the two accesses stay unordered, which is what
	// lets -race observe the borrowed-slice defect. Adding a happens-before
	// edge here (for example, mutating before the release) would mask it. The
	// assertions below read the aliased slice after the mutation, so they also
	// fail deterministically when the fix is absent.
	replacement := resident
	replacement.StableTurnID = "s2"
	close(probe.proceed)
	if err := sess.retainDelegateAttentionTurn(replacement); err != nil {
		t.Fatalf("retainDelegateAttentionTurn: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("notifyStrategyAfterAction: %v", err)
	}

	if len(probe.got) != 1 {
		t.Fatalf("AfterAction snapshot length = %d, want 1", len(probe.got))
	}
	if got := probe.got[0].StableTurnID; got != "s1" {
		t.Fatalf("AfterAction snapshot mutated by attention replacement: StableTurnID = %q, want %q", got, "s1")
	}
	if got, want := probe.read, len("attention one"); got != want {
		t.Fatalf("AfterAction read %d text bytes, want %d", got, want)
	}
}

// TestAfterAction_HistorySnapshot_AttentionRemoval covers the deletion path:
// when failed attention verification removes a resident entry, the backing
// array shifts under mu. A snapshot must not see the shift.
func TestAfterAction_HistorySnapshot_AttentionRemoval(t *testing.T) {
	t.Parallel()
	probe := &borrowProbeStrategy{entered: make(chan struct{}), proceed: make(chan struct{})}
	sess := newBorrowProbeSession(t, probe)
	resident := seedResidentAttention(t, sess, "a1", "s1", "attention one")
	sess.mu.Lock()
	sess.history = append(sess.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("second")))
	sess.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- sess.notifyStrategyAfterAction(context.Background()) }()
	<-probe.entered

	close(probe.proceed)
	// As in the replacement test, the mutation stays unordered with the
	// reader; do not sequence one before the other, which would hide the race.
	sess.removeUnverifiedDelegateAttentionTurn(resident)
	if err := <-done; err != nil {
		t.Fatalf("notifyStrategyAfterAction: %v", err)
	}

	if len(probe.got) != 2 {
		t.Fatalf("AfterAction snapshot length = %d, want 2", len(probe.got))
	}
	if got := probe.got[0].StableTurnID; got != "s1" {
		t.Fatalf("AfterAction snapshot shifted by attention removal: first StableTurnID = %q, want %q", got, "s1")
	}
	if got := probe.got[1].Message.Text(); got != "second" {
		t.Fatalf("AfterAction snapshot second turn = %q, want %q", got, "second")
	}
	if got, want := probe.read, len("attention one")+len("second"); got != want {
		t.Fatalf("AfterAction read %d text bytes, want %d", got, want)
	}
}
