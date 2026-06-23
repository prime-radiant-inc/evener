package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// transcriptToolMaxChars is the explicit registry output limit applied to every
// transcript tool. The render/outline layer is the single source of truncation; this
// ceiling only stops the registry from re-truncating a render-bounded envelope into
// invalid JSON. It is a backstop, not a participant.
const transcriptToolMaxChars = 600_000

// transcriptTools returns the read-only transcript inspection tools. read_transcript
// is always available for job:<job_id> refs; archived session lookup tools are
// advertised only when state persistence is enabled.
func transcriptTools(deps *toolDeps) []tool.RegisteredTool {
	tools := []tool.RegisteredTool{
		readTranscriptTool(deps),
	}
	if deps != nil && deps.stateDir != "" {
		tools = append(tools,
			readSessionTranscriptTool(deps),
			findSessionTranscriptsTool(deps),
		)
	}
	for i := range tools {
		tools[i].Limit = schema.ToolOutputLimit{MaxChars: transcriptToolMaxChars}
	}
	return tools
}

// readMarkdownEnvelope is the wire shape returned for a markdown read. Field
// order follows spec §"Default Response Shape".
type readMarkdownEnvelope struct {
	TranscriptRef string           `json:"transcript_ref"`
	Format        string           `json:"format"`
	ContentType   string           `json:"content_type"`
	Content       string           `json:"content"`
	Meta          readMarkdownMeta `json:"meta"`
}

// readMarkdownMeta is the meta block for a markdown read. It deliberately omits
// session_id, redaction, and raw_formats — those fields were in an older shape
// and are dropped per the current spec.
type readMarkdownMeta struct {
	TurnsTotal          int    `json:"turns_total"`
	Range               string `json:"range"`
	TurnsRendered       int    `json:"turns_rendered"`
	Truncated           bool   `json:"truncated"`
	ElidedTurns         int    `json:"elided_turns"`
	SkippedCorruptLines int    `json:"skipped_corrupt_lines,omitempty"`
	RangeWarning        string `json:"range_warning,omitempty"`
}

const formatMarkdown = "markdown"

func readTranscriptTool(deps *toolDeps) tool.RegisteredTool {
	return tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefReadTranscript(), ReadOnly: true},
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			return execReadTranscript(deps, args)
		},
	}
}

func readSessionTranscriptTool(deps *toolDeps) tool.RegisteredTool {
	return tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefReadSessionTranscript(), ReadOnly: true},
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			return execReadSessionTranscript(deps, args)
		},
	}
}

func execReadTranscript(deps *toolDeps, args map[string]any) (any, error) {
	selector := strings.TrimSpace(stringArg(args, "transcript_ref"))
	rangeArg := strings.TrimSpace(stringArg(args, "range"))
	format := strings.TrimSpace(stringArg(args, "format"))
	if strings.HasPrefix(selector, "job:") {
		return readJobTranscript(deps, selector, rangeArg, format)
	}
	return execReadSessionTranscript(deps, args)
}

// execReadSessionTranscript dispatches to the appropriate format handler.
// The outline and jsonl arms are not yet implemented (other tasks).
func execReadSessionTranscript(deps *toolDeps, args map[string]any) (any, error) {
	selector := strings.TrimSpace(stringArg(args, "transcript_ref"))
	rangeArg := strings.TrimSpace(stringArg(args, "range"))
	format := strings.TrimSpace(stringArg(args, "format"))
	if format == "" {
		format = "markdown"
	}
	expandTurn := optionalPositiveIntArg(args, "expand_turn")
	path, ref, err := resolveTranscript(selector, deps.stateDir, deps.sessionID)
	if err != nil {
		return nil, err
	}
	meta := resolvedSessionMeta(deps, path, ref)
	switch format {
	case "markdown":
		return readMarkdown(path, ref, meta, rangeArg, expandTurn)
	case "outline":
		return readOutline(path, ref, rangeArg)
	case "jsonl":
		return readRaw(path, ref, rangeArg)
	default:
		return nil, fmt.Errorf("unknown format %q: use markdown, outline, or jsonl", format)
	}
}

func readJobTranscript(deps *toolDeps, ref, rangeArg, format string) (any, error) {
	_ = rangeArg
	if deps == nil || deps.jobManager == nil {
		return nil, errors.New("job transcript unavailable: job manager is not available")
	}
	if format == "" {
		format = formatMarkdown
	}
	if format != formatMarkdown {
		return nil, fmt.Errorf("job transcript format %q is not supported: use markdown", format)
	}
	jobID := strings.TrimSpace(strings.TrimPrefix(ref, "job:"))
	if jobID == "" {
		return nil, errors.New("invalid_request: job transcript_ref must be job:<job_id>")
	}
	rec, err := findJobRecord(deps.jobManager, jobID)
	if err != nil {
		return nil, err
	}
	content, total, dropped, truncated, err := deps.jobManager.readJobWindow(jobID, maxJobOutputRetentionBytes, false)
	if err != nil {
		return nil, err
	}
	return readMarkdownEnvelope{
		TranscriptRef: ref,
		Format:        formatMarkdown,
		ContentType:   "text/markdown",
		Content:       renderShellJobTranscript(rec, content, total, dropped),
		Meta: readMarkdownMeta{
			TurnsTotal:    1,
			Range:         "shell-log",
			TurnsRendered: 1,
			Truncated:     truncated || dropped > 0 || total > int64(len([]byte(content))),
		},
	}, nil
}

func renderShellJobTranscript(rec *jobstore.JobRecord, output string, total, dropped int64) string {
	var b strings.Builder
	jobID := ""
	status := ""
	command := ""
	if rec != nil {
		jobID = rec.JobID
		status = string(rec.Status)
		command = rec.Command
	}
	fmt.Fprintf(&b, "# Shell Job %s\n\n", jobID)
	if status != "" {
		fmt.Fprintf(&b, "- status: %s\n", status)
	}
	if command != "" {
		fmt.Fprintf(&b, "- command: `%s`\n", strings.ReplaceAll(command, "`", "\\`"))
	}
	fmt.Fprintf(&b, "- total_bytes: %d\n", total)
	if dropped > 0 {
		fmt.Fprintf(&b, "- dropped_bytes: %d\n", dropped)
	}
	b.WriteString("\n```text\n")
	b.WriteString(output)
	if output != "" && !strings.HasSuffix(output, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

// rangeAcceptedGrammar is the grammar hint included in range_warning messages
// when a malformed range spec is received.
const rangeAcceptedGrammar = "N-M | last:N | start:N"

// readMarkdown builds the markdown envelope for the resolved transcript. When
// rangeArg is non-empty and malformed, it falls back to the smart default range,
// sets meta.range_warning, and surfaces the warning in the content. Valid ranges
// (including empty) produce no warning.
//
// When turns_rendered < turns_total, a self-announcing window line is spliced
// after the document header so a default read never silently masquerades as the
// whole session.
func readMarkdown(path, ref string, meta schema.SessionMeta, rangeArg string, expandTurn *int) (any, error) {
	data, err := readTranscriptFull(path)
	if err != nil {
		return nil, err
	}

	// Try the strict parser; on a malformed spec, fall back to the default and
	// record a warning. parseRangeErr never errors on "".
	effectiveRange := rangeArg
	var rangeWarning string
	if _, _, err := parseRangeErr(rangeArg, len(data.Entries)); err != nil {
		rangeWarning = fmt.Sprintf("invalid range %q; rendered the default instead. Accepted: %s", rangeArg, rangeAcceptedGrammar)
		effectiveRange = ""
	}

	content, rmeta := renderTranscript(data.Header, data.Entries, effectiveRange, renderOpts{
		meta:          meta,
		fullResultFor: expandTurn,
	})

	// Surface a range warning prominently when a malformed range was given.
	if rangeWarning != "" {
		content = spliceRangeWarning(content, rangeWarning)
	}

	// Self-announcing window: when the render is a window (not the whole session),
	// splice a line after the header naming the window so the model knows it is
	// seeing a subset. We compute start/end from the effective (clamped) range to
	// report honest first/last turn numbers.
	if rmeta.TurnsRendered < rmeta.TurnsTotal {
		content = spliceWindowLine(content, rmeta)
	}

	return readMarkdownEnvelope{
		TranscriptRef: ref,
		Format:        formatMarkdown,
		ContentType:   "text/markdown",
		Content:       content,
		Meta: readMarkdownMeta{
			TurnsTotal:          rmeta.TurnsTotal,
			Range:               rmeta.Range,
			TurnsRendered:       rmeta.TurnsRendered,
			Truncated:           rmeta.Truncated,
			ElidedTurns:         rmeta.ElidedTurns,
			SkippedCorruptLines: data.Skipped,
			RangeWarning:        rangeWarning,
		},
	}, nil
}

const formatOutline = "outline"

// readOutline builds the outline envelope for the resolved transcript. When
// rangeArg is non-empty and malformed, it falls back to the full session (no
// range applied) so the model is never silently wrong. Valid ranges produce the
// filtered outline with absolute turn numbers.
func readOutline(path, ref, rangeArg string) (any, error) {
	data, err := readTranscriptFull(path)
	if err != nil {
		return nil, err
	}

	n := len(data.Entries)
	start, end := 0, n-1

	if rangeArg != "" {
		if s, e, err := parseRangeErr(rangeArg, n); err == nil {
			start, end = s, e
		}
		// Malformed range: silently fall back to the full session (start=0, end=n-1).
		// The outline is always bounded by boundOutline so no runaway output.
	}

	content, truncated, elidedTurns := renderOutline(data.Entries, start, end)

	return readOutlineEnvelope{
		TranscriptRef: ref,
		Format:        formatOutline,
		TurnsTotal:    n,
		Content:       content,
		Truncated:     truncated,
		ElidedTurns:   elidedTurns,
		Hint:          outlineHint,
	}, nil
}

const formatJSONL = "jsonl"

// readRawEnvelope is the wire shape for a jsonl read: the verbatim NDJSON bytes for
// the range. It is the debug/replay escape hatch — noisy (system prompt + api_call
// records) and steered against for comprehension.
type readRawEnvelope struct {
	TranscriptRef string      `json:"transcript_ref"`
	Format        string      `json:"format"`
	ContentType   string      `json:"content_type"`
	Content       string      `json:"content"`
	Meta          readRawMeta `json:"meta"`
}

type readRawMeta struct {
	LinesReturned       int    `json:"lines_returned"`
	Truncated           bool   `json:"truncated"`
	SkippedCorruptLines int    `json:"skipped_corrupt_lines"`
	Hint                string `json:"hint,omitempty"`
	RangeWarning        string `json:"range_warning,omitempty"`
}

// readRaw returns the verbatim JSONL lines for the range (header + interleaved
// api_call lines), bounded only by the 200k hard cap (head-only, valid NDJSON). A
// malformed range falls back to the default and records range_warning; expand_turn
// does not apply to raw output.
func readRaw(path, ref, rangeArg string) (any, error) {
	_, entries, _, err := readTranscript(path)
	if err != nil {
		return nil, err
	}

	effectiveRange := rangeArg
	var rangeWarning string
	if _, _, err := parseRangeErr(rangeArg, len(entries)); err != nil {
		rangeWarning = fmt.Sprintf("invalid range %q; rendered the default instead. Accepted: %s", rangeArg, rangeAcceptedGrammar)
		effectiveRange = ""
	}

	start, end := parseRange(effectiveRange, len(entries))
	content, lines, skipped, truncated, err := rawLinesForRange(path, start, end)
	if err != nil {
		return nil, err
	}

	return readRawEnvelope{
		TranscriptRef: ref,
		Format:        formatJSONL,
		ContentType:   "application/x-ndjson",
		Content:       content,
		Meta: readRawMeta{
			LinesReturned:       lines,
			Truncated:           truncated,
			SkippedCorruptLines: skipped,
			Hint:                "raw NDJSON; for comprehension, re-read with format=markdown.",
			RangeWarning:        rangeWarning,
		},
	}, nil
}

// spliceWindowLine splices a self-announcing window line after the document header.
// It names the ACTUAL rendered turn span — readMeta.FirstRendered/LastRendered, which
// the engine computes after range selection AND budget trimming — so the line can
// never announce turns that are not on screen, regardless of range anchor or how much
// the conversation budget trimmed. Emitted only for a non-empty render; the caller
// invokes it only when the render is a partial window (turns_rendered < turns_total).
func spliceWindowLine(content string, rm readMeta) string {
	if rm.TurnsRendered <= 0 || rm.LastRendered < rm.FirstRendered {
		return content
	}
	line := fmt.Sprintf(
		"\n_Showing turns %d–%d of %d. For the whole shape use format=outline; for other turns set range._\n",
		rm.FirstRendered, rm.LastRendered, rm.TurnsTotal,
	)
	return spliceAfterHeader(content, line)
}

// spliceRangeWarning inserts a one-line range warning after the document header block.
func spliceRangeWarning(content, warning string) string {
	warningLine := fmt.Sprintf("\n> [range warning] %s\n", warning)
	return spliceAfterHeader(content, warningLine)
}

// resolvedSessionMeta returns the render metadata for the resolved transcript.
// When the resolved ref is the current session, the live in-memory meta is used.
// Otherwise the meta.json sitting next to the transcript is loaded; a missing or
// unreadable meta degrades gracefully to a zero meta (the transcript still renders,
// with the session ID filled in from the path so it stays identifiable).
func resolvedSessionMeta(deps *toolDeps, path, ref string) schema.SessionMeta {
	if deps.currentMeta != nil && ref == encodeRef("", deps.sessionID) {
		return deps.currentMeta()
	}
	bucketDir, sessionID := bucketAndSessionFromPath(path)
	if meta, err := schema.LoadSessionMeta(bucketDir, sessionID); err == nil {
		return meta
	}
	return schema.SessionMeta{ID: sessionID}
}

// bucketAndSessionFromPath splits a resolved transcript path
// (<bucketDir>/sessions/<id>.transcript.jsonl) back into its bucket dir and
// session ID.
func bucketAndSessionFromPath(path string) (bucketDir, sessionID string) {
	bucketDir = filepath.Dir(filepath.Dir(path))
	sessionID = strings.TrimSuffix(filepath.Base(path), ".transcript.jsonl")
	return bucketDir, sessionID
}
