package contextmgr

// Tests for replaceSteeringMarkerTurn, the shared marker-swap helper behind
// the three self-injecting strategies (memory-crystals, ooda, and
// recursive-distill share it so the marker filtering, pre-baseline removal
// counting, and net-delta reporting cannot drift apart).

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func steeringMarkerTurn(text string) schema.Turn {
	return schema.NewTurn(schema.TurnSteering, llm.User(text))
}

func plainMarkerTestTurn(text string) schema.Turn {
	return schema.NewTurn(schema.TurnUserInput, llm.User(text))
}

// markerTexts returns the texts of steering turns containing marker, in order.
func markerTexts(history []schema.Turn, marker string) []string {
	var texts []string
	for _, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), marker) {
			texts = append(texts, t.Message.Text())
		}
	}
	return texts
}

func TestReplaceSteeringMarkerTurn_AppendsWhenAbsent(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{plainMarkerTestTurn("a"), plainMarkerTestTurn("b")}
	total := 0
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { total += n })

	replaceSteeringMarkerTurn(ctx, &history, "[MARK]", "[MARK] fresh")

	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	if got := history[2].Message.Text(); got != "[MARK] fresh" {
		t.Fatalf("appended turn = %q, want the fresh marker at the end", got)
	}
	if total != 1 {
		t.Fatalf("reported injection delta = %d, want 1 for a plain append", total)
	}
}

func TestReplaceSteeringMarkerTurn_SwapWithoutBaselineReportsZero(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		plainMarkerTestTurn("a"),
		steeringMarkerTurn("[MARK] stale"),
		plainMarkerTestTurn("b"),
	}
	total := 0
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { total += n })

	replaceSteeringMarkerTurn(ctx, &history, "[MARK]", "[MARK] fresh")

	if got := markerTexts(history, "[MARK]"); len(got) != 1 || got[0] != "[MARK] fresh" {
		t.Fatalf("marker turns after swap = %v, want exactly the fresh one", got)
	}
	if got := history[len(history)-1].Message.Text(); got != "[MARK] fresh" {
		t.Fatalf("fresh marker at %q, want the end of history", got)
	}
	if total != 0 {
		t.Fatalf("reported injection delta = %d, want 0 for a one-for-one swap with no baseline installed", total)
	}
}

func TestReplaceSteeringMarkerTurn_PreBaselineSwapAddsCorrection(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		steeringMarkerTurn("[MARK] stale"),
		plainMarkerTestTurn("x"),
		plainMarkerTestTurn("y"),
	}
	total := 0
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { total += n })
	ctx = WithBaselineQuery(ctx, func() (int, bool) { return 2, true })

	replaceSteeringMarkerTurn(ctx, &history, "[MARK]", "[MARK] fresh")

	if got := markerTexts(history, "[MARK]"); len(got) != 1 || got[0] != "[MARK] fresh" {
		t.Fatalf("marker turns after swap = %v, want exactly the fresh one", got)
	}
	// The removal at index 0 sits before the baseline (2): every in-flight
	// turn shifted left by one, which the trailing re-append does not undo,
	// so the net delta (0) must be corrected by +1.
	if total != 1 {
		t.Fatalf("reported injection delta = %d, want 1 (net 0 swap + 1 pre-baseline removal)", total)
	}
}

// A steering turn that merely MENTIONS the marker text is not a marker turn:
// only turns that begin with the marker are the strategy's own banners, and
// only those may be swapped out.
func TestReplaceSteeringMarkerTurn_KeepsTurnsThatOnlyMentionTheMarker(t *testing.T) {
	t.Parallel()
	const mention = "note: the [MARK] banner will be refreshed on the next fold"
	history := []schema.Turn{
		steeringMarkerTurn("[MARK] stale"),
		steeringMarkerTurn(mention),
		plainMarkerTestTurn("x"),
	}
	total := 0
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { total += n })

	replaceSteeringMarkerTurn(ctx, &history, "[MARK]", "[MARK] fresh")

	found := false
	for _, t := range history {
		if t.Message.Text() == mention {
			found = true
		}
	}
	if !found {
		t.Fatalf("a steering turn that only mentions the marker was removed as if it were the marker banner; history = %v", markerTexts(history, "[MARK]"))
	}
	if got := markerTexts(history, "[MARK]"); len(got) != 2 || got[1] != "[MARK] fresh" {
		t.Fatalf("marker-bearing turns after swap = %v, want the mention kept and exactly one fresh banner at the end", got)
	}
	if total != 0 {
		t.Fatalf("reported injection delta = %d, want 0 for a one-for-one banner swap", total)
	}
}

func TestReplaceSteeringMarkerTurn_CountsEveryPreBaselineRemoval(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		steeringMarkerTurn("[MARK] stale one"),
		plainMarkerTestTurn("x"),
		steeringMarkerTurn("[MARK] stale two"),
		plainMarkerTestTurn("y"),
		steeringMarkerTurn("[MARK] stale three"),
	}
	total := 0
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { total += n })
	ctx = WithBaselineQuery(ctx, func() (int, bool) { return 3, true })

	replaceSteeringMarkerTurn(ctx, &history, "[MARK]", "[MARK] fresh")

	if got := markerTexts(history, "[MARK]"); len(got) != 1 || got[0] != "[MARK] fresh" {
		t.Fatalf("marker turns after swap = %v, want exactly the fresh one", got)
	}
	if got, want := len(history), 3; got != want {
		t.Fatalf("history length = %d, want %d ([x, y, fresh marker])", got, want)
	}
	if history[0].Message.Text() != "x" || history[1].Message.Text() != "y" {
		t.Fatalf("surviving turns out of order: %q, %q", history[0].Message.Text(), history[1].Message.Text())
	}
	// Three removals, two of them (indexes 0 and 2) before the baseline (3):
	// net delta 3-5 = -2, corrected by +2 — one per pre-baseline removal,
	// not a flat +1 for the last match.
	if total != 0 {
		t.Fatalf("reported injection delta = %d, want 0 (net -2 + 2 pre-baseline removals)", total)
	}
}
