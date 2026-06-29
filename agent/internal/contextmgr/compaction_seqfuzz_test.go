package contextmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"

	"pgregory.net/rapid"
)

// TestCompactionSeqFuzz is roadmap item 8.3's compaction/context-manager
// stateful model. Every non-crash bug this project has hit has been a STATE
// bug, and the context manager's deterministic checkpoint (Layer 3) is the
// surface behind what survives a reload after compaction. The existing unit
// tests each pin ONE checkpoint of ONE hand-built history; none drives the
// real accumulate -> compact -> continue -> compact-again loop and checks
// invariants that must hold ACROSS the whole SEQUENCE.
//
// This rapid state machine does. It draws a sequence of legal session
// transitions — adding user turns, assistant text, working notes, paired
// tool-call/result rounds, and terminal communicate replies — building real
// schema.Turn history one op at a time, and interleaves compactions driven
// through the production /compact entry point (Manager.ForceCompact with a nil
// LLM client, which runs exactly the deterministic checkpoint layer). After
// each op it checks, weakest-first:
//
//	I1 (length monotonicity / shrink-only): every non-compact op strictly grows
//	   history (we always append a turn); a compact op never grows it. Together
//	   these prove history length shrinks ONLY across a compaction — never
//	   spontaneously.
//	I2 (single checkpoint, at the front): at most one TurnCheckpoint exists in
//	   history at any time, and when present it is at index 0. A compaction folds
//	   any prior checkpoint into the new one (cutoff>=1), and continuation appends
//	   to the tail, so the checkpoint is never duplicated or buried — the property
//	   a reload depends on.
//	I3 (no orphaned tool result): walking history in order, every tool result
//	   references a tool call that appeared EARLIER. Compaction must never drop an
//	   assistant tool call while keeping its result — safeCutoff guarantees the
//	   preserved tail never STARTS with a bare tool-result turn, so call/result
//	   pairs are never split.
//	I4 (no dangling tool call): symmetrically, every assistant tool call has a
//	   matching result somewhere in history — a call+result pair is compacted as a
//	   unit (both in the dropped prefix or both in the kept tail), never half.
//	I5 (retained content survives, even across REPEATED compaction): every user
//	   message, every terminal agent reply, and every working note ever added
//	   still appears verbatim in the current history — either live in the tail or
//	   folded into the checkpoint, which on re-compaction re-extracts and
//	   re-renders conversation + working notes so they survive each pass.
//	I6 (preserved tail is verbatim): after a compaction that produced a
//	   checkpoint, the turns after the checkpoint are a byte-identical suffix of
//	   the pre-compaction history — recent turns are carried through untouched.
//	I7 (checkpoint is deterministic): running the pure checkpoint extractor twice
//	   on the same history yields identical content (timestamps aside) — the
//	   property that makes a checkpoint reproducible on reload.
//
// The op table only proposes transitions the real session could emit (paired
// call/result turns, end_turn communicate replies), so generated histories are
// always legal shapes, not arbitrary bytes.
//
// Run hard with: go test -run '^TestCompactionSeqFuzz$' -rapid.checks=5000 .
// Steps are bounded and tokens kept small so the checkpoint's char budget never
// sheds content (which would otherwise be a legitimate, by-design loss the
// survival invariant must not mistake for a bug).
//
// SCOPE: the LLM summarization layer (Layer 4) is intentionally excluded. Its
// output is model-generated prose with no deterministic content contract, so it
// cannot be asserted at the unit level without mocking the very logic under
// test. The deterministic checkpoint layer IS the state-preserving surface and
// is exercised through its real ForceCompact entry point.
func TestCompactionSeqFuzz(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := newCompactionModel(rapid.IntRange(1, 4).Draw(rt, "preserveRecent"))
		steps := rapid.IntRange(1, 25).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			op := rapid.SampledFrom(compactionOps).Draw(rt, "op")
			lenBefore := len(m.history)
			m.applyOp(rt, op, i)
			if op == opCompact {
				if len(m.history) > lenBefore {
					rt.Fatalf("step %d: compaction GREW history %d -> %d", i, lenBefore, len(m.history))
				}
			} else if len(m.history) <= lenBefore {
				rt.Fatalf("step %d: add op %d did not grow history (was %d, now %d)", i, op, lenBefore, len(m.history))
			}
			m.checkInvariants(rt, i)
		}
	})
}

// --- op vocabulary ---

type compactionOp int

const (
	opAddUser compactionOp = iota
	opAddAck
	opAddNote
	opAddEdit
	opAddRead
	opAddCommunicate
	opCompact
)

var compactionOps = []compactionOp{
	opAddUser, opAddAck, opAddNote, opAddEdit, opAddRead, opAddCommunicate, opCompact,
}

// --- the model ---

type compactionModel struct {
	history        []schema.Turn
	preserveRecent int
	cm             *Manager

	userTokens  []string // verbatim user message tokens that must survive
	agentTokens []string // terminal communicate reply tokens that must survive
	noteTokens  []string // working-note tokens that must survive

	ctr int
}

func newCompactionModel(preserveRecent int) *compactionModel {
	cm := NewManager(testProfile("openai", "test", 1_000_000), nil)
	cm.PreserveRecentTurns = preserveRecent
	return &compactionModel{preserveRecent: preserveRecent, cm: cm}
}

// nextID returns a globally-unique, delimiter-bounded token. The leading and
// trailing underscores prevent prefix aliasing (e.g. "USER_1_" is never a
// substring of "USER_12_"), so the survival check cannot have a token's loss
// masked by a later token that contains it.
func (m *compactionModel) nextID(prefix string) string {
	m.ctr++
	return prefix + "_" + strconv.Itoa(m.ctr) + "_"
}

func noopEmit(events.EventKind, events.EventData) {}

func (m *compactionModel) applyOp(rt *rapid.T, op compactionOp, step int) {
	switch op {
	case opAddUser:
		tok := m.nextID("USER")
		m.history = append(m.history, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(tok)})
		m.userTokens = append(m.userTokens, tok)

	case opAddAck:
		// Short assistant text (<50 chars): neither a working note nor a
		// conversation entry, so it is NOT retained by compaction. Not tracked.
		m.history = append(m.history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("ok")})

	case opAddNote:
		tok := m.nextID("NOTE")
		// >50 and <500 chars: captured verbatim as a working note.
		text := tok + strings.Repeat("x", 55)
		m.history = append(m.history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(text)})
		m.noteTokens = append(m.noteTokens, tok)

	case opAddEdit:
		id := m.nextID("call")
		path := "f" + m.nextID("p") + ".go"
		m.history = append(m.history,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "edit_file", `{"file_path":"`+path+`"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "edit_file", "OK", false)},
		)

	case opAddRead:
		id := m.nextID("call")
		m.history = append(m.history,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "read_file", `{"file_path":"r.go"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "read_file", "1 | x\n", false)},
		)

	case opAddCommunicate:
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

	case opCompact:
		m.compact(rt, step)
	}
}

// compact runs the production /compact entry point and verifies the
// compaction-step-local invariants (I6 tail-verbatim, I7 determinism, plus the
// checkpoint header and safeCutoff tail-head guarantees).
func (m *compactionModel) compact(rt *rapid.T, step int) {
	before := cloneHistory(m.history)

	// I7: the pure checkpoint extractor is deterministic on identical input
	// (timestamps aside — checkpoint stamps the new turn with time.Now()).
	c1 := checkpoint(cloneHistory(before), m.preserveRecent, &CompactionMeta{}, "communicate")
	c2 := checkpoint(cloneHistory(before), m.preserveRecent, &CompactionMeta{}, "communicate")
	if !bytes.Equal(mustJSON(zeroTimestamps(c1)), mustJSON(zeroTimestamps(c2))) {
		rt.Fatalf("step %d: checkpoint nondeterministic:\n a=%s\n b=%s", step, mustJSON(zeroTimestamps(c1)), mustJSON(zeroTimestamps(c2)))
		return
	}

	m.cm.ForceCompact(context.Background(), &m.history, "", noopEmit)
	after := m.history

	if len(after) == 0 || after[0].Kind != schema.TurnCheckpoint {
		// No compaction happened (history too short, or safeCutoff backed off
		// the whole prefix). Length-shrink is checked by the caller.
		return
	}

	txt := after[0].Message.Text()
	if !strings.Contains(txt, "[CONTEXT CHECKPOINT]") || !strings.Contains(txt, "[END CHECKPOINT]") {
		rt.Fatalf("step %d: checkpoint turn missing header/footer: %q", step, txt)
		return
	}

	// I6: the kept tail is a byte-identical suffix of the pre-compaction history.
	tail := after[1:]
	if len(tail) > len(before) {
		rt.Fatalf("step %d: tail (%d) longer than pre-compaction history (%d)", step, len(tail), len(before))
		return
	}
	wantTail := before[len(before)-len(tail):]
	if !reflect.DeepEqual(tail, wantTail) {
		rt.Fatalf("step %d: preserved tail is not a verbatim suffix of pre-compaction history", step)
		return
	}

	// safeCutoff guarantee: the tail never STARTS with a bare tool/steering turn
	// (its tool call would be orphaned in the dropped prefix).
	if len(tail) > 0 {
		switch tail[0].Kind {
		case schema.TurnTool, schema.TurnToolResults, schema.TurnSteering:
			rt.Fatalf("step %d: preserved tail starts with %s — its tool call was orphaned", step, tail[0].Kind)
			return
		}
	}
}

// --- invariants checked over the whole history after every op ---

func (m *compactionModel) checkInvariants(rt *rapid.T, step int) {
	// I2: at most one checkpoint, and it lives at index 0.
	checkpoints := 0
	for idx, t := range m.history {
		if t.Kind == schema.TurnCheckpoint {
			checkpoints++
			if idx != 0 {
				rt.Fatalf("step %d: checkpoint at index %d, must be 0", step, idx)
				return
			}
		}
	}
	if checkpoints > 1 {
		rt.Fatalf("step %d: %d checkpoint turns in history, want <=1", step, checkpoints)
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

	// I5: all retained content survives, including across repeated compaction.
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

// --- helpers ---

func cloneHistory(h []schema.Turn) []schema.Turn {
	out := make([]schema.Turn, len(h))
	copy(out, h)
	return out
}

func zeroTimestamps(h []schema.Turn) []schema.Turn {
	out := make([]schema.Turn, len(h))
	copy(out, h)
	for i := range out {
		out[i].Timestamp = time.Time{}
	}
	return out
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("marshal: " + err.Error())
	}
	return b
}
