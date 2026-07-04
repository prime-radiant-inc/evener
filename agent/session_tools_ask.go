package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const (
	// askUserUnavailableErr is the exec-time guard's error (spec §7 point 4):
	// config drift and future registration refactors both land here instead of
	// a panic or a silent no-op.
	askUserUnavailableErr = "ask_user unavailable: no interactive user in this session; decide with your best judgment"

	// askUserAckText is returned on a successful ask_user call (spec §5.1).
	askUserAckText = "questions posted; answers arrive in the user's reply after your turn ends"
)

// askQuestion is one question posted by an ask_user call, recorded in the
// session's per-turn pending set (spec §5.1) purely so a round-boundary check
// can tell whether the round just posted question(s). The transcript remains
// the durable, renderable record of the questions and their options (spec
// §5.1, §6); this struct carries just enough to identify a pending question,
// not the full option/detail payload.
type askQuestion struct {
	Header   string
	Question string
}

// isSubagentSession reports whether this session is a subagent, for the
// ask_user root-only gate (spec §7.1): a live spawn (cfg.spawn.parentSessionID,
// covering delegate spawns and job restores) OR a session restored from a
// persisted meta whose IsSubagent flag was set. The flag alone catches a bare
// `serve --resume <delegate-id>`: spawn is never persisted (json:"-"), so that
// restore path leaves cfg.spawn.parentSessionID empty. A forked root is NOT a
// subagent — fork lineage lives in meta.ParentSessionID with IsSubagent==false
// (Session.Meta already keeps the two concepts apart when writing the flag),
// a distinct concept from cfg.spawn.parentSessionID.
func (s *Session) isSubagentSession() bool {
	return s.cfg.spawn.parentSessionID != "" || s.restoredMetaIsSubagent
}

// askPendingCount returns the number of questions currently pending this turn
// (spec §5.1's per-turn pending set) — a later round-boundary check uses this
// to decide whether the round just posted question(s).
func (s *Session) askPendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.askPending)
}

// HasPendingAsk reports whether the session has an unresolved ask_user
// question. Exported so a cross-module gate can mirror the entry gate's
// refusal predicate (session_lifecycle.go's processInputKindWithProvenance,
// spec §5.3) exactly: cmd/serf/serve.go's pre-dispatch /status shadow-write
// hold must skip the write for precisely the wakes the entry gate will
// refuse. Keying on the pending set rather than raw SessionState matters
// since attention-status-model v5: SessionAwaiting no longer implies a
// pending question on its own (the general inbox-semantics upgrade also
// rests a session awaiting after any clean, output-producing turn).
func (s *Session) HasPendingAsk() bool {
	return s.askPendingCount() > 0
}

// clearAskPending empties the pending set. Nothing in this task calls it in
// production: a later task wires the actual clear points (a resolving user
// turn, an interrupted turn).
func (s *Session) clearAskPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.askPending = nil
}

// parseAskQuestions extracts the askQuestions from an ask_user call's parsed
// arguments. Schema-level shape (question/option counts, header maxLength,
// required fields) is already enforced by the registry's JSON-Schema
// validation before the live Exec below ever runs (spec §5.1) — this checks
// only the two semantic rules the schema cannot express: option labels
// unique within a question, and at most one recommended option per
// question. Both are all-or-nothing per call: on either violation nothing
// from that call is returned.
//
// Shared verbatim by the live Exec path below and by restore's pending-set
// rebuild (deriveRestoredAskPending / questionsFromAskCalls, ask-attention-
// tiering spec §2) so the two paths can never drift apart. The two callers
// treat a returned error differently — Exec aborts the whole tool call, so
// nothing is posted; restore treats it as "unparseable arguments" and skips
// just that one call, never failing the restore.
func parseAskQuestions(args map[string]any) ([]askQuestion, error) {
	raw, _ := args["questions"].([]any)
	parsed := make([]askQuestion, 0, len(raw))
	for _, r := range raw {
		qm, _ := r.(map[string]any)
		labelsSeen := map[string]bool{}
		recommendedCount := 0
		opts, _ := qm["options"].([]any)
		for _, o := range opts {
			om, _ := o.(map[string]any)
			label := fmt.Sprint(om["label"])
			if labelsSeen[label] {
				return nil, errors.New("ask_user: option labels must be unique within a question")
			}
			labelsSeen[label] = true
			if rec, _ := om["recommended"].(bool); rec {
				recommendedCount++
			}
		}
		if recommendedCount > 1 {
			return nil, errors.New("ask_user: at most one option may be recommended")
		}
		parsed = append(parsed, askQuestion{
			Header:   fmt.Sprint(qm["header"]),
			Question: fmt.Sprint(qm["question"]),
		})
	}
	return parsed, nil
}

// registerAskTool registers ask_user. registerCoreTools calls this only when
// the session is interactive and root (spec §7 point 1); the exec-time guard
// below is defense in depth for config drift (spec §7 point 4).
func registerAskTool(reg *tool.Registry, s *Session, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefAskUser()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}
			if s.cfg.NonInteractive || s.isSubagentSession() {
				return nil, errors.New(askUserUnavailableErr)
			}

			parsed, err := parseAskQuestions(args)
			if err != nil {
				return nil, err
			}

			s.mu.Lock()
			s.askPending = append(s.askPending, parsed...)
			s.mu.Unlock()

			return askUserAckText, nil
		},
	})
}

// deriveRestoredState re-derives a restored session's at-rest state from its
// history tail. It is the single resume-derivation function, unifying two
// rules that were designed independently and must both hold everywhere:
// ask_user's pending definition (spec §6: "an interrupted ack-less ask is
// never pending") and attention-status-model v5's general resume rule ("agent
// moved last" resumes awaiting, round-3 A2) — the ask rule is that general
// rule's specific instance, not a competing one, since an ack is just the
// terminal tool result of the round the agent last moved in.
//
// Walking backward from the most recent turn, the first decisive turn wins:
//
//   - TurnUserInput: a reply already resolved whatever was pending, or
//     nothing ever was — idle.
//   - TurnAssistant with no tool calls: a plain final response with nothing
//     else after it — the agent moved last — awaiting.
//   - TurnAssistant WITH tool calls: not decisive, the scan continues past
//     it. Ordinarily a tool call is immediately followed by a matching
//     TurnToolResults, so the scan reaches that turn first — but when every
//     one of THIS turn's calls resolved to nothing but error placeholders
//     (the case directly below), the scan falls through to the assistant
//     turn that issued them, and it must not then be mistaken for a
//     plain completed response: it is the same interrupted round, not a
//     second, earlier one.
//   - TurnToolResults carrying at least one completed (non-error) result:
//     the round ended its turn on a real completion — a communicate, an
//     ask_user ack, or any other terminal tool — awaiting.
//   - TurnToolResults carrying ONLY error results: not decisive: the scan
//     continues past it. This is the ask-specific carve-out generalized:
//     when a tool call (ask_user or otherwise) is interrupted before its
//     result is ever recorded, ResumeHistory's orphan repair
//     (history_repair.go) synthesizes an IsError TOOL_RESULTS entry so the
//     provider never sees a dangling call — but that placeholder is not a
//     completion, so a crash-interrupted round must not read as "agent moved
//     last" (spec §6: "an interrupted ack-less ask is never pending"). A
//     denied or invalid ask_user call is IsError for the same reason and is
//     excluded the same way.
//   - TurnSteering, TurnCheckpoint, TurnSummary, TurnSystem, and the
//     deprecated TurnTool: bookkeeping, not decisive — the scan continues
//     past them. A trailing steering turn (e.g. a task-nudge reminder
//     injected before the round-boundary check runs) must not resolve a
//     pending ask by looking like the user moved last (spec §6); a trailing
//     checkpoint/summary is the resume anchor ResumeHistory already
//     truncated to, not a new decisive event.
//
// No decisive turn anywhere in the (possibly compacted) history defaults to
// idle, matching a fresh session.
func deriveRestoredState(history []schema.Turn) SessionState {
	for i := len(history) - 1; i >= 0; i-- {
		turn := history[i]
		switch turn.Kind {
		case schema.TurnUserInput:
			return SessionIdle
		case schema.TurnAssistant:
			if len(assistantToolCalls(turn.Message)) == 0 {
				return SessionAwaiting
			}
			// This turn's calls resolved to an all-error placeholder we
			// already scanned past (or repair guarantees one exists ahead of
			// it) — not a completion. Keep scanning past it too.
		case schema.TurnToolResults:
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && !part.ToolResult.IsError {
					return SessionAwaiting
				}
			}
			// Every result here is an error placeholder (orphan repair, a
			// denied/invalid call, or a round where every call failed): not a
			// completion. Keep scanning past it.
		}
	}
	return SessionIdle
}

// deriveRestoredAskPending rebuilds the pending-ask SET from a restored
// history's tail (ask-attention-tiering spec §2): deriveRestoredState above
// re-derives that the session rests awaiting, but every hold keyed on
// askPending itself — the entry gate (session_lifecycle.go:453), the goal
// engine's arm-don't-kick paths (session_goal.go:61,:246), and Compact's
// guard (session_compaction.go:30) — reads len(s.askPending), which stays
// empty unless this also runs.
//
// This performs the IDENTICAL backward walk deriveRestoredState uses (same
// turn-kind cases, same "not decisive, keep scanning" fallthroughs — see
// that function's doc comment for the full rationale) so the two can never
// disagree about which turn is decisive; it just does more once it reaches
// one:
//
//   - TurnUserInput, or a TurnAssistant final with no tool calls: idle or a
//     GENERIC awaiting rest, not an ask round — returns (nil, false); the
//     pending set must stay empty (spec §2's first edge case).
//   - TurnToolResults carrying only error-placeholder results: not decisive,
//     keep scanning (matches deriveRestoredState's orphan-repair carve-out).
//   - TurnToolResults carrying a completed (non-error) result, but none of
//     them named "ask_user" (e.g. a communicate ack): decisive, but still a
//     GENERIC awaiting rest — returns (nil, false).
//   - TurnToolResults carrying at least one completed, non-error "ask_user"
//     result: an ask round. Delegates to questionsFromAskCalls for exactly
//     those calls, in call order (spec §2's "multiple ask_user calls in the
//     round: union, in call order").
//
// isAskRound tells the caller whether an empty pending slice means "nothing
// was pending" (false) or "an ask round was found but none of its calls'
// arguments parsed" (true) — spec §2's unparseable-arguments edge case: the
// caller logs a warning only for the latter, and the restore must never fail
// over either.
func deriveRestoredAskPending(history []schema.Turn) (pending []askQuestion, isAskRound bool) {
	for i := len(history) - 1; i >= 0; i-- {
		turn := history[i]
		switch turn.Kind {
		case schema.TurnUserInput:
			return nil, false
		case schema.TurnAssistant:
			if len(assistantToolCalls(turn.Message)) == 0 {
				return nil, false
			}
			// Not decisive; matches deriveRestoredState's scan past an
			// all-error placeholder round.
		case schema.TurnToolResults:
			askCallIDs := map[string]bool{}
			anyNonError := false
			for _, part := range turn.Message.Content {
				if part.Kind != llm.ContentToolResult || part.ToolResult == nil || part.ToolResult.IsError {
					continue
				}
				anyNonError = true
				if part.ToolResult.Name == "ask_user" {
					askCallIDs[part.ToolResult.ToolCallID] = true
				}
			}
			if !anyNonError {
				continue // error-only placeholder; not decisive, matches deriveRestoredState
			}
			if len(askCallIDs) == 0 {
				return nil, false // decisive, but a generic completion (e.g. a communicate ack)
			}
			return questionsFromAskCalls(history, i, askCallIDs), true
		}
	}
	return nil, false
}

// questionsFromAskCalls parses the questions for a decisive ask round found
// at history[toolResultsIdx] (a TurnToolResults turn). It walks backward for
// the round's assistant turn: every round appends exactly one TurnAssistant
// turn immediately followed by one aggregated TurnToolResults turn
// (session_tool_round.go's appendAssistantTurn -> execToolBatch ->
// persistToolResults, with nothing interposed), so the nearest preceding
// TurnAssistant turn is always the right one. It then parses, in call order,
// every ask_user call whose ID is in wantCallIDs (the decisive turn's
// non-error ask_user results) via the shared parseAskQuestions — a call
// whose arguments fail to unmarshal or fail parseAskQuestions's validation
// is skipped, not fatal (spec §2: "rebuild what parses").
func questionsFromAskCalls(history []schema.Turn, toolResultsIdx int, wantCallIDs map[string]bool) []askQuestion {
	for j := toolResultsIdx - 1; j >= 0; j-- {
		if history[j].Kind != schema.TurnAssistant {
			continue
		}
		var out []askQuestion
		for _, call := range assistantToolCalls(history[j].Message) {
			if call.Name != "ask_user" || !wantCallIDs[call.ID] {
				continue
			}
			var args map[string]any
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				continue // unparseable JSON; skip this call, never fail the restore
			}
			parsed, err := parseAskQuestions(args)
			if err != nil {
				continue // semantic violation on a hand-edited/drifted transcript; skip
			}
			out = append(out, parsed...)
		}
		return out
	}
	return nil
}
