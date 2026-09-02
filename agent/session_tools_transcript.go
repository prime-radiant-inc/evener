package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/artifactstore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// transcriptToolMaxChars is the explicit registry output limit applied to every
// transcript tool. The render/outline layer is the single source of truncation; this
// ceiling only stops the registry from re-truncating a render-bounded envelope into
// invalid JSON. It is a backstop, not a participant.
const transcriptToolMaxChars = 600_000

// retainedOutputMatchMaxChars leaves ample room for the bounded 64 KiB match
// payload even when JSON escaping expands every pattern character. The final
// envelope check below remains the authoritative backstop for the complete
// serialized response.
const retainedOutputMatchMaxChars = 64 << 10

const artifactUnavailableReadError = "artifact_unavailable: retained artifact could not be read"

// transcriptTools returns the read-only transcript inspection tools. read_transcript
// is always available for job:<job_id> refs; archived session lookup tools are
// advertised only when state persistence is enabled.
func transcriptTools(deps *toolDeps) []tool.RegisteredTool {
	tools := []tool.RegisteredTool{
		readTranscriptTool(deps),
	}
	if deps != nil && deps.stateDir != "" {
		tools = append(tools,
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
	transcriptSource                    = "transcript"
	apiLogSource                        = "api_log"
	apiLogTranscriptPlaceholderMaxBytes = 1024
	maxExpansionBytes                   = 64 << 10
	transcriptExpansionReadHint         = "use expand_turn and its continuation for exact bytes"
	jobTranscriptTruncationNotice       = "additional output is not available from this transcript view"
	transcriptV2JSONLRepresentation     = "transcript_v2_jsonl"
)

type apiLogTranscriptPlaceholder struct {
	Source                 string                      `json:"source"`
	PrivateEvidenceOmitted bool                        `json:"private_evidence_omitted"`
	ReRead                 apiLogTranscriptReadHandle  `json:"re_read"`
	Continuation           *apiLogTranscriptReadHandle `json:"continuation,omitempty"`
}

type apiLogTranscriptReadHandle struct {
	Tool          string `json:"tool"`
	TranscriptRef string `json:"transcript_ref,omitempty"`
	Source        string `json:"source"`
	AttemptID     string `json:"attempt_id,omitempty"`
	Body          string `json:"body,omitempty"`
	OffsetBytes   int    `json:"offset_bytes,omitempty"`
}

type apiLogTranscriptResultIdentity struct {
	TranscriptRef string `json:"transcript_ref"`
	Source        string `json:"source"`
	Attempt       struct {
		AttemptID string `json:"attempt_id"`
	} `json:"attempt"`
	Body *struct {
		Body        string `json:"body"`
		OffsetBytes int    `json:"offset_bytes"`
	} `json:"body"`
	Continuation *apiLogBodyContinuation `json:"continuation"`
}

// projectToolResultsForTranscript replaces only explicit private API-log reads
// with a bounded re-read handle. The live history retains the complete output.
func projectToolResultsForTranscript(calls []llm.ToolCallData, results []tool.ExecResult, parts []llm.ContentPart) []llm.ContentPart {
	var projected []llm.ContentPart
	for i := range parts {
		if i >= len(calls) || i >= len(results) {
			continue
		}
		placeholder, ok := apiLogResultTranscriptPlaceholder(calls[i], results[i])
		if !ok || parts[i].ToolResult == nil {
			continue
		}
		if projected == nil {
			projected = append([]llm.ContentPart(nil), parts...)
		}
		toolResult := *parts[i].ToolResult
		toolResult.Content = placeholder
		projected[i].ToolResult = &toolResult
	}
	if projected == nil {
		return parts
	}
	return projected
}

func apiLogResultTranscriptPlaceholder(call llm.ToolCallData, result tool.ExecResult) (string, bool) {
	if call.Name != "read_session_transcript" && result.ToolName != "read_session_transcript" {
		return "", false
	}

	var resultIdentity apiLogTranscriptResultIdentity
	resultIsAPILog := json.Unmarshal([]byte(result.Output), &resultIdentity) == nil && resultIdentity.Source == apiLogSource
	var args map[string]any
	_ = json.Unmarshal(call.Arguments, &args)
	callIsAPILog := stringArg(args, "source") == apiLogSource || strings.TrimSpace(stringArg(args, "attempt_id")) != ""
	if !resultIsAPILog && !callIsAPILog {
		return "", false
	}
	if !resultIsAPILog {
		return `{"source":"api_log","private_evidence_omitted":true,"re_read":{"tool":"read_session_transcript","source":"api_log"}}`, true
	}

	reRead := apiLogTranscriptReadHandle{
		Tool:          "read_session_transcript",
		TranscriptRef: resultIdentity.TranscriptRef,
		Source:        apiLogSource,
		AttemptID:     resultIdentity.Attempt.AttemptID,
	}
	if resultIdentity.Body != nil {
		reRead.Body = resultIdentity.Body.Body
		reRead.OffsetBytes = resultIdentity.Body.OffsetBytes
	}
	placeholder := apiLogTranscriptPlaceholder{
		Source:                 apiLogSource,
		PrivateEvidenceOmitted: true,
		ReRead:                 reRead,
	}
	if resultIsAPILog && resultIdentity.Continuation != nil {
		continuation := apiLogTranscriptReadHandle{
			Tool:          "read_session_transcript",
			TranscriptRef: resultIdentity.TranscriptRef,
			Source:        apiLogSource,
			AttemptID:     resultIdentity.Continuation.AttemptID,
			Body:          resultIdentity.Continuation.Body,
			OffsetBytes:   resultIdentity.Continuation.OffsetBytes,
		}
		placeholder.Continuation = &continuation
	}
	encoded, err := json.Marshal(placeholder)
	if err != nil || len(encoded) > apiLogTranscriptPlaceholderMaxBytes {
		return `{"source":"api_log","private_evidence_omitted":true,"re_read":{"tool":"read_session_transcript","source":"api_log"}}`, true
	}
	return string(encoded), true
}

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
		Definition: tool.DefReadTranscript(), ReadOnly: true,
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			return execReadTranscript(deps, args)
		},
	}
}

var readTranscriptPublicRejectedParams = []string{"source", "attempt_id", "body", "max_bytes"}

type retainedReadArgs struct {
	Ref          string
	OffsetSet    bool
	OffsetBytes  int64
	OutputMatch  string
	ContextLines int
}

type retainedReadOperation uint8

const (
	retainedReadDefault retainedReadOperation = iota
	retainedReadPage
	retainedReadSearch
)

type retainedPageBody struct {
	OffsetBytes   int64  `json:"offset_bytes"`
	BytesReturned int64  `json:"bytes_returned"`
	TotalBytes    int64  `json:"total_bytes"`
	Encoding      string `json:"encoding"`
	Data          string `json:"data"`
}

type retainedPageEnvelope struct {
	TranscriptRef     string                `json:"transcript_ref"`
	Representation    string                `json:"representation"`
	ContentType       string                `json:"content_type"`
	Page              retainedPageBody      `json:"page"`
	RetainedStartByte int64                 `json:"retained_start_bytes"`
	JobStatus         string                `json:"job_status,omitempty"`
	Continuation      *retainedContinuation `json:"continuation,omitempty"`
}

type retainedSearchResult struct {
	TranscriptRef         string                      `json:"transcript_ref"`
	OutputMatch           string                      `json:"output_match"`
	ContextLines          int                         `json:"context_lines"`
	OffsetBytes           int64                       `json:"offset_bytes"`
	RetainedStartBytes    int64                       `json:"retained_start_bytes"`
	TotalBytes            int64                       `json:"total_bytes"`
	JobStatus             string                      `json:"job_status,omitempty"`
	SearchComplete        bool                        `json:"search_complete"`
	SkippedPartialPrefix  bool                        `json:"skipped_partial_prefix"`
	Matches               []retainedSearchMatch       `json:"matches"`
	SkippedOversizedLines []retainedSearchSkippedLine `json:"skipped_oversized_lines,omitempty"`
	Continuation          *retainedContinuation       `json:"continuation,omitempty"`
}

func execReadTranscript(deps *toolDeps, args map[string]any) (any, error) {
	for _, name := range readTranscriptPublicRejectedParams {
		if _, present := args[name]; present {
			if name == "source" || name == "attempt_id" || name == "body" {
				return nil, fmt.Errorf("invalid_request: %s is not supported by read_transcript; API-log inspection is available through evener-doctor apilog", name)
			}
			return nil, errors.New("invalid_request: max_bytes is not supported by read_transcript; expansion pages are fixed at 16 KiB and continue with offset_bytes")
		}
	}
	parsed, operation, parseIssues := parseRetainedReadArgsWithIssues(args)
	if strings.HasPrefix(parsed.Ref, "artifact:") {
		incompatible := retainedReadIncompatibleFields("artifact", args)
		if len(parseIssues) > 0 || len(incompatible) > 0 {
			if len(incompatible) == 0 {
				return nil, errors.New("invalid_request: " + parseIssues[0].Reason)
			}
			return nil, retainedReadArgsValidationError("artifact", args, incompatible, parseIssues, operation)
		}
		if operation == retainedReadSearch {
			return searchArtifactTranscript(deps, parsed)
		}
		return pageArtifactTranscript(deps, parsed)
	}
	if strings.HasPrefix(parsed.Ref, "job:") {
		incompatible := retainedReadIncompatibleFields("job", args)
		if len(parseIssues) > 0 || len(incompatible) > 0 {
			if len(incompatible) == 0 {
				return nil, errors.New("invalid_request: " + parseIssues[0].Reason)
			}
			return nil, retainedReadArgsValidationError("job", args, incompatible, parseIssues, operation)
		}
		if operation == retainedReadPage {
			return pageJobTranscript(deps, parsed)
		}
		if operation == retainedReadSearch {
			return searchJobTranscript(deps, parsed)
		}
		return readJobTranscript(deps, parsed.Ref, strings.TrimSpace(stringArg(args, "range")), strings.TrimSpace(stringArg(args, "format")))
	}
	if len(parseIssues) > 0 {
		return nil, errors.New("invalid_request: " + parseIssues[0].Reason)
	}
	if operation == retainedReadSearch {
		return nil, errors.New("invalid_request: output_match applies only to job: and artifact: refs")
	}
	return execReadSessionTranscript(deps, args)
}

func parseRetainedReadArgs(args map[string]any) (retainedReadArgs, retainedReadOperation, error) {
	parsed, operation, issues := parseRetainedReadArgsWithIssues(args)
	if len(issues) > 0 {
		return retainedReadArgs{}, retainedReadDefault, errors.New("invalid_request: " + issues[0].Reason)
	}
	return parsed, operation, nil
}

type retainedReadParseIssue struct {
	Field  string
	Reason string
}

func parseRetainedReadArgsWithIssues(args map[string]any) (retainedReadArgs, retainedReadOperation, []retainedReadParseIssue) {
	parsed := retainedReadArgs{
		Ref:         strings.TrimSpace(stringArg(args, "transcript_ref")),
		OutputMatch: stringArg(args, "output_match"),
	}
	_, outputMatchSet := args["output_match"]
	_, contextSet := args["context_lines"]
	issues := make([]retainedReadParseIssue, 0, 3)
	if contextSet && !outputMatchSet {
		issues = append(issues, retainedReadParseIssue{Field: "context_lines", Reason: "context_lines requires output_match"})
	}
	if value := optionalIntArg(args, "context_lines"); value != nil {
		parsed.ContextLines = *value
	}
	if parsed.ContextLines < 0 || parsed.ContextLines > 10 {
		issues = append(issues, retainedReadParseIssue{Field: "context_lines", Reason: "context_lines must be between 0 and 10"})
	}
	if utf8.RuneCountInString(parsed.OutputMatch) > retainedOutputMatchMaxChars {
		issues = append(issues, retainedReadParseIssue{Field: "output_match", Reason: fmt.Sprintf("output_match must be at most %d characters", retainedOutputMatchMaxChars)})
	}
	if value := optionalIntArg(args, "offset_bytes"); value != nil {
		parsed.OffsetSet = true
		parsed.OffsetBytes = int64(*value)
	}
	if parsed.OffsetSet && parsed.OffsetBytes < 0 {
		issues = append(issues, retainedReadParseIssue{Field: "offset_bytes", Reason: "offset_bytes must be non-negative"})
	}
	if outputMatchSet {
		return parsed, retainedReadSearch, issues
	}
	if parsed.OffsetSet {
		return parsed, retainedReadPage, issues
	}
	return parsed, retainedReadDefault, issues
}

func validateArtifactReadArgs(args map[string]any, operation retainedReadOperation) error {
	incompatible := retainedReadIncompatibleFields("artifact", args)
	if len(incompatible) > 0 {
		return retainedReadArgsValidationError("artifact", args, incompatible, nil, operation)
	}
	return nil
}

func validateJobReadArgs(args map[string]any, operation retainedReadOperation) error {
	incompatible := retainedReadIncompatibleFields("job", args)
	if len(incompatible) > 0 {
		return retainedReadArgsValidationError("job", args, incompatible, nil, operation)
	}
	return nil
}

func retainedReadIncompatibleFields(refKind string, args map[string]any) []string {
	incompatible := make([]string, 0, 3)
	for _, name := range []string{"range", "expand_turn", "format"} {
		value, present := args[name]
		if !present || (name == "format" && refKind == "job" && isNeutralJobFormat(value)) {
			continue
		}
		incompatible = append(incompatible, name)
	}
	return incompatible
}

// retainedReadArgsValidationError is deliberately diagnostic: tool results are
// retained as repair telemetry, so include every parse and mode incompatibility
// exactly as received and a smallest valid call instead of naming only one key.
func retainedReadArgsValidationError(refKind string, args map[string]any, names []string, parseIssues []retainedReadParseIssue, operation retainedReadOperation) error {
	received := make([]string, 0, len(names)+len(parseIssues))
	reasons := make([]string, 0, len(names)+len(parseIssues))
	receivedFields := make(map[string]bool, len(names)+len(parseIssues))
	appendReceived := func(name string) {
		if receivedFields[name] {
			return
		}
		receivedFields[name] = true
		encoded, err := json.Marshal(args[name])
		if err != nil {
			encoded = []byte(fmt.Sprintf("%#v", args[name]))
		}
		received = append(received, fmt.Sprintf("%s=%s", name, encoded))
	}
	for _, issue := range parseIssues {
		reasons = append(reasons, issue.Reason)
		appendReceived(issue.Field)
	}
	for _, name := range names {
		appendReceived(name)
		switch name {
		case "range", "expand_turn":
			reasons = append(reasons, fmt.Sprintf("%s applies only to session transcript refs", name))
		case "format":
			if refKind == "artifact" {
				reasons = append(reasons, "format is not supported for artifact: refs")
			} else if operation != retainedReadDefault {
				reasons = append(reasons, "format cannot be combined with offset_bytes or output_match on job: refs")
			} else {
				reasons = append(reasons, "job: refs support only format=markdown")
			}
		}
	}
	if refKind == "artifact" {
		return fmt.Errorf("invalid_request: %s; incompatible fields: %s; minimal valid call: {\"transcript_ref\":\"artifact:<id>\"}", strings.Join(reasons, "; "), strings.Join(received, ", "))
	}
	return fmt.Errorf("invalid_request: %s; incompatible fields: %s; minimal valid call: {\"transcript_ref\":\"job:<job_id>\"}", strings.Join(reasons, "; "), strings.Join(received, ", "))
}

func compileOutputMatch(expression string) (*regexp.Regexp, error) {
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("invalid_request: output_match is not valid RE2: %w", err)
	}
	return compiled, nil
}

func validArtifactTranscriptRef(ref string) bool {
	const prefix = "artifact:"
	const hexLength = 32
	if len(ref) != len(prefix)+hexLength || !strings.HasPrefix(ref, prefix) {
		return false
	}
	for _, c := range ref[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func openArtifactTranscript(deps *toolDeps, ref string) (artifactReadSeekCloser, int64, error) {
	if !validArtifactTranscriptRef(ref) {
		return nil, 0, errors.New("invalid_request: artifact transcript_ref must be a valid artifact:<id>")
	}
	if deps == nil || deps.openArtifact == nil {
		return nil, 0, errors.New("artifact_expired: artifact is not available in this root session tree")
	}
	reader, err := deps.openArtifact(ref)
	if err != nil {
		switch {
		case errors.Is(err, artifactstore.ErrInvalidRef):
			return nil, 0, errors.New("invalid_request: artifact transcript_ref must be a valid artifact:<id>")
		case errors.Is(err, artifactstore.ErrExpired):
			return nil, 0, errors.New("artifact_expired: artifact is not available in this root session tree")
		default:
			return nil, 0, errors.New("artifact_unavailable: retained artifact could not be opened")
		}
	}
	total, err := reader.Seek(0, io.SeekEnd)
	if err != nil || total < 0 {
		_ = reader.Close()
		return nil, 0, errors.New("artifact_unavailable: retained artifact size could not be read")
	}
	return reader, total, nil
}

func pageArtifactTranscript(deps *toolDeps, args retainedReadArgs) (any, error) {
	reader, total, err := openArtifactTranscript(deps, args.Ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	offset := args.OffsetBytes
	page, err := readRetainedPage(reader, 0, total, offset)
	if err != nil {
		if errors.Is(err, errRetainedOffsetOutOfRange) {
			return nil, fmt.Errorf("invalid_request: offset_bytes %d is beyond EOF %d; valid byte interval is [0,%d]", offset, total, total)
		}
		return nil, errors.New(artifactUnavailableReadError)
	}
	return retainedPageResult(args.Ref, 0, "", page), nil
}

type artifactSearchSource struct {
	reader io.ReaderAt
	total  int64
}

func (s artifactSearchSource) ReadWindow(offset int64, maxBytes int) (jobstore.OutputWindowSnapshot, error) {
	snapshot := jobstore.OutputWindowSnapshot{Start: offset, End: offset, TotalBytes: s.total}
	if offset < 0 || offset > s.total {
		return snapshot, jobstore.ErrInvalidOffset
	}
	if maxBytes < 0 {
		return snapshot, jobstore.ErrInvalidLimit
	}
	end := min(s.total, offset+int64(maxBytes))
	if end < offset {
		end = s.total
	}
	snapshot.End = end
	snapshot.Content = make([]byte, int(end-offset))
	if len(snapshot.Content) > 0 {
		read, err := s.reader.ReadAt(snapshot.Content, offset)
		if err != nil && (!errors.Is(err, io.EOF) || read != len(snapshot.Content)) {
			return jobstore.OutputWindowSnapshot{}, fmt.Errorf("read artifact search window: %w", err)
		}
		if read != len(snapshot.Content) {
			return jobstore.OutputWindowSnapshot{}, io.ErrUnexpectedEOF
		}
	}
	snapshot.Truncated = offset > 0 || end < s.total
	return snapshot, nil
}

func searchArtifactTranscript(deps *toolDeps, args retainedReadArgs) (any, error) {
	compiled, err := compileOutputMatch(args.OutputMatch)
	if err != nil {
		return nil, err
	}
	reader, total, err := openArtifactTranscript(deps, args.Ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	if args.OffsetBytes > total {
		return nil, fmt.Errorf("invalid_request: offset_bytes %d is beyond EOF %d; valid byte interval is [0,%d]", args.OffsetBytes, total, total)
	}
	result, err := searchRetainedOutput(artifactSearchSource{reader: reader, total: total}, retainedSearchOptions{
		Regexp:       compiled,
		StartOffset:  args.OffsetBytes,
		ContextLines: args.ContextLines,
	})
	if err != nil {
		return nil, errors.New(artifactUnavailableReadError)
	}
	return boundedRetainedSearchResult(args, "", result)
}

func pageJobTranscript(deps *toolDeps, args retainedReadArgs) (any, error) {
	if deps == nil {
		// Preserve the legacy nil-dependency validation contract used by callers
		// that have no job reader at all. Real registered tools always provide deps.
		return nil, errors.New("invalid_request: offset_bytes applies only to session transcript refs when the job transcript reader is unavailable")
	}
	jobID, err := parseJobTranscriptID(args.Ref)
	if err != nil {
		return nil, err
	}
	target, err := locateLocalJobRetainedTarget(deps.stateDir, jobID)
	if err != nil {
		return nil, err
	}
	snapshot, err := (localJobSearchSource{target: target}).ReadWindow(args.OffsetBytes, retainedOutputPageBytes)
	if err != nil {
		return nil, err
	}
	page, err := readRetainedPage(bytes.NewReader(snapshot.Content), snapshot.Start, snapshot.TotalBytes, args.OffsetBytes)
	if err != nil {
		return nil, err
	}
	return retainedPageResult(args.Ref, snapshot.RetainedStart, localJobEnvelopeStatus(target.Record), page), nil
}

func searchJobTranscript(deps *toolDeps, args retainedReadArgs) (any, error) {
	if deps == nil {
		return nil, errors.New("job transcript unavailable: job manager is not available")
	}
	jobID, err := parseJobTranscriptID(args.Ref)
	if err != nil {
		return nil, err
	}
	compiled, err := compileOutputMatch(args.OutputMatch)
	if err != nil {
		return nil, err
	}
	target, err := locateLocalJobRetainedTarget(deps.stateDir, jobID)
	if err != nil {
		return nil, err
	}
	metadata, err := readLocalJobRetainedMetadata(target)
	if err != nil {
		return nil, err
	}
	offset := args.OffsetBytes
	if !args.OffsetSet {
		offset = metadata.RetainedStart
	}
	if offset < metadata.RetainedStart {
		return nil, fmt.Errorf("output_unavailable: job %q offset %d is no longer retained; first available offset is %d", jobID, offset, metadata.RetainedStart)
	}
	if offset > metadata.TotalBytes {
		return nil, fmt.Errorf(
			"invalid_request: offset_bytes %d is beyond EOF %d; valid byte interval is [%d,%d]; job_status=%s",
			offset, metadata.TotalBytes, metadata.RetainedStart, metadata.TotalBytes, localJobEnvelopeStatus(target.Record),
		)
	}
	result, err := searchRetainedOutput(localJobSearchSource{target: target}, retainedSearchOptions{
		Regexp:            compiled,
		StartOffset:       offset,
		ContextLines:      args.ContextLines,
		SkipPartialPrefix: metadata.RetainedStartPartial && offset == metadata.RetainedStart,
		DeferEOFFragment:  target.Record != nil && !target.Record.Status.IsTerminal(),
	})
	if err != nil {
		return nil, err
	}
	args.OffsetBytes = offset
	return boundedRetainedSearchResult(args, localJobEnvelopeStatus(target.Record), result)
}

func parseJobTranscriptID(ref string) (string, error) {
	jobID := strings.TrimSpace(strings.TrimPrefix(ref, "job:"))
	if jobID == "" {
		return "", errors.New("invalid_request: job transcript_ref must be job:<job_id>")
	}
	return jobID, nil
}

func retainedPageResult(ref string, retainedStart int64, jobStatus string, page retainedPage) retainedPageEnvelope {
	return retainedPageEnvelope{
		TranscriptRef:  ref,
		Representation: "raw_bytes",
		ContentType:    "text/plain",
		Page: retainedPageBody{
			OffsetBytes:   page.OffsetBytes,
			BytesReturned: page.BytesReturned,
			TotalBytes:    page.TotalBytes,
			Encoding:      page.Encoding,
			Data:          page.Data,
		},
		RetainedStartByte: retainedStart,
		JobStatus:         jobStatus,
		Continuation:      page.Continuation,
	}
}

func retainedSearchResultFor(args retainedReadArgs, jobStatus string, result retainedSearchEnvelope) retainedSearchResult {
	for i := range result.Matches {
		if result.Matches[i].Before == nil {
			result.Matches[i].Before = make([]string, 0)
		}
		if result.Matches[i].After == nil {
			result.Matches[i].After = make([]string, 0)
		}
	}
	return retainedSearchResult{
		TranscriptRef:         args.Ref,
		OutputMatch:           args.OutputMatch,
		ContextLines:          args.ContextLines,
		OffsetBytes:           result.OffsetBytes,
		RetainedStartBytes:    result.RetainedStartBytes,
		TotalBytes:            result.TotalBytes,
		JobStatus:             jobStatus,
		SearchComplete:        result.SearchComplete,
		SkippedPartialPrefix:  result.SkippedPartialPrefix,
		Matches:               result.Matches,
		SkippedOversizedLines: result.SkippedOversized,
		Continuation:          result.Continuation,
	}
}

func boundedRetainedSearchResult(args retainedReadArgs, jobStatus string, result retainedSearchEnvelope) (retainedSearchResult, error) {
	response := retainedSearchResultFor(args, jobStatus, result)
	encoded, err := json.Marshal(response)
	if err != nil {
		return retainedSearchResult{}, errors.New("retained search response could not be encoded")
	}
	if utf8.RuneCount(encoded) >= transcriptToolMaxChars {
		return retainedSearchResult{}, errors.New("invalid_request: output_match is too large for a bounded search response")
	}
	return response, nil
}

// execReadSessionTranscript validates the source-specific arguments before it
// resolves or opens either transcript artifact.
func execReadSessionTranscript(deps *toolDeps, args map[string]any) (any, error) {
	return execReadSessionTranscriptWithContext(context.Background(), deps, args)
}

func execReadSessionTranscriptWithContext(ctx context.Context, deps *toolDeps, args map[string]any) (any, error) {
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
			return readAPILogAttempt(ctx, apiLogPathForTranscript(path), ref, parsed.AttemptID, parsed.Body, parsed.OffsetBytes, parsed.MaxBytes)
		}
		return readAPILogSummary(ctx, apiLogPathForTranscript(path), ref, parsed.Range)
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
		parsed.Source = transcriptSource
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
		if parsed.Body != "request" && parsed.Body != "response" && parsed.Body != "request_headers" {
			return readSessionTranscriptArgs{}, fmt.Errorf("invalid_request: body %q is not supported: use request, response, or request_headers", parsed.Body)
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
	if deps == nil {
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
	snap, err := readLocalJobSnapshot(deps.stateDir, jobID, maxJobOutputRetentionBytes)
	if err != nil {
		return nil, err
	}
	envelope := readMarkdownEnvelope{
		TranscriptRef: ref,
		Format:        formatMarkdown,
		ContentType:   "text/markdown",
		Content:       renderJobTranscript(snap.Record, snap.Content, snap.TotalBytes, snap.DroppedBytes),
		Meta: readMarkdownMeta{
			TurnsTotal:    1,
			Range:         "shell-log",
			TurnsRendered: 1,
			Truncated:     snap.Truncated || snap.DroppedBytes > 0 || snap.TotalBytes > int64(len([]byte(snap.Content))),
		},
	}
	return boundReadMarkdownEnvelopeWithHint(envelope, jobTranscriptTruncationNotice)
}

// renderJobTranscript renders one job:<job_id> read. A delegate's retained
// output is its final report, it has no command, and it may carry a
// structured_result, so it gets its own heading; everything else is a shell
// job's process log.
func renderJobTranscript(rec *jobstore.JobRecord, output string, total, dropped int64) string {
	if rec != nil && string(rec.Type) == "delegate" {
		return renderDelegateJobTranscript(rec, output, total, dropped)
	}
	return renderShellJobTranscript(rec, output, total, dropped)
}

// renderDelegateJobTranscript renders a delegate job's report. The
// structured_result is appended as JSON with its validity flag, so one read
// carries both the report text and the machine-readable result together with
// whether it parsed. It deliberately omits the delegate's transcript_ref: this
// view reports one job's evidence, not the child's separate conversation.
func renderDelegateJobTranscript(rec *jobstore.JobRecord, output string, total, dropped int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Delegate Job %s\n\n", rec.JobID)
	if rec.Status != "" {
		fmt.Fprintf(&b, "- status: %s\n", rec.Status)
	}
	if rec.Reason != "" {
		fmt.Fprintf(&b, "- reason: %s\n", rec.Reason)
	}
	if task := strings.TrimSpace(rec.Task); task != "" {
		fmt.Fprintf(&b, "- task: %s\n", strings.ReplaceAll(task, "\n", " "))
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
	if rec.StructuredResult != nil {
		if encoded, err := json.Marshal(rec.StructuredResult); err == nil {
			valid := rec.StructuredResultValid != nil && *rec.StructuredResultValid
			fmt.Fprintf(&b, "\nstructured_result (valid=%v): %s\n", valid, encoded)
		}
	}
	if reason := strings.TrimSpace(rec.StructuredResultReason); reason != "" {
		fmt.Fprintf(&b, "structured_result_reason: %s\n", reason)
	}
	return b.String()
}

func renderShellJobTranscript(rec *jobstore.JobRecord, output string, total, dropped int64) string {
	var b strings.Builder
	jobID := ""
	status := ""
	reason := ""
	command := ""
	if rec != nil {
		jobID = rec.JobID
		status = string(rec.Status)
		reason = rec.Reason
		command = rec.Command
	}
	fmt.Fprintf(&b, "# Shell Job %s\n\n", jobID)
	if status != "" {
		fmt.Fprintf(&b, "- status: %s\n", status)
	}
	if reason != "" {
		fmt.Fprintf(&b, "- reason: %s\n", reason)
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

func transcriptExpansionJSONL(data transcriptData, pin int) ([]byte, error) {
	if pin < 0 || pin >= len(data.Entries) {
		return nil, fmt.Errorf("invalid_request: expand_turn %d does not identify a transcript turn", pin)
	}
	if data.Entries[pin].Turn.Kind == schema.TurnAttentionResolution {
		return nil, fmt.Errorf("invalid_request: expand_turn %d does not identify a public transcript turn", pin)
	}
	if len(data.EntryLines) != len(data.Entries) {
		return nil, errors.New("transcript expansion unavailable: persisted entries are not retained")
	}

	first, last, ok := resolvePinnedSpan(data.Entries, pin)
	if !ok {
		return nil, fmt.Errorf("invalid_request: expand_turn %d does not identify a transcript turn", pin)
	}
	exact := make([]byte, 0)
	for _, line := range data.EntryLines[first : last+1] {
		exact = append(exact, line...)
		exact = append(exact, '\n')
	}
	return exact, nil
}

// publicTranscriptEntry strips correlation metadata that is durable only for
// crash recovery. Resolution markers carry no model/public content and are
// omitted entirely so interleaved tool calls and results remain adjacent.
func publicTranscriptEntry(entry transcript.Entry) (transcript.Entry, bool) {
	if entry.Turn.Kind == schema.TurnAttentionResolution {
		return transcript.Entry{}, false
	}
	entry.Turn.AttentionID = ""
	entry.Turn.AttentionResolution = nil
	entry.Turn.DelegateDeliveryCommits = nil
	return entry, true
}

func publicTranscriptEntries(entries []transcript.Entry) []transcript.Entry {
	public := make([]transcript.Entry, 0, len(entries))
	for _, entry := range entries {
		entry, include := publicTranscriptEntry(entry)
		if !include {
			continue
		}
		entry.Seq = len(public)
		public = append(public, entry)
	}
	return public
}

func publicTranscriptData(data transcriptData) transcriptData {
	public := transcriptData{Header: data.Header, Skipped: data.Skipped}
	retainLines := len(data.EntryLines) == len(data.Entries)
	public.Entries = make([]transcript.Entry, 0, len(data.Entries))
	if retainLines {
		public.EntryLines = make([][]byte, 0, len(data.EntryLines))
	}
	for i, entry := range data.Entries {
		entry, include := publicTranscriptEntry(entry)
		if !include {
			continue
		}
		entry.Seq = len(public.Entries)
		public.Entries = append(public.Entries, entry)
		if retainLines {
			line, include, err := publicTranscriptLine(data.EntryLines[i], entry.Seq)
			if err != nil || !include {
				// The retained line was strictly decoded into entry above, and its
				// kind already passed the same inclusion check.
				panic("validated transcript entry could not be projected publicly")
			}
			public.EntryLines = append(public.EntryLines, line)
		}
	}
	return public
}

func publicTranscriptLine(line []byte, seq int) ([]byte, bool, error) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, false, fmt.Errorf("decode public transcript entry: %w", err)
	}
	var turn map[string]json.RawMessage
	if err := json.Unmarshal(entry["turn"], &turn); err != nil {
		return nil, false, fmt.Errorf("decode public transcript turn: %w", err)
	}
	var kind schema.TurnKind
	if err := json.Unmarshal(turn["kind"], &kind); err != nil {
		return nil, false, fmt.Errorf("decode public transcript turn kind: %w", err)
	}
	if kind == schema.TurnAttentionResolution {
		return nil, false, nil
	}
	delete(turn, "attention_id")
	delete(turn, "attention_resolution")
	delete(turn, "delegate_delivery_commits")
	encodedTurn, err := json.Marshal(turn)
	if err != nil {
		return nil, false, fmt.Errorf("encode public transcript turn: %w", err)
	}
	entry["turn"] = encodedTurn
	entry["seq"], err = json.Marshal(seq)
	if err != nil {
		return nil, false, fmt.Errorf("encode public transcript sequence: %w", err)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return nil, false, fmt.Errorf("encode public transcript entry: %w", err)
	}
	return encoded, true, nil
}

// readMarkdownPage builds the markdown envelope for the resolved transcript.
// When rangeArg is non-empty and malformed, it falls back to the smart default
// range, sets meta.range_warning, and surfaces the warning in the content. Valid
// ranges (including empty) produce no warning.
//
// When turns_rendered < turns_total, a self-announcing window line is spliced
// after the document header so a default read never silently masquerades as the
// whole session.
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
	data = publicTranscriptData(data)

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
		end := min(offsetBytes+maxBytes, len(exact))
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
	data = publicTranscriptData(data)

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
	entries = publicTranscriptEntries(entries)

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
			Hint:                "semantic transcript-v2 NDJSON; system-prompt and provider API data are excluded. For comprehension use format=markdown.",
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
