package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// This file holds the memory harness's post-exit transcript verifier and the
// always-running tests for it. The verifier is a pure function of the recorded
// transcript plus an expectation authored independently by the harness (from
// the same pure script the adapter answers from), so it needs no daemon and
// can be exercised with hand-authored entries.
//
// It runs after the serve daemon exits, outside every CPU/heap sample: reading
// and decoding the whole transcript, then the full output journal of every
// windowed shell result, is verification work, not measurement work.

// The verifier's check names. They are the structured identities the negative
// tests assert, so a mismatch can never be mistaken for a different one.
const (
	memHarnessCheckHeader             = "header"
	memHarnessCheckInputMissing       = "input_missing"
	memHarnessCheckInputDuplicate     = "input_duplicate"
	memHarnessCheckInputTurnID        = "input_turn_id"
	memHarnessCheckInputText          = "input_text"
	memHarnessCheckInputOrder         = "input_order"
	memHarnessCheckInputTextDuplicate = "input_text_duplicate"
	memHarnessCheckCompletionMissing  = "completion_missing"
	memHarnessCheckCompletionMultiple = "completion_duplicate"
	memHarnessCheckCompletionStatus   = "completion_status"
	memHarnessCheckCompletionOrder    = "completion_order"
	memHarnessCheckToolRounds         = "tool_rounds"
	memHarnessCheckToolMissing        = "tool_missing"
	memHarnessCheckToolName           = "tool_name"
	memHarnessCheckToolError          = "tool_error"
	memHarnessCheckToolOutput         = "tool_output"
	memHarnessCheckToolOutputMiddle   = "tool_output_middle"
	memHarnessCheckToolOutputFull     = "tool_output_full"
	memHarnessCheckFinalMessage       = "final_message"
	memHarnessCheckAssistantMissing   = "assistant_missing"
	memHarnessCheckProtocol           = "protocol"
	memHarnessCheckProvider           = "provider"
	memHarnessCheckModel              = "model"
)

// memHarnessExpectedRound is one scripted model round of a turn and what the
// transcript must hold for it.
type memHarnessExpectedRound struct {
	Tool string // "shell" or "delegate"
	// ShellOutput is the complete command output the script asked for. For a
	// windowed result it is what the durable job log must hold byte for byte.
	ShellOutput []byte
}

// memHarnessExpectedTurn is one input the harness authored, in order.
type memHarnessExpectedTurn struct {
	ClientMutationID string
	InputText        string
	// TurnID is the root turn identity the daemon's receipt named. Empty reads
	// it back from the recorded input entry (the queued path learns it late),
	// but a test can pin it to reject a wrong identity.
	TurnID       string
	Rounds       []memHarnessExpectedRound
	FinalMessage string
}

// memHarnessTranscriptExpectation is what the harness authored for one run.
type memHarnessTranscriptExpectation struct {
	// StateDir and SessionID locate the durable job output journal beside the
	// transcript (sessions/<id>/jobs/<job>.log).
	StateDir  string
	SessionID string
	Turns     []memHarnessExpectedTurn
	// Protocol is the exact ResponseProtocol every recorded assistant turn must
	// carry. The modern variant requires the value the adapter noted; the
	// legacy variant requires it to be empty while provider and model stay.
	Protocol string
	Provider string
	Model    string
	// RequireUniqueInputText holds for a fresh root: no two recorded user
	// inputs may share an expected text. A resumed transcript already holds
	// earlier runs' inputs, so resume relaxes this to this run's ids.
	RequireUniqueInputText bool
	// TolerateExtraToolRounds relaxes the per-turn tool-result count for the
	// delegate lifecycle, where a delegate delivery can add rounds of its own.
	TolerateExtraToolRounds bool
}

// memHarnessVerifyFailure is the verifier's structured evidence: which check
// failed, for which turn/mutation/round, and what was expected against what was
// recorded. Negative tests assert these fields rather than copy.
type memHarnessVerifyFailure struct {
	Check            string
	TurnID           string
	ClientMutationID string
	Round            int
	Want             string
	Got              string
}

func (e *memHarnessVerifyFailure) Error() string {
	var b strings.Builder
	b.WriteString("memory harness transcript verification failed: ")
	b.WriteString(e.Check)
	if e.TurnID != "" {
		fmt.Fprintf(&b, " turn %s", e.TurnID)
	}
	if e.ClientMutationID != "" {
		fmt.Fprintf(&b, " input %s", e.ClientMutationID)
	}
	if e.Round >= 0 {
		fmt.Fprintf(&b, " round %d", e.Round)
	}
	if e.Want != "" || e.Got != "" {
		fmt.Fprintf(&b, ": want %s, got %s", e.Want, e.Got)
	}
	return b.String()
}

func memHarnessFail(check, turnID, mutationID string, round int, want, got string) error {
	return &memHarnessVerifyFailure{Check: check, TurnID: turnID, ClientMutationID: mutationID, Round: round, Want: want, Got: got}
}

type memHarnessInputRecord struct {
	Seq              int
	TurnID           string
	ClientMutationID string
	Text             string
}

type memHarnessToolEntry struct {
	Seq   int
	Parts []llm.ToolResultData
}

type memHarnessAssistantRecord struct {
	Seq      int
	Protocol string
	Provider string
	Model    string
}

type memHarnessTranscriptView struct {
	header       transcript.Header
	inputs       []memHarnessInputRecord
	inputTexts   map[string]int
	completions  map[string][]memHarnessCompletion
	tools        map[string][]memHarnessToolEntry
	assistants   map[string][]memHarnessAssistantRecord
	communicates map[string][]string
}

type memHarnessCompletion struct {
	Seq    int
	Status schema.TurnCompletionStatus
}

func (v *memHarnessTranscriptView) add(entry transcript.Entry) {
	turn := entry.Turn
	switch turn.Kind {
	case schema.TurnUserInput:
		v.inputs = append(v.inputs, memHarnessInputRecord{Seq: entry.Seq, TurnID: turn.TurnID, ClientMutationID: turn.ClientMutationID, Text: turn.Message.Text()})
		v.inputTexts[turn.Message.Text()]++
	case schema.TurnCompletion:
		if turn.Completion != nil {
			v.completions[turn.TurnID] = append(v.completions[turn.TurnID], memHarnessCompletion{Seq: entry.Seq, Status: turn.Completion.Status})
		}
	case schema.TurnToolResults:
		parts := make([]llm.ToolResultData, 0, len(turn.Message.Content))
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil {
				parts = append(parts, *part.ToolResult)
			}
		}
		v.tools[turn.TurnID] = append(v.tools[turn.TurnID], memHarnessToolEntry{Seq: entry.Seq, Parts: parts})
	case schema.TurnAssistant:
		v.assistants[turn.TurnID] = append(v.assistants[turn.TurnID], memHarnessAssistantRecord{Seq: entry.Seq, Protocol: turn.ResponseProtocol, Provider: turn.ResponseProvider, Model: turn.ResponseModel})
	case schema.TurnCommunicate:
		if turn.Communicate != nil {
			v.communicates[turn.TurnID] = append(v.communicates[turn.TurnID], turn.Communicate.Message)
		}
	}
}

// memHarnessToolState is the persisted structured shell result the verifier
// reads to find a windowed result's durable job log.
type memHarnessToolState struct {
	JobID string `json:"job_id"`
}

// memHarnessVerifyTranscript decodes the whole transcript at path and checks it
// against want. It returns a *memHarnessVerifyFailure naming the first mismatch.
func memHarnessVerifyTranscript(ctx context.Context, path string, want memHarnessTranscriptExpectation) error {
	f, err := os.Open(path)
	if err != nil {
		return memHarnessFail(memHarnessCheckHeader, "", "", -1, "readable transcript", err.Error())
	}
	defer f.Close()
	view := memHarnessTranscriptView{
		inputTexts:   make(map[string]int),
		completions:  make(map[string][]memHarnessCompletion),
		tools:        make(map[string][]memHarnessToolEntry),
		assistants:   make(map[string][]memHarnessAssistantRecord),
		communicates: make(map[string][]string),
	}
	result, err := transcript.Scan(ctx, f, 0, func(_ []byte, entry transcript.Entry) error {
		view.add(entry)
		return nil
	})
	if err != nil {
		return memHarnessFail(memHarnessCheckHeader, "", "", -1, "decodable transcript", err.Error())
	}
	view.header = result.Header
	if view.header.SessionID != want.SessionID {
		return memHarnessFail(memHarnessCheckHeader, "", "", -1, want.SessionID, view.header.SessionID)
	}

	previousSeq := -1
	for _, expected := range want.Turns {
		turn := memHarnessTurnInputs(view.inputs, expected.ClientMutationID)
		switch len(turn) {
		case 0:
			return memHarnessFail(memHarnessCheckInputMissing, expected.TurnID, expected.ClientMutationID, -1, "one recorded input", "none")
		case 1:
		default:
			return memHarnessFail(memHarnessCheckInputDuplicate, expected.TurnID, expected.ClientMutationID, -1, "one recorded input", fmt.Sprintf("%d", len(turn)))
		}
		input := turn[0]
		turnID := expected.TurnID
		if turnID == "" {
			turnID = input.TurnID
		}
		if input.TurnID != turnID {
			return memHarnessFail(memHarnessCheckInputTurnID, turnID, expected.ClientMutationID, -1, turnID, input.TurnID)
		}
		if input.Text != expected.InputText {
			return memHarnessFail(memHarnessCheckInputText, turnID, expected.ClientMutationID, -1, expected.InputText, input.Text)
		}
		if input.Seq <= previousSeq {
			return memHarnessFail(memHarnessCheckInputOrder, turnID, expected.ClientMutationID, -1, "entry after the previous expected input", fmt.Sprintf("entry %d", input.Seq))
		}
		previousSeq = input.Seq
		if want.RequireUniqueInputText && view.inputTexts[expected.InputText] != 1 {
			return memHarnessFail(memHarnessCheckInputTextDuplicate, turnID, expected.ClientMutationID, -1, "one recorded input with this text", fmt.Sprintf("%d", view.inputTexts[expected.InputText]))
		}

		if err := memHarnessVerifyCompletion(view, turnID, input.Seq, expected.ClientMutationID); err != nil {
			return err
		}
		if err := memHarnessVerifyToolRounds(view, want, turnID, expected); err != nil {
			return err
		}
		if !slices.Contains(view.communicates[turnID], expected.FinalMessage) {
			return memHarnessFail(memHarnessCheckFinalMessage, turnID, expected.ClientMutationID, -1, expected.FinalMessage, strings.Join(view.communicates[turnID], " | "))
		}
		if err := memHarnessVerifyProvenance(view, want, turnID, expected.ClientMutationID); err != nil {
			return err
		}
	}
	return nil
}

func memHarnessTurnInputs(inputs []memHarnessInputRecord, mutationID string) []memHarnessInputRecord {
	var found []memHarnessInputRecord
	for _, input := range inputs {
		if input.ClientMutationID == mutationID {
			found = append(found, input)
		}
	}
	return found
}

func memHarnessVerifyCompletion(view memHarnessTranscriptView, turnID string, inputSeq int, mutationID string) error {
	completions := view.completions[turnID]
	switch len(completions) {
	case 0:
		return memHarnessFail(memHarnessCheckCompletionMissing, turnID, mutationID, -1, string(schema.TurnCompleted), "none")
	case 1:
	default:
		return memHarnessFail(memHarnessCheckCompletionMultiple, turnID, mutationID, -1, "one completion", fmt.Sprintf("%d", len(completions)))
	}
	completion := completions[0]
	if completion.Seq <= inputSeq {
		return memHarnessFail(memHarnessCheckCompletionOrder, turnID, mutationID, -1, "completion after its input", fmt.Sprintf("entry %d", completion.Seq))
	}
	if completion.Status != schema.TurnCompleted {
		return memHarnessFail(memHarnessCheckCompletionStatus, turnID, mutationID, -1, string(schema.TurnCompleted), string(completion.Status))
	}
	return nil
}

func memHarnessVerifyToolRounds(view memHarnessTranscriptView, want memHarnessTranscriptExpectation, turnID string, expected memHarnessExpectedTurn) error {
	entries := view.tools[turnID]
	// The scripted turn ends with communicate, whose accepted result is itself
	// one more TOOL_RESULTS entry.
	wantEntries := len(expected.Rounds) + 1
	if want.TolerateExtraToolRounds {
		if len(entries) < wantEntries {
			return memHarnessFail(memHarnessCheckToolRounds, turnID, expected.ClientMutationID, -1, fmt.Sprintf("at least %d tool-result entries", wantEntries), fmt.Sprintf("%d", len(entries)))
		}
	} else if len(entries) != wantEntries {
		return memHarnessFail(memHarnessCheckToolRounds, turnID, expected.ClientMutationID, -1, fmt.Sprintf("%d tool-result entries", wantEntries), fmt.Sprintf("%d", len(entries)))
	}
	for round, expectedRound := range expected.Rounds {
		if round >= len(entries) {
			return memHarnessFail(memHarnessCheckToolMissing, turnID, expected.ClientMutationID, round, expectedRound.Tool+" result", "none")
		}
		parts := entries[round].Parts
		if len(parts) != 1 {
			return memHarnessFail(memHarnessCheckToolRounds, turnID, expected.ClientMutationID, round, "one tool result part", fmt.Sprintf("%d parts", len(parts)))
		}
		part := parts[0]
		if part.Name != expectedRound.Tool {
			return memHarnessFail(memHarnessCheckToolName, turnID, expected.ClientMutationID, round, expectedRound.Tool, part.Name)
		}
		if part.IsError {
			content, _ := part.Content.(string)
			return memHarnessFail(memHarnessCheckToolError, turnID, expected.ClientMutationID, round, "a successful tool result", content)
		}
		content, ok := part.Content.(string)
		if !ok {
			return memHarnessFail(memHarnessCheckToolOutput, turnID, expected.ClientMutationID, round, "string tool output", fmt.Sprintf("%T", part.Content))
		}
		switch expectedRound.Tool {
		case "delegate":
			if strings.TrimSpace(content) == "" {
				return memHarnessFail(memHarnessCheckToolOutput, turnID, expected.ClientMutationID, round, "a delegate report", "empty")
			}
		case "shell":
			if err := memHarnessVerifyShellOutput(want, turnID, expected.ClientMutationID, round, expectedRound.ShellOutput, content, part.ToolState); err != nil {
				return err
			}
		}
	}
	return nil
}

func memHarnessVerifyShellOutput(want memHarnessTranscriptExpectation, turnID, mutationID string, round int, expected []byte, content string, rawState json.RawMessage) error {
	var state memHarnessToolState
	if len(rawState) > 0 {
		if err := json.Unmarshal(rawState, &state); err != nil {
			return memHarnessFail(memHarnessCheckToolOutput, turnID, mutationID, round, "decodable tool_state", err.Error())
		}
	}
	// The middle is what a head/tail digest throws away: every path below
	// proves the recorded bytes still hold it.
	middle := memHarnessMiddleBytes(expected)
	if state.JobID == "" {
		if !strings.Contains(content, string(middle)) {
			return memHarnessFail(memHarnessCheckToolOutputMiddle, turnID, mutationID, round, fmt.Sprintf("inline output holding %q", middle), memHarnessClip(content))
		}
		if !strings.HasPrefix(content, string(expected)) {
			return memHarnessFail(memHarnessCheckToolOutput, turnID, mutationID, round, fmt.Sprintf("inline output starting with %d recorded bytes", len(expected)), memHarnessClip(content))
		}
		return nil
	}
	path := filepath.Join(schema.SessionWorkDir(want.StateDir, want.SessionID), "jobs", state.JobID+".log")
	full, err := os.ReadFile(path)
	if err != nil {
		return memHarnessFail(memHarnessCheckToolOutputFull, turnID, mutationID, round, "readable job output journal", err.Error())
	}
	if !bytes.Equal(full, expected) {
		return memHarnessFail(memHarnessCheckToolOutputFull, turnID, mutationID, round, fmt.Sprintf("%d recorded output bytes", len(expected)), fmt.Sprintf("%d job-log bytes (first difference at byte %d)", len(full), memHarnessFirstDifference(full, expected)))
	}
	// A windowed result's inline form is a head/tail digest; the journal above
	// holds every byte. The digest must be shorter than the output it names,
	// or the result kept the whole thing (or a copy of it) inline.
	if len(content) >= len(expected) {
		return memHarnessFail(memHarnessCheckToolOutputMiddle, turnID, mutationID, round, fmt.Sprintf("an inline digest shorter than %d bytes", len(expected)), fmt.Sprintf("%d inline bytes", len(content)))
	}
	head, tail := memHarnessDigestEdges(expected)
	if !strings.Contains(content, head) || !strings.Contains(content, tail) {
		return memHarnessFail(memHarnessCheckToolOutput, turnID, mutationID, round, "digest edges from the recorded output", memHarnessClip(content))
	}
	return nil
}

func memHarnessVerifyProvenance(view memHarnessTranscriptView, want memHarnessTranscriptExpectation, turnID, mutationID string) error {
	assistants := view.assistants[turnID]
	if len(assistants) == 0 {
		return memHarnessFail(memHarnessCheckAssistantMissing, turnID, mutationID, -1, "recorded assistant turns", "none")
	}
	for _, assistant := range assistants {
		if assistant.Protocol != want.Protocol {
			return memHarnessFail(memHarnessCheckProtocol, turnID, mutationID, -1, fmt.Sprintf("%q", want.Protocol), fmt.Sprintf("%q", assistant.Protocol))
		}
		if assistant.Provider != want.Provider {
			return memHarnessFail(memHarnessCheckProvider, turnID, mutationID, -1, want.Provider, assistant.Provider)
		}
		if assistant.Model != want.Model {
			return memHarnessFail(memHarnessCheckModel, turnID, mutationID, -1, want.Model, assistant.Model)
		}
	}
	return nil
}

func memHarnessMiddleBytes(b []byte) []byte {
	if len(b) <= 64 {
		return b
	}
	mid := len(b) / 2
	return b[mid-32 : mid+32]
}

func memHarnessDigestEdges(b []byte) (head, tail string) {
	half := 256
	if len(b) < half*2 {
		half = len(b) / 2
	}
	return string(b[:half]), string(b[len(b)-half:])
}

func memHarnessFirstDifference(got, want []byte) int {
	n := min(len(got), len(want))
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			return i
		}
	}
	return n
}

func memHarnessClip(s string) string {
	const limit = 160
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// The adapter and the harness's expectation both answer from the same authored
// script, so pin the script itself literally: changing its formula must fail
// here rather than move both sides of the verification together.
func TestMemHarnessScriptMatchesAuthoredTable(t *testing.T) {
	for _, want := range []struct {
		turn, round int
		command     string
		outputBytes int
	}{
		{1, 0, "head -c 30000 /dev/zero | tr '\\0' 'x' | fold -w 100", 30_000},
		{1, 1, "head -c 8000 /dev/zero | tr '\\0' 'x' | fold -w 100", 8_000},
		{3, 4, "head -c 16000 /dev/zero | tr '\\0' 'x' | fold -w 100", 16_000},
		{4, 4, "head -c 2000 /dev/zero | tr '\\0' 'x' | fold -w 100", 2_000},
	} {
		script := memHarnessRoundScriptFor(want.turn, want.round)
		if script.Command != want.command || script.OutputBytes != want.outputBytes {
			t.Fatalf("script(%d,%d) = %q/%d, want %q/%d", want.turn, want.round, script.Command, script.OutputBytes, want.command, want.outputBytes)
		}
	}
	if got, want := memHarnessInputText(3), "turn 3: please do more work"; got != want {
		t.Fatalf("input text = %q, want %q", got, want)
	}
	if got, want := memHarnessShellCallID(2, 1), "call_t2_r1"; got != want {
		t.Fatalf("shell call id = %q, want %q", got, want)
	}
	if got, want := memHarnessDelegateCallID(2), "call_t2_delegate"; got != want {
		t.Fatalf("delegate call id = %q, want %q", got, want)
	}
	output := memHarnessShellOutput(2_000)
	if len(output) != 2_019 || output[len(output)-1] != 'x' {
		t.Fatalf("shell output = %d bytes ending %q, want 2019 ending 'x'", len(output), output[len(output)-1])
	}
}

// memHarnessFixtureTurn builds one hand-authored recorded turn set for the
// verifier's always-running tests.
type memHarnessFixture struct {
	t          *testing.T
	dir        string
	sessionID  string
	entries    []transcript.Entry
	expect     memHarnessTranscriptExpectation
	jobLogs    map[string][]byte
	mutationID string
	turnID     string
	shellSmall []byte
	shellLarge []byte
}

func newMemHarnessFixture(t *testing.T, dir string) *memHarnessFixture {
	f := &memHarnessFixture{
		t:          t,
		dir:        dir,
		sessionID:  "memharness-session",
		mutationID: "memharness-run-start-1",
		turnID:     "turn_m1",
		jobLogs:    map[string][]byte{},
	}
	f.shellSmall = memHarnessShellOutput(120)
	f.shellLarge = memHarnessShellOutput(4096)
	return f
}

// build records two shell rounds plus communicate, with provenance, completion
// and a windowed second round whose journal holds the full output.
func (f *memHarnessFixture) build() {
	inputText := "turn 1: please do more work"
	finalMessage := "done with turn 1. explanation explanation"
	f.entries = []transcript.Entry{
		memHarnessFixtureEntry(1, schema.Turn{
			Kind: schema.TurnUserInput, TurnID: f.turnID, StableTurnID: f.turnID,
			ClientMutationID: f.mutationID, Message: llm.User(inputText),
		}),
		memHarnessFixtureAssistant(2, f.turnID, "openai-responses"),
		memHarnessFixtureToolResults(3, f.turnID, llm.ToolResultData{
			ToolCallID: "call_t1_r0", Name: "shell", Content: string(f.shellSmall) + "\n[exit 0]",
		}),
		memHarnessFixtureAssistant(4, f.turnID, "openai-responses"),
		memHarnessFixtureToolResults(5, f.turnID, llm.ToolResultData{
			ToolCallID: "call_t1_r1", Name: "shell",
			Content:   string(f.shellLarge[:256]) + "\n…[3.5 KB elided — read more with read_transcript]…\n" + string(f.shellLarge[len(f.shellLarge)-256:]),
			ToolState: json.RawMessage(`{"job_id":"job_fixture","output_status":"windowed","total_bytes":4096}`),
		}),
		memHarnessFixtureAssistant(6, f.turnID, "openai-responses"),
		memHarnessFixtureToolResults(7, f.turnID, llm.ToolResultData{
			ToolCallID: "communicate_fixture", Name: "communicate", Content: `{"accepted":true}`,
		}),
		{Kind: "entry", MachineryFlagged: true, Seq: 8, Turn: schema.Turn{
			Kind: schema.TurnCommunicate, TurnID: f.turnID,
			Communicate: &schema.CommunicateInfo{CallID: "communicate_fixture", EndTurn: true, Message: finalMessage},
		}},
		{Kind: "entry", MachineryFlagged: true, Seq: 9, Turn: schema.Turn{
			Kind: schema.TurnCompletion, TurnID: f.turnID,
			Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted},
		}},
	}
	f.jobLogs["job_fixture"] = f.shellLarge
	f.expect = memHarnessTranscriptExpectation{
		StateDir:               f.dir,
		SessionID:              f.sessionID,
		Protocol:               "openai-responses",
		Provider:               "openai",
		Model:                  "gpt-5.2",
		RequireUniqueInputText: true,
		Turns: []memHarnessExpectedTurn{{
			ClientMutationID: f.mutationID, InputText: inputText, TurnID: f.turnID,
			Rounds: []memHarnessExpectedRound{
				{Tool: "shell", ShellOutput: f.shellSmall},
				{Tool: "shell", ShellOutput: f.shellLarge},
			},
			FinalMessage: finalMessage,
		}},
	}
}

func memHarnessFixtureEntry(seq int, turn schema.Turn) transcript.Entry {
	return transcript.Entry{Kind: "entry", MachineryFlagged: true, Seq: seq, Turn: turn}
}

func memHarnessFixtureAssistant(seq int, turnID, protocol string) transcript.Entry {
	return memHarnessFixtureEntry(seq, schema.Turn{
		Kind: schema.TurnAssistant, TurnID: turnID,
		ResponseProtocol: protocol, ResponseProvider: "openai", ResponseModel: "gpt-5.2",
		Message: llm.Assistant("working"),
	})
}

func memHarnessFixtureToolResults(seq int, turnID string, results ...llm.ToolResultData) transcript.Entry {
	parts := make([]llm.ContentPart, 0, len(results))
	for i := range results {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &results[i]})
	}
	return memHarnessFixtureEntry(seq, schema.Turn{
		Kind: schema.TurnToolResults, TurnID: turnID,
		Message: llm.Message{Role: llm.RoleTool, Content: parts},
	})
}

func (f *memHarnessFixture) write() string {
	header, err := json.Marshal(transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: f.sessionID})
	if err != nil {
		f.t.Fatalf("marshal header: %v", err)
	}
	var b bytes.Buffer
	b.Write(header)
	b.WriteByte('\n')
	for _, entry := range f.entries {
		line, err := json.Marshal(entry)
		if err != nil {
			f.t.Fatalf("marshal entry %d: %v", entry.Seq, err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	path := filepath.Join(f.dir, f.sessionID+".transcript.jsonl")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		f.t.Fatalf("write transcript: %v", err)
	}
	for jobID, output := range f.jobLogs {
		jobDir := filepath.Join(f.dir, "sessions", f.sessionID, "jobs")
		if err := os.MkdirAll(jobDir, 0o755); err != nil {
			f.t.Fatalf("make job dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(jobDir, jobID+".log"), output, 0o644); err != nil {
			f.t.Fatalf("write job log: %v", err)
		}
	}
	return path
}

func (f *memHarnessFixture) verify(path string) error {
	return memHarnessVerifyTranscript(context.Background(), path, f.expect)
}

// TestMemHarnessVerifierAcceptsAuthoredTranscript pins that a correct
// hand-authored transcript, including a windowed result whose journal holds the
// full output, passes.
func TestMemHarnessVerifierAcceptsAuthoredTranscript(t *testing.T) {
	f := newMemHarnessFixture(t, t.TempDir())
	f.build()
	if err := f.verify(f.write()); err != nil {
		t.Fatalf("correct transcript rejected: %v", err)
	}
}

// The queued path is chosen from the typed conflict, not its message: product
// copy is not a protocol, and a copy-only error is not that refusal.
func TestMemHarnessTurnActiveUsesTypedConflict(t *testing.T) {
	if !memHarnessTurnActive(appwire.Conflict("turn is already active")) {
		t.Fatal("typed active-turn conflict not recognized")
	}
	if memHarnessTurnActive(errors.New("turn is already active")) {
		t.Fatal("copy alone recognized as the active-turn conflict")
	}
	if memHarnessTurnActive(appwire.InvalidParams("turn is already active")) {
		t.Fatal("non-conflict wire error recognized as the active-turn conflict")
	}
}

// TestMemHarnessTranscriptMutationTurnFindsQueuedInput pins the queued path's
// correlation: a turn/queue receipt names no turn id, so the harness reads the
// turn id the transcript recorded for that client mutation.
func TestMemHarnessTranscriptMutationTurnFindsQueuedInput(t *testing.T) {
	f := newMemHarnessFixture(t, t.TempDir())
	f.build()
	path := f.write()
	turnID, ok, err := memHarnessTranscriptMutationTurn(path, f.mutationID)
	if err != nil || !ok || turnID != f.turnID {
		t.Fatalf("mutation turn = %q, %v; want %q, true", turnID, ok, f.turnID)
	}
	if turnID, ok, err := memHarnessTranscriptMutationTurn(path, "memharness-run-start-9"); err != nil || ok {
		t.Fatalf("absent mutation named turn %q", turnID)
	}
}

// TestMemHarnessRequestPositionIgnoresDelegateNotification pins that a
// delegate report delivered mid-turn, whose metadata embeds the child's
// "delegated unit: turn 1: ..." task, cannot rewind the adapter to the child's
// turn ordinal.
func TestMemHarnessRequestPositionIgnoresDelegateNotification(t *testing.T) {
	notification := `<delegate-notification delegate_id="dlg_1" kind="report">{"kind":"reported","message":"done","metadata":{"task":"delegated unit: turn 1: please do more work context"}}</delegate-notification>`
	req := llm.Request{Messages: []llm.Message{
		llm.User(memHarnessInputText(2)),
		llm.User(notification),
		llm.Assistant("running a command"),
	}}
	turn, rounds := memHarnessRequestPosition(req)
	if turn != 2 || rounds != 1 {
		t.Fatalf("position = (%d, %d), want (2, 1)", turn, rounds)
	}
	child := llm.Request{Messages: []llm.Message{llm.User("delegated unit: turn 5: please do more work context")}}
	if turn, rounds := memHarnessRequestPosition(child); turn != 5 || rounds != 0 {
		t.Fatalf("child position = (%d, %d), want (5, 0)", turn, rounds)
	}
}

// TestMemHarnessVerifierAcceptsLegacyMissingProtocol pins the legacy variant:
// an empty ResponseProtocol is accepted only when the expectation says so, and
// provider/model identity must still be recorded.
func TestMemHarnessVerifierAcceptsLegacyMissingProtocol(t *testing.T) {
	f := newMemHarnessFixture(t, t.TempDir())
	f.build()
	f.expect.Protocol = ""
	for i := range f.entries {
		if f.entries[i].Turn.Kind == schema.TurnAssistant {
			f.entries[i].Turn.ResponseProtocol = ""
		}
	}
	if err := f.verify(f.write()); err != nil {
		t.Fatalf("legacy transcript rejected: %v", err)
	}
}

// TestMemHarnessVerifierRejectsAuthorshipMismatches is the negative table. Each
// case mutates one authored input, completion, tool result or provenance field
// and asserts the verifier's structured failure identifies that exact mismatch.
func TestMemHarnessVerifierRejectsAuthorshipMismatches(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(f *memHarnessFixture)
		check    string
		round    int
		want     string
		gotHint  string
		gotEmpty bool
	}{
		{
			name: "omitted input",
			mutate: func(f *memHarnessFixture) {
				f.entries = f.entries[1:]
			},
			check: memHarnessCheckInputMissing,
			round: -1,
		},
		{
			name: "duplicated input",
			mutate: func(f *memHarnessFixture) {
				duplicate := f.entries[0]
				duplicate.Seq = 100
				f.entries = append(f.entries, duplicate)
			},
			check: memHarnessCheckInputDuplicate,
			round: -1,
		},
		{
			name: "wrong turn id",
			mutate: func(f *memHarnessFixture) {
				f.entries[0].Turn.TurnID = "turn_m9"
			},
			check:   memHarnessCheckInputTurnID,
			round:   -1,
			want:    "turn_m1",
			gotHint: "turn_m9",
		},
		{
			name: "failed completion",
			mutate: func(f *memHarnessFixture) {
				f.entries[8].Turn.Completion.Status = schema.TurnFailed
			},
			check:   memHarnessCheckCompletionStatus,
			round:   -1,
			want:    string(schema.TurnCompleted),
			gotHint: string(schema.TurnFailed),
		},
		{
			name: "missing completion",
			mutate: func(f *memHarnessFixture) {
				f.entries = append(f.entries[:8], f.entries[9:]...)
			},
			check: memHarnessCheckCompletionMissing,
			round: -1,
		},
		{
			name: "interrupted completion",
			mutate: func(f *memHarnessFixture) {
				f.entries[8].Turn.Completion.Status = schema.TurnInterrupted
			},
			check:   memHarnessCheckCompletionStatus,
			round:   -1,
			want:    string(schema.TurnCompleted),
			gotHint: string(schema.TurnInterrupted),
		},
		{
			name: "duplicated completion",
			mutate: func(f *memHarnessFixture) {
				duplicate := f.entries[8]
				duplicate.Seq = 100
				f.entries = append(f.entries, duplicate)
			},
			check: memHarnessCheckCompletionMultiple,
			round: -1,
		},
		{
			name: "wrong inline output",
			mutate: func(f *memHarnessFixture) {
				content := f.entries[2].Turn.Message.Content[0].ToolResult.Content.(string)
				f.entries[2].Turn.Message.Content[0].ToolResult.Content = "y" + content[1:]
			},
			check: memHarnessCheckToolOutput,
			round: 0,
		},
		{
			name: "inline output missing its middle",
			mutate: func(f *memHarnessFixture) {
				f.entries[2].Turn.Message.Content[0].ToolResult.Content = string(f.shellSmall[:32]) + string(f.shellSmall[len(f.shellSmall)-32:])
			},
			check: memHarnessCheckToolOutputMiddle,
			round: 0,
		},
		{
			name: "windowed journal missing its middle",
			mutate: func(f *memHarnessFixture) {
				f.jobLogs["job_fixture"] = append(append([]byte{}, f.shellLarge[:256]...), f.shellLarge[len(f.shellLarge)-256:]...)
			},
			check: memHarnessCheckToolOutputFull,
			round: 1,
			want:  fmt.Sprintf("%d recorded output bytes", len(memHarnessShellOutput(4096))),
		},
		{
			name: "missing protocol identity",
			mutate: func(f *memHarnessFixture) {
				f.entries[1].Turn.ResponseProtocol = ""
			},
			check:   memHarnessCheckProtocol,
			round:   -1,
			want:    `"openai-responses"`,
			gotHint: `""`,
		},
		{
			name: "missing provider identity",
			mutate: func(f *memHarnessFixture) {
				f.entries[1].Turn.ResponseProvider = ""
			},
			check:    memHarnessCheckProvider,
			round:    -1,
			want:     "openai",
			gotEmpty: true,
		},
		{
			name: "wrong final message",
			mutate: func(f *memHarnessFixture) {
				f.entries[7].Turn.Communicate.Message = "done with turn 2."
			},
			check: memHarnessCheckFinalMessage,
			round: -1,
		},
		{
			name: "missing a tool round",
			mutate: func(f *memHarnessFixture) {
				f.entries = append(f.entries[:2], f.entries[3:]...)
			},
			check: memHarnessCheckToolRounds,
			round: -1,
			want:  "3 tool-result entries",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newMemHarnessFixture(t, t.TempDir())
			f.build()
			tc.mutate(f)
			err := f.verify(f.write())
			if err == nil {
				t.Fatal("verifier accepted a transcript it must reject")
			}
			failure, ok := err.(*memHarnessVerifyFailure)
			if !ok {
				t.Fatalf("failure not structured: %T %v", err, err)
			}
			if failure.Check != tc.check {
				t.Fatalf("check = %q, want %q (failure: %v)", failure.Check, tc.check, failure)
			}
			if failure.Round != tc.round {
				t.Fatalf("round = %d, want %d (failure: %v)", failure.Round, tc.round, failure)
			}
			if failure.ClientMutationID != f.mutationID {
				t.Fatalf("client mutation id = %q, want %q", failure.ClientMutationID, f.mutationID)
			}
			if tc.want != "" && failure.Want != tc.want {
				t.Fatalf("want evidence = %q, want %q (failure: %v)", failure.Want, tc.want, failure)
			}
			if tc.gotHint != "" && !strings.Contains(failure.Got, tc.gotHint) {
				t.Fatalf("got evidence %q missing %q (failure: %v)", failure.Got, tc.gotHint, failure)
			}
			if tc.gotEmpty && failure.Got != "" {
				t.Fatalf("got evidence = %q, want empty (failure: %v)", failure.Got, failure)
			}
		})
	}
}
