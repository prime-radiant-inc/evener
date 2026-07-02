package contextmgr

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"pgregory.net/rapid"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"

	"testing"
)

// TestFc1MaybeCompactSeqFuzz is the pressure-driven counterpart to
// TestCompactionSeqFuzz. Where the latter drives the user-initiated /compact
// entry point (ForceCompact, which always runs the checkpoint layer), this one
// drives the AUTOMATIC entry point MaybeCompact across a drawn sequence of
// turn-appends interleaved with pressure changes — both real (appending content
// grows char/4 pressure) and injected (RecordInputTokens plants an API
// measurement, the baseline MaybeCompact prefers). The client is nil, so only the
// deterministic checkpoint layer (Layer 1, gated at CheckpointThreshold) can ever
// fire; the LLM summarization layer is excluded exactly as the unit tests exclude
// it (no deterministic content contract).
//
// This exercises the parts ForceCompact never reaches: the pressure gate itself,
// the token-measurement invalidation that stops both layers cascading in one pass,
// and the interaction between a planted measurement and the char/4 fallback across
// repeated compaction. After each op it checks, weakest-first:
//
//	I1 (shrink-only): an add op grows history; a MaybeCompact op never grows it
//	   (checkpoint either shrinks or no-ops); a RecordInputTokens op leaves it
//	   unchanged. Together: history length shrinks ONLY across a compaction.
//	I2 (single checkpoint, at the front): at most one TurnCheckpoint, always at
//	   index 0 — the shape a reload depends on.
//	I3/I4 (no orphaned result / no dangling call): call/result pairs are never
//	   split by a compaction cutoff.
//	I5 (retained content survives): every user message, terminal agent reply, and
//	   working note ever added still appears verbatim — live in the tail or folded
//	   into the checkpoint (which re-extracts them on re-compaction).
//	I6 (pressure bound): the estimated post-op pressure is finite and non-negative.
func TestFc1MaybeCompactSeqFuzz(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := newFc1MaybeCompactModel(rapid.IntRange(1, 4).Draw(rt, "preserveRecent"))
		steps := rapid.IntRange(1, 25).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			op := rapid.SampledFrom(fc1MaybeCompactOps).Draw(rt, "op")
			lenBefore := len(m.history)
			m.applyOp(rt, op, i)
			switch op {
			case fc1OpMaybeCompact:
				if len(m.history) > lenBefore {
					rt.Fatalf("step %d: MaybeCompact GREW history %d -> %d", i, lenBefore, len(m.history))
				}
			case fc1OpRecordTokens:
				if len(m.history) != lenBefore {
					rt.Fatalf("step %d: RecordInputTokens changed history %d -> %d", i, lenBefore, len(m.history))
				}
			default:
				if len(m.history) <= lenBefore {
					rt.Fatalf("step %d: add op %d did not grow history (%d -> %d)", i, op, lenBefore, len(m.history))
				}
			}
			m.checkInvariants(rt, i)
		}
	})
}

type fc1MaybeCompactOp int

const (
	fc1OpAddUser fc1MaybeCompactOp = iota
	fc1OpAddNote
	fc1OpAddEdit
	fc1OpAddRead
	fc1OpAddCommunicate
	fc1OpRecordTokens
	fc1OpMaybeCompact
)

var fc1MaybeCompactOps = []fc1MaybeCompactOp{
	fc1OpAddUser, fc1OpAddNote, fc1OpAddEdit, fc1OpAddRead, fc1OpAddCommunicate,
	fc1OpRecordTokens, fc1OpMaybeCompact,
}

// fc1MaybeCompactSysPromptChars is the fixed system-prompt size fed to the
// pressure estimate. fc1MaybeCompactWindow is the context window; both are small
// so a handful of appended turns, or a planted measurement, can cross the
// checkpoint threshold and actually trigger compaction within a bounded sequence.
const (
	fc1MaybeCompactSysPromptChars = 400
	fc1MaybeCompactWindow         = 1200
)

type fc1MaybeCompactModel struct {
	history        []schema.Turn
	preserveRecent int
	cm             *Manager

	userTokens  []string
	agentTokens []string
	noteTokens  []string

	ctr int
}

func newFc1MaybeCompactModel(preserveRecent int) *fc1MaybeCompactModel {
	// nil client: only the deterministic checkpoint layer can fire.
	cm := NewManager(testProfile("openai", "test", fc1MaybeCompactWindow), nil)
	cm.PreserveRecentTurns = preserveRecent
	return &fc1MaybeCompactModel{preserveRecent: preserveRecent, cm: cm}
}

func (m *fc1MaybeCompactModel) nextID(prefix string) string {
	m.ctr++
	return prefix + "_" + strconv.Itoa(m.ctr) + "_"
}

func (m *fc1MaybeCompactModel) applyOp(rt *rapid.T, op fc1MaybeCompactOp, step int) {
	switch op {
	case fc1OpAddUser:
		tok := m.nextID("USER")
		m.history = append(m.history, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(tok + " asks a question")})
		m.userTokens = append(m.userTokens, tok)

	case fc1OpAddNote:
		tok := m.nextID("NOTE")
		text := tok + strings.Repeat("x", 55)
		m.history = append(m.history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(text)})
		m.noteTokens = append(m.noteTokens, tok)

	case fc1OpAddEdit:
		id := m.nextID("call")
		path := "f" + m.nextID("p") + ".go"
		m.history = append(m.history,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "edit_file", `{"file_path":"`+path+`"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "edit_file", "OK", false)},
		)

	case fc1OpAddRead:
		id := m.nextID("call")
		m.history = append(m.history,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "read_file", `{"file_path":"r.go"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "read_file", "1 | x\n", false)},
		)

	case fc1OpAddCommunicate:
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

	case fc1OpRecordTokens:
		// Plant an API measurement the estimator prefers as a baseline. Drawing up
		// to ~1.5x the window lets it push pressure across the checkpoint threshold
		// (exercising the measurement path) or sit below it (exercising the reset).
		tokens := rapid.IntRange(0, fc1MaybeCompactWindow*3/2).Draw(rt, "recordTokens")
		m.cm.RecordInputTokens(tokens, len(m.history))

	case fc1OpMaybeCompact:
		m.cm.MaybeCompact(context.Background(), &m.history, fc1MaybeCompactSysPromptChars, noopEmit)
	}
}

func (m *fc1MaybeCompactModel) checkInvariants(rt *rapid.T, step int) {
	// I2: at most one checkpoint, at index 0.
	checkpoints := 0
	for idx, t := range m.history {
		if t.Kind == schema.TurnCheckpoint {
			checkpoints++
			if idx != 0 {
				rt.Fatalf("step %d: checkpoint at index %d, must be 0", step, idx)
			}
		}
	}
	if checkpoints > 1 {
		rt.Fatalf("step %d: %d checkpoint turns, want <=1", step, checkpoints)
	}

	// I3: every tool result references a tool call seen EARLIER.
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
			}
		}
	}

	// I5: all retained content survives, including across repeated compaction.
	hb := mustJSON(m.history)
	for _, tok := range m.userTokens {
		if !bytes.Contains(hb, []byte(tok)) {
			rt.Fatalf("step %d: user message %q lost from history", step, tok)
		}
	}
	for _, tok := range m.agentTokens {
		if !bytes.Contains(hb, []byte(tok)) {
			rt.Fatalf("step %d: agent reply %q lost from history", step, tok)
		}
	}
	for _, tok := range m.noteTokens {
		if !bytes.Contains(hb, []byte(tok)) {
			rt.Fatalf("step %d: working note %q lost from history", step, tok)
		}
	}

	// I6: pressure stays a finite, non-negative fraction.
	p := m.cm.EstimatePressure(m.history, fc1MaybeCompactSysPromptChars)
	if p < 0 || p != p { // NaN check: p != p is true only for NaN.
		rt.Fatalf("step %d: pressure out of range: %v", step, p)
	}
}
