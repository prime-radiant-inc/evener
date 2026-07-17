package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
	TranscriptRef string                      `json:"transcript_ref"`
	Format        string                      `json:"format"`
	ContentType   string                      `json:"content_type"`
	Content       string                      `json:"content"`
	Meta          readMarkdownMeta            `json:"meta"`
	Expansion     *transcriptTurnExpansion    `json:"expansion,omitempty"`
	Continuation  *transcriptTurnContinuation `json:"continuation,omitempty"`
}

type transcriptTurnExpansion struct {
	ExpandTurn     int    `json:"expand_turn"`
	OffsetBytes    int    `json:"offset_bytes"`
	BytesReturned  int    `json:"bytes_returned"`
	TotalBytes     int    `json:"total_bytes"`
	Representation string `json:"representation"`
	Encoding       string `json:"encoding"`
	Data           string `json:"data"`
}

type transcriptTurnContinuation struct {
	ExpandTurn  int `json:"expand_turn"`
	OffsetBytes int `json:"offset_bytes"`
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

const (
	transcriptSource                = "transcript"
	apiLogSource                    = "api_log"
	maxExpansionBytes               = 64 << 10
	transcriptExpansionReadHint     = "use expand_turn and its continuation for exact bytes"
	jobTranscriptTruncationNotice   = "additional output is not available from this transcript view"
	transcriptV2JSONLRepresentation = "transcript_v2_jsonl"
)

type readSessionTranscriptArgs struct {
	TranscriptRef string
	Source        string
	Format        string
	Range         string
	ExpandTurn    *int
	AttemptID     string
	Body          string
	OffsetBytes   int
	MaxBytes      int
}

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
		for _, name := range []string{"range", "expand_turn", "offset_bytes", "max_bytes"} {
			if _, present := args[name]; present {
				return nil, fmt.Errorf("invalid_request: %s applies only to session transcript refs", name)
			}
		}
		return readJobTranscript(deps, selector, rangeArg, format)
	}
	return execReadSessionTranscript(deps, args)
}

// execReadSessionTranscript validates the source-specific arguments before it
// resolves or opens either transcript artifact.
func execReadSessionTranscript(deps *toolDeps, args map[string]any) (any, error) {
	parsed, err := parseReadSessionTranscriptArgs(args)
	if err != nil {
		return nil, err
	}
	if parsed.Source == apiLogSource {
		path, ref, err := resolveTranscript(parsed.TranscriptRef, deps.stateDir, deps.sessionID)
		if err != nil {
			return nil, err
		}
		if parsed.AttemptID != "" {
			return readAPILogAttempt(apiLogPathForTranscript(path), ref, parsed.AttemptID, parsed.Body, parsed.OffsetBytes, parsed.MaxBytes)
		}
		return readAPILogSummary(apiLogPathForTranscript(path), ref, parsed.Range)
	}

	path, ref, err := resolveTranscript(parsed.TranscriptRef, deps.stateDir, deps.sessionID)
	if err != nil {
		return nil, err
	}
	meta := resolvedSessionMeta(deps, path, ref)
	switch parsed.Format {
	case "markdown":
		return readMarkdownPage(path, ref, meta, parsed.Range, parsed.ExpandTurn, parsed.OffsetBytes, parsed.MaxBytes)
	case "outline":
		return readOutline(path, ref, parsed.Range)
	case "jsonl":
		return readRaw(path, ref, parsed.Range)
	default:
		panic("validated transcript format")
	}
}

func parseReadSessionTranscriptArgs(args map[string]any) (readSessionTranscriptArgs, error) {
	parsed := readSessionTranscriptArgs{
		TranscriptRef: strings.TrimSpace(stringArg(args, "transcript_ref")),
		Source:        strings.TrimSpace(stringArg(args, "source")),
		Format:        strings.TrimSpace(stringArg(args, "format")),
		Range:         strings.TrimSpace(stringArg(args, "range")),
		ExpandTurn:    optionalIntArg(args, "expand_turn"),
		AttemptID:     strings.TrimSpace(stringArg(args, "attempt_id")),
		Body:          strings.TrimSpace(stringArg(args, "body")),
	}
	if value := optionalIntArg(args, "offset_bytes"); value != nil {
		parsed.OffsetBytes = *value
	}
	if value := optionalIntArg(args, "max_bytes"); value != nil {
		parsed.MaxBytes = *value
	}

	explicitSource := parsed.Source != ""
	if parsed.Source == "" {
		if parsed.AttemptID != "" {
			parsed.Source = apiLogSource
		} else {
			parsed.Source = transcriptSource
		}
	}
	if parsed.Source != transcriptSource && parsed.Source != apiLogSource {
		return readSessionTranscriptArgs{}, fmt.Errorf("invalid_request: source %q is not supported: use transcript or api_log", parsed.Source)
	}
	if parsed.ExpandTurn != nil && *parsed.ExpandTurn < 0 {
		return readSessionTranscriptArgs{}, errors.New("invalid_request: expand_turn must be non-negative")
	}

	if parsed.Source == apiLogSource {
		if parsed.Format != "" {
			return readSessionTranscriptArgs{}, errors.New("invalid_request: format applies only to source=transcript")
		}
		if parsed.ExpandTurn != nil {
			return readSessionTranscriptArgs{}, errors.New("invalid_request: expand_turn applies only to transcript markdown")
		}
		if parsed.AttemptID != "" && parsed.Range != "" {
			return readSessionTranscriptArgs{}, errors.New("invalid_request: range cannot be combined with attempt_id")
		}
	} else {
		if parsed.Format == "" {
			parsed.Format = formatMarkdown
		}
		switch parsed.Format {
		case formatMarkdown, formatOutline, formatJSONL:
		default:
			return readSessionTranscriptArgs{}, fmt.Errorf("invalid_request: unknown format %q: use markdown, outline, or jsonl", parsed.Format)
		}
		if parsed.AttemptID != "" {
			if explicitSource {
				return readSessionTranscriptArgs{}, errors.New("invalid_request: attempt_id cannot be combined with source=transcript")
			}
			return readSessionTranscriptArgs{}, errors.New("invalid_request: attempt_id requires source=api_log")
		}
		if parsed.Body != "" {
			return readSessionTranscriptArgs{}, errors.New("invalid_request: body requires attempt_id")
		}
		if parsed.ExpandTurn != nil && parsed.Format != formatMarkdown {
			return readSessionTranscriptArgs{}, errors.New("invalid_request: expand_turn applies only to transcript markdown")
		}
	}

	if parsed.Body != "" {
		if parsed.AttemptID == "" {
			return readSessionTranscriptArgs{}, errors.New("invalid_request: body requires attempt_id")
		}
		if parsed.Body != "request" && parsed.Body != "response" {
			return readSessionTranscriptArgs{}, fmt.Errorf("invalid_request: body %q is not supported: use request or response", parsed.Body)
		}
	}
	expanding := parsed.ExpandTurn != nil || parsed.Body != ""
	if _, present := args["offset_bytes"]; present && !expanding {
		return readSessionTranscriptArgs{}, errors.New("invalid_request: offset_bytes requires expand_turn or an explicit API body")
	}
	if parsed.OffsetBytes < 0 {
		return readSessionTranscriptArgs{}, errors.New("invalid_request: offset_bytes must be non-negative")
	}
	if _, present := args["max_bytes"]; present && !expanding {
		return readSessionTranscriptArgs{}, errors.New("invalid_request: max_bytes requires expand_turn or an explicit API body")
	}
	if parsed.MaxBytes < 0 || parsed.MaxBytes > maxExpansionBytes {
		return readSessionTranscriptArgs{}, fmt.Errorf("invalid_request: max_bytes must be between 1 and %d", maxExpansionBytes)
	}
	if _, present := args["max_bytes"]; present && parsed.MaxBytes == 0 {
		return readSessionTranscriptArgs{}, fmt.Errorf("invalid_request: max_bytes must be between 1 and %d", maxExpansionBytes)
	}
	return parsed, nil
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
	envelope := readMarkdownEnvelope{
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
	}
	return boundReadMarkdownEnvelopeWithHint(envelope, jobTranscriptTruncationNotice)
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
	return readMarkdownPage(path, ref, meta, rangeArg, expandTurn, 0, 0)
}

func transcriptExpansionJSONL(data transcriptData, pin int) ([]byte, error) {
	if pin < 0 || pin >= len(data.Entries) {
		return nil, fmt.Errorf("invalid_request: expand_turn %d does not identify a transcript turn", pin)
	}
	if len(data.EntryLines) != len(data.Entries) {
		return nil, errors.New("transcript expansion unavailable: persisted entries are not retained")
	}

	first, last, ok := resolvePinnedSpan(data.Entries, pin)
	if !ok {
		return nil, fmt.Errorf("invalid_request: expand_turn %d does not identify a transcript turn", pin)
	}
	total := last - first + 1
	for _, line := range data.EntryLines[first : last+1] {
		total += len(line)
	}
	exact := make([]byte, 0, total)
	for _, line := range data.EntryLines[first : last+1] {
		exact = append(exact, line...)
		exact = append(exact, '\n')
	}
	return exact, nil
}

func readMarkdownPage(path, ref string, meta schema.SessionMeta, rangeArg string, expandTurn *int, offsetBytes, maxBytes int) (any, error) {
	var data transcriptData
	var err error
	if expandTurn == nil {
		data, err = readTranscriptFull(path)
	} else {
		data, err = readTranscriptFullWithEntryLines(path)
	}
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

	var exact []byte
	var expansionFirst int
	if expandTurn != nil {
		first, _, ok := resolvePinnedSpan(data.Entries, *expandTurn)
		if !ok {
			return nil, fmt.Errorf("invalid_request: expand_turn %d does not identify a transcript turn", *expandTurn)
		}
		expansionFirst = first
	}

	renderOpt := renderOpts{meta: meta, fullResultFor: expandTurn}
	if expandTurn != nil {
		renderOpt.fullResultFor = &expansionFirst
		exact, err = transcriptExpansionJSONL(data, *expandTurn)
		if err != nil {
			return nil, err
		}
	}
	content, rmeta := renderTranscript(data.Header, data.Entries, effectiveRange, renderOpt)
	var expansion *transcriptTurnExpansion
	var continuation *transcriptTurnContinuation
	if expandTurn != nil {
		if maxBytes == 0 {
			maxBytes = defaultExpansionBytes
		}
		if offsetBytes > len(exact) {
			return nil, fmt.Errorf("invalid_request: offset_bytes %d exceeds expanded turn length %d", offsetBytes, len(exact))
		}
		if offsetBytes > 0 || len(exact) > maxBytes {
			boundedOpt := renderOpts{meta: meta}
			boundedOpt.fullResultFor = &expansionFirst
			boundedOpt.headTailEvidenceFor = &expansionFirst
			content, rmeta = renderTranscript(data.Header, data.Entries, effectiveRange, boundedOpt)
			rmeta.Truncated = true
		}
		end := offsetBytes + maxBytes
		if end > len(exact) {
			end = len(exact)
		}
		chunk := exact[offsetBytes:end]
		expansion = &transcriptTurnExpansion{
			ExpandTurn:     *expandTurn,
			OffsetBytes:    offsetBytes,
			BytesReturned:  len(chunk),
			TotalBytes:     len(exact),
			Representation: transcriptV2JSONLRepresentation,
		}
		if utf8.Valid(chunk) {
			expansion.Encoding = "utf8"
			expansion.Data = string(chunk)
		} else {
			expansion.Encoding = "base64"
			expansion.Data = base64.StdEncoding.EncodeToString(chunk)
		}
		if end < len(exact) {
			continuation = &transcriptTurnContinuation{ExpandTurn: *expandTurn, OffsetBytes: end}
		}
	}

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

	envelope := readMarkdownEnvelope{
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
		Expansion:    expansion,
		Continuation: continuation,
	}
	return boundReadMarkdownEnvelope(envelope)
}

func boundReadMarkdownEnvelope(envelope readMarkdownEnvelope) (readMarkdownEnvelope, error) {
	return boundReadMarkdownEnvelopeWithHint(envelope, transcriptExpansionReadHint)
}

func boundReadMarkdownEnvelopeWithHint(envelope readMarkdownEnvelope, exactReadHint string) (readMarkdownEnvelope, error) {
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return readMarkdownEnvelope{}, fmt.Errorf("encode transcript expansion: %w", err)
	}
	if len(encoded) <= hardCapChars {
		return envelope, nil
	}
	if envelope.Expansion == nil {
		return boundReadMarkdownContentWithHint(envelope, hardCapChars, exactReadHint)
	}

	raw, err := decodeTranscriptExpansion(envelope.Expansion)
	if err != nil {
		return readMarkdownEnvelope{}, err
	}

	// The caller's max_bytes budget applies to raw expansion bytes. Shrink the
	// human markdown first so JSON escaping never changes that paging contract.
	bounded, boundErr := boundReadMarkdownContentWithHint(envelope, hardCapChars, exactReadHint)
	if boundErr == nil {
		return bounded, nil
	}

	// An unusually escape-heavy expansion can exceed the serialized hard cap by
	// itself. Keep the hard backstop by fitting the largest honest raw prefix.
	empty := transcriptEnvelopeWithExpansionBytes(envelope, nil)
	contentTarget := hardCapChars - min(64<<10, hardCapChars/3)
	empty, err = boundReadMarkdownContentWithHint(empty, contentTarget, exactReadHint)
	if err != nil {
		return readMarkdownEnvelope{}, err
	}
	envelope.Content = empty.Content
	envelope.Meta.Truncated = envelope.Meta.Truncated || empty.Meta.Truncated

	best, err := largestTranscriptExpansionPrefix(envelope, raw, hardCapChars)
	if err != nil {
		return readMarkdownEnvelope{}, err
	}
	if best == 0 {
		candidate := transcriptEnvelopeWithExpansionBytes(envelope, nil)
		encoded, encodeErr := json.MarshalIndent(candidate, "", "  ")
		if encodeErr != nil {
			return readMarkdownEnvelope{}, fmt.Errorf("encode transcript expansion: %w", encodeErr)
		}
		if len(encoded) > hardCapChars || len(raw) > 0 {
			return readMarkdownEnvelope{}, fmt.Errorf("transcript expansion metadata exceeds %d-byte output limit", hardCapChars)
		}
		return candidate, nil
	}
	return transcriptEnvelopeWithExpansionBytes(envelope, raw[:best]), nil
}

func boundReadMarkdownContent(envelope readMarkdownEnvelope, maxEncodedBytes int) (readMarkdownEnvelope, error) {
	return boundReadMarkdownContentWithHint(envelope, maxEncodedBytes, transcriptExpansionReadHint)
}

func boundReadMarkdownContentWithHint(envelope readMarkdownEnvelope, maxEncodedBytes int, exactReadHint string) (readMarkdownEnvelope, error) {
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return readMarkdownEnvelope{}, fmt.Errorf("encode transcript envelope: %w", err)
	}
	if len(encoded) <= maxEncodedBytes {
		return envelope, nil
	}

	original := envelope.Content
	keep := len([]rune(original))
	for len(encoded) > maxEncodedBytes && keep > 0 {
		next := keep * maxEncodedBytes / len(encoded)
		if next >= keep {
			next = keep - 1
		}
		keep = next
		envelope.Content = boundOversizedTurnEvidenceWithHint(original, keep, exactReadHint)
		envelope.Meta.Truncated = true
		encoded, err = json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			return readMarkdownEnvelope{}, fmt.Errorf("encode transcript envelope: %w", err)
		}
	}
	if len(encoded) > maxEncodedBytes {
		return readMarkdownEnvelope{}, fmt.Errorf("transcript metadata exceeds %d-byte output limit", maxEncodedBytes)
	}
	return envelope, nil
}

// largestTranscriptExpansionPrefix searches UTF-8 and base64 candidates
// separately. Within each encoding the serialized size is monotone; switching
// encodings at an incomplete rune boundary is not.
func largestTranscriptExpansionPrefix(envelope readMarkdownEnvelope, raw []byte, maxEncodedBytes int) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	full := transcriptEnvelopeWithExpansionBytes(envelope, raw)
	encoded, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encode transcript expansion: %w", err)
	}
	if len(encoded) <= maxEncodedBytes {
		return len(raw), nil
	}

	valid, invalid := transcriptExpansionPrefixClasses(raw)
	bestValid, err := largestFittingTranscriptPrefix(envelope, raw, valid, maxEncodedBytes)
	if err != nil {
		return 0, err
	}
	bestInvalid, err := largestFittingTranscriptPrefix(envelope, raw, invalid, maxEncodedBytes)
	if err != nil {
		return 0, err
	}
	return max(bestValid, bestInvalid), nil
}

func transcriptExpansionPrefixClasses(raw []byte) (valid, invalid []int) {
	for offset := 0; offset < len(raw); {
		_, size := utf8.DecodeRune(raw[offset:])
		if size == 1 && raw[offset] >= utf8.RuneSelf {
			for end := offset + 1; end <= len(raw); end++ {
				invalid = append(invalid, end)
			}
			break
		}
		for end := offset + 1; end < offset+size && end <= len(raw); end++ {
			invalid = append(invalid, end)
		}
		offset += size
		if offset <= len(raw) {
			valid = append(valid, offset)
		}
	}
	return valid, invalid
}

func largestFittingTranscriptPrefix(envelope readMarkdownEnvelope, raw []byte, candidates []int, maxEncodedBytes int) (int, error) {
	best := 0
	for low, high := 0, len(candidates)-1; low <= high; {
		mid := low + (high-low)/2
		n := candidates[mid]
		candidate := transcriptEnvelopeWithExpansionBytes(envelope, raw[:n])
		encoded, err := json.MarshalIndent(candidate, "", "  ")
		if err != nil {
			return 0, fmt.Errorf("encode transcript expansion: %w", err)
		}
		if len(encoded) <= maxEncodedBytes {
			best = n
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best, nil
}

func decodeTranscriptExpansion(expansion *transcriptTurnExpansion) ([]byte, error) {
	if expansion.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(expansion.Data)
		if err != nil {
			return nil, fmt.Errorf("decode transcript expansion: %w", err)
		}
		return decoded, nil
	}
	return []byte(expansion.Data), nil
}

func transcriptEnvelopeWithExpansionBytes(envelope readMarkdownEnvelope, data []byte) readMarkdownEnvelope {
	expansion := *envelope.Expansion
	expansion.BytesReturned = len(data)
	if utf8.Valid(data) {
		expansion.Encoding = "utf8"
		expansion.Data = string(data)
	} else {
		expansion.Encoding = "base64"
		expansion.Data = base64.StdEncoding.EncodeToString(data)
	}
	envelope.Expansion = &expansion
	end := expansion.OffsetBytes + len(data)
	if end < expansion.TotalBytes {
		envelope.Continuation = &transcriptTurnContinuation{ExpandTurn: expansion.ExpandTurn, OffsetBytes: end}
	} else {
		envelope.Continuation = nil
	}
	return envelope
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

var readRawLinesForRange = rawLinesForRange

// readRawEnvelope is the wire shape for bounded semantic transcript-v2 NDJSON.
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

// readRaw returns the semantic JSONL header and entry lines for the range,
// bounded by the 200k hard cap (head-only, valid NDJSON). A
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
	content, lines, skipped, truncated, err := readRawLinesForRange(path, start, end)
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
			Hint:                "semantic transcript-v2 NDJSON; system-prompt and provider API data are excluded. For provider attempts use source=api_log; for comprehension use format=markdown.",
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
