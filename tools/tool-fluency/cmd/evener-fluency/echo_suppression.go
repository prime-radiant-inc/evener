package main

// echo_suppression.go recognizes the same duplicate evener itself never
// shows: a communicate/result-tool message that repeats assistant text
// already shown within the same logical turn. Without it, a packet or a
// prose count would show the reader a repetition the user never saw.
//
// evener's own rule lives in internal/apptranscript:
//   - the echo test itself: apptranscript.EchoesAssistantText, reused
//     directly below (it is exported).
//   - what "same logical turn" means: internal/apptranscript/logical_turn.go's
//     opensLogicalTurn/continuesLogicalTurn. Those are unexported, so the
//     predicate continuesOpenLogicalTurn below mirrors continuesLogicalTurn's
//     switch; opensLogicalTurn's only reachable case here is a plain
//     USER_INPUT (the goal-continuation-STEERING case never occurs in a
//     fluency run's single-goal session).
//   - where the check is applied and what it is applied to: apptranscript.go's
//     ASSISTANT case only records lastAssistantText when it is non-empty
//     (apptranscript.go:740-746: a text-less ASSISTANT record must not clear
//     what an earlier record in the same logical turn showed), and the
//     healed-communicate echo check scopes by that logical turn
//     (flush_communicate.go:37, apptranscript.go:807).
//
// doctor.Transcript (what tool-fluency reads) has no logical-turn id at all:
// TurnSummary is one entry, one turn, with no grouping. echoSuppressor tracks
// turn continuity itself, as a monotonic sequence number that advances
// exactly when the mirrored rule would open a new logical turn, using only
// the turn kind doctor's TurnSummary carries.
import (
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/apptranscript"
)

// echoSuppressor is one session transcript's echo-detection state, built
// fresh per transcript the way evener builds one ToolCallRegistry per
// session.
type echoSuppressor struct {
	open              bool
	turnSeq           int
	lastAssistantText string
	lastAssistantTurn int
}

// continuesOpenLogicalTurn mirrors logical_turn.go's continuesLogicalTurn:
// these turn kinds extend the currently open logical turn rather than
// starting a new one.
func continuesOpenLogicalTurn(kind string) bool {
	switch schema.TurnKind(kind) {
	case schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults, schema.TurnFailure, schema.TurnSteering:
		return true
	default:
		return false
	}
}

// observe advances the tracker for one turn, in transcript order, and
// returns the logical turn (as this tracker's own sequence number, not
// evener's persisted turn id — only equality between two calls' results
// matters) it belongs to. It must be called for every turn, including ones
// the caller will not otherwise use, so the open/closed state stays correct
// across turns the caller skips.
func (e *echoSuppressor) observe(kind, text string) int {
	switch {
	case schema.TurnKind(kind) == schema.TurnUserInput:
		e.turnSeq++
		e.open = true
	case continuesOpenLogicalTurn(kind) && e.open:
		// Same logical turn: turnSeq unchanged.
	default:
		e.turnSeq++
		e.open = continuesOpenLogicalTurn(kind)
	}
	// Only a non-empty ASSISTANT turn's text becomes the tracked "last
	// assistant text": a text-less ASSISTANT turn (the bare-tool-call
	// continuation that carries a healed communicate) must not clear what an
	// earlier turn in the same logical turn already showed.
	if schema.TurnKind(kind) == schema.TurnAssistant {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			e.lastAssistantText = trimmed
			e.lastAssistantTurn = e.turnSeq
		}
	}
	return e.turnSeq
}

// echoes reports whether msg, said on the turn observe just returned turnSeq
// for, repeats assistant text already shown within that SAME logical turn.
// A cross-turn repeat (turnSeq differs) is a genuine message, not an echo,
// and must still render.
func (e *echoSuppressor) echoes(turnSeq int, msg string) bool {
	return turnSeq == e.lastAssistantTurn && apptranscript.EchoesAssistantText(e.lastAssistantText, msg)
}
