package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// transcriptDoc is a parsed transcript file: the header, the conversation
// entries (turns), and the raw api_call lines (kept verbatim for the mentions
// scan, which is deliberately separate from the structural tool-call count).
type transcriptDoc struct {
	Header   transcript.Header
	Entries  []transcript.Entry
	apiLines []string
}

func loadTranscript(path string) (transcriptDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return transcriptDoc{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) == "" {
		lines = lines[:n-1]
	}
	var doc transcriptDoc
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var peek struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			if i == len(lines)-1 {
				break // tolerate a partial trailing line from an in-flight append
			}
			return transcriptDoc{}, fmt.Errorf("parse transcript line %d: %w", i+1, err)
		}
		switch peek.Kind {
		case "header":
			_ = json.Unmarshal([]byte(line), &doc.Header)
		case "entry":
			var e transcript.Entry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				return transcriptDoc{}, fmt.Errorf("parse transcript entry line %d: %w", i+1, err)
			}
			doc.Entries = append(doc.Entries, e)
		case "api_call":
			doc.apiLines = append(doc.apiLines, line)
		}
	}
	return doc, nil
}

// CountResult is the structural tool-invocation count plus the textual-mention
// counts, kept separate so a tool NAMED in instructions or api-call payloads is
// never conflated with a tool actually CALLED.
type CountResult struct {
	SessionID             string `json:"session_id"`
	Tool                  string `json:"tool"`
	Calls                 int    `json:"calls"`
	MentionsAPICalls      int    `json:"mentions_api_calls"`
	MentionsAssistantText int    `json:"mentions_assistant_text"`
}

// Count returns how many times a tool was structurally invoked in a session
// (a content part of kind tool_call whose ToolCall.Name matches), distinct from
// how many times the tool name merely appears as text in api_call payloads or
// assistant prose.
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
	for _, line := range doc.apiLines {
		res.MentionsAPICalls += strings.Count(line, tool)
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
	if r.MentionsAPICalls > 0 || r.MentionsAssistantText > 0 {
		out += fmt.Sprintf("  (%d textual mention(s) in api_call payloads, %d in assistant text — not invocations)",
			r.MentionsAPICalls, r.MentionsAssistantText)
	}
	return out
}

// ToolCallSummary is one tool call in a turn.
type ToolCallSummary struct {
	Name       string `json:"name"`
	ArgPreview string `json:"arg_preview,omitempty"`
	IsResult   bool   `json:"is_result,omitempty"` // the session's effective result tool
}

// TurnSummary is the structural view of one transcript turn.
type TurnSummary struct {
	Index     int               `json:"index"` // 1-based position in the conversation
	Kind      string            `json:"kind"`
	Role      string            `json:"role,omitempty"`
	ToolCalls []ToolCallSummary `json:"tool_calls,omitempty"`
	Text      string            `json:"text,omitempty"`
}

// TranscriptResult is the rendered transcript with an honest elision footer:
// turns_rendered + elided == turns_total always holds.
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
	Range  string // "last:N" | "start:N" | "A-B"
}

const argPreviewMax = 80
const textPreviewMax = 200

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

	total := len(doc.Entries)
	lo, hi := applyRange(opts.Range, total)
	res := TranscriptResult{
		SessionID:     paths.SessionID,
		ResultTool:    resultTool,
		TurnsTotal:    total,
		TurnsRendered: hi - lo,
		Elided:        total - (hi - lo),
	}
	for i := lo; i < hi; i++ {
		res.Turns = append(res.Turns, summarizeTurn(i+1, doc.Entries[i], resultTool))
	}
	return res, nil
}

func summarizeTurn(index int, e transcript.Entry, resultTool string) TurnSummary {
	ts := TurnSummary{Index: index, Kind: string(e.Turn.Kind), Role: string(e.Turn.Message.Role)}
	var text strings.Builder
	for _, part := range e.Turn.Message.Content {
		switch part.Kind {
		case llm.ContentText:
			text.WriteString(part.Text)
		case llm.ContentToolCall:
			if part.ToolCall == nil {
				continue
			}
			ts.ToolCalls = append(ts.ToolCalls, ToolCallSummary{
				Name:       part.ToolCall.Name,
				ArgPreview: truncate(string(part.ToolCall.Arguments), argPreviewMax),
				IsResult:   part.ToolCall.Name == resultTool,
			})
		}
	}
	ts.Text = truncate(strings.TrimSpace(text.String()), textPreviewMax)
	return ts
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
			fmt.Fprintf(&b, "[%d] %s", t.Index, t.Kind)
			if names := toolCallNames(t.ToolCalls); names != "" {
				fmt.Fprintf(&b, "  tools: %s", names)
			}
			if t.Text != "" {
				fmt.Fprintf(&b, "  %q", oneLine(t.Text))
			}
			b.WriteString("\n")
			continue
		}
		// markdown
		fmt.Fprintf(&b, "### [%d] %s\n", t.Index, t.Kind)
		if t.Text != "" {
			fmt.Fprintf(&b, "%s\n", t.Text)
		}
		for _, tc := range t.ToolCalls {
			label := "→ " + tc.Name
			if tc.IsResult {
				label = "⇒ " + tc.Name + " (result)"
			}
			fmt.Fprintf(&b, "%s `%s`\n", label, oneLine(tc.ArgPreview))
		}
		b.WriteString("\n")
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

// applyRange resolves a range expression to a [lo, hi) window over total turns.
// An empty or unrecognized range yields the whole transcript.
func applyRange(rangeArg string, total int) (lo, hi int) {
	lo, hi = 0, total
	rangeArg = strings.TrimSpace(rangeArg)
	switch {
	case rangeArg == "":
		return 0, total
	case strings.HasPrefix(rangeArg, "last:"):
		if n := atoi(strings.TrimPrefix(rangeArg, "last:")); n > 0 && n < total {
			lo = total - n
		}
	case strings.HasPrefix(rangeArg, "start:"):
		if n := atoi(strings.TrimPrefix(rangeArg, "start:")); n > 1 {
			lo = min(n-1, total)
		}
	case strings.Contains(rangeArg, "-"):
		a, b, ok := strings.Cut(rangeArg, "-")
		if ok {
			if x := atoi(a); x > 1 {
				lo = min(x-1, total)
			}
			if y := atoi(b); y > 0 && y < total {
				hi = y
			}
		}
	}
	if lo > hi {
		lo = hi
	}
	return lo, hi
}

func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
}
