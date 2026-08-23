package contextmgr

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"

	"pgregory.net/rapid"
)

// TestForceCompactDoubleLayerSeqFuzz is the double-layer counterpart to
// TestCompactionSeqFuzz. That test (and its MaybeCompact sibling,
// TestFc1MaybeCompactSeqFuzz) both build their Manager with a nil LLM client,
// which is never the live shape: NewSession hard-errors before constructing a
// context manager with a nil client (agent/session_init.go:151-153), so every
// live session's ForceCompact runs BOTH layers back-to-back in one call —
// Layer 1 (deterministic checkpoint, context_manager.go:442) unconditionally,
// then Layer 2 (LLM summarization via summarizeWithLLMSteered,
// context_manager.go:456) on the already-checkpointed history. Layer 2's
// prompt construction, its cm.client.Complete call, and its result-splice
// (context_manager.go:1392,1410) had zero property-test executions before
// this test (issue #168 / kata dk86).
//
// This drives the same stateful op-sequence model as TestCompactionSeqFuzz —
// accumulate history, then compact — but with a real, non-nil *llm.Client
// wired to the fakeAdapter test double (registered exactly as
// compaction_kind_test.go's TestSummarizeWithLLM_UsesTurnSummaryKind does),
// so ForceCompact's Layer 2 branch actually executes. The fake's response
// text is left at its trivial default (llm.Assistant("done")) deliberately:
// see I5 below for why the survival oracle must not depend on what the fake
// says.
//
// The op vocabulary adds TurnSteering, TurnHookCompleted, and
// TurnAttentionResolution turns (the marker kinds safeCutoff special-cases,
// context_manager.go:1428-1436) to the existing user/note/edit/read/communicate
// set. These are the turns most likely to land at the seam between Layer 1's
// cutoff and Layer 2's own, separate safeCutoff call on the already-checkpointed
// history — a seam neither single-layer seqfuzz test can construct, since
// Layer 2 never runs in either of them.
//
// After each op it checks, weakest-first:
//
//	I1 (length monotonicity): an add op strictly grows history; a compact op
//	   never grows it. Checked twice: once against the combined ForceCompact
//	   result (outer test loop, same as TestCompactionSeqFuzz), and once
//	   against c1 — Layer 1's own checkpoint() output — directly. The second
//	   check matters here specifically: Layer 2 fires in nearly every case
//	   Layer 1 also does, and unconditionally replaces *history with its own
//	   re-cut result, which would otherwise re-normalize a Layer-1-only growth
//	   bug down to a shrunk length and hide it from the combined-result check.
//	I2 (single compaction marker, at the front): at most one turn of kind
//	   TurnCheckpoint OR TurnSummary exists at any time (never both at once),
//	   and when present it is at index 0. Generalizes the single-layer I2:
//	   with a live client, ForceCompact's Layer 2 always subsumes Layer 1's
//	   TurnCheckpoint into its own TurnSummary in the same call — Layer 2's
//	   safeCutoff position is always >= 1 into the [checkpoint, ...tail]
//	   array Layer 1 produced, so the checkpoint turn always lands in the
//	   dropped prefix Layer 2 folds away. A history with a live TurnCheckpoint
//	   at index 0 after a compact op means Layer 2 declined (too little
//	   content past preserveRecent, or its own safeCutoff returned -1).
//	I3/I4 (orphan oracle, both directions): unchanged from TestCompactionSeqFuzz
//	   — every tool result has an earlier call, every tool call has a
//	   later-or-equal result. Checked over the full history after every op.
//	I5 (retained content survives — split from the single-layer I5): checked
//	   twice, for the same reason as I1 above. First, unconditionally against
//	   c1 — Layer 1's own checkpoint() output — before Layer 2 runs at all;
//	   this is the same guarantee TestCompactionSeqFuzz's I5 already proves
//	   true of checkpoint() in isolation, reused here to catch a Layer-1
//	   content-preservation bug directly, since the second check below cannot:
//	   whenever ForceCompact's Layer 2 fires (nearly always, once Layer 1 also
//	   would), it unconditionally replaces *history with its own re-cut
//	   result, and the `summarized`-gated pruning immediately below excuses
//	   ANY token currently missing — including one a Layer-1 bug dropped
//	   before Layer 2 ever saw it, not just one Layer 2's own opaque summary
//	   legitimately swallowed. Second, after ForceCompact: content still live
//	   in the preserved tail (I6, below) trivially still appears in history,
//	   but content folded into a Layer-2 TurnSummary has NO deterministic
//	   content contract — unlike a checkpoint, which re-extracts and
//	   re-renders tracked content verbatim on every pass, a summary is opaque
//	   model prose that might not preserve a token at all. So whenever
//	   ForceCompact actually produces a summary (its own `summarized` return
//	   value), any tracked token no longer found in the post-compaction
//	   history is pruned from the required set instead of failing the test —
//	   that loss is Layer 2's documented, by-design behavior, not a
//	   regression. Do NOT strengthen this second check back to unconditional
//	   re-extraction survival: it will spuriously fail the moment a summary
//	   (fake or real) doesn't happen to echo a token it swallowed.
//	I6 (preserved tail is verbatim): generalized to accept either marker kind
//	   at index 0 — the turns after it are a byte-identical suffix of the
//	   pre-compaction history, whether that marker is a TurnCheckpoint (Layer 2
//	   declined) or a TurnSummary (Layer 2 fired).
//	I7 (checkpoint is deterministic): unchanged from TestCompactionSeqFuzz —
//	   the pure checkpoint() extractor is deterministic on identical input.
//
// Also unchanged: the safeCutoff tail-head guarantee (the preserved tail
// never starts with a bare TurnTool/TurnToolResults/TurnSteering turn).
//
// A final check outside the per-op loop asserts Layer 2 fired at least once
// across the whole rapid.Check run, so a change that silently makes this test
// nil-client again (or otherwise starves Layer 2) fails loudly instead of
// leaving the double-layer path unexercised again.
//
// Run hard with:
//
//	EVENER_FUZZ_TESTS=1 go test ./agent/internal/contextmgr -run '^TestForceCompactDoubleLayerSeqFuzz$' -rapid.checks=400 -v
//	EVENER_FUZZ_TESTS=1 go test ./agent/internal/contextmgr -run '^TestForceCompactDoubleLayerSeqFuzz$' -rapid.checks=1200 -v
//
// evener:fuzz rapid
func TestForceCompactDoubleLayerSeqFuzz(t *testing.T) {
	if os.Getenv("EVENER_FUZZ_TESTS") != "1" {
		t.Skip("fuzz: skipped by default; run `make test-fuzz`, or EVENER_FUZZ_TESTS=1 go test ./agent/internal/contextmgr -run TestForceCompactDoubleLayerSeqFuzz -count=1 -v")
	}
	var sawLayer2 atomic.Bool
	rapid.Check(t, func(rt *rapid.T) {
		m := newDoubleLayerModel(rapid.IntRange(1, 4).Draw(rt, "preserveRecent"), &sawLayer2)
		steps := rapid.IntRange(1, 25).Draw(rt, "steps")
		for i := range steps {
			op := rapid.SampledFrom(dlOps).Draw(rt, "op")
			lenBefore := len(m.history)
			m.applyOp(rt, op, i)
			if op == dlOpCompact {
				if len(m.history) > lenBefore {
					rt.Fatalf("step %d: compaction GREW history %d -> %d", i, lenBefore, len(m.history))
				}
			} else if len(m.history) <= lenBefore {
				rt.Fatalf("step %d: add op %d did not grow history (was %d, now %d)", i, op, lenBefore, len(m.history))
			}
			m.checkInvariants(rt, i)
		}
	})
	if !sawLayer2.Load() {
		t.Fatal("Layer 2 (LLM summarization) never fired in any rapid iteration — this test would be vacuous for the double-layer path it targets")
	}
}

// --- op vocabulary ---

type dlOp int

const (
	dlOpAddUser dlOp = iota
	dlOpAddAck
	dlOpAddNote
	dlOpAddEdit
	dlOpAddRead
	dlOpAddCommunicate
	dlOpAddSteering
	dlOpAddHook
	dlOpAddAttention
	dlOpCompact
)

var dlOps = []dlOp{
	dlOpAddUser, dlOpAddAck, dlOpAddNote, dlOpAddEdit, dlOpAddRead, dlOpAddCommunicate,
	dlOpAddSteering, dlOpAddHook, dlOpAddAttention, dlOpCompact,
}

// --- the model ---

type doubleLayerModel struct {
	history        []schema.Turn
	preserveRecent int
	cm             *Manager
	sawLayer2      *atomic.Bool

	userTokens  []string // verbatim user message tokens that must survive
	agentTokens []string // terminal communicate reply tokens that must survive
	noteTokens  []string // working-note tokens that must survive

	ctr int
}

func newDoubleLayerModel(preserveRecent int, sawLayer2 *atomic.Bool) *doubleLayerModel {
	// A real, non-nil client — the shape every live session actually has —
	// wired to the trivial fakeAdapter so Layer 2 executes for real instead
	// of being skipped like the single-layer seqfuzz tests.
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(testProfile("openai", "test", 1_000_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = preserveRecent
	return &doubleLayerModel{preserveRecent: preserveRecent, cm: cm, sawLayer2: sawLayer2}
}

func (m *doubleLayerModel) nextID(prefix string) string {
	m.ctr++
	return prefix + "_" + strconv.Itoa(m.ctr) + "_"
}

func (m *doubleLayerModel) applyOp(rt *rapid.T, op dlOp, step int) {
	switch op {
	case dlOpAddUser:
		tok := m.nextID("USER")
		m.history = append(m.history, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(tok)})
		m.userTokens = append(m.userTokens, tok)

	case dlOpAddAck:
		// Short assistant text (<50 chars): neither a working note nor a
		// conversation entry, so it is NOT retained by compaction. Not tracked.
		m.history = append(m.history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("ok")})

	case dlOpAddNote:
		tok := m.nextID("NOTE")
		// >50 and <500 chars: captured verbatim as a working note.
		text := tok + strings.Repeat("x", 55)
		m.history = append(m.history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(text)})
		m.noteTokens = append(m.noteTokens, tok)

	case dlOpAddEdit:
		id := m.nextID("call")
		path := "f" + m.nextID("p") + ".go"
		m.history = append(m.history,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "edit_file", `{"file_path":"`+path+`"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "edit_file", "OK", false)},
		)

	case dlOpAddRead:
		id := m.nextID("call")
		m.history = append(m.history,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "read_file", `{"file_path":"r.go"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "read_file", "1 | x\n", false)},
		)

	case dlOpAddCommunicate:
		tok := m.nextID("AGENT")
		id := m.nextID("call")
		cc := communicateCall(id, tok)
		m.history = append(m.history,
			schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &cc}},
			}},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "communicate", `{"delivered":true}`, false)},
		)
		m.agentTokens = append(m.agentTokens, tok)

	case dlOpAddSteering:
		// Not tracked: steering content has never been part of the survival
		// contract (it isn't extracted by checkpoint's collectCheckpointData
		// either), only added to probe safeCutoff's marker handling.
		tok := m.nextID("STEER")
		m.history = append(m.history, schema.NewTurn(schema.TurnSteering, llm.User(tok)))

	case dlOpAddHook:
		tok := m.nextID("HOOK")
		m.history = append(m.history, schema.NewTurn(schema.TurnHookCompleted, llm.System(tok)))

	case dlOpAddAttention:
		tok := m.nextID("ATTN")
		m.history = append(m.history, schema.NewTurn(schema.TurnAttentionResolution, llm.System(tok)))

	case dlOpCompact:
		m.compact(rt, step)
	}
}

// compact runs the production /compact entry point (with a live client, so
// both layers can fire) and verifies the compaction-step-local invariants (I6
// tail-verbatim, I7 determinism, the safeCutoff tail-head guarantee, I1/I5's
// Layer-1-isolated checks against c1, and I5's Layer-2 pruning).
func (m *doubleLayerModel) compact(rt *rapid.T, step int) {
	before := cloneHistory(m.history)

	// I7: the pure checkpoint extractor is deterministic on identical input
	// (timestamps aside — checkpoint stamps the new turn with time.Now()).
	c1 := checkpoint(cloneHistory(before), m.preserveRecent, &CompactionMeta{}, "communicate")
	c2 := checkpoint(cloneHistory(before), m.preserveRecent, &CompactionMeta{}, "communicate")
	if !bytes.Equal(mustJSON(zeroTimestamps(c1)), mustJSON(zeroTimestamps(c2))) {
		rt.Fatalf("step %d: checkpoint nondeterministic:\n a=%s\n b=%s", step, mustJSON(zeroTimestamps(c1)), mustJSON(zeroTimestamps(c2)))
		return
	}

	// I1/I5, Layer-1-isolated: checked against c1 (Layer 1's own output on
	// `before`) BEFORE ForceCompact runs both layers. Layer 2 fires in nearly
	// every case Layer 1 also does (both gate on the same
	// attentionTransparentTurnCount vs preserveRecent comparison), and Layer 2
	// unconditionally replaces *history with its own re-cut result — so a
	// Layer-1-only regression (checkpoint() growing history, or dropping
	// tracked content) is invisible to the length check in the outer test loop
	// and to I5 below: both are computed against the COMBINED ForceCompact
	// result, which Layer 2 re-normalizes right past the bug. Checking c1
	// directly closes that hole; c1's own content-preservation guarantee is
	// already proven unconditionally by TestCompactionSeqFuzz's I5, so this
	// reuses a property known true of checkpoint() in isolation, not a new one.
	if len(c1) > len(before) {
		rt.Fatalf("step %d: Layer 1 checkpoint alone GREW history %d -> %d", step, len(before), len(c1))
		return
	}
	c1JSON := mustJSON(c1)
	for _, tok := range m.userTokens {
		if !bytes.Contains(c1JSON, []byte(tok)) {
			rt.Fatalf("step %d: user message %q lost from Layer 1's own checkpoint", step, tok)
			return
		}
	}
	for _, tok := range m.agentTokens {
		if !bytes.Contains(c1JSON, []byte(tok)) {
			rt.Fatalf("step %d: agent reply %q lost from Layer 1's own checkpoint", step, tok)
			return
		}
	}
	for _, tok := range m.noteTokens {
		if !bytes.Contains(c1JSON, []byte(tok)) {
			rt.Fatalf("step %d: working note %q lost from Layer 1's own checkpoint", step, tok)
			return
		}
	}

	summarized := m.cm.ForceCompact(context.Background(), &m.history, "", noopEmit)
	after := m.history

	if len(after) == 0 {
		// No compaction happened. Length-shrink is checked by the caller.
		return
	}
	switch after[0].Kind {
	case schema.TurnCheckpoint, schema.TurnSummary:
		// A compaction marker landed at the front — proceed to the
		// tail/survival checks below.
	default:
		// No compaction happened (history too short, or safeCutoff backed off
		// the whole prefix on both layers). Length-shrink is checked by the
		// caller.
		return
	}

	// I6, generalized: the kept tail is a byte-identical suffix of the
	// pre-compaction history, regardless of which marker kind fired.
	tail := after[1:]
	if len(tail) > len(before) {
		rt.Fatalf("step %d: tail (%d) longer than pre-compaction history (%d)", step, len(tail), len(before))
		return
	}
	wantTail := before[len(before)-len(tail):]
	if !reflect.DeepEqual(tail, wantTail) {
		rt.Fatalf("step %d: preserved tail is not a verbatim suffix of pre-compaction history (marker=%s)", step, after[0].Kind)
		return
	}

	// safeCutoff guarantee: the tail never STARTS with a bare tool/steering
	// turn (its tool call would be orphaned in the dropped prefix).
	if len(tail) > 0 {
		switch tail[0].Kind {
		case schema.TurnTool, schema.TurnToolResults, schema.TurnSteering:
			rt.Fatalf("step %d: preserved tail starts with %s — its tool call was orphaned", step, tail[0].Kind)
			return
		}
	}

	// I5: content folded into a Layer-2 summary loses the re-extraction
	// survival guarantee (see the test's doc comment). Prune any tracked
	// token no longer present instead of failing — that loss is by-design.
	if summarized {
		m.sawLayer2.Store(true)
		hb := mustJSON(m.history)
		m.userTokens = pruneMissingTokens(m.userTokens, hb)
		m.agentTokens = pruneMissingTokens(m.agentTokens, hb)
		m.noteTokens = pruneMissingTokens(m.noteTokens, hb)
	}
}

// pruneMissingTokens drops tokens no longer present in hb. Used to relax I5's
// survival requirement once content has been folded into a Layer-2 summary —
// opaque prose with no re-extraction contract, unlike a checkpoint.
func pruneMissingTokens(tokens []string, hb []byte) []string {
	kept := tokens[:0]
	for _, tok := range tokens {
		if bytes.Contains(hb, []byte(tok)) {
			kept = append(kept, tok)
		}
	}
	return kept
}

// --- invariants checked over the whole history after every op ---

func (m *doubleLayerModel) checkInvariants(rt *rapid.T, step int) {
	// I2: at most one compaction-marker turn (TurnCheckpoint OR TurnSummary),
	// and if present it lives at index 0.
	markers := 0
	for idx, t := range m.history {
		if t.Kind == schema.TurnCheckpoint || t.Kind == schema.TurnSummary {
			markers++
			if idx != 0 {
				rt.Fatalf("step %d: compaction marker %s at index %d, must be 0", step, t.Kind, idx)
				return
			}
		}
	}
	if markers > 1 {
		rt.Fatalf("step %d: %d compaction marker turns in history, want <=1", step, markers)
		return
	}

	// I3: every tool result references a tool call seen EARLIER in history.
	seenCall := map[string]bool{}
	for _, t := range m.history {
		if t.Kind == schema.TurnAssistant {
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					seenCall[p.ToolCall.ID] = true
				}
			}
		}
		if t.Kind == schema.TurnTool || t.Kind == schema.TurnToolResults {
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil && !seenCall[p.ToolResult.ToolCallID] {
					rt.Fatalf("step %d: orphaned tool result %q (no preceding call)", step, p.ToolResult.ToolCallID)
					return
				}
			}
		}
	}

	// I4: every tool call has a matching result somewhere in history.
	resultIDs := map[string]bool{}
	for _, t := range m.history {
		if t.Kind == schema.TurnTool || t.Kind == schema.TurnToolResults {
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					resultIDs[p.ToolResult.ToolCallID] = true
				}
			}
		}
	}
	for _, t := range m.history {
		if t.Kind != schema.TurnAssistant {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall != nil && !resultIDs[p.ToolCall.ID] {
				rt.Fatalf("step %d: dangling tool call %q (no result)", step, p.ToolCall.ID)
				return
			}
		}
	}

	// I5: all currently-tracked content survives. compact() prunes a token
	// from these lists the moment it's folded into a Layer-2 summary, so
	// what remains here must always be found.
	hb := mustJSON(m.history)
	for _, tok := range m.userTokens {
		if !bytes.Contains(hb, []byte(tok)) {
			rt.Fatalf("step %d: user message %q lost from history", step, tok)
			return
		}
	}
	for _, tok := range m.agentTokens {
		if !bytes.Contains(hb, []byte(tok)) {
			rt.Fatalf("step %d: agent reply %q lost from history", step, tok)
			return
		}
	}
	for _, tok := range m.noteTokens {
		if !bytes.Contains(hb, []byte(tok)) {
			rt.Fatalf("step %d: working note %q lost from history", step, tok)
			return
		}
	}
}
