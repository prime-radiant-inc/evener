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
	"primeradiant.com/evener/agent/transcript"
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
		{name: "ordinals", header: header, lines: ordinals},
	}
	var everything []fixtureLine
	for _, set := range sets {
		everything = append(everything, set.lines...)
	}
	return append(sets, fixture{name: "everything", header: prelude, lines: everything})
}
