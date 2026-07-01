package contextmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/fuzz/oracle"
	"primeradiant.com/serf/llm"
)

// This file fuzzes ObsMaskStrategy.ManageContext and its aggressive
// observation-masking core (aggressiveMaskObservations) in strategy_obs_mask.go.
//
// New top-level identifiers are prefixed with the lane token "cxjs_" so this
// file never collides with the sibling "ctxmgr_" fuzz lane in the same package.
// It reuses that lane's transcript builder (ctxmgr_reader / ctxmgr_buildHistory),
// the shared fingerprint helper (ctxmgr_fingerprints), the package's
// no-op emit sink (ctxmgr_noopEmit), and the profile/Manager test fixtures
// rather than redefining any of them.

// cxjs_edgeTurns builds tool-result turns that exercise the branches of
// aggressiveMaskObservations that ctxmgr_buildHistory never produces: a
// ContentToolResult part whose ToolResult pointer is nil, a tool-result turn
// carrying a non-tool-result part, and a tool result whose Content is a
// non-string value. Which edges are appended is selected by sel so the fuzzer
// can drive any combination.
func cxjs_edgeTurns(sel uint8) []schema.Turn {
	var turns []schema.Turn
	if sel&0x01 != 0 {
		turns = append(turns, schema.NewTurn(schema.TurnToolResults, llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: nil}},
		}))
	}
	if sel&0x02 != 0 {
		turns = append(turns, schema.NewTurn(schema.TurnToolResults, llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "not a tool result"}},
		}))
	}
	if sel&0x04 != 0 {
		turns = append(turns, schema.NewTurn(schema.TurnTool, llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				Name:    "structured",
				Content: map[string]any{"nested": "value"},
			}}},
		}))
	}
	if sel&0x08 != 0 {
		// A long, unmasked, non-error string result that WILL be rewritten.
		turns = append(turns, schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(
			"call_edge", "read_file",
			strings.Repeat("long output content that should be masked ", 8), false)))
	}
	return turns
}

// cxjs_buildObsHistory assembles the fuzzed transcript plus the selected edge
// turns. Edge turns are placed at the FRONT so a small preserve count leaves
// them inside the masking window (history[:cutoff]).
func cxjs_buildObsHistory(data []byte, edgeSel uint8) []schema.Turn {
	r := &ctxmgr_reader{b: data}
	base := ctxmgr_buildHistory(r)
	edges := cxjs_edgeTurns(edgeSel)
	return append(edges, base...)
}

// cxjs_historyJSON serializes a history to JSON for structural equality; it is
// the eq basis for the idempotence/preservation oracles on the masking pass.
func cxjs_historyJSON(h []schema.Turn) string {
	b, _ := json.Marshal(h)
	return string(b)
}

// cxjs_cloneHistory deep-copies a history through JSON so the in-place masking
// pass can be applied to an isolated copy without disturbing the original.
func cxjs_cloneHistory(h []schema.Turn) []schema.Turn {
	if h == nil {
		return nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil
	}
	var out []schema.Turn
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// cxjs_maskOnce returns the result of aggressiveMaskObservations applied to a
// fresh clone of h, as a pure function suitable for the idempotence oracle.
func cxjs_maskOnce(preserve int) func([]schema.Turn) []schema.Turn {
	return func(h []schema.Turn) []schema.Turn {
		c := cxjs_cloneHistory(h)
		aggressiveMaskObservations(c, preserve)
		return c
	}
}

// FuzzCxjsObsMaskManageContext drives ObsMaskStrategy.ManageContext end to end
// over fuzzed transcript + masking/checkpoint threshold state, and additionally
// pins the invariants of the aggressiveMaskObservations core.
//
// Oracles (never a bare no-panic):
//   - ManageContext never panics and returns nil; compaction never grows the
//     history (len(after) <= len(before)).
//   - determinism: identical input + thresholds → identical resulting history
//     (compared by turn kind + message content).
//   - the two defensive guards (nil Manager, non-positive context window) are
//     no-ops that leave the history untouched.
//   - masking preserves turn count (oracle.Preserves) and is idempotent
//     (oracle.Idempotent) — a second pass is a no-op because "[name: OK]" markers
//     are recognized as already-masked.
//   - error tool results are never masked; every rewritten result is exactly the
//     "[name: OK]" marker; recent turns (>= cutoff) are byte-identical.
func FuzzCxjsObsMaskManageContext(f *testing.F) {
	f.Add([]byte("\x05\x03hey\x06\x02ls\x040\x00\x03end\x02\x04work"), uint8(0x0F), uint8(3), uint8(0), uint8(7))
	f.Add([]byte("\x00\x05hello\x02\x04work\x00\x03end"), uint8(0x00), uint8(0), uint8(0x80), uint8(1))
	f.Add([]byte{}, uint8(0x01), uint8(1), uint8(0), uint8(0))
	f.Add([]byte{}, uint8(0x00), uint8(1), uint8(0), uint8(0))
	f.Add(ctxmgr_richHistorySeed, uint8(0x0C), uint8(2), uint8(0), uint8(200))
	f.Add(ctxmgr_richHistorySeed, uint8(0x0F), uint8(255), uint8(0x80), uint8(50))

	f.Fuzz(func(t *testing.T, data []byte, edgeSel uint8, preserveSel uint8, cpSel uint8, sysSel uint8) {
		// ctxmgr_buildHistory already caps at 60 turns; bounding the raw bytes
		// keeps the fuzzer from ballooning inputs it cannot turn into more
		// history, which otherwise slows execs without adding coverage.
		if len(data) > 4096 {
			data = data[:4096]
		}
		history := cxjs_buildObsHistory(data, edgeSel)
		preserve := 1 + int(preserveSel)%12
		sysChars := int(sysSel) * 32

		run := func() []schema.Turn {
			h := cxjs_cloneHistory(history)
			profile := testOpenAIProfileWithContextWindow(1000)
			cm := NewManager(profile, llm.NewClient())
			// Layer 1 always fires; Layer 2 (checkpoint) toggles via cpSel so both
			// the mask-only and mask-then-checkpoint pipelines are exercised.
			cm.ObservationMaskThreshold = 0
			if cpSel&0x80 != 0 {
				cm.CheckpointThreshold = 0
			} else {
				cm.CheckpointThreshold = 2.0
			}
			cm.PreserveRecentTurns = preserve

			s := NewObsMaskStrategy(cm)
			before := len(h)
			if err := s.ManageContext(context.Background(), &h, sysChars, ctxmgr_noopEmit); err != nil {
				t.Fatalf("ManageContext returned error: %v", err)
			}
			if len(h) > before {
				t.Fatalf("compaction grew history: %d -> %d", before, len(h))
			}
			return h
		}
		a := run()
		b := run()
		if !oracle.DeepEqual(ctxmgr_fingerprints(a), ctxmgr_fingerprints(b)) {
			t.Fatalf("ObsMaskStrategy.ManageContext not deterministic")
		}

		cxjs_checkGuards(t, history, sysChars)
		cxjs_checkMaskInvariants(t, history, preserve)
	})
}

// cxjs_checkGuards exercises the two early-return guards of ManageContext: a nil
// Manager and a non-positive context window. Both must be no-ops that leave the
// history unchanged and return nil.
func cxjs_checkGuards(t *testing.T, history []schema.Turn, sysChars int) {
	// nil Manager guard.
	nilStrat := &ObsMaskStrategy{cm: nil}
	h1 := cxjs_cloneHistory(history)
	before := cxjs_historyJSON(h1)
	if err := nilStrat.ManageContext(context.Background(), &h1, sysChars, ctxmgr_noopEmit); err != nil {
		t.Fatalf("nil-Manager ManageContext returned error: %v", err)
	}
	if cxjs_historyJSON(h1) != before {
		t.Fatalf("nil-Manager guard mutated history")
	}

	// Non-positive context window guard: a zero-value profile reports window 0.
	cm := NewManager(&provider.Profile{}, llm.NewClient())
	cm.ObservationMaskThreshold = 0
	cm.CheckpointThreshold = 0
	zeroStrat := NewObsMaskStrategy(cm)
	h2 := cxjs_cloneHistory(history)
	before2 := cxjs_historyJSON(h2)
	if err := zeroStrat.ManageContext(context.Background(), &h2, sysChars, ctxmgr_noopEmit); err != nil {
		t.Fatalf("zero-window ManageContext returned error: %v", err)
	}
	if cxjs_historyJSON(h2) != before2 {
		t.Fatalf("zero-window guard mutated history")
	}
}

// cxjs_checkMaskInvariants pins the invariants of aggressiveMaskObservations
// directly, independent of the ManageContext thresholds.
func cxjs_checkMaskInvariants(t *testing.T, history []schema.Turn, preserve int) {
	f := cxjs_maskOnce(preserve)
	oracle.Preserves(t, f, history, func(h []schema.Turn) int { return len(h) })
	oracle.Idempotent(t, f, history, func(x, y []schema.Turn) bool {
		return cxjs_historyJSON(x) == cxjs_historyJSON(y)
	})

	cutoff := len(history) - preserve
	before := cxjs_cloneHistory(history)
	masked := cxjs_cloneHistory(history)
	aggressiveMaskObservations(masked, preserve)

	for i := range masked {
		if i >= cutoff {
			// Recent turns are untouched.
			if cxjs_turnJSON(masked[i]) != cxjs_turnJSON(before[i]) {
				t.Fatalf("recent turn %d (>= cutoff %d) was mutated", i, cutoff)
			}
			continue
		}
		for j := range masked[i].Message.Content {
			mp := masked[i].Message.Content[j]
			bp := before[i].Message.Content[j]
			if mp.Kind != llm.ContentToolResult || mp.ToolResult == nil {
				continue
			}
			// Error results are never masked.
			if bp.ToolResult != nil && bp.ToolResult.IsError {
				if cxjs_turnJSON(masked[i]) != cxjs_turnJSON(before[i]) {
					t.Fatalf("error tool result at turn %d was masked", i)
				}
			}
			// Any content that changed must have become the "[name: OK]" marker.
			if !cxjs_contentEqual(mp.ToolResult.Content, bp.ToolResult.Content) {
				want := "[" + mp.ToolResult.Name + ": OK]"
				got, ok := mp.ToolResult.Content.(string)
				if !ok || got != want {
					t.Fatalf("rewritten result at turn %d is %#v, want %q", i, mp.ToolResult.Content, want)
				}
			}
		}
	}
}

func cxjs_turnJSON(turn schema.Turn) string {
	b, _ := json.Marshal(turn.Message)
	return string(turn.Kind) + "\x00" + string(b)
}

func cxjs_contentEqual(a, b any) bool {
	ba, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ba, bb)
}
