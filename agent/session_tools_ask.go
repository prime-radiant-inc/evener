package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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
// spec §5.3) exactly: cmd/evener/serve.go's pre-dispatch status shadow-write
// hold must skip the write for precisely the wakes the entry gate will
// refuse. Keying on the pending set rather than raw SessionState matters
// since attention-status-model v5: SessionAwaiting no longer implies a
// pending question on its own (the general inbox-semantics upgrade also
// rests a session awaiting after any clean, output-producing turn).
func (s *Session) HasPendingAsk() bool {
	return s.askPendingCount() > 0
}

// clearAskPending empties the pending set. Callers: durable user-input
// admission, the interrupt branch (session_lifecycle.go, directly), and
// clearAskPendingForResolvingSteer below (a drained user-sourced steer,
// mid-round).
func (s *Session) clearAskPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.askPending = nil
}

// clearAskPendingForResolvingSteer clears the pending set the moment a
// drained user-sourced steer that resolves it actually lands in the
// transcript — not just at the next accepted-turn entry.
// injectDrainedSteering/injectPostToolSteering run MID-ROUND, well before
// any such entry, and the server owns the ask boundary: the wire's
// EvenerThread.AskPending (hydrate) and ThreadStatusChangedParams.AskPending
// (stamped on every status change, server/appwire_runtime.go's
// stampAskPendingOnStatusChange) are wire-authoritative, and the client
// trusts that flag rather than re-deriving the boundary itself
// (appwire-client/typescript/reducer.test.ts's "askPending is
// wire-authoritative"); leaving s.askPending stale here would desync the
// flag this session next reports, not merely a restore-time bug. An
// interrupted steer (SteeringKindInterrupted) never reaches here — the
// interrupt branch calls clearAskPending directly before this turn is ever
// appended — so this checks steeringAnswersAsk alone.
func (s *Session) clearAskPendingForResolvingSteer(t schema.Turn) {
	if steeringAnswersAsk(t.SteeringSource, steeringOrigin{kind: t.SteeringKind}, "") {
		s.clearAskPending()
	}
}

// steeringCarrierClaimAnswersAsk reports whether the steer a steering-carrier
// identity is about to carry (queuedClientMutationIdentity.SteeringCarrier)
// answers a pending ask per steeringAnswersAsk. processOneInput's entry
// clear runs before acceptSteeringCarrierInput ever drains the steer — before
// its turn even knows the steer's kind — so this reads the provenance from
// the durable client-mutation journal (steeringOriginFromJournal) instead of
// a live steeringMessage. Every steer a carrier exists for is user-sourced
// (acceptSteeringCarrierInput's own doc: "already-accepted user steering"),
// so only the provenance can vary. A non-carrier identity, or one with
// nothing to look up, answers by definition, preserving the entry clear's
// ordinary unconditional behavior for a genuine user reply. A carrier
// identity whose journal record is MISSING entirely (as opposed to present
// but kindless) fails CLOSED — does not answer — rather than defaulting to
// "answers": an unknown id is exactly the case where a lost or
// not-yet-visible record (rather than an ordinary steer that simply never
// recorded a kind) could silently resolve a still-unanswered human note.
func (s *Session) steeringCarrierClaimAnswersAsk(identity queuedClientMutationIdentity) bool {
	if !identity.SteeringCarrier || identity.ClientMutationID == "" || s.clientMutations == nil {
		return true
	}
	journal := s.clientMutations.snapshot().Journal
	if _, ok := journal[identity.ClientMutationID]; !ok {
		return false
	}
	origin := steeringOriginFromJournal(journal, identity.ClientMutationID)
	return steeringAnswersAsk(events.SteeringSourceUser, origin, "")
}

// steeringAnswersAsk reports whether steering carrying this source and
// provenance answers a pending ask_user question the way a plain user reply
// would. SteeringSourceUser marks steering as user-sourced in general, but a
// human-note update is also user-sourced without addressing the question —
// saving a note while a question is pending must not clear it (RoboRev
// #1806 member-0 Medium) — so origin.isHumanNoteSteer excludes it.
// textEvidence is the turn's text where the caller has it (turnResolvesAskBoundary's
// restore scan) and "" where it does not (a journal-only lookup): exactly
// where isHumanNoteSteer's last-resort write-path-shape check has nothing to
// read anyway, since a journal record that reached this far already decided
// by kind or method. One predicate for the live mid-round clear
// (clearAskPendingForResolvingSteer), the carrier-claim check
// (steeringCarrierClaimAnswersAsk), and the restore boundary scan
// (turnResolvesAskBoundary) so the three cannot independently drift on which
// kinds count as an answer.
func steeringAnswersAsk(source string, origin steeringOrigin, textEvidence string) bool {
	return source == events.SteeringSourceUser && !origin.isHumanNoteSteer(textEvidence)
}

// steeringOriginBoundary clamps divergenceTurn (expressed exactly as
// escapeHistoryWithSessionProvenance defines it, session_init.go) to a valid
// inherited-turn count for a history of historyLen turns: turns before the
// bound came from a parent session whose journal this session does not
// hold, and a child mutation may reuse a parent's client mutation id, so
// provenance lookups for them must use nil origins; turns from the bound
// onward are this session's own and may consult its journal. Shared by
// escapeHistoryWithSessionProvenance and turnResolvesAskBoundary's callers
// (deriveRestoredState/deriveRestoredAskPending) so a second copy of the
// clamp can't drift from it (RoboRev #1806 round-5 Medium: the ask-boundary
// scan used to apply the whole journal to the whole history, letting a
// reused id misclassify an inherited turn).
func steeringOriginBoundary(divergenceTurn, historyLen int) int {
	inherited := divergenceTurn - 1
	if inherited <= 0 {
		return 0
	}
	if inherited >= historyLen {
		return historyLen
	}
	return inherited
}

// minimalExampleQuestionsArray returns a minimal valid example for error messages.
func minimalExampleQuestionsArray() string {
	ex := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which option?",
				"options": []any{
					map[string]any{"label": "Option A", "detail": "First choice"},
					map[string]any{"label": "Option B", "detail": "Second choice"},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(ex, "", "  ")
	return string(b)
}

// normalizeAskArgs normalizes ask_user arguments by wrapping the shorthand form
// into the canonical batch form. When `questions` is absent but `question` +
// `options` are present (plus optional `why`, `if_unanswered`, `multi_select`,
// `header`), wraps them into a one-element `questions` array. Returns a
// normalized copy of args, or an error if the shape is invalid.
func normalizeAskArgs(args map[string]any) (map[string]any, error) {
	_, hasQuestions := args["questions"]
	question, hasQuestion := args["question"]
	options, hasOptions := args["options"]

	// Case 1: questions present, question/options absent → use as-is (batch form)
	if hasQuestions && !hasQuestion && !hasOptions {
		return args, nil
	}

	// Case 2: question + options present, questions absent → wrap into batch form
	if !hasQuestions && hasQuestion && hasOptions {
		// Collect optional fields
		wrapped := map[string]any{
			"question": question,
			"options":  options,
		}
		if why, ok := args["why"].(string); ok && why != "" {
			wrapped["why"] = why
		}
		if ifUnanswered, ok := args["if_unanswered"].(string); ok && ifUnanswered != "" {
			wrapped["if_unanswered"] = ifUnanswered
		}
		if multiSelect, ok := args["multi_select"].(bool); ok && multiSelect {
			wrapped["multi_select"] = multiSelect
		}
		if header, ok := args["header"].(string); ok && header != "" {
			wrapped["header"] = header
		}

		// Create normalized args with wrapped questions
		out := make(map[string]any, len(args))
		for k, v := range args {
			if k != "question" && k != "options" && k != "why" && k != "if_unanswered" &&
				k != "multi_select" && k != "header" {
				out[k] = v
			}
		}
		out["questions"] = []any{wrapped}
		return out, nil
	}

	// Case 3: distinguish sub-cases within invalid shapes
	// Sub-case 3a: both questions and question/options present
	if hasQuestions && hasQuestion {
		errorMsg := "ask_user: both 'questions' and 'question'/'options' given — supply exactly one form. Minimal example:\n" + minimalExampleQuestionsArray()
		return nil, errors.New(errorMsg)
	}

	// Sub-case 3b: question present but options missing (shorthand attempted but incomplete)
	if hasQuestion && !hasOptions {
		errorMsg := "ask_user: 'options' is required when using the 'question' shorthand. Minimal example:\n" + minimalExampleQuestionsArray()
		return nil, errors.New(errorMsg)
	}

	// Sub-case 3c: neither questions nor question+options present
	errorMsg := "ask_user: 'questions' is required (or use the 'question'+'options' shorthand for a single question). Minimal example:\n" + minimalExampleQuestionsArray()
	return nil, errors.New(errorMsg)
}

// parseAskQuestions extracts the askQuestions from an ask_user call's parsed
// arguments. Schema-level shape (question/option counts, required fields) is
// already enforced by the registry's JSON-Schema
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
				errorMsg := "ask_user: option labels must be unique within a question. Minimal example:\n" + minimalExampleQuestionsArray()
				return nil, errors.New(errorMsg)
			}
			labelsSeen[label] = true
			if rec, _ := om["recommended"].(bool); rec {
				recommendedCount++
			}
		}
		if recommendedCount > 1 {
			errorMsg := "ask_user: at most one option may be recommended. Minimal example:\n" + minimalExampleQuestionsArray()
			return nil, errors.New(errorMsg)
		}
		header, _ := qm["header"].(string)
		parsed = append(parsed, askQuestion{
			Header:   header,
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
		Definition: tool.DefAskUser(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			release, err := s.beginRetirementMutation("question")
			if err != nil {
				return nil, err
			}
			defer release()
			_ = env
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}
			if s.cfg.NonInteractive || s.isSubagentSession() {
				return nil, errors.New(askUserUnavailableErr)
			}

			// Args have already been normalized by the registry's ExecuteCall
			// before schema validation, so we can parse directly.
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

// turnResolvesAskBoundary reports whether turn is one of the three shapes
// that end a pending-ask round the same way a plain reply does (spec §6).
// deriveRestoredState and deriveRestoredAskPending both walk backward over
// the SAME history and must agree on exactly this boundary — factored here
// once so the two can never independently narrow it and drift apart.
//
// origins is the client-mutation journal's steering provenance by mutation
// id (clientMutationStore.steeringOrigins(), nil when there is nothing to
// look up): a steering turn persisted before SteeringKind was stamped keeps
// none of its own, and only the record of the mutation that wrote it can
// still say whether it was a notes update or an ordinary steer
// (steeringOriginForTurn). Without this, a kindless legacy human-note turn
// reads as an ordinary answering steer and wrongly resolves the boundary.
//
//   - TurnUserInput: the user spoke — resolves.
//   - TurnSteering carrying the admitted interrupt marker
//     (SteeringKindInterrupted), or a steer whose
//     provenance (steeringOriginForTurn(turn, origins)) steeringAnswersAsk
//     reports as answering the user: the runtime already cleared askPending
//     on this turn's behalf before it ever ran (session_lifecycle.go: the
//     interrupt branch calls clearAskPending directly; a resolving user
//     steer clears askPending via clearAskPendingForResolvingSteer) —
//     resolves. A human-note update (events.SteeringKindHumanNote, by its
//     own kind or by its journal record's provenance, or — for a kindless,
//     provenance-less inherited fork prefix — by the write-path text shape
//     isHumanNoteSteer falls back to) is user-sourced but does not answer
//     the question, so steeringAnswersAsk excludes it: does not resolve.
//     The interrupted salvage explanation (SteeringKindInterruptedSalvage) is
//     a daemon-authored explanation rather than an admitted boundary, like
//     any other non-resolving TurnSteering. Any other TurnSteering (a
//     reminder) carries neither marker: does not resolve, the scan continues
//     past it — a trailing steering turn must not resolve a pending ask by
//     looking like the user moved last (spec §6).
//   - TurnFailure tagged SteeringCarrier (schema.TurnFailureInfo.
//     SteeringCarrier's own doc comment): the turn's mere acceptance cleared
//     askPending before it recorded nothing else — either its steer failed
//     to append, or a carrier-claim's own steer failed its selection prepare
//     — resolves. Every OTHER TurnFailure (a retry-budget exhaustion, a
//     non-carrier (inline) failed steering-selection prepare, a provider
//     error) does not resolve: the round it happened to may have posted real
//     content — an ask_user call among it — before failing, and that
//     content's own turn is still ahead in the scan to decide the outcome.
//
// Every other turn kind does not resolve here either; the caller's own
// switch handles TurnAssistant/TurnToolResults, where the two functions
// genuinely differ on what a resolved boundary settles TO (SessionAwaiting
// vs a typed ask_user pending set) — that part is not shared.
func turnResolvesAskBoundary(turn schema.Turn, origins map[string]steeringOrigin) bool {
	switch turn.Kind {
	case schema.TurnUserInput:
		return true
	case schema.TurnSteering:
		if turn.SteeringKind == events.SteeringKindInterrupted {
			return true
		}
		origin := steeringOriginForTurn(turn, origins)
		return steeringAnswersAsk(turn.SteeringSource, origin, turn.Message.Text())
	case schema.TurnFailure:
		return turn.Error != nil && turn.Error.SteeringCarrier
	default:
		return false
	}
}

// roundEntryResolvesAskBoundary reports whether the turn that OPENED the
// round containing history[idx] itself resolves the ask boundary
// (turnResolvesAskBoundary). A round is exactly one entry turn — TurnUserInput,
// or a drained TurnSteering (session_tool_round.go) — followed by a chain of
// TurnAssistant/TurnToolResults pairs; this walks backward from idx past that
// chain to find it. deriveRestoredAskPending's own "generic completion is
// decisive" branches (a TurnAssistant final with no tool calls, or a
// TurnToolResults with no ask_user result) assume the round's entry already
// cleared askPending live — true for a genuine user reply, but NOT for a
// non-resolving carrier (a human-note update): its entry clear is skipped
// (steeringCarrierClaimAnswersAsk, session_lifecycle.go), so a generic
// completion inside that round must not resolve the boundary either, or
// restore would lose an ask the live session never resolved (RoboRev #1806's
// round-6 Medium). boundaryStart/origins are deriveRestoredAskPending's own
// steeringOriginBoundary scoping, applied identically here so the entry
// turn's provenance lookup can't diverge from the rest of the scan. Finding
// no entry turn (history begins mid-round, e.g. a compaction boundary) keeps
// the previous behavior: decisive.
//
// Only TurnUserInput, TurnSteering and a resolving SteeringCarrier TurnFailure
// are entry-capable kinds (turnResolvesAskBoundary's own switch is the only
// thing that can call any of them decisive; every other kind is transparent).
// In particular, a non-carrier TurnFailure is bookkeeping just like
// TurnCheckpoint, TurnSummary, TurnModelSwitch, TurnHookCompleted,
// TurnEnvironment, TurnNotesContext, TurnAttentionResolution, or TurnSystem:
// the outer scan also passes through it while looking for the round's real
// entry.
// Stopping at the first non-Assistant/ToolResults turn, as this used to,
// misreads a bookkeeping turn interleaved before the real entry as the entry
// itself.
func roundEntryResolvesAskBoundary(history []schema.Turn, idx, boundaryStart int, origins map[string]steeringOrigin) bool {
	for j := idx - 1; j >= 0; j-- {
		switch history[j].Kind {
		case schema.TurnUserInput, schema.TurnSteering:
			turnOrigins := origins
			if j < boundaryStart {
				turnOrigins = nil
			}
			return turnResolvesAskBoundary(history[j], turnOrigins)
		case schema.TurnFailure:
			if history[j].Error == nil || !history[j].Error.SteeringCarrier {
				continue
			}
			return true
		default:
			continue
		}
	}
	return true
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
//   - turnResolvesAskBoundary's decisive turns (TurnUserInput, a resolving
//     TurnSteering, a steering-carrier TurnFailure) resolve to idle — a
//     reply already resolved whatever was pending, or nothing ever was.
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
//   - Every other kind not covered above (a non-decisive TurnSteering,
//     TurnCheckpoint, TurnSummary, TurnSystem, TurnEnvironment, a non-carrier
//     TurnFailure, and the deprecated TurnTool): bookkeeping, not decisive —
//     the scan continues past it. A trailing checkpoint/summary is the
//     resume anchor ResumeHistory already truncated to, not a new decisive
//     event.
//
// No decisive turn anywhere in the (possibly compacted) history defaults to
// idle, matching a fresh session. origins is turnResolvesAskBoundary's same
// steering-provenance lookup (nil when there is nothing to look up).
// divergenceTurn scopes it exactly as escapeHistoryWithSessionProvenance
// does (steeringOriginBoundary): a forked child's inherited prefix is
// decided with nil origins regardless of what origins carries, since a
// reused client mutation id in the child's OWN journal must never reclassify
// a turn the parent wrote.
func deriveRestoredState(history []schema.Turn, divergenceTurn int, origins map[string]steeringOrigin) SessionState {
	inherited := steeringOriginBoundary(divergenceTurn, len(history))
	for i := range slices.Backward(history) {
		turn := history[i]
		turnOrigins := origins
		if i < inherited {
			turnOrigins = nil
		}
		if turnResolvesAskBoundary(turn, turnOrigins) {
			return SessionIdle
		}
		switch turn.Kind {
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
// This shares turnResolvesAskBoundary's decisive boundary with
// deriveRestoredState above — the two functions cannot disagree about which
// turn is decisive because they call the same code to decide it — and does
// more once it reaches one:
//
//   - A resolving boundary (turnResolvesAskBoundary's TurnUserInput/
//     TurnSteering/TurnFailure cases): idle, not an ask round — returns
//     (nil, false); the pending set must stay empty (spec §2's first edge
//     case).
//   - A TurnAssistant final with no tool calls, or a TurnToolResults
//     carrying a completed (non-error) result but none of them named
//     "ask_user" (e.g. a communicate ack): a GENERIC completion. Decisive —
//     returns (nil, false) — ONLY when the round it ends was itself opened by
//     a turn that resolves the boundary (roundEntryResolvesAskBoundary): a
//     genuine user reply always clears askPending on entry, so anything the
//     round then completes with is safe to treat as the end of the story.
//     A round opened by a non-resolving carrier (a human-note update) never
//     cleared askPending on entry, so its own generic completion must not
//     either — not decisive, keep scanning past it for the real boundary
//     further back (RoboRev #1806's round-6 Medium).
//   - TurnToolResults carrying only error-placeholder results: not decisive,
//     keep scanning (matches deriveRestoredState's orphan-repair carve-out).
//   - TurnToolResults carrying at least one completed, non-error "ask_user"
//     result: an ask round. Delegates to questionsFromAskCalls for exactly
//     those calls, in call order (spec §2's "multiple ask_user calls in the
//     round: union, in call order"), and accumulates them onto whatever a
//     LATER (already-scanned) non-resolving round found. Decisive — stops
//     the scan — only when THIS round's own entry resolves the boundary
//     (roundEntryResolvesAskBoundary); otherwise the scan keeps going for a
//     still-earlier pending ask, because ask_user's live Exec APPENDS to
//     askPending (registerAskTool) rather than replacing it: a human-note
//     carrier's own round can post a brand-new question without ever
//     answering one already pending, so live askPending can hold questions
//     from more than one round at once (RoboRev #1906/#1907 round-2
//     Medium). Every accumulated round's questions land in call order,
//     oldest round first, matching that append order.
//
// isAskRound tells the caller whether an empty pending slice means "nothing
// was pending" (false) or "an ask round was found but none of its calls'
// arguments parsed" (true) — spec §2's unparseable-arguments edge case: the
// caller logs a warning only for the latter, and the restore must never fail
// over either. origins is turnResolvesAskBoundary's same steering-provenance
// lookup (nil when there is nothing to look up). divergenceTurn scopes it
// exactly as deriveRestoredState does above (steeringOriginBoundary).
func deriveRestoredAskPending(history []schema.Turn, divergenceTurn int, origins map[string]steeringOrigin) (pending []askQuestion, isAskRound bool) {
	inherited := steeringOriginBoundary(divergenceTurn, len(history))
	// roundsNewestFirst collects each non-resolving round's own ask_user
	// questions as one element, newest round first (the scan walks
	// backward); finish flattens them oldest-round-first to match the live
	// path's own append order (registerAskTool's Exec: "s.askPending =
	// append(...)", never a replace) — each round's own call-order slice
	// from questionsFromAskCalls stays intact, only the ROUNDS reverse, not
	// the calls within one. A human-note carrier's round can post its own
	// ask_user question without ever answering an earlier one still pending
	// (its entry clear is skipped), so live askPending can hold questions
	// from more than one round at once — the scan must keep going past a
	// round that found questions but did not itself resolve the boundary,
	// rather than stopping at the first (newest) one (RoboRev #1906/#1907
	// round-2 Medium).
	var roundsNewestFirst [][]askQuestion
	finish := func() (pending []askQuestion, isAskRound bool) {
		if len(roundsNewestFirst) == 0 {
			return nil, false
		}
		for _, round := range slices.Backward(roundsNewestFirst) {
			pending = append(pending, round...)
		}
		return pending, true
	}
	for i := range slices.Backward(history) {
		turn := history[i]
		turnOrigins := origins
		if i < inherited {
			turnOrigins = nil
		}
		if turnResolvesAskBoundary(turn, turnOrigins) {
			return finish()
		}
		switch turn.Kind {
		case schema.TurnAssistant:
			if len(assistantToolCalls(turn.Message)) == 0 {
				if !roundEntryResolvesAskBoundary(history, i, inherited, origins) {
					continue // this round's entry never cleared askPending live either; not decisive
				}
				return finish()
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
				if !roundEntryResolvesAskBoundary(history, i, inherited, origins) {
					continue // this round's entry never cleared askPending live either; not decisive
				}
				return finish() // decisive, but a generic completion (e.g. a communicate ack)
			}
			roundsNewestFirst = append(roundsNewestFirst, questionsFromAskCalls(history, i, askCallIDs))
			if roundEntryResolvesAskBoundary(history, i, inherited, origins) {
				return finish() // this round's own entry resolved everything before it
			}
			// This round's entry did not resolve anything (a non-resolving
			// carrier): an earlier still-pending ask may exist further back.
			// Keep scanning, accumulating.
		}
	}
	return finish()
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
			// Normalize shorthand form into batch form
			normalized, err := normalizeAskArgs(args)
			if err != nil {
				continue // invalid shape; skip this call, never fail the restore
			}
			parsed, err := parseAskQuestions(normalized)
			if err != nil {
				continue // semantic violation on a hand-edited/drifted transcript; skip
			}
			out = append(out, parsed...)
		}
		return out
	}
	return nil
}
