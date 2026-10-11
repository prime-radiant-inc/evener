package main

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// echoNudgeText is the harness's real "use communicate" steering, verbatim
// from decideNoToolCalls (agent/session_tool_round.go), with "communicate"
// standing in for the resolved result-tool name.
const echoNudgeText = "You responded with bare text instead of a tool call. " +
	"All user-facing messages MUST use communicate. " +
	"If that bare text was meant for the user, call communicate now with that text in message, set end_turn=true, and include the output envelope. " +
	"Otherwise call your next tool and keep working."

// echoTurnsFixture builds the turns a real run writes when the model answers
// with bare text, the harness steers it back with the "use communicate"
// nudge (a STEERING turn, SteeringKind no-tool-calls, matching
// appendSteeringTurn), and the model then calls communicate repeating that
// same text. When gap is true, a compaction SUMMARY turn is inserted between
// the nudge and the repeat, closing the logical turn the text was shown in:
// evener would then treat the repeat as a genuine cross-turn message, not an
// echo.
func echoTurnsFixture(text string, gap bool) []schema.Turn {
	nudge := schema.NewTurn(schema.TurnSteering, llm.User(echoNudgeText))
	nudge.SteeringKind = events.SteeringKindNoToolCalls

	message, err := json.Marshal(map[string]any{"message": text, "end_turn": true})
	if err != nil {
		panic(err)
	}
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Fix the tally bug and tell me what changed.")),
		assistantTurn(textPart(text)),
		nudge,
	}
	if gap {
		turns = append(turns, schema.NewTurn(schema.TurnSummary, llm.Message{Content: []llm.ContentPart{textPart("Compacted the transcript so far.")}}))
	}
	turns = append(turns, assistantTurn(fluencyToolCall("communicate", string(message))))
	return turns
}

// countExact counts the elements of strs equal to want.
func countExact(strs []string, want string) int {
	n := 0
	for _, s := range strs {
		if s == want {
			n++
		}
	}
	return n
}

// writeEchoFixtureRun writes echoTurnsFixture(text, gap) as a root session's
// transcript under stateDir and returns its rendered packet and prose.
func writeEchoFixtureRun(t *testing.T, text string, gap bool) (packet string, prose runProse) {
	t.Helper()
	stateDir := t.TempDir()
	rootMeta(t, stateDir, proseRootID)
	writeFluencyTranscript(t, stateDir, proseRootID, echoTurnsFixture(text, gap))
	tr, err := runnerReadTranscript(stateDir, proseRootID, doctor.TranscriptOpts{TextMax: doctor.TextMaxFull})
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	packet = renderPacket(tr)
	prose, err = extractRunProse(stateDir)
	if err != nil {
		t.Fatalf("extractRunProse: %v", err)
	}
	return packet, prose
}

// TestSameLogicalTurnEchoIsSuppressed: the harness's "use communicate" nudge
// does not open a new logical turn (STEERING continues the open one), so a
// communicate call after it that repeats the assistant's own preceding bare
// text is the SAME echo evener itself never renders (EchoesAssistantText,
// scoped to the logical turn that showed the text). The packet must show the
// text once, not twice, and prose-stats' "all" channel must count it once.
// The suppressed call must still say the text above was delivered: a blind
// reader who only sees an empty turn where the communicate call was cannot
// tell the report was sent at all (issue found after 2df0f9eac6 landed).
// The suppressed call must also still count once in "to_user": the
// communicate call is what actually delivered the turn (the
// all-messages-go-through-the-result-tool contract), so to_user's message
// count, median words, and per-1k rates must not drop to zero for exactly
// the behavior those metrics exist to measure (issue found after
// 09ee7c01f0 landed).
func TestSameLogicalTurnEchoIsSuppressed(t *testing.T) {
	t.Parallel()
	const text = "Fixed the off-by-one bug in Sum(). Tests pass now."
	packet, prose := writeEchoFixtureRun(t, text, false)

	if got := strings.Count(packet, text); got != 1 {
		t.Errorf("packet shows the text %d times, want 1 (the echoed communicate call must not repeat it):\n%s", got, packet)
	}
	if !strings.Contains(packet, "⇒ communicate (sent the text above to the user)") {
		t.Errorf("packet does not say the suppressed communicate call still delivered the text above:\n%s", packet)
	}
	if got := countExact(prose.All, text); got != 1 {
		t.Errorf("prose.All has the text %d times, want 1 (the echo must not double count): %+v", got, prose.All)
	}
	if got := countExact(prose.ToUser, text); got != 1 {
		t.Errorf("prose.ToUser has the text %d times, want 1 (the echoed call still delivered the turn): %+v", got, prose.ToUser)
	}
}

// TestCrossLogicalTurnDuplicateIsKept: a compaction turn between the bare
// text and the later communicate call closes the logical turn that showed
// the text (SUMMARY is not a continuation), so the repeat is a genuine
// cross-turn message, not an echo — evener still renders it, and so must the
// packet and the prose count.
func TestCrossLogicalTurnDuplicateIsKept(t *testing.T) {
	t.Parallel()
	const text = "Fixed the off-by-one bug in Sum(). Tests pass now."
	packet, prose := writeEchoFixtureRun(t, text, true)

	if got := strings.Count(packet, text); got != 2 {
		t.Errorf("packet shows the text %d times, want 2 (a cross-turn repeat is genuine, not an echo):\n%s", got, packet)
	}
	if !strings.Contains(packet, "⇒ communicate") {
		t.Errorf("packet dropped the genuine cross-turn communicate call:\n%s", packet)
	}
	if got := countExact(prose.All, text); got != 2 {
		t.Errorf("prose.All has the text %d times, want 2 (a cross-turn repeat is genuine): %+v", got, prose.All)
	}
}

// TestEchoedCallStillCountsADistinctOutputMessage: an echoed communicate
// call is skipped in "all" only for the piece that echoes. A distinct
// output.message on that same call is not itself an echo (it never matched
// the assistant text) and must still reach "all" once.
func TestEchoedCallStillCountsADistinctOutputMessage(t *testing.T) {
	t.Parallel()
	const text = "Fixed the off-by-one bug in Sum(). Tests pass now."
	const outputText = "Structured summary: fixed the off-by-one in Sum()."
	stateDir := t.TempDir()
	rootMeta(t, stateDir, proseRootID)
	nudge := schema.NewTurn(schema.TurnSteering, llm.User(echoNudgeText))
	nudge.SteeringKind = events.SteeringKindNoToolCalls
	message, err := json.Marshal(map[string]any{
		"message":  text,
		"output":   map[string]any{"message": outputText},
		"end_turn": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Fix the tally bug and tell me what changed.")),
		assistantTurn(textPart(text)),
		nudge,
		assistantTurn(fluencyToolCall("communicate", string(message))),
	})

	prose, err := extractRunProse(stateDir)
	if err != nil {
		t.Fatalf("extractRunProse: %v", err)
	}
	if got := countExact(prose.All, text); got != 1 {
		t.Errorf("prose.All has the echoed text %d times, want 1: %+v", got, prose.All)
	}
	if got := countExact(prose.All, outputText); got != 1 {
		t.Errorf("prose.All has the distinct output.message %d times, want 1: %+v", got, prose.All)
	}
}
