package contextmgr

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/fuzz/fault"
	"primeradiant.com/serf/llm"
)

// This file fuzzes the compaction / context-management core of the contextmgr
// package: the deterministic checkpoint distiller (collectCheckpointData +
// formatCheckpoint), the LLM summarizer (summarizeWithLLMSteered), and the two
// pluggable ManageContext strategies (CheckpointPredStrategy, SessionLogStrategy).
//
// All new top-level identifiers are prefixed with the lane token "ctxmgr_" so
// this file never collides with a sibling fuzz lane editing the same package. It
// reuses existing package test helpers (communicateCall, fakeStrategyHost,
// testOpenAIProfileWithContextWindow, NewManager, ...) and the shared
// agenttest.ScriptedAdapter rather than redefining them.

// ctxmgr_richHistorySeed is a hand-built byte payload that ctxmgr_buildHistory
// decodes into a history covering every turn shape and tool branch the
// compaction code inspects. Used as a seed so the reproducible seed-replay walks
// the full distiller rather than a thin subset.
var ctxmgr_richHistorySeed = []byte("\x00\x17original task: fix auth\x03\x00\x0a/a/edit.go\x03\x01\x0b/a/write.go\x03\x02\x0c/a/patchA.go\x0c/a/patchB.go\x03\x03\x09using-git\x03\x04\x0a/a/read.go\x03\x05\x07pattern\x04\x05query\x06\x0dgo test ./...\x010\x05\x11here is my answer\x02\x51this is a fairly long analytical note about the debugging approach used here okay\x07\x0fagent said this\x0ekeep this note\x08\x0dprior summary\x09\x0csystem steer\x0a\x07visible\x10hidden reasoning\x01\x09older msg")

// ctxmgr_reader is a tiny cursor over fuzzer bytes used to build varied but
// well-formed transcript/turn state deterministically.
type ctxmgr_reader struct {
	b []byte
	i int
}

func (r *ctxmgr_reader) byte() byte {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

func (r *ctxmgr_reader) more() bool { return r.i < len(r.b) }

// str reads a length-prefixed string of at most limit bytes.
func (r *ctxmgr_reader) str(limit int) string {
	if limit <= 0 {
		return ""
	}
	n := int(r.byte()) % (limit + 1)
	if n == 0 {
		return ""
	}
	out := make([]byte, 0, n)
	for j := 0; j < n; j++ {
		out = append(out, r.byte())
	}
	return string(out)
}

// ctxmgr_buildHistory turns fuzzer bytes into a plausible conversation history.
// It emits every turn shape the compaction code inspects: plain and
// compaction-framed user input, assistant text (working-note candidates),
// assistant tool calls for each file/shell/skill/web-search branch, matched
// tool-result turns (communicate + shell), thinking parts, checkpoints,
// summaries, and steering turns.
func ctxmgr_buildHistory(r *ctxmgr_reader) []schema.Turn {
	var history []schema.Turn
	id := 0
	nextID := func() string {
		id++
		return "call_" + string(rune('a'+id%26)) + strings.Repeat("x", id%3)
	}
	const maxTurns = 60
	for r.more() && len(history) < maxTurns {
		switch r.byte() % 12 {
		case 0: // plain user input
			history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(r.str(300))))
		case 1: // old-format compaction stored as user input
			text := "[CONTEXT CHECKPOINT]\n## Conversation\n\n### User\n\n```text\n" + r.str(120) + "\n```\n"
			history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(text)))
		case 2: // assistant analytical text (working-note candidate when long)
			history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(r.str(400))))
		case 3: // assistant tool call
			history = append(history, schema.NewTurn(schema.TurnAssistant, ctxmgr_toolCallMsg(r, nextID())))
		case 4: // assistant web search part
			msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: r.str(40), Raw: json.RawMessage(`{}`)}},
			}}
			history = append(history, schema.NewTurn(schema.TurnAssistant, msg))
		case 5: // communicate pair: assistant call + matching terminal result
			cid := nextID()
			msg := r.str(200)
			history = append(history,
				schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: ctxmgr_ptrToolCall(communicateCall(cid, msg))},
				}}),
				schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(cid, "communicate", "ok", false)),
			)
		case 6: // shell pair: assistant call + matching result carrying an exit code
			cid := nextID()
			args, _ := json.Marshal(map[string]any{"command": r.str(80)})
			history = append(history,
				schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: cid, Name: "shell", Arguments: args, Type: "function"}},
				}}),
				schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(cid, "shell", "exit_code="+r.str(4)+"\noutput", false)),
			)
		case 7: // checkpoint turn with framed conversation + working notes
			text := "[CONTEXT CHECKPOINT]\n## Conversation\n\n### Agent\n\n```text\n" + r.str(120) + "\n```\n## Working Notes\n\n### Note\n\n```text\n" + r.str(80) + "\n```\n[END CHECKPOINT]\n"
			history = append(history, schema.NewTurn(schema.TurnCheckpoint, llm.User(text)))
		case 8: // summary turn
			history = append(history, schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\n"+r.str(200)+"\n[END SUMMARY]")))
		case 9: // steering
			history = append(history, schema.NewTurn(schema.TurnSteering, llm.User(r.str(120))))
		case 10: // assistant thinking part (drives clearThinking)
			msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: r.str(80)},
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: r.str(200)}},
			}}
			history = append(history, schema.NewTurn(schema.TurnAssistant, msg))
		case 11: // deprecated TurnTool with a plain tool result
			cid := nextID()
			history = append(history, schema.NewTurn(schema.TurnTool, llm.ToolResultNamed(cid, r.str(20), r.str(120), false)))
		}
	}
	return history
}

// ctxmgr_ptrToolCall copies a ToolCallData onto the heap so it can be embedded
// in a ContentPart.
func ctxmgr_ptrToolCall(tc llm.ToolCallData) *llm.ToolCallData { return &tc }

// ctxmgr_toolCallMsg builds an assistant message carrying a single tool call for
// one of the file/skill tools collectCheckpointData recognizes.
func ctxmgr_toolCallMsg(r *ctxmgr_reader, id string) llm.Message {
	var name string
	var args map[string]any
	switch r.byte() % 6 {
	case 0:
		name, args = "edit_file", map[string]any{"file_path": r.str(60)}
	case 1:
		name, args = "write_file", map[string]any{"file_path": r.str(60)}
	case 2:
		name = "apply_patch"
		args = map[string]any{"patch": "*** Update File: " + r.str(40) + "\n*** Add File: " + r.str(40) + "\n"}
	case 3:
		name, args = "use_skill", map[string]any{"skill_name": r.str(30)}
	case 4:
		name, args = "read_file", map[string]any{"file_path": r.str(60)}
	default:
		name, args = "grep", map[string]any{"pattern": r.str(30)}
	}
	raw, _ := json.Marshal(args)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: raw, Type: "function"}},
	}}
}

// ctxmgr_faultResponder returns a ScriptedAdapter.FaultResponder that fails
// roughly one call in four, deterministically, from a plan of fuzzer bytes —
// mirroring fuzz/fault's Schedule sparsity while using its exported ErrInjected
// on the adapter seam (Schedule.trip itself is unexported). An empty plan
// injects nothing, so the success path still runs.
func ctxmgr_faultResponder(plan []byte) func(req llm.Request) error {
	n := 0
	return func(_ llm.Request) error {
		if len(plan) == 0 {
			return nil
		}
		b := plan[n%len(plan)]
		n++
		if b%4 != 0 {
			return nil
		}
		return fault.ErrInjected
	}
}

// ctxmgr_scriptedClient builds an llm.Client whose single provider answers with
// reply, failing calls per the fault plan. The provider name matches the
// profile's own id so summarizeWithLLM's routes resolve to it.
func ctxmgr_scriptedClient(provider, reply string, plan []byte) *llm.Client {
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider: provider,
		Responder: func(req llm.Request) llm.Response {
			return llm.Response{Model: req.Model, Message: llm.Assistant(reply)}
		},
		FaultResponder: ctxmgr_faultResponder(plan),
	})
	return client
}

// ctxmgr_msgFingerprint captures a turn's kind and full message content
// (excluding the nondeterministic Turn.Timestamp) so two runs can be compared
// for determinism.
func ctxmgr_msgFingerprint(t schema.Turn) string {
	b, _ := json.Marshal(t.Message)
	return string(t.Kind) + "\x00" + string(b)
}

func ctxmgr_fingerprints(h []schema.Turn) []string {
	out := make([]string, len(h))
	for i, t := range h {
		out[i] = ctxmgr_msgFingerprint(t)
	}
	return out
}

// ctxmgr_countToolParts independently counts the tool-call and web-search parts
// in the assistant turns of history[:cutoff] — the exact set
// collectCheckpointData tallies into toolCounts.
func ctxmgr_countToolParts(history []schema.Turn, cutoff int) int {
	n := 0
	for i := 0; i < cutoff && i < len(history); i++ {
		if history[i].Kind != schema.TurnAssistant {
			continue
		}
		for _, p := range history[i].Message.Content {
			switch p.Kind {
			case llm.ContentWebSearch:
				n++
			case llm.ContentToolCall:
				if p.ToolCall != nil {
					n++
				}
			}
		}
	}
	return n
}

// FuzzCtxmgrCheckpointData drives the deterministic checkpoint distiller —
// collectCheckpointData (which walks history[:cutoff] extracting modified files,
// tool counts, shell results, conversation, and working notes) and
// formatCheckpoint (which renders that into the [CONTEXT CHECKPOINT] block within
// a char budget, shedding oldest content first).
//
// Oracles (never bare no-panic):
//   - collectCheckpointData is deterministic (same input → DeepEqual output).
//   - count preservation: sumCounts(toolCounts) equals an independent count of
//     the tool-call + web-search parts in the walked prefix, so no call is
//     dropped or double-counted.
//   - formatCheckpoint is deterministic and always emits the exact frame
//     ("[CONTEXT CHECKPOINT]\n" … "[END CHECKPOINT]\n").
//   - the never-shed metadata (files modified, per-tool counts, activated skills)
//     survives into the rendered checkpoint verbatim.
func FuzzCtxmgrCheckpointData(f *testing.F) {
	f.Add([]byte("\x00\x05hello\x03\x03cat\x05\x04msg1"), uint8(3), "sess-1", "skillA\nskillB")
	f.Add([]byte("\x03\x00\x06/x/y.go\x06\x02ls\x040"), uint8(2), "", "")
	f.Add([]byte{}, uint8(0), "", "")
	f.Add([]byte("\x07data\x08sum\x09steer\x0a\x02hi\x04think"), uint8(4), "S", "s1")
	// A rich, hand-built history exercising every collectCheckpointData branch
	// (edit/write/apply_patch/read/grep/use_skill/shell/communicate/web_search/
	// thinking/checkpoint/summary/steering), walked to the very end (cutoff=all).
	f.Add(ctxmgr_richHistorySeed, uint8(255), "sess-rich", "using-git\nusing-tmux")

	f.Fuzz(func(t *testing.T, data []byte, cutoffSel uint8, sessionID, skillsRaw string) {
		r := &ctxmgr_reader{b: data}
		history := ctxmgr_buildHistory(r)

		cutoff := 0
		if len(history) > 0 {
			cutoff = int(cutoffSel) % (len(history) + 1)
		}
		const resultTool = "communicate"

		got := collectCheckpointData(history, cutoff, resultTool)
		again := collectCheckpointData(history, cutoff, resultTool)
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("collectCheckpointData not deterministic")
		}

		// Count preservation: no tool call or web-search part is dropped.
		if sum, want := sumCounts(got.toolCounts), ctxmgr_countToolParts(history, cutoff); sum != want {
			t.Fatalf("toolCounts sum=%d want=%d", sum, want)
		}

		var skills []string
		for _, s := range strings.Split(skillsRaw, "\n") {
			if s = strings.TrimSpace(s); s != "" {
				skills = append(skills, s)
			}
		}
		meta := &CompactionMeta{SessionID: sessionID, ActivatedSkills: skills}

		cp := formatCheckpoint(got, meta, 60_000)
		if cp2 := formatCheckpoint(got, meta, 60_000); cp != cp2 {
			t.Fatalf("formatCheckpoint not deterministic")
		}
		if !strings.HasPrefix(cp, "[CONTEXT CHECKPOINT]\n") {
			t.Fatalf("checkpoint missing opening frame: %q", cp[:min(40, len(cp))])
		}
		if !strings.HasSuffix(cp, "[END CHECKPOINT]\n") {
			t.Fatalf("checkpoint missing closing frame: %q", cp[max(0, len(cp)-40):])
		}
		// Never-shed metadata survives verbatim into the rendered checkpoint.
		for fpath := range got.modifiedFiles {
			if !strings.Contains(cp, fpath) {
				t.Fatalf("modified file %q missing from checkpoint", fpath)
			}
		}
		for name := range got.toolCounts {
			if !strings.Contains(cp, name) {
				t.Fatalf("tool name %q missing from checkpoint", name)
			}
		}
		for _, s := range skills {
			if !strings.Contains(cp, s) {
				t.Fatalf("activated skill %q missing from checkpoint", s)
			}
		}
	})
}

// FuzzCtxmgrSummarizeSteered drives summarizeWithLLMSteered against a scripted,
// fault-injected adapter so both the success path (a SUMMARY turn replaces the
// old prefix) and the model-error path (every route fails → error returned) are
// reached without any network call.
//
// Oracles:
//   - never panic; the returned slice is never nil when err is nil.
//   - preserved-suffix invariant: on success the recent turns are carried through
//     byte-identical — result[1:] is exactly the matching-length suffix of the
//     input history (order + content preserved, nothing reordered).
//   - when compaction actually happened the leading turn is a SUMMARY turn framed
//     "[CONTEXT SUMMARY]\n" … "[END SUMMARY]".
//   - determinism: same input + same fault plan → same result (compared by turn
//     kind + message content, excluding the new turn's timestamp).
func FuzzCtxmgrSummarizeSteered(f *testing.F) {
	f.Add([]byte("\x00\x05hello\x02\x04work\x00\x03end"), uint8(2), []byte(nil), "", true)
	f.Add([]byte("\x03\x06/a/b.go\x06\x02ls\x040\x00\x02hi"), uint8(3), []byte{0x00, 0x04, 0x08}, "keep exact IDs", false)
	f.Add([]byte{}, uint8(6), []byte(nil), "", true)

	f.Fuzz(func(t *testing.T, data []byte, preserveSel uint8, faultPlan []byte, instructions string, withCheap bool) {
		run := func() ([]schema.Turn, []schema.Turn, error) {
			r := &ctxmgr_reader{b: data}
			history := ctxmgr_buildHistory(r)
			preserve := int(preserveSel) % 10

			profile := testOpenAIProfileWithContextWindow(1000)
			if withCheap {
				profile = WithCheapModel(profile, "cheap-model")
			}
			client := ctxmgr_scriptedClient(profile.ID(), "handoff summary body", faultPlan)
			cm := NewManager(profile, client)

			result, err := cm.summarizeWithLLMSteered(context.Background(), history, preserve, instructions)
			return result, history, err
		}

		result, history, err := run()
		if err != nil {
			return
		}
		// A nil result is only legitimate when the input history was itself nil
		// (the early no-op returns history unchanged).
		if result == nil && len(history) > 0 {
			t.Fatalf("summarizeWithLLMSteered returned nil result for non-empty history")
		}

		// Preserved-suffix invariant.
		k := len(result) - 1
		if k >= 0 {
			if k > len(history) {
				t.Fatalf("result longer than input: len(result)=%d len(history)=%d", len(result), len(history))
			}
			if !reflect.DeepEqual(result[1:], history[len(history)-k:]) {
				t.Fatalf("preserved suffix not carried through verbatim")
			}
		}

		// If compaction happened the head is a framed SUMMARY turn.
		if !reflect.DeepEqual(ctxmgr_fingerprints(result), ctxmgr_fingerprints(history)) {
			if len(result) == 0 || result[0].Kind != schema.TurnSummary {
				t.Fatalf("compacted head is not a SUMMARY turn")
			}
			head := result[0].Message.Text()
			if !strings.HasPrefix(head, "[CONTEXT SUMMARY]\n") || !strings.HasSuffix(head, "[END SUMMARY]") {
				t.Fatalf("summary turn not framed: %q", head)
			}
		}

		// Determinism.
		result2, _, err2 := run()
		if err2 != nil {
			t.Fatalf("summarize nondeterministic error: run2 err=%v", err2)
		}
		if !reflect.DeepEqual(ctxmgr_fingerprints(result), ctxmgr_fingerprints(result2)) {
			t.Fatalf("summarizeWithLLMSteered not deterministic")
		}
	})
}

// ctxmgr_manageThresholds derives compaction thresholds and a preserve count
// from fuzzer bytes. Layers 1-3 default low (fire readily); the summarize layer
// toggles between firing and skipping so the pipeline is explored both ways.
func ctxmgr_manageThresholds(cm *Manager, sel uint8, preserveSel uint8) {
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = float64(sel%4) * 0.0001
	if sel&0x80 != 0 {
		cm.SummarizeThreshold = 0.0001
	} else {
		cm.SummarizeThreshold = 2.0
	}
	cm.PreserveRecentTurns = 1 + int(preserveSel)%8
}

// FuzzCtxmgrCheckpointPredManageContext drives CheckpointPredStrategy.ManageContext
// end to end: observation masking, thinking clearing, predictive checkpoint (LLM,
// falling back to the deterministic checkpoint on the fault-injected error), and
// LLM summarization. The scripted adapter never hits the network.
//
// Oracles:
//   - never panic; ManageContext returns nil.
//   - compaction never grows history: len(after) <= len(before).
//   - determinism: same input + same fault plan → same resulting history
//     (compared by turn kind + message content).
func FuzzCtxmgrCheckpointPredManageContext(f *testing.F) {
	f.Add([]byte("\x00\x05hello\x02\x04work\x03\x06/a/b.go\x00\x03end\x02\x03more"), uint8(0x80), uint8(2), []byte(nil))
	f.Add([]byte("\x05\x03hey\x06\x02ls\x040\x0a\x02t\x04tk"), uint8(0x03), uint8(3), []byte{0x00, 0x08})
	f.Add([]byte{}, uint8(0), uint8(1), []byte(nil))
	// Rich history + summarize-on + LLM success (nil fault plan): drives the
	// predictive-checkpoint success path plus every prior masking layer.
	f.Add(ctxmgr_richHistorySeed, uint8(0x80), uint8(2), []byte(nil))
	// Same, but with an injected LLM fault so the deterministic-checkpoint
	// fallback branch runs.
	f.Add(ctxmgr_richHistorySeed, uint8(0x80), uint8(2), []byte{0x00, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, data []byte, thrSel uint8, preserveSel uint8, faultPlan []byte) {
		run := func() []schema.Turn {
			r := &ctxmgr_reader{b: data}
			history := ctxmgr_buildHistory(r)

			profile := testOpenAIProfileWithContextWindow(1000)
			client := ctxmgr_scriptedClient(profile.ID(), "predicted checkpoint body", faultPlan)
			cm := NewManager(profile, client)
			ctxmgr_manageThresholds(cm, thrSel, preserveSel)

			s := NewCheckpointPredStrategy(cm)
			before := len(history)
			if err := s.ManageContext(context.Background(), &history, int(thrSel), ctxmgr_noopEmit); err != nil {
				t.Fatalf("ManageContext returned error: %v", err)
			}
			if len(history) > before {
				t.Fatalf("compaction grew history: %d -> %d", before, len(history))
			}
			return history
		}
		a := run()
		b := run()
		if !reflect.DeepEqual(ctxmgr_fingerprints(a), ctxmgr_fingerprints(b)) {
			t.Fatalf("CheckpointPredStrategy.ManageContext not deterministic")
		}
	})
}

// FuzzCtxmgrSessionLogManageContext drives SessionLogStrategy.ManageContext:
// masking, thinking clearing, the session-log checkpoint (which replaces the old
// prefix with a checkpoint turn built from the session log + original prompt),
// and LLM summarization on a fault-injected adapter. The session log lives under
// a t.TempDir sandbox; no real network or process is touched.
//
// Oracles: never panic; ManageContext returns nil; compaction never grows
// history; determinism over the same input + fault plan.
func FuzzCtxmgrSessionLogManageContext(f *testing.F) {
	f.Add([]byte("\x00\x05hello\x02\x04work\x03\x06/a/b.go\x00\x03end\x02\x03more"), uint8(0x80), uint8(2), []byte(nil))
	f.Add([]byte("\x07cp\x08sum\x00\x04task\x02\x03aaa\x05\x02hi"), uint8(0x02), uint8(3), []byte{0x00})
	f.Add([]byte{}, uint8(0), uint8(1), []byte(nil))
	// Rich history forces the session-log checkpoint layer (original-prompt
	// extraction + log-backed checkpoint build) and the summarize layer.
	f.Add(ctxmgr_richHistorySeed, uint8(0x80), uint8(2), []byte(nil))
	f.Add(ctxmgr_richHistorySeed, uint8(0x82), uint8(3), []byte{0x00})

	f.Fuzz(func(t *testing.T, data []byte, thrSel uint8, preserveSel uint8, faultPlan []byte) {
		run := func() []schema.Turn {
			r := &ctxmgr_reader{b: data}
			history := ctxmgr_buildHistory(r)

			profile := testOpenAIProfileWithContextWindow(1000)
			client := ctxmgr_scriptedClient(profile.ID(), "session summary body", faultPlan)
			cm := NewManager(profile, client)
			ctxmgr_manageThresholds(cm, thrSel, preserveSel)

			host := &fakeStrategyHost{stateDir: t.TempDir(), id: "CTXMGR-FUZZ", profile: profile}
			s, err := NewSessionLogStrategy(cm, host)
			if err != nil {
				t.Fatalf("NewSessionLogStrategy: %v", err)
			}
			before := len(history)
			if err := s.ManageContext(context.Background(), &history, int(thrSel), ctxmgr_noopEmit); err != nil {
				t.Fatalf("ManageContext returned error: %v", err)
			}
			if len(history) > before {
				t.Fatalf("compaction grew history: %d -> %d", before, len(history))
			}
			return history
		}
		a := run()
		b := run()
		if !reflect.DeepEqual(ctxmgr_fingerprints(a), ctxmgr_fingerprints(b)) {
			t.Fatalf("SessionLogStrategy.ManageContext not deterministic")
		}
	})
}

// ctxmgr_noopEmit is a shared no-op event sink for the ManageContext fuzzers.
func ctxmgr_noopEmit(events.EventKind, events.EventData) {}
