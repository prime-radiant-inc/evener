package agent

// Tests for Session.CancelQueued (issue #23): an unconsumed queued
// follow-up can be removed from the FIFO input queue so it is never
// consumed. The primitive returns the removed entry's full text and image
// count so the caller (the web UI's edit action) can restore the text into
// the composer and warn about dropped attachments. Like promote (issue
// #22, review F1), a non-empty expectedID must match the entry at index so
// a queue that shifted under the client's snapshot is rejected instead of
// removing the wrong message. Unlike promote, cancel does NOT require an
// active turn: a queued entry is cancellable whenever it is still queued,
// including entries buffered on an idle session.

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
)

func TestSession_CancelQueued_RemovesOnlyThatEntry(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	for _, msg := range []string{"alpha", "bravo", "charlie"} {
		if err := sess.Enqueue(context.Background(), msg); err != nil {
			t.Fatalf("Enqueue %s: %v", msg, err)
		}
	}
	markProcessing(sess)

	text, images, err := sess.CancelQueued(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}
	if text != "bravo" || images != 0 {
		t.Fatalf("CancelQueued returned (%q, %d), want (bravo, 0)", text, images)
	}

	// The other queued messages stay queued, in order.
	preview := sess.QueuePreview()
	if len(preview) != 2 || preview[0] != "alpha" || preview[1] != "charlie" {
		t.Fatalf("QueuePreview after cancel: got %#v, want [alpha charlie]", preview)
	}
	texts := sess.QueueTexts()
	if len(texts) != 2 || texts[0] != "alpha" || texts[1] != "charlie" {
		t.Fatalf("QueueTexts after cancel: got %#v, want [alpha charlie]", texts)
	}
}

func TestSession_CancelQueued_WorksWhileIdle(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "buffered on idle session"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// No turn in flight: cancel is still valid — unlike promote, nothing
	// about removal needs an in-flight turn to inject into.
	text, _, err := sess.CancelQueued(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("CancelQueued idle: %v", err)
	}
	if text != "buffered on idle session" {
		t.Fatalf("CancelQueued returned %q, want the queued text", text)
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth after cancel: got %d, want 0", depth)
	}
}

func TestSession_CancelQueued_ReturnsFullMultilineText(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	full := "first line\nsecond line\nthird line"
	if err := sess.Enqueue(context.Background(), full); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	text, _, err := sess.CancelQueued(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}
	if text != full {
		t.Fatalf("CancelQueued text = %q, want the full untruncated message %q", text, full)
	}
}

func TestSession_CancelQueued_ReportsImageCount(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	images := []ImageAttachment{
		{MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "a.png"},
		{MediaType: "image/png", Data: []byte{4, 5, 6}, Name: "b.png"},
	}
	if err := sess.EnqueueWithImages(context.Background(), "look at these", images); err != nil {
		t.Fatalf("EnqueueWithImages: %v", err)
	}
	text, n, err := sess.CancelQueued(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}
	if text != "look at these" || n != 2 {
		t.Fatalf("CancelQueued returned (%q, %d), want (look at these, 2)", text, n)
	}
}

func TestSession_CancelQueued_EmitsQueueChangedWithTexts(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	for _, msg := range []string{"alpha", "bravo"} {
		if err := sess.Enqueue(context.Background(), msg); err != nil {
			t.Fatalf("Enqueue %s: %v", msg, err)
		}
	}
	if _, _, err := sess.CancelQueued(context.Background(), 0, ""); err != nil {
		t.Fatalf("CancelQueued: %v", err)
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
	if last.Depth != 1 {
		t.Fatalf("QueueChanged depth = %d, want 1", last.Depth)
	}
	if len(last.Texts) != 1 || last.Texts[0] != "bravo" {
		t.Fatalf("QueueChanged Texts = %#v, want [bravo] (full text, FIFO-aligned with Preview)", last.Texts)
	}
	if len(last.IDs) != 1 || last.IDs[0] == "" {
		t.Fatalf("QueueChanged IDs = %#v, want one non-empty id", last.IDs)
	}
}

func TestSession_CancelQueued_IndexOutOfRange(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := sess.CancelQueued(context.Background(), 5, ""); err == nil {
		t.Fatal("CancelQueued out-of-range: expected error, got nil")
	}
	if _, _, err := sess.CancelQueued(context.Background(), -1, ""); err == nil {
		t.Fatal("CancelQueued negative index: expected error, got nil")
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "alpha" {
		t.Fatalf("QueuePreview after rejected cancel: got %#v, want [alpha]", preview)
	}
}

func TestSession_CancelQueued_ExpectedIDMismatchLeavesQueueIntact(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	for _, msg := range []string{"alpha", "bravo"} {
		if err := sess.Enqueue(context.Background(), msg); err != nil {
			t.Fatalf("Enqueue %s: %v", msg, err)
		}
	}
	ids := sess.QueueIDs()
	if len(ids) != 2 {
		t.Fatalf("QueueIDs = %#v, want 2 ids", ids)
	}
	// Review F1 shape: the id belongs to the OTHER entry (a stale snapshot
	// after the queue shifted). The cancel must fail and remove nothing.
	_, _, err := sess.CancelQueued(context.Background(), 0, ids[1])
	if err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("CancelQueued mismatch err=%v, want 'no longer matches'", err)
	}
	if preview := sess.QueuePreview(); len(preview) != 2 {
		t.Fatalf("QueuePreview after mismatch: got %#v, want both entries intact", preview)
	}
}

func TestSession_CancelQueued_MatchingExpectedIDSucceeds(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	for _, msg := range []string{"alpha", "bravo"} {
		if err := sess.Enqueue(context.Background(), msg); err != nil {
			t.Fatalf("Enqueue %s: %v", msg, err)
		}
	}
	ids := sess.QueueIDs()
	text, _, err := sess.CancelQueued(context.Background(), 1, ids[1])
	if err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}
	if text != "bravo" {
		t.Fatalf("CancelQueued text = %q, want bravo", text)
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "alpha" {
		t.Fatalf("QueuePreview after cancel: got %#v, want [alpha]", preview)
	}
}

func TestSession_CancelQueued_ClosedSession(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	sess.Close()
	if _, _, err := sess.CancelQueued(context.Background(), 0, ""); err == nil {
		t.Fatal("CancelQueued on closed session: expected error, got nil")
	}
}

func TestSession_CancelQueued_CanceledContext(t *testing.T) {
	t.Parallel()
	sess := newPromoteTestSession(t)
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := sess.CancelQueued(ctx, 0, ""); err == nil {
		t.Fatal("CancelQueued with canceled ctx: expected error, got nil")
	}
	if depth := sess.QueueDepth(); depth != 1 {
		t.Fatalf("QueueDepth after ctx-canceled cancel: got %d, want 1", depth)
	}
}
