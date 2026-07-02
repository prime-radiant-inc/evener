package contextmgr

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzFc1EstimateUsedTokens drives estimateUsedTokens — the pure token-accounting
// core lifted out of estimatePressure and EstimateUsage (both computed the same
// used-token figure inline) — over adversarial (lastTokens, measuredLen, history,
// sysPromptChars) inputs. The estimator picks between a measured baseline (last
// API token count + only the turns appended since) and the char/4 fallback; this
// puts arbitrary shapes through both branches.
//
// Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same figure;
//   - non-negativity: a used-token estimate is never negative (all inputs are the
//     non-negative production domain: token counts and char counts are lengths);
//   - baseline dominance: when the measurement applies, the baseline path returns
//     at least lastTokens (the appended-turns estimate is non-negative);
//   - append-monotonicity: appending a turn never DECREASES the estimate — the
//     property that makes rising pressure a reliable compaction trigger. This
//     holds in both branches: the fallback re-estimates a superset of turns, and
//     the baseline path estimates a superset of appended turns.
func FuzzFc1EstimateUsedTokens(f *testing.F) {
	f.Add(0, 0, 3, 0)
	f.Add(1000, 2, 5, 400)
	f.Add(500, 9, 1, 12)
	f.Add(1, 0, 0, 1)

	f.Fuzz(func(t *testing.T, lastTokens, measuredLen, nTurns, sysPromptChars int) {
		// Constrain to the production domain: token/char counts are lengths (>=0),
		// and historyLenAtMeasure is a slice length (>=0). Negative measuredLen with
		// lastTokens>0 would index history[neg:] — a state the session never creates.
		if lastTokens < 0 || measuredLen < 0 || sysPromptChars < 0 {
			return
		}
		nTurns %= 64
		if nTurns < 0 {
			nTurns += 64
		}
		history := fc1SyntheticHistory(nTurns)
		// Production invariant: a live measurement (lastTokens > 0) always records
		// the history length AT that measurement, and any later shrink resets
		// lastInputTokens to 0. So measuredLen <= len(history) holds whenever
		// lastTokens > 0. Outside that domain the estimate can legitimately flip
		// from the char/4 fallback to the (smaller) measurement baseline as history
		// grows past measuredLen — a state the session never creates.
		if lastTokens > 0 && measuredLen > len(history) {
			return
		}

		used := estimateUsedTokens(lastTokens, measuredLen, history, sysPromptChars)
		if used2 := estimateUsedTokens(lastTokens, measuredLen, history, sysPromptChars); used != used2 {
			t.Fatalf("non-deterministic: %d vs %d", used, used2)
		}
		if used < 0 {
			t.Fatalf("negative used tokens: %d", used)
		}
		baselineApplies := lastTokens > 0 && measuredLen <= len(history)
		if baselineApplies && used < lastTokens {
			t.Fatalf("baseline path returned %d < lastTokens %d", used, lastTokens)
		}

		// Append-monotonicity: growing history never lowers the estimate.
		grown := append(fc1SyntheticHistory(nTurns), schema.Turn{
			Kind:    schema.TurnUserInput,
			Message: llm.User("one more appended turn of content"),
		})
		usedGrown := estimateUsedTokens(lastTokens, measuredLen, grown, sysPromptChars)
		if usedGrown < used {
			t.Fatalf("appending a turn lowered the estimate: %d -> %d", used, usedGrown)
		}
	})
}

// fc1SyntheticHistory builds a deterministic history of n turns cycling through
// the turn shapes the estimator sees. Content is non-trivial so token estimates
// are non-zero, exercising the char/4 arithmetic.
func fc1SyntheticHistory(n int) []schema.Turn {
	out := make([]schema.Turn, 0, n)
	for i := 0; i < n; i++ {
		switch i % 3 {
		case 0:
			out = append(out, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("user turn content here")})
		case 1:
			out = append(out, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("assistant reply content")})
		default:
			out = append(out, schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed("call", "read_file", "1 | x\n", false)})
		}
	}
	return out
}

// FuzzFc1SummarizationRoutes drives the two pure decision cores behind the
// context summarizer's provider/model selection: summarizationModels (the ordered
// route list) and shouldFallbackSummarizationModel (the per-error advance decision).
//
// Oracles for summarizationModels (beyond never-panic + determinism):
//   - at most two routes (cheap + active);
//   - every emitted route names a non-empty model;
//   - when a cheap model is configured it is the FIRST route (cheap-first);
//   - no two adjacent routes are identical (the active fallback is skipped when it
//     equals the cheap route).
//
// Oracles for shouldFallbackSummarizationModel (beyond never-panic + determinism):
//   - a nil error never advances;
//   - a cancelled/expired context never advances (the caller's own cancellation
//     is terminal, not a provider fallback);
//   - a fallback-classified error under a live context always advances.
func FuzzFc1SummarizationRoutes(f *testing.F) {
	f.Add("gpt-main", "gpt-cheap", true, uint8(0), 400, "bad request", uint8(0))
	f.Add("model-x", "", false, uint8(1), 404, "model does not exist", uint8(5))
	f.Add("", "cheapy", true, uint8(2), 403, "forbidden", uint8(3))
	f.Add("same", "same", true, uint8(0), 429, "rate limited", uint8(2))

	f.Fuzz(func(t *testing.T, model, cheapModel string, withCheap bool,
		ctxSel uint8, status int, msg string, kindSel uint8) {

		prof := testProfile("openai", model, 0)
		if withCheap {
			prof = WithCheapModel(prof, cheapModel)
		}

		routes := summarizationModels(prof)
		if routes2 := summarizationModels(prof); len(routes) != len(routes2) {
			t.Fatalf("non-deterministic route count: %d vs %d", len(routes), len(routes2))
		}
		if len(routes) > 2 {
			t.Fatalf("more than two routes: %d", len(routes))
		}
		for i, r := range routes {
			if r.model == "" {
				t.Fatalf("route %d has empty model: %+v", i, r)
			}
			if i > 0 && routes[i-1] == r {
				t.Fatalf("adjacent duplicate routes at %d: %+v", i, r)
			}
		}
		// Cheap-first: a configured, non-empty cheap model heads the list.
		if cheap := prof.ConfiguredCheapModel(); cheap != "" && len(routes) > 0 {
			if routes[0].model != cheap || routes[0].provider != prof.CheapProvider() {
				t.Fatalf("configured cheap model %q not first: %+v", cheap, routes[0])
			}
		}

		// shouldFallbackSummarizationModel over a crafted error + context state.
		ctx, err := fc1FallbackInput(ctxSel, status, msg)
		got := shouldFallbackSummarizationModel(ctx, err)
		if got2 := shouldFallbackSummarizationModel(ctx, err); got != got2 {
			t.Fatalf("non-deterministic fallback decision: %v vs %v", got, got2)
		}
		if err == nil && got {
			t.Fatalf("nil error must not advance")
		}
		if ctx != nil && ctx.Err() != nil && got {
			t.Fatalf("cancelled/expired context must not advance (err=%v)", err)
		}
		if err != nil && ctx.Err() == nil && llm.Classify(err) == llm.ErrorClassFallback && !got {
			t.Fatalf("fallback-classified error under live context must advance: %v", err)
		}
	})
}

// fc1FallbackInput builds a (context, error) pair covering the branches
// shouldFallbackSummarizationModel discriminates: a live context, a
// cancelled/expired context, a nil error, and a variety of HTTP-classified
// provider errors.
func fc1FallbackInput(ctxSel uint8, status int, msg string) (context.Context, error) {
	var ctx context.Context
	switch ctxSel % 3 {
	case 0:
		ctx = context.Background()
	case 1:
		c, cancel := context.WithCancel(context.Background())
		cancel()
		ctx = c
	default:
		c, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		ctx = c
	}
	var err error
	switch status % 4 {
	case 0:
		err = nil
	case 1:
		err = context.Canceled
	default:
		err = llm.ErrorFromHTTPStatus("openai", status, msg, nil, nil)
	}
	return ctx, err
}
