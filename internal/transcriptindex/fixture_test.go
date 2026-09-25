package transcriptindex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// fixture is a transcript written line by line, so a test can carry what the
// writer never produces on purpose but real files contain: duplicate Seqs,
// blank lines and an unterminated tail.
type fixture struct {
	name   string
	header transcript.Header
	lines  []fixtureLine
}

// fixtureLine is one entry, or a blank line when blank is set. seq overrides
// the running sequence number when nonzero.
type fixtureLine struct {
	turn  schema.Turn
	seq   int
	blank bool
}

func entryLine(turn schema.Turn) fixtureLine { return fixtureLine{turn: turn} }

// encodeHeader and encodeEntry produce the exact bytes transcript.Writer does.
func encodeHeader(t testing.TB, header transcript.Header) []byte {
	t.Helper()
	header.Kind = "header"
	header.FormatVersion = transcript.FormatVersion
	data, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func encodeEntry(t testing.TB, seq int, turn schema.Turn) []byte {
	t.Helper()
	data, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: seq, Turn: turn, MachineryFlagged: true})
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func (fx fixture) encode(t testing.TB) (header []byte, lines [][]byte) {
	t.Helper()
	header = encodeHeader(t, fx.header)
	seq := 0
	for _, line := range fx.lines {
		if line.blank {
			lines = append(lines, []byte("\n"))
			continue
		}
		seq++
		if line.seq != 0 {
			seq = line.seq
		}
		lines = append(lines, encodeEntry(t, seq, line.turn))
	}
	return header, lines
}

func writeFixture(t testing.TB, fx fixture) string {
	t.Helper()
	header, lines := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(header, bytes.Join(lines, nil)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var fixtureClock = time.Unix(1_700_000_000, 0).UTC()

func at(turn schema.Turn, second int) schema.Turn {
	turn.Timestamp = fixtureClock.Add(time.Duration(second) * time.Second)
	return turn
}

func withUsage(turn schema.Turn, input, output, cacheRead, total int) schema.Turn {
	turn.Usage = llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total}
	if cacheRead != 0 {
		turn.Usage.CacheReadTokens = &cacheRead
	}
	return turn
}

func user(s string) schema.Turn { return standalone(schema.TurnUserInput, s) }

func assistant(parts ...llm.ContentPart) schema.Turn {
	return schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: parts}}
}

func results(parts ...llm.ContentPart) schema.Turn {
	return schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: parts}}
}

func text(s string) llm.ContentPart { return llm.ContentPart{Kind: llm.ContentText, Text: s} }

func thinking(s string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: s}}
}

func call(id, name, args string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: json.RawMessage(args)}}
}

func result(id, name, content string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: id, Name: name, Content: content}}
}

func standalone(kind schema.TurnKind, s string) schema.Turn { return schema.NewTurn(kind, llm.User(s)) }

func steering(s string) schema.Turn { return standalone(schema.TurnSteering, s) }

// pngBytes is a 1x1 PNG, enough for events.ToolResultOutputImage to describe.
var pngBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// fixtures is the corpus every index test runs over: each shape today's file
// projection handles, then all of them in one transcript.
func fixtures() []fixture {
	header := transcript.Header{SessionID: "th_fixture", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
	prelude := header
	prelude.SystemPrompt = "You are Evener."

	imageResult := result("c_img", "read_file", "image")
	imageResult.ToolResult.ImageData = pngBytes
	imageResult.ToolResult.ImageMediaType = "image/png"
	errorResult := result("c_err", "shell", "exit 1")
	errorResult.ToolResult.IsError = true
	errorResult.ToolResult.ToolState = json.RawMessage(`{"exit_code":1}`)

	basic := []fixtureLine{
		entryLine(at(user("read the image"), 1)),
		entryLine(withUsage(at(assistant(thinking("plan"), text("reading"), call("c_img", "read_file", `{"path":"a.png","intent":"look"}`), call("c_err", "shell", `{"cmd":"false"}`)), 2), 10, 5, 3, 15)),
		entryLine(at(results(imageResult, errorResult), 3)),
		entryLine(withUsage(at(assistant(text("done")), 4), 7, 2, 0, 9)),
	}

	communicate := []fixtureLine{
		entryLine(user("talk")),
		entryLine(assistant(text("hello"), call("k1", "communicate", `{"message":"hello"}`))),
		entryLine(results(result("k1", "communicate", "delivered"))),
		entryLine(assistant(call("k2", "communicate", `{"output":{"message":"a fresh message"}}`))),
		entryLine(results(result("k2", "", "delivered"))),
	}

	orphans := []fixtureLine{
		entryLine(user("orphans")),
		entryLine(assistant(call("o1", "read_file", `{}`), call("o2", "grep", `{}`))),
		entryLine(standalone(schema.TurnHookCompleted, "hook ran")),
		entryLine(results(result("o1", "read_file", "named orphan"), result("o2", "", "nameless orphan"))),
		entryLine(results(result("o3", "", "never called"))),
	}

	communicateNames := []fixtureLine{
		entryLine(user("names")),
		entryLine(assistant(call("n1", "communicate", `{"message":"hi"}`), call("n2", "read_file", `{}`))),
		entryLine(standalone(schema.TurnHookCompleted, "hook")),
		entryLine(results(result("n1", "", "nameless communicate result"), result("n2", "communicate", "renamed"))),
		entryLine(results(result("n2", "", "after the delete"))),
	}

	reused := []fixtureLine{
		entryLine(user("reuse")),
		entryLine(assistant(call("r1", "read_file", `{"path":"one"}`), call("r1", "read_file", `{"path":"two"}`))),
		entryLine(results(result("r1", "read_file", "first"))),
		entryLine(assistant(call("r1", "grep", `{"pattern":"x"}`))),
		entryLine(results(result("r1", "", "third contributor"), result("r1", "grep", "same entry twice"))),
		entryLine(results(result("r1", "", "fourth contributor"))),
	}

	interrupted := steering("stop")
	interrupted.SteeringKind = events.SteeringKindInterrupted
	ownedEarlier := steering("owned by an earlier turn")
	ownedEarlier.OwningTurnID = "turn_1"
	goal := schema.NewTurn(schema.TurnSteering, llm.User("continue"))
	goal.GoalContinuation = &schema.GoalContinuationInfo{Text: "keep going"}
	steerings := []fixtureLine{
		entryLine(user("steer me")),
		entryLine(steering("plain steering")),
		entryLine(assistant(text("ok"))),
		entryLine(interrupted),
		entryLine(ownedEarlier),
		entryLine(assistant(text("continuing the earlier turn"))),
		entryLine(goal),
		entryLine(assistant(text("goal work"))),
	}

	failedWithError := schema.NewTurn(schema.TurnFailure, llm.User(""))
	failedWithError.Error = &schema.TurnFailureInfo{Message: "provider exploded", Title: "Provider error"}
	failures := []fixtureLine{
		entryLine(user("fail")),
		entryLine(standalone(schema.TurnFailure, "first failure")),
		entryLine(assistant(text("retrying"))),
		entryLine(failedWithError),
		entryLine(user("fail quietly")),
		entryLine(standalone(schema.TurnFailure, "")),
	}

	standalones := []fixtureLine{
		entryLine(standalone(schema.TurnEnvironment, "env")),
		entryLine(standalone(schema.TurnModelSwitch, "switched to gpt-next")),
		entryLine(standalone(schema.TurnModelSwitch, "")),
		entryLine(standalone(schema.TurnCheckpoint, "checkpoint text")),
		entryLine(standalone(schema.TurnSummary, "summary text")),
		entryLine(standalone(schema.TurnNotesContext, "notes")),
		entryLine(standalone(schema.TurnHookCompleted, "")),
		entryLine(standalone(schema.TurnAttentionResolution, "resolved")),
		entryLine(user("after the markers")),
	}

	stable := user("client turn")
	stable.StableTurnID = "turn_m3"
	ordinals := []fixtureLine{
		{blank: true},
		entryLine(stable),
		{turn: assistant(text("seq 9390")), seq: 9390},
		{turn: assistant(text("seq 9391")), seq: 9391},
		{blank: true},
		{turn: results(result("x1", "read_file", "dup seq")), seq: 9390},
		{turn: assistant(text("dup again")), seq: 9391},
		entryLine(user("after duplicates")),
	}

	sets := []fixture{
		{name: "basic", header: prelude, lines: basic},
		{name: "communicate", header: header, lines: communicate},
		{name: "orphans", header: header, lines: orphans},
		{name: "communicate names", header: header, lines: communicateNames},
		{name: "reused call ids", header: header, lines: reused},
		{name: "steering", header: header, lines: steerings},
		{name: "failures", header: header, lines: failures},
		{name: "standalone kinds", header: header, lines: standalones},
		{name: "transcript only", header: header, lines: interleaveTranscriptOnly(append(append([]fixtureLine(nil), basic...), communicate...))},
	}
	sets = append(sets, newFormatFixtures(header, prelude)...)
	// Last: its final entry sits in the validated tail of "everything".
	sets = append(sets, fixture{name: "ordinals", header: header, lines: ordinals})
	var everything []fixtureLine
	for _, set := range sets {
		everything = append(everything, set.lines...)
	}
	return append(sets, fixture{name: "everything", header: prelude, lines: everything})
}

// interleaveTranscriptOnly puts a transcript-only entry before, between and
// after lines, the way a phase 2 writer records them among the entries today's
// projection reads.
func interleaveTranscriptOnly(lines []fixtureLine) []fixtureLine {
	turns := make([]schema.Turn, len(lines))
	for i, line := range lines {
		turns[i] = line.turn
	}
	out := make([]fixtureLine, 0, 2*len(lines)+1)
	for _, turn := range schematest.InterleaveTranscriptOnly(turns) {
		out = append(out, entryLine(turn))
	}
	return out
}

// inTurn stamps turn as a new-format entry of turn turnID: the identity format
// marker and TurnID every writer placement stamps.
func inTurn(turnID string, turn schema.Turn) schema.Turn {
	turn.Format = schema.TurnFormatIdentity
	turn.TurnID = turnID
	return turn
}

// opens is inTurn for a turn's first entry, which also records the turn's kind.
func opens(turnID string, kind schema.TurnSpanKind, turn schema.Turn) schema.Turn {
	turn = inTurn(turnID, turn)
	turn.TurnKind = kind
	return turn
}

func transcriptOnly(kind schema.TurnKind) schema.Turn {
	return schema.Turn{Kind: kind, Message: llm.Message{Role: llm.RoleUser}}
}

func completion(turnID string, status schema.TurnCompletionStatus, second int, durationMS int64) schema.Turn {
	turn := at(transcriptOnly(schema.TurnCompletion), second)
	turn.Completion = &schema.TurnCompletionInfo{Status: status, CompletedAt: turn.Timestamp, DurationMS: durationMS}
	return inTurn(turnID, turn)
}

func reopen(turnID string) schema.Turn { return inTurn(turnID, transcriptOnly(schema.TurnReopen)) }

func communicated(turnID, callID, message string) schema.Turn {
	turn := transcriptOnly(schema.TurnCommunicate)
	turn.Communicate = &schema.CommunicateInfo{CallID: callID, EndTurn: true, Message: message}
	return inTurn(turnID, turn)
}

func noticed(info schema.NoticeInfo) schema.Turn {
	turn := transcriptOnly(schema.TurnNotice)
	turn.Notice = &info
	return turn
}

func withModel(turn schema.Turn, model string) schema.Turn {
	turn.Model = model
	return turn
}

func inRound(turn schema.Turn, roundID string) schema.Turn {
	turn.RoundID = roundID
	return turn
}

// foldCopy is the copy of turn a compaction fold re-appends after its markers.
func foldCopy(turn schema.Turn, original uint64) schema.Turn {
	turn.OriginalOrdinal = &original
	return turn
}

// newFormatFixtures are transcripts whose entries carry persisted turn
// identity. Turn ids are unique across them, since "everything" joins them.
func newFormatFixtures(header, prelude transcript.Header) []fixture {
	execution, gap, delivery := schema.TurnSpanExecution, schema.TurnSpanGap, schema.TurnSpanDelivery

	session := []fixtureLine{
		entryLine(opens(appwire.SystemPreludeTurnID, schema.TurnSpanPrelude, at(standalone(schema.TurnEnvironment, "prelude environment"), 1))),
		entryLine(inTurn(appwire.SystemPreludeTurnID, standalone(schema.TurnNotesContext, "prelude notes"))),
		entryLine(opens("turn_m10", execution, withModel(at(user("new format"), 2), "gpt-test"))),
		entryLine(inTurn("turn_m10", withModel(withUsage(at(inRound(assistant(
			text("a"), text("b"), thinking("plan"),
			call("f1", "read_file", `{"path":"x","intent":"read x"}`),
			call("fk1", "communicate", `{"message":"told"}`),
		), "r_10"), 3), 11, 4, 2, 15), "gpt-test"))),
		entryLine(at(communicated("turn_m10", "fk1", "told"), 4)),
		// Both results are nameless: the call names them, and the
		// communicate call's result shows nothing.
		entryLine(inTurn("turn_m10", at(results(result("f1", "", "file body"), result("fk1", "", "delivered")), 4))),
		entryLine(inTurn("turn_m10", withModel(withUsage(at(inRound(assistant(text("done")), "r_11"), 5), 3, 2, 0, 5), "gpt-next"))),
		entryLine(completion("turn_m10", schema.TurnCompleted, 6, 4000)),
	}

	gaps := []fixtureLine{
		entryLine(opens("t_gap11", gap, at(standalone(schema.TurnHookCompleted, "first hook"), 10))),
		entryLine(inTurn("t_gap11", standalone(schema.TurnHookCompleted, "second hook"))),
		entryLine(inTurn("t_gap11", withModel(standalone(schema.TurnModelSwitch, "switched to gpt-next"), "gpt-next"))),
		entryLine(opens("turn_m11", execution, user("between the gaps"))),
		entryLine(completion("turn_m11", schema.TurnCompleted, 11, 5)),
		entryLine(opens("t_gap11b", gap, standalone(schema.TurnHookCompleted, "a later gap"))),
	}

	deliveries := []fixtureLine{
		entryLine(opens("turn_m12", execution, user("deliver"))),
		entryLine(inTurn("turn_m12", inRound(assistant(text("working"), call("d1", "shell", `{"cmd":"ls"}`)), "r_12"))),
		entryLine(opens("t_delivery12", delivery, steering("a delivered note"))),
		entryLine(inTurn("turn_m12", results(result("d1", "shell", "listing"), result("zz", "", "no such call"), result("zy", "grep", "named, no such call")))),
		entryLine(inTurn("turn_m12", assistant(text("after the delivery")))),
		entryLine(completion("turn_m12", schema.TurnCompleted, 20, 1500)),
	}

	failure := schema.NewTurn(schema.TurnFailure, llm.User(""))
	failure.Error = &schema.TurnFailureInfo{Message: "model refused", Title: "Provider error"}
	failedRetry := []fixtureLine{
		entryLine(opens("turn_m13", execution, user("try"))),
		entryLine(inTurn("turn_m13", assistant(text("trying")))),
		entryLine(inTurn("turn_m13", failure)),
		entryLine(completion("turn_m13", schema.TurnFailed, 30, 900)),
		entryLine(opens("turn_m14", execution, user("try again"))),
		entryLine(inTurn("turn_m14", assistant(text("succeeded")))),
		entryLine(completion("turn_m14", schema.TurnCompleted, 31, 800)),
	}

	reclaimed := []fixtureLine{
		entryLine(opens("turn_m15", execution, at(user("crash here"), 40))),
		entryLine(inTurn("turn_m15", withUsage(at(assistant(text("partial")), 41), 5, 5, 0, 10))),
		// Recovery reclaims the open turn and runs it again.
		entryLine(reopen("turn_m15")),
		entryLine(inTurn("turn_m15", at(assistant(text("reclaimed work")), 43))),
		entryLine(completion("turn_m15", schema.TurnCompleted, 45, 5000)),
		// A failed turn recovery reclaims after other work.
		entryLine(opens("turn_m16", execution, user("fail first"))),
		entryLine(inTurn("turn_m16", standalone(schema.TurnFailure, "first run failed"))),
		entryLine(completion("turn_m16", schema.TurnFailed, 46, 100)),
		entryLine(opens("t_gap16", gap, standalone(schema.TurnHookCompleted, "between the runs"))),
		entryLine(reopen("turn_m16")),
		entryLine(inTurn("turn_m16", assistant(text("second run")))),
		entryLine(completion("turn_m16", schema.TurnCompleted, 48, 2500)),
	}

	legacyPrefix := []fixtureLine{
		entryLine(user("legacy question")),
		entryLine(assistant(text("legacy answer"), call("lg1", "read_file", `{}`))),
		entryLine(results(result("lg1", "read_file", "legacy result"))),
		entryLine(opens("turn_m18", execution, user("new question"))),
		entryLine(inTurn("turn_m18", assistant(text("new answer")))),
		entryLine(completion("turn_m18", schema.TurnCompleted, 50, 10)),
	}

	question := inTurn("turn_m19", user("fold me"))
	answer := inTurn("turn_m19", withUsage(assistant(text("original answer"), call("fc1", "read_file", `{}`)), 2, 2, 0, 4))
	answered := inTurn("turn_m19", results(result("fc1", "read_file", "original result")))
	question.TurnKind = execution
	folds := []fixtureLine{
		entryLine(question),
		entryLine(answer),
		entryLine(answered),
		entryLine(completion("turn_m19", schema.TurnCompleted, 52, 10)),
		entryLine(opens("t_gap19", gap, standalone(schema.TurnCheckpoint, "checkpoint"))),
		entryLine(foldCopy(question, 0)),
		entryLine(foldCopy(answer, 1)),
		entryLine(foldCopy(answered, 2)),
		entryLine(inTurn("t_gap19", standalone(schema.TurnHookCompleted, "after the fold"))),
	}

	echoes := []fixtureLine{
		entryLine(opens("turn_m20", execution, user("say it"))),
		entryLine(inTurn("turn_m20", assistant(text("same "), text("words"), call("ek1", "communicate", `{"message":"same words"}`)))),
		entryLine(communicated("turn_m20", "ek1", " same words ")),
		entryLine(inTurn("turn_m20", results(result("ek1", "communicate", "delivered")))),
		entryLine(inTurn("turn_m20", assistant(text("more"), call("ek2", "communicate", `{"message":"other words"}`)))),
		entryLine(communicated("turn_m20", "ek2", "other words")),
		entryLine(inTurn("turn_m20", results(result("ek2", "", "delivered")))),
		entryLine(completion("turn_m20", schema.TurnCompleted, 60, 10)),
	}

	notices := []fixtureLine{
		entryLine(opens("t_gap21", gap, noticed(schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{ToolName: "read_file", CallID: "c9", Changes: []string{"renamed argument"}}}))),
		entryLine(inTurn("t_gap21", noticed(schema.NoticeInfo{Kind: schema.NoticeGoalEnded, GoalEnded: &schema.GoalEndedNotice{Status: "complete", Iterations: 2}}))),
		entryLine(inTurn("t_gap21", noticed(schema.NoticeInfo{Kind: schema.NoticeTurnLimit, TurnLimit: &schema.TurnLimitNotice{MaxToolRoundsPerInput: 3}}))),
		entryLine(opens("turn_m21", execution, user("notice inside"))),
		entryLine(inTurn("turn_m21", noticed(schema.NoticeInfo{Kind: schema.NoticeSkillActivated, SkillActivated: &schema.SkillActivatedNotice{Name: "a-skill"}}))),
		entryLine(completion("turn_m21", schema.TurnCompleted, 70, 10)),
	}

	interruptedCall := []fixtureLine{
		entryLine(opens("turn_m22", execution, user("crash mid-call"))),
		entryLine(inTurn("turn_m22", inRound(assistant(text("calling"), call("ic1", "shell", `{"cmd":"sleep"}`), call("ic2", "read_file", `{}`)), "r_22"))),
		entryLine(inTurn("turn_m22", results(result("ic2", "read_file", "partial results")))),
		// Resume after the crash records the open execution's interrupted
		// completion.
		entryLine(completion("turn_m22", schema.TurnInterrupted, 80, 10)),
	}

	return []fixture{
		{name: "new format session", header: prelude, lines: session},
		{name: "interrupted call", header: header, lines: interruptedCall},
		{name: "gap turns", header: header, lines: gaps},
		{name: "delivery inside an execution", header: header, lines: deliveries},
		{name: "failed execution and retry", header: header, lines: failedRetry},
		{name: "open and reclaimed", header: header, lines: reclaimed},
		{name: "legacy prefix", header: header, lines: legacyPrefix},
		{name: "fold copies", header: header, lines: folds},
		{name: "communicate echoes", header: header, lines: echoes},
		{name: "notices", header: header, lines: notices},
	}
}

// legacy reports whether every entry of the fixture is a legacy entry.
func (fx fixture) legacy() bool {
	for _, line := range fx.lines {
		if line.turn.Format != 0 {
			return false
		}
	}
	return true
}
