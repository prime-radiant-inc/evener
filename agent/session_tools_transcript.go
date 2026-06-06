package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// transcriptToolMaxChars is the explicit registry output limit applied to every
// transcript tool. The render/outline layer is the single source of truncation; this
// ceiling only stops the registry from re-truncating a render-bounded envelope into
// invalid JSON. It is a backstop, not a participant.
const transcriptToolMaxChars = 600_000

// transcriptTools returns the read-only transcript inspection tools. It is wired into
// the session's tool assembly only when state persistence is enabled (a non-empty
// StateDir), since the tools resolve and read on-disk transcript files.
func transcriptTools(deps *toolDeps) []tool.RegisteredTool {
	tools := []tool.RegisteredTool{
		readSessionTranscriptTool(deps),
		findSessionTranscriptsTool(deps),
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

func readSessionTranscriptTool(deps *toolDeps) tool.RegisteredTool {
	return tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefReadSessionTranscript(), ReadOnly: true},
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			return execReadSessionTranscript(deps, args)
		},
	}
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
		return nil, fmt.Errorf("format \"outline\" not yet implemented")
	case "jsonl":
		return nil, fmt.Errorf("format \"jsonl\" not yet implemented")
	default:
		return nil, fmt.Errorf("unknown format %q: use markdown, outline, or jsonl", format)
	}
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
