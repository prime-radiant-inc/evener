//go:build serffuzz

package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// This file fuzzes the pure round-content decision cores lifted out of
// processOneInput: classifyRoundContent (the noContent/hasPhase/skipHistory split
// that decides what to append to history and whether to retry) and
// routeNoToolCalls (the no-tool-calls route that finishes idle for a bare-text
// system turn vs runs the empty-retry budget). Both are pure over their inputs, so
// fuzzing them directly exercises the branch logic the effectful loop buries under
// a model round.
//
// The lf_ prefix marks helpers owned by this refactor/fuzz lane.

var lf_entryKinds = []EntryKind{
	EntryUserInput, EntryContinuation, EntryNotification, EntryWatchDelivery,
}

// lf_buildContent turns a byte mask into content parts, setting a non-empty Phase
// on selected parts so the hasPhase branch is reachable independent of text.
func lf_buildContent(phaseMask uint8, count uint8) []llm.ContentPart {
	n := int(count % 5)
	parts := make([]llm.ContentPart, 0, n)
	for i := 0; i < n; i++ {
		p := llm.ContentPart{Kind: llm.ContentText}
		if phaseMask&(1<<uint(i%8)) != 0 {
			p.Phase = "final_answer"
		}
		parts = append(parts, p)
	}
	return parts
}

func FuzzLfClassifyRoundContent(f *testing.F) {
	// Seeds hitting distinct branches.
	f.Add("", 0, uint8(0), uint8(0))        // noContent + no phase => skipHistory
	f.Add("", 0, uint8(1), uint8(2))        // noContent + phase => not skipHistory
	f.Add("hello", 0, uint8(0), uint8(1))   // has text => not noContent
	f.Add("", 2, uint8(0), uint8(0))        // has tool calls => not noContent
	f.Add("  \t\n ", 0, uint8(4), uint8(3)) // whitespace-only text + phase

	f.Fuzz(func(t *testing.T, txt string, callCount int, phaseMask, partCount uint8) {
		content := lf_buildContent(phaseMask, partCount)

		got := classifyRoundContent(txt, callCount, content)

		// Determinism: identical inputs yield an identical classification.
		if got2 := classifyRoundContent(txt, callCount, content); got != got2 {
			t.Fatalf("nondeterministic: %+v vs %+v", got, got2)
		}

		// NoContent is exactly "blank text AND no tool calls".
		wantNoContent := strings.TrimSpace(txt) == "" && callCount == 0
		if got.NoContent != wantNoContent {
			t.Fatalf("NoContent=%v want %v (txt=%q callCount=%d)", got.NoContent, wantNoContent, txt, callCount)
		}
		// NoContent implies no calls and blank text.
		if got.NoContent && !(callCount == 0 && strings.TrimSpace(txt) == "") {
			t.Fatalf("NoContent true but callCount=%d txt=%q", callCount, txt)
		}
		// SkipHistory implies NoContent.
		if got.SkipHistory && !got.NoContent {
			t.Fatalf("SkipHistory without NoContent: %+v", got)
		}
		// SkipHistory is exactly NoContent AND not HasPhase.
		if got.SkipHistory != (got.NoContent && !got.HasPhase) {
			t.Fatalf("SkipHistory=%v but NoContent=%v HasPhase=%v", got.SkipHistory, got.NoContent, got.HasPhase)
		}
	})
}

func FuzzLfRouteNoToolCalls(f *testing.F) {
	f.Add(uint8(0), false) // EntryUserInput, empty => runNoToolCalls
	f.Add(uint8(3), true)  // EntryWatchDelivery, non-empty => finishIdle
	f.Add(uint8(2), true)  // EntryNotification, non-empty => finishIdle
	f.Add(uint8(2), false) // EntryNotification, empty => runNoToolCalls
	f.Add(uint8(1), true)  // EntryContinuation, non-empty => runNoToolCalls

	f.Fuzz(func(t *testing.T, kindSel uint8, noContent bool) {
		kind := lf_entryKinds[int(kindSel)%len(lf_entryKinds)]

		route := routeNoToolCalls(kind, noContent)

		// Determinism.
		if route2 := routeNoToolCalls(kind, noContent); route != route2 {
			t.Fatalf("nondeterministic route: %v vs %v", route, route2)
		}
		// Total function: exactly one of the two valid routes.
		switch route {
		case finishIdle, runNoToolCalls:
		default:
			t.Fatalf("invalid route %v", route)
		}
		// finishIdle is chosen ONLY for a non-empty watch-delivery or notification
		// turn; every other combination routes through the retry budget.
		wantFinish := (kind == EntryWatchDelivery || kind == EntryNotification) && !noContent
		if (route == finishIdle) != wantFinish {
			t.Fatalf("route=%v wantFinish=%v (kind=%v noContent=%v)", route, wantFinish, kind, noContent)
		}
		// An empty round never finishes idle (it must reach the empty-retry path).
		if noContent && route != runNoToolCalls {
			t.Fatalf("empty round routed to %v, want runNoToolCalls", route)
		}
	})
}
