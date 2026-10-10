package doctor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/internal/turnwindow"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

var openDoctorTranscriptFile = func(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

// transcriptDoc is the semantic transcript header and conversation entries.
type transcriptDoc struct {
	Header  transcript.Header
	Entries []transcript.Entry
}

func loadTranscript(path string) (transcriptDoc, error) {
	return loadTranscriptWithMaxLineBytes(path, transcript.DefaultMaxLineBytes)
}

func loadTranscriptWithMaxLineBytes(path string, maxLineBytes int) (transcriptDoc, error) {
	file, err := openDoctorTranscriptFile(path)
	if err != nil {
		return transcriptDoc{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	defer file.Close() //nolint:errcheck // read-only file
	reader := bufio.NewReaderSize(file, 64*1024)
	var doc transcriptDoc
	headerRead := false
	lineNumber := 0
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, maxLineBytes)
		if readErr != nil {
			return transcriptDoc{}, fmt.Errorf("read transcript line %d: %w", lineNumber+1, readErr)
		}
		if !complete {
			break
		}
		lineNumber++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			doc.Header, err = transcript.DecodeHeader(line)
			if err != nil {
				return transcriptDoc{}, fmt.Errorf("parse transcript header line %d: %w", lineNumber, err)
			}
			headerRead = true
			continue
		}
		e, err := transcript.DecodeEntry(line)
		if err != nil {
			return transcriptDoc{}, fmt.Errorf("parse transcript entry line %d: %w", lineNumber, err)
		}
		doc.Entries = append(doc.Entries, e)
	}
	if !headerRead {
		return transcriptDoc{}, fmt.Errorf("%w: missing transcript header", transcript.ErrUnsupportedFormat)
	}
	return doc, nil
}

// CountResult is the structural tool-invocation count plus the assistant-prose
// mention count, kept separate so a tool named in text is never conflated with
// a tool actually called.
type CountResult struct {
	SessionID             string `json:"session_id"`
	Tool                  string `json:"tool"`
	Calls                 int    `json:"calls"`
	MentionsAssistantText int    `json:"mentions_assistant_text"`
}

// Count returns how many times a tool was structurally invoked in a session
// (a content part of kind tool_call whose ToolCall.Name matches), distinct from
// how many times the tool name merely appears in assistant prose.
func Count(stateBase, selector, tool string) (CountResult, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return CountResult{}, err
	}
	doc, err := loadTranscript(paths.TranscriptPath)
	if err != nil {
		return CountResult{}, err
	}
	res := CountResult{SessionID: paths.SessionID, Tool: tool}
	for _, e := range doc.Entries {
		for _, part := range e.Turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.Name == tool {
				res.Calls++
			}
			if e.Turn.Kind == schema.TurnAssistant && part.Kind == llm.ContentText {
				res.MentionsAssistantText += strings.Count(part.Text, tool)
			}
		}
	}
	return res, nil
}

// RenderCount renders the structural count with the mention disambiguation —
// the exact answer the delegate_send "5 vs 0" confusion needed.
func RenderCount(r CountResult) string {
	calls := "calls"
	if r.Calls == 1 {
		calls = "call"
	}
	out := fmt.Sprintf("%s: %d %s", r.Tool, r.Calls, calls)
	if r.MentionsAssistantText > 0 {
		out += fmt.Sprintf("  (%d textual mention(s) in assistant text — not invocations)",
			r.MentionsAssistantText)
	}
	return out
}

// ToolCallSummary is one tool call in a turn.
type ToolCallSummary struct {
	Name string `json:"name"`
	// Arguments is the full raw JSON arguments object, untruncated. It is
	// the machine-readable form callers needing exact argument values must
	// use; ArgPreview remains the human-facing bounded rendering. For a call
	// whose arguments were not valid JSON, this is the model's raw text,
	// carried in the record's raw_arguments field.
	Arguments  string `json:"arguments,omitempty"`
	ArgPreview string `json:"arg_preview,omitempty"`
	Intent     string `json:"intent,omitempty"`    // the model's stated reason for this call, if any
	IsResult   bool   `json:"is_result,omitempty"` // the session's effective result tool
}

type ToolResultSummary struct {
	Name           string `json:"name,omitempty"`
	ContentPreview string `json:"content_preview,omitempty"`
	IsError        bool   `json:"is_error,omitempty"`
}

// TurnSummary is the structural view of one transcript entry.
type TurnSummary struct {
	// Turn is the entry's turn number in read_transcript's coordinates, so a
	// number read here selects the same turn there. Absent on entries
	// read_transcript omits (attention resolutions and transcript-only kinds),
	// which render after the turn they follow.
	Turn        *int                `json:"turn,omitempty"`
	Kind        string              `json:"kind"`
	Role        string              `json:"role,omitempty"`
	ToolCalls   []ToolCallSummary   `json:"tool_calls,omitempty"`
	ToolResults []ToolResultSummary `json:"tool_results,omitempty"`
	Text        string              `json:"text,omitempty"`
	// SteeringSource carries a steering turn's provenance so a caller can tell
	// whether the person spoke or the harness did. Empty on non-steering turns.
	SteeringSource string `json:"steering_source,omitempty"`
}

// TranscriptResult is the rendered transcript with an honest elision footer:
// turns_rendered + elided == turns_total always holds. The counts are of
// numbered turns; Turns also holds the unnumbered entries inside the window.
type TranscriptResult struct {
	SessionID     string        `json:"session_id"`
	ResultTool    string        `json:"result_tool"`
	TurnsTotal    int           `json:"turns_total"`
	TurnsRendered int           `json:"turns_rendered"`
	Elided        int           `json:"elided"`
	Turns         []TurnSummary `json:"turns"`
}

// TranscriptOpts narrows a transcript render.
type TranscriptOpts struct {
	Format string // "outline" | "markdown" (default markdown)
	// Range is a window of read_transcript turn numbers: "last:N" |
	// "start:N" | "N-M". Empty renders the whole transcript.
	Range string
	// TextMax is the byte cap on each turn's rendered text and on each
	// tool-result preview. Zero or less selects DefaultTextMax; TextMaxFull
	// renders in full.
	TextMax int
}

const argPreviewMax = 80

// DefaultTextMax is the byte cap on a rendered turn's text and on each
// tool-result preview when a render does not ask for another. It keeps the
// default output summary-sized: a transcript cannot be dumped by accident.
const DefaultTextMax = 200

// TextMaxFull, as TranscriptOpts.TextMax, renders turn text and tool-result
// previews whole. Repetition *inside* one response is only ever visible this
// way: a salvaged partial response (agent/salvage.go) carries its repeated tool
// calls as turn text, not as tool-call parts, so no structural tool-call metric
// counts them and any cap hides the run.
const TextMaxFull = math.MaxInt

// Transcript renders a session's logical turns (a turn map / conversation view),
// applying the range window and reporting elision honestly.
func Transcript(stateBase, selector string, opts TranscriptOpts) (TranscriptResult, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return TranscriptResult{}, err
	}
	doc, err := loadTranscript(paths.TranscriptPath)
	if err != nil {
		return TranscriptResult{}, err
	}
	resultTool := resolveResultTool(paths)

	// turnStarts[n] is the entry index of read_transcript's Turn n.
	var turnStarts []int
	for i, e := range doc.Entries {
		if e.Turn.Kind.PublicTranscript() {
			turnStarts = append(turnStarts, i)
		}
	}
	total := len(turnStarts)
	first, last := 0, total-1
	if opts.Range != "" {
		first, last, err = turnwindow.Parse(opts.Range, total)
		if err != nil {
			return TranscriptResult{}, fmt.Errorf("invalid range %q; accepted: %s", opts.Range, turnwindow.Grammar)
		}
	}
	// The entry window runs from the first selected turn to just before the
	// turn after the last, so unnumbered entries ride with the turn they
	// follow. Entries before Turn 0 belong to a window that starts there.
	lo, hi, rendered := 0, len(doc.Entries), 0
	if last >= first {
		rendered = last - first + 1
		if first > 0 {
			lo = turnStarts[first]
		}
		if last+1 < total {
			hi = turnStarts[last+1]
		}
	} else if opts.Range != "" {
		hi = 0
	}
	res := TranscriptResult{
		SessionID:     paths.SessionID,
		ResultTool:    resultTool,
		TurnsTotal:    total,
		TurnsRendered: rendered,
		Elided:        total - rendered,
	}
	textMax := opts.TextMax
	if textMax <= 0 {
		textMax = DefaultTextMax
	}
	turn := first
	for i := lo; i < hi; i++ {
		ts := summarizeTurn(doc.Entries[i], resultTool, textMax)
		if doc.Entries[i].Turn.Kind.PublicTranscript() {
			n := turn
			ts.Turn = &n
			turn++
		}
		res.Turns = append(res.Turns, ts)
	}
	return res, nil
}

func summarizeTurn(e transcript.Entry, resultTool string, textMax int) TurnSummary {
	ts := TurnSummary{
		Kind:           string(e.Turn.Kind),
		Role:           string(e.Turn.Message.Role),
		SteeringSource: e.Turn.SteeringSource,
	}
	var text strings.Builder
	for _, part := range e.Turn.Message.Content {
		switch part.Kind {
		case llm.ContentText:
			text.WriteString(part.Text)
		case llm.ContentToolCall:
			if part.ToolCall == nil {
				continue
			}
			arguments := part.ToolCall.SentArguments()
			var intent string
			// A rejected call (raw arguments set) shows the model's raw bytes
			// and skips the intent lookup: its recorded arguments are the {}
			// placeholder, which can only parse to an empty intent anyway.
			if part.ToolCall.RawArguments == "" {
				arguments = strings.TrimSpace(arguments)
				intent = toolIntentFromArguments(part.ToolCall.Arguments)
			}
			ts.ToolCalls = append(ts.ToolCalls, ToolCallSummary{
				Name:       part.ToolCall.Name,
				Arguments:  arguments,
				ArgPreview: Truncate(arguments, argPreviewMax),
				Intent:     intent,
				IsResult:   part.ToolCall.Name == resultTool,
			})
		case llm.ContentToolResult:
			if part.ToolResult == nil {
				continue
			}
			ts.ToolResults = append(ts.ToolResults, ToolResultSummary{
				Name:           part.ToolResult.Name,
				ContentPreview: Truncate(toolResultContentText(part.ToolResult.Content), textMax),
				IsError:        part.ToolResult.IsError,
			})
		}
	}
	ts.Text = Truncate(strings.TrimSpace(text.String()), textMax)
	return ts
}

// toolIntentFromArguments returns a tool call's stated "intent" argument, or
// "" if none is present. Falls back to "purpose" -- the field's name before
// the 2026-08-29 rename (7512a736e) -- so a transcript recorded before that
// rename still shows its intent line when read back (issue #709). Doctor
// imports agent/schema and agent/transcript but not the agent package
// itself (toolIntent's home) or internal/apptranscript, so this mirrors --
// rather than shares -- the same reader-side rule applied independently in
// each of those packages.
func toolIntentFromArguments(raw json.RawMessage) string {
	var args map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
		return ""
	}
	if v, ok := args["intent"].(string); ok {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	if v, ok := args["purpose"].(string); ok {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func toolResultContentText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, err := json.Marshal(v)
		if err == nil {
			return string(b)
		}
		return fmt.Sprint(v)
	}
}

// resolveResultTool reads the session's effective result-tool name from meta
// (Config.ResultToolName, else "communicate"), mirroring effectiveResultToolName.
func resolveResultTool(paths Paths) string {
	meta, err := schema.LoadSessionMeta(paths.BucketDir, paths.SessionID)
	if err == nil && meta.Config.ResultToolName != "" {
		return meta.Config.ResultToolName
	}
	return "communicate"
}

// RenderTranscript renders a TranscriptResult as an outline (turn map) or a
// markdown conversation view, ending with the elision footer.
func RenderTranscript(r TranscriptResult, format string) string {
	var b strings.Builder
	for _, t := range r.Turns {
		if format == "outline" {
			fmt.Fprintf(&b, "[%s] %s", turnLabel(t), t.Kind)
			if names := toolCallNames(t.ToolCalls); names != "" {
				fmt.Fprintf(&b, "  tools: %s", names)
			}
			if names := toolResultNames(t.ToolResults); names != "" {
				fmt.Fprintf(&b, "  results: %s", names)
			}
			if t.Text != "" {
				fmt.Fprintf(&b, "  %q", oneLine(t.Text))
			}
			b.WriteString("\n")
			continue
		}
		// markdown
		fmt.Fprintf(&b, "### [%s] %s\n", turnLabel(t), t.Kind)
		if t.Text != "" {
			fmt.Fprintf(&b, "%s\n", t.Text)
		}
		for _, tc := range t.ToolCalls {
			label := "→ " + tc.Name
			if tc.IsResult {
				label = "⇒ " + tc.Name + " (result)"
			}
			if tc.Intent != "" {
				label += " — intent: " + tc.Intent
			}
			fmt.Fprintf(&b, "%s `%s`\n", label, oneLine(tc.ArgPreview))
		}
		for _, tr := range t.ToolResults {
			label := "← " + tr.Name
			if tr.IsError {
				label += " (error)"
			}
			fmt.Fprintf(&b, "%s `%s`\n", label, oneLine(tr.ContentPreview))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "— turns_total=%d turns_rendered=%d elided=%d (session %s, result_tool=%s)\n",
		r.TurnsTotal, r.TurnsRendered, r.Elided, r.SessionID, r.ResultTool)
	return b.String()
}

// turnLabel is a row's read_transcript turn number, or "-" for an entry
// read_transcript omits.
func turnLabel(t TurnSummary) string {
	if t.Turn == nil {
		return "-"
	}
	return strconv.Itoa(*t.Turn)
}

func toolCallNames(tcs []ToolCallSummary) string {
	if len(tcs) == 0 {
		return ""
	}
	names := make([]string, len(tcs))
	for i, tc := range tcs {
		names[i] = tc.Name
	}
	return strings.Join(names, ", ")
}

func toolResultNames(trs []ToolResultSummary) string {
	if len(trs) == 0 {
		return ""
	}
	names := make([]string, len(trs))
	for i, tr := range trs {
		name := tr.Name
		if name == "" {
			name = "<unnamed>"
		}
		if tr.IsError {
			name += "(error)"
		}
		names[i] = name
	}
	return strings.Join(names, ", ")
}

// Truncate caps s at maxLen bytes plus an ellipsis. The cap is a byte budget,
// but the cut backs off to the previous rune boundary so a valid input never
// yields invalid UTF-8: the kept prefix is at most maxLen bytes, never more.
// It never trims: a caller that wants tidy edges trims before calling, so the
// full-text tool-result path can report the result's bytes exactly.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return runetrim.Cut(s, maxLen) + "…"
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
}
