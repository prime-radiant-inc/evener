package doctor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/internal/tool/wirenames"
	"primeradiant.com/evener/agent/internal/turnwindow"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// sessionRowTranscriptTailBytes is the first tail window a sessions row reads
// from a transcript longer than it. The row consumes only the header and the
// last assistant turn's entry, so one window holds it for nearly every
// transcript; when it does not, the window doubles until it does or the row
// falls back to the full scan, so the outcome is never guessed from a window
// that merely ran out. A transcript at or under the bound is read whole by
// the full scan, which keeps the row identical on every small transcript.
// Production never assigns it; a test shrinks it to prove the bound.
var sessionRowTranscriptTailBytes int64 = 1 << 20

var openDoctorTranscriptFile = func(path string) (io.ReadSeekCloser, error) {
	return os.Open(path)
}

// transcriptDoc is the semantic transcript header and conversation entries.
type transcriptDoc struct {
	Header  transcript.Header
	Entries []transcript.Entry
}

func loadTranscript(path string) (transcriptDoc, error) {
	return loadTranscriptContext(context.Background(), path)
}

// loadTranscriptContext is loadTranscript that stops decoding, with ctx's
// error, once ctx is cancelled.
func loadTranscriptContext(ctx context.Context, path string) (transcriptDoc, error) {
	return scanTranscriptContext(ctx, path, false)
}

// loadRecordedTranscript loads a session's recorded turns, omitting copies a
// compaction fold re-appended after its markers (OriginalOrdinal != nil) whose
// originals already appear earlier in the transcript. Callers that need
// physical or read_transcript entry coordinates (Transcript, ScanTurnIDs) use
// loadTranscript instead.
func loadRecordedTranscript(path string) (transcriptDoc, error) {
	return loadRecordedTranscriptContext(context.Background(), path)
}

func loadRecordedTranscriptContext(ctx context.Context, path string) (transcriptDoc, error) {
	return scanTranscriptContext(ctx, path, true)
}

// loadSessionRowTranscript reads only what a sessions row consumes: the
// header, and the last assistant turn's entry — the outcome hint's input,
// which reports "none" only when the transcript holds no complete assistant
// record. A transcript at or under sessionRowTranscriptTailBytes goes through
// the full scan unchanged, so a small transcript keeps its exact summary and
// error shape. A longer transcript is read in a tail window that doubles
// until it holds an assistant record or reaches the transcript's start, so
// the full scan's cost returns only for a transcript with no assistant record
// to find. In every window, compaction fold copies are skipped and an
// unterminated final tail is tolerated, exactly as the full scan does; a
// corrupt complete line inside the window still fails the read, so the
// unreadable-session contract holds within the window.
func loadSessionRowTranscript(ctx context.Context, path string) (transcriptDoc, error) {
	file, err := openDoctorTranscriptFile(path)
	if err != nil {
		return transcriptDoc{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	defer file.Close() //nolint:errcheck // read-only file
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return transcriptDoc{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	if size <= sessionRowTranscriptTailBytes {
		return loadRecordedTranscriptContext(ctx, path)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return transcriptDoc{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	header, err := transcript.ReadHeader(ctx, reader, transcript.DefaultMaxLineBytes)
	if err != nil {
		return transcriptDoc{}, err
	}
	doc := transcriptDoc{Header: header}
	for window := sessionRowTranscriptTailBytes; window < size; window *= 2 {
		// A record that starts inside the window is kept even when the
		// window begins exactly at its boundary; only the tail of a record
		// that began before the window is discarded, without decoding.
		if _, err := file.Seek(size-window-1, io.SeekStart); err != nil {
			return transcriptDoc{}, fmt.Errorf("read transcript %s: %w", path, err)
		}
		preceding := make([]byte, 1)
		if _, err := io.ReadFull(file, preceding); err != nil {
			return transcriptDoc{}, err
		}
		reader.Reset(file)
		if preceding[0] != '\n' {
			if _, _, _, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes); err != nil {
				return transcriptDoc{}, err
			}
		}
		for {
			if err := ctx.Err(); err != nil {
				return transcriptDoc{}, err
			}
			line, complete, _, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
			if err != nil {
				return transcriptDoc{}, err
			}
			if !complete {
				break // end of file, or a torn trailing write: not a record
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			entry, err := transcript.DecodeEntry(line)
			if err != nil {
				return transcriptDoc{}, err
			}
			if entry.Turn.OriginalOrdinal != nil {
				continue // compaction fold copy: the original appears earlier
			}
			// The row's only entries consumer is the outcome hint, which
			// reads the last assistant turn; retaining just that entry
			// keeps the window's decoded cost from piling up per session.
			if entry.Turn.Kind == schema.TurnAssistant {
				doc.Entries = []transcript.Entry{entry}
			}
		}
		if len(doc.Entries) > 0 {
			return doc, nil
		}
	}
	// No window reached an assistant record: read the whole transcript
	// through the full scan so the outcome is the true one, never a guess
	// from a window that ran out.
	return loadRecordedTranscriptContext(ctx, path)
}

func scanTranscriptContext(ctx context.Context, path string, skipFoldCopies bool) (transcriptDoc, error) {
	file, err := openDoctorTranscriptFile(path)
	if err != nil {
		return transcriptDoc{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	defer file.Close() //nolint:errcheck // read-only file
	var doc transcriptDoc
	result, err := transcript.Scan(ctx, file, transcript.DefaultMaxLineBytes, func(_ []byte, entry transcript.Entry) error {
		if skipFoldCopies && entry.Turn.OriginalOrdinal != nil {
			return nil
		}
		doc.Entries = append(doc.Entries, entry)
		return nil
	})
	if err != nil {
		return transcriptDoc{}, err
	}
	doc.Header = result.Header
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
	doc, err := loadRecordedTranscript(paths.TranscriptPath)
	if err != nil {
		return CountResult{}, err
	}
	resultTool := resolveResultTool(paths)
	resolver := newSessionToolResolver(doc.Entries, resultTool)
	target := tool
	mentionNames := []string{tool}
	if tool != resultTool {
		target = wirenames.Canonical(tool)
		aliases := wirenames.NamesOf(target)
		if slices.Contains(aliases, tool) {
			mentionNames = make([]string, 0, len(aliases))
			for _, a := range aliases {
				if a != resultTool {
					mentionNames = append(mentionNames, a)
				}
			}
		}
	}
	res := CountResult{SessionID: paths.SessionID, Tool: tool}
	// Exact-first (#6241): when some call in this session resolved to the
	// queried name literally — a distinct custom or rejected tool holding a
	// wire alias — a query by that name counts only that tool, exactly as
	// TranscriptHealth and metricSource.hasExactTool prefer the exact key.
	// Only a session with no exact match falls back to alias matching.
	exactResolved := tool != resultTool && sessionHasExactResolvedTool(doc.Entries, resolver, tool)
	for _, e := range doc.Entries {
		for _, part := range e.Turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
				resolved := resolver.callName(part.ToolCall)
				if tool == resultTool {
					if resolved == resultTool {
						res.Calls++
					}
				} else if exactResolved {
					if resolved == tool {
						res.Calls++
					}
				} else if resolved == target || (resolved == tool && resolved != resultTool) {
					res.Calls++
				}
			}
			if e.Turn.Kind == schema.TurnAssistant && part.Kind == llm.ContentText {
				res.MentionsAssistantText += countToolMentions(part.Text, mentionNames)
			}
		}
	}
	return res, nil
}

type sessionToolResolver struct {
	resultTool string
	callByID   map[string]string
	resultByID map[string]string
}

// sessionHasExactResolvedTool reports whether any tool call in entries
// resolved to name literally, under the same resolver Count counts with.
func sessionHasExactResolvedTool(entries []transcript.Entry, resolver sessionToolResolver, name string) bool {
	for _, e := range entries {
		for _, part := range e.Turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil && resolver.callName(part.ToolCall) == name {
				return true
			}
		}
	}
	return false
}

func newSessionToolResolver(entries []transcript.Entry, resultTool string) sessionToolResolver {
	r := sessionToolResolver{
		resultTool: resultTool,
		callByID:   make(map[string]string),
		resultByID: make(map[string]string),
	}
	for _, e := range entries {
		for _, part := range e.Turn.Message.Content {
			switch part.Kind {
			case llm.ContentToolCall:
				if part.ToolCall != nil && part.ToolCall.ID != "" && part.ToolCall.Name != "" {
					if _, exists := r.callByID[part.ToolCall.ID]; !exists {
						r.callByID[part.ToolCall.ID] = part.ToolCall.Name
					}
				}
			case llm.ContentToolResult:
				if part.ToolResult != nil && part.ToolResult.ToolCallID != "" && part.ToolResult.Name != "" {
					if _, exists := r.resultByID[part.ToolResult.ToolCallID]; !exists {
						r.resultByID[part.ToolResult.ToolCallID] = part.ToolResult.Name
					}
				}
			}
		}
	}
	return r
}

func (r sessionToolResolver) canonicalize(name string) string {
	if name == r.resultTool {
		return name
	}
	return wirenames.Canonical(name)
}

func (r sessionToolResolver) callName(tc *llm.ToolCallData) string {
	if tc.Name == r.resultTool {
		return tc.Name
	}
	if resName := r.resultByID[tc.ID]; resName != "" {
		if resName == tc.Name {
			return resName
		}
		return r.canonicalize(resName)
	}
	return r.canonicalize(tc.Name)
}

func (r sessionToolResolver) resultName(tr *llm.ToolResultData) string {
	if tr.Name == r.resultTool {
		return tr.Name
	}
	if callName := r.callByID[tr.ToolCallID]; callName != "" && callName == tr.Name {
		return tr.Name
	}
	return r.canonicalize(tr.Name)
}

func countToolMentions(text string, names []string) int {
	var candidates []string
	for _, n := range names {
		if n != "" {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 0 || text == "" {
		return 0
	}
	slices.SortFunc(candidates, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	count := 0
	for i := 0; i < len(text); {
		matchedLen := 0
		for _, cand := range candidates {
			if strings.HasPrefix(text[i:], cand) {
				matchedLen = len(cand)
				break
			}
		}
		if matchedLen > 0 {
			count++
			i += matchedLen
		} else {
			i++
		}
	}
	return count
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
	SessionID     string `json:"session_id"`
	ResultTool    string `json:"result_tool"`
	TurnsTotal    int    `json:"turns_total"`
	TurnsRendered int    `json:"turns_rendered"`
	// RangeWarning says a malformed range was replaced by the whole
	// transcript, mirroring read_transcript's range_warning.
	RangeWarning string        `json:"range_warning,omitempty"`
	Elided       int           `json:"elided"`
	Turns        []TurnSummary `json:"turns"`
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

	// turnOf[i] is entry i's read_transcript turn number, or -1 for an entry
	// read_transcript omits; turnStarts[n] is the entry index of Turn n.
	turnOf := make([]int, len(doc.Entries))
	var turnStarts []int
	for i, e := range doc.Entries {
		turnOf[i] = -1
		if e.Turn.Kind.PublicTranscript() {
			turnOf[i] = len(turnStarts)
			turnStarts = append(turnStarts, i)
		}
	}
	total := len(turnStarts)
	lo, hi, rendered := 0, len(doc.Entries), total
	var rangeWarning string
	// Surrounding whitespace is ignored, as read_transcript ignores it.
	if spec := strings.TrimSpace(opts.Range); spec != "" {
		first, last, err := turnwindow.Parse(spec, total)
		switch {
		case err != nil:
			rangeWarning = fmt.Sprintf("invalid range %q; rendered the whole transcript instead. Accepted: %s", opts.Range, turnwindow.Grammar)
		case last < first:
			lo, hi, rendered = 0, 0, 0
		default:
			// The entry window runs from the first selected turn to just
			// before the turn after the last, so unnumbered entries ride with
			// the turn they follow. Entries before Turn 0 belong to a window
			// that starts there.
			rendered = last - first + 1
			if first > 0 {
				lo = turnStarts[first]
			}
			if last+1 < total {
				hi = turnStarts[last+1]
			}
		}
	}
	res := TranscriptResult{
		SessionID:     paths.SessionID,
		ResultTool:    resultTool,
		TurnsTotal:    total,
		TurnsRendered: rendered,
		RangeWarning:  rangeWarning,
		Elided:        total - rendered,
	}
	textMax := opts.TextMax
	if textMax <= 0 {
		textMax = DefaultTextMax
	}
	for i := lo; i < hi; i++ {
		ts := summarizeTurn(doc.Entries[i], resultTool, textMax)
		if turnOf[i] >= 0 {
			ts.Turn = &turnOf[i]
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
				intent = schema.ToolIntent(part.ToolCall.Arguments)
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
			fmt.Fprintf(&b, "[%s] %s", optionalIntString(t.Turn), t.Kind)
			if names := toolCallNames(t.ToolCalls); names != "" {
				fmt.Fprintf(&b, "  tools: %s", names)
			}
			if names := toolResultNames(t.ToolResults); names != "" {
				fmt.Fprintf(&b, "  results: %s", names)
			}
			if t.Text != "" {
				fmt.Fprintf(&b, "  %q", runetrim.OneLine(t.Text))
			}
			b.WriteString("\n")
			continue
		}
		// markdown
		fmt.Fprintf(&b, "### [%s] %s\n", optionalIntString(t.Turn), t.Kind)
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
			fmt.Fprintf(&b, "%s `%s`\n", label, runetrim.OneLine(tc.ArgPreview))
		}
		for _, tr := range t.ToolResults {
			label := "← " + tr.Name
			if tr.IsError {
				label += " (error)"
			}
			fmt.Fprintf(&b, "%s `%s`\n", label, runetrim.OneLine(tr.ContentPreview))
		}
		b.WriteString("\n")
	}
	if r.RangeWarning != "" {
		fmt.Fprintf(&b, "range warning: %s\n", r.RangeWarning)
	}
	fmt.Fprintf(&b, "— turns_total=%d turns_rendered=%d elided=%d (session %s, result_tool=%s)\n",
		r.TurnsTotal, r.TurnsRendered, r.Elided, r.SessionID, r.ResultTool)
	return b.String()
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
