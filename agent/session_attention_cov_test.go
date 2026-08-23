package agent

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestNewDelegateAttentionFoldAndPendingIDs covers newDelegateAttentionFold
// and the pendingIDs method (session_attention.go:190-208).
func TestNewDelegateAttentionFoldAndPendingIDs(t *testing.T) {
	t.Parallel()
	fold := newDelegateAttentionFold()
	if fold.content == nil || fold.turns == nil || fold.resolutions == nil || fold.resumeGenerations == nil || fold.deliveryCommits == nil {
		t.Fatal("newDelegateAttentionFold did not initialize all maps")
	}
	// No items -> no pending.
	if got := fold.pendingIDs(); len(got) != 0 {
		t.Fatalf("empty fold pendingIDs = %v, want empty", got)
	}
	// Add two attention items.
	fold.content["attn_1"] = llm.User("hello")
	fold.content["attn_2"] = llm.User("world")
	fold.order = append(fold.order, "attn_1", "attn_2")
	// Both pending.
	got := fold.pendingIDs()
	if len(got) != 2 || got[0] != "attn_1" || got[1] != "attn_2" {
		t.Fatalf("pendingIDs = %v, want [attn_1 attn_2]", got)
	}
	// Resolve attn_1 -> only attn_2 pending.
	fold.resolutions["attn_1"] = delegateAttentionConsumed
	got = fold.pendingIDs()
	if len(got) != 1 || got[0] != "attn_2" {
		t.Fatalf("after resolve: pendingIDs = %v, want [attn_2]", got)
	}
	// Resolve both -> none pending.
	fold.resolutions["attn_2"] = delegateAttentionDiscarded
	got = fold.pendingIDs()
	if len(got) != 0 {
		t.Fatalf("all resolved: pendingIDs = %v, want empty", got)
	}
}

// TestHasPendingRootDelegateAttentionNilGuard covers the nil-session guard
// (session_attention.go:586-587).
func TestHasPendingRootDelegateAttentionNilGuard(t *testing.T) {
	t.Parallel()
	var s *Session
	if s.hasPendingRootDelegateAttention() {
		t.Fatal("nil session should return false")
	}
}

// TestIsRootDelegateAttentionReceiverNilGuard covers the nil-session and
// nil-controller guards (session_attention.go:553-554).
func TestIsRootDelegateAttentionReceiverNilGuard(t *testing.T) {
	t.Parallel()
	var s *Session
	if s.isRootDelegateAttentionReceiver() {
		t.Fatal("nil session should return false")
	}
	// Session without delegate controller returns false — create a bare
	// session that never initialized a delegate controller.
	s2 := &Session{}
	if s2.isRootDelegateAttentionReceiver() {
		t.Fatal("session without controller should return false")
	}
}

// TestHasPendingDelegateAttentionArmRetryNilGuard covers the nil-session guard
// (session_attention.go:486-487).
func TestHasPendingDelegateAttentionArmRetryNilGuard(t *testing.T) {
	t.Parallel()
	var s *Session
	if s.hasPendingDelegateAttentionArmRetry() {
		t.Fatal("nil session should return false")
	}
}

// TestPendingDelegateAttentionIDsNilGuard covers the nil-session guard
// (session_attention.go:496-497).
func TestPendingDelegateAttentionIDsNilGuard(t *testing.T) {
	t.Parallel()
	var s *Session
	ids, err := s.pendingDelegateAttentionIDs()
	if err != nil || ids != nil {
		t.Fatalf("nil session: ids=%v err=%v", ids, err)
	}
}

// TestScheduleStableDelegateAttentionRetryNilGuard covers the nil-session guard
// (session_attention.go:741-742).
func TestScheduleStableDelegateAttentionRetryNilGuard(t *testing.T) {
	t.Parallel()
	var s *Session
	// Should not panic.
	s.scheduleStableDelegateAttentionRetry()
}

// TestResetStableDelegateAttentionRetryNilGuard covers the nil-session guard
// (session_attention.go:785-786).
func TestResetStableDelegateAttentionRetryNilGuard(t *testing.T) {
	t.Parallel()
	var s *Session
	// Should not panic.
	s.resetStableDelegateAttentionRetry()
}

// TestFinishRootDelegateAttentionTurnEmptyIDs covers the empty-ids early-return
// (session_attention.go:622-623).
func TestFinishRootDelegateAttentionTurnEmptyIDs(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if err := s.finishRootDelegateAttentionTurn(nil, nil); err != nil {
		t.Fatalf("empty ids: %v", err)
	}
	if err := s.finishRootDelegateAttentionTurn([]string{}, nil); err != nil {
		t.Fatalf("empty slice: %v", err)
	}
}

// TestFoldDelegateAttentionResolutionBeforeAppend covers the error path where
// a resolution appears before the attention was appended
// (session_attention.go:135-136).
func TestFoldDelegateAttentionResolutionBeforeAppend(t *testing.T) {
	t.Parallel()
	resolutionTurn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("resolved"))
	resolutionTurn.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID: "attn_missing",
		Disposition: string(delegateAttentionConsumed),
	}
	_, err := foldDelegateAttention([]transcript.Entry{{Turn: resolutionTurn}})
	if err == nil {
		t.Fatal("resolution before append should error")
	}
}

// TestFoldDelegateAttentionInvalidResolutionTurn covers the error path where
// a resolution turn has an attention ID set or wrong kind
// (session_attention.go:125-126).
func TestFoldDelegateAttentionInvalidResolutionTurn(t *testing.T) {
	t.Parallel()
	// Resolution turn with attention ID set (should not be set on resolution turns).
	resolutionTurn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("resolved"))
	resolutionTurn.AttentionID = "attn_1"
	resolutionTurn.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID: "attn_1",
		Disposition: string(delegateAttentionConsumed),
	}
	_, err := foldDelegateAttention([]transcript.Entry{{Turn: resolutionTurn}})
	if err == nil {
		t.Fatal("resolution with attention ID should error")
	}

	// Resolution turn with wrong kind.
	badTurn := schema.NewTurn(schema.TurnSteering, llm.System("x"))
	badTurn.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID: "attn_1",
		Disposition: string(delegateAttentionConsumed),
	}
	_, err = foldDelegateAttention([]transcript.Entry{{Turn: badTurn}})
	if err == nil {
		t.Fatal("resolution with wrong kind should error")
	}
}

// TestFoldDelegateAttentionResolutionTurnNoResolution covers the error path
// where a TurnAttentionResolution has no resolution info
// (session_attention.go:120-121).
func TestFoldDelegateAttentionResolutionTurnNoResolution(t *testing.T) {
	t.Parallel()
	turn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("no resolution"))
	_, err := foldDelegateAttention([]transcript.Entry{{Turn: turn}})
	if err == nil {
		t.Fatal("attention resolution turn with no resolution should error")
	}
}

// TestFoldDelegateAttentionInvalidDisposition covers the error path where a
// resolution has an invalid disposition string
// (session_attention.go:129-130).
func TestFoldDelegateAttentionInvalidDisposition(t *testing.T) {
	t.Parallel()
	// First add the attention.
	attentionTurn := schema.NewTurn(schema.TurnSteering, llm.User("hello"))
	attentionTurn.AttentionID = "attn_1"
	// Then resolve with invalid disposition.
	resolutionTurn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("resolved"))
	resolutionTurn.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID: "attn_1",
		Disposition: "bogus",
	}
	_, err := foldDelegateAttention([]transcript.Entry{
		{Turn: attentionTurn},
		{Turn: resolutionTurn},
	})
	if err == nil {
		t.Fatal("invalid disposition should error")
	}
}

// TestFoldDelegateAttentionDiscardedWithResumeGeneration covers the error path
// where a discarded resolution carries a non-zero resume generation
// (session_attention.go:132-133).
func TestFoldDelegateAttentionDiscardedWithResumeGeneration(t *testing.T) {
	t.Parallel()
	attentionTurn := schema.NewTurn(schema.TurnSteering, llm.User("hello"))
	attentionTurn.AttentionID = "attn_1"
	resolutionTurn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("resolved"))
	resolutionTurn.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID:      "attn_1",
		Disposition:      string(delegateAttentionDiscarded),
		ResumeGeneration: 5,
	}
	_, err := foldDelegateAttention([]transcript.Entry{
		{Turn: attentionTurn},
		{Turn: resolutionTurn},
	})
	if err == nil {
		t.Fatal("discarded with resume generation should error")
	}
}

// TestFoldDelegateAttentionEmpty is a sanity test for the happy path with no
// entries.
func TestFoldDelegateAttentionEmpty(t *testing.T) {
	t.Parallel()
	fold, err := foldDelegateAttention(nil)
	if err != nil {
		t.Fatalf("empty fold: %v", err)
	}
	if got := fold.pendingIDs(); len(got) != 0 {
		t.Fatalf("empty fold pendingIDs = %v", got)
	}
}
