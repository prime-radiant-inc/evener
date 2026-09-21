package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// Evidence-Preserving Reducer (SoL-Pi auto-research design, mechanism 4):
// behind the per-session LogReducer flag, a shell command from a declared
// build/test command set whose observation reaches 4 KiB is archived to the
// session artifact store, and the observation the model receives is replaced
// by a receipt extracted by the configured cheap model (the --fast-cheap-model
// side channel) and verified by deterministic code. The verifier checks the
// receipt's schema, that its command and exit status match the executed call,
// its source hash against the full log, that every quoted line appears
// verbatim in the log, that it carries no suspected credentials, and that it
// is strictly smaller than the log. Any failure, suspected credentials (in
// the receipt, the command, or the log's extraction window), or no size
// reduction falls back to the original log, byte-identical.
//
// The seam is record-time: persistToolResults calls reduceLogObservation while
// assembling the round's tool-result parts, so every later request carries the
// receipt from history. The durable transcript is not reduced —
// projectToolResultsForTranscript restores the full original log for a
// receipt-bearing part — because the archive is process-scoped and the
// transcript is the session's durable evidence record; restored sessions
// replay the original log. ObservationPack recognizes receipts and skips them
// (they are already small; never double-process).
//
// Receipt extraction runs only when a cheap model is explicitly configured
// (provider.Profile.ConfiguredCheapModel, set by --fast-cheap-model): the
// design names the configured cheap model as the extractor, so unconfigured
// means no extraction attempt and no provider call — the original log is used,
// silently, as its own default state.
const (
	// logReducerMinBytes is the reduction threshold: build/test logs of 4 KiB
	// and up are reducer-eligible (the spec's "4 KiB and up").
	logReducerMinBytes = 4 * 1024
	// logReceiptMarker prefixes every rendered receipt; ObservationPack and
	// the transcript projection recognize receipts by it.
	logReceiptMarker = "[log-reduced receipt:"
	// logReducerExtractionTimeout bounds one receipt-extraction call.
	logReducerExtractionTimeout = 30 * time.Second
	// logReducerMaxQuotedLines caps how many evidence lines one receipt quotes.
	logReducerMaxQuotedLines = 40
	// logReducerMaxQuotedLineBytes caps one quoted line; a longer "line" is
	// not evidence a decision needs, and unbounded lines could defeat the
	// strict size reduction.
	logReducerMaxQuotedLineBytes = 512
	// logReducerMaxSummaryRunes caps the receipt summary.
	logReducerMaxSummaryRunes = 600
	// logReducerExtractWindowBytes is the head and tail byte window of the
	// log sent to the cheap model for extraction. A quoted line from the
	// elided middle would fail verification and fall back, so the window
	// bounds the extraction cost without bounding honesty. The credential
	// pre-scan covers exactly these bytes: what is transmitted is what is
	// scanned (logReducerExtractionWindowCredential).
	logReducerExtractWindowBytes = 8 * 1024
)

// logReducerCommandRe is the declared command set: the build/test commands
// whose logs the reducer may replace. It is a byte-for-byte mirror of the
// research oracle's researchTestBuildRe (agent/research/oracle.go), so the
// oracle's log-reducer projection keeps ranking this mechanism against
// exactly the commands it reduces. The two must stay in sync;
// TestLogReducerCommandRegexMirrorsOracleLiteral pins the literal and
// TestLogReducerDeclaredSetMatchesOracle pins the behavior through the
// read-only oracle itself.
var logReducerCommandRe = regexp.MustCompile(`\b(go test|go build|go vet|make(?:\s+\S+)?|npm test|npm run (?:test|build)|pytest|cargo (?:test|build))\b`)

// logReducerCredentialRes are the high-precision credential shapes the
// reducer refuses to let a receipt carry or sit next to. A false positive
// only costs one reduction (the original log flows unchanged); a false
// negative could leak a secret into a durable receipt. The patterns name
// fixed token shapes and literal keyword assignments, not entropy heuristics.
var logReducerCredentialRes = []*regexp.Regexp{
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                                                                    // AWS access key id
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),                                                                      // private key block
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),                                                                            // GitHub token
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),                                                                          // Slack token
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`),                                                                                // Google API key
	regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`),                                                                  // OpenAI/Anthropic key
	regexp.MustCompile(`\bBearer\s+[A-Za-z0-9_./+=-]{16,}`),                                                                       // bearer credential
	regexp.MustCompile(`(?i)\b[a-z0-9_]*(?:password|passwd|secret|token|api_?key)[a-z0-9_]*\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{8,}`), // credential assignment
}

// credentialPatternNames lines up with logReducerCredentialRes for safe
// warning text: a hit is reported by the shape's name, never by the matched
// text (the match may be the secret itself).
var credentialPatternNames = []string{
	"aws access key id",
	"private key block",
	"github token",
	"slack token",
	"google api key",
	"provider api key",
	"bearer credential",
	"credential assignment",
}

// logReducerCredentialHit reports the name of the first credential shape
// matching s, or "" when s looks clean.
func logReducerCredentialHit(s string) string {
	for i, re := range logReducerCredentialRes {
		if re.MatchString(s) {
			return credentialPatternNames[i]
		}
	}
	return ""
}

// logReducerExtractionWindowCredential scans exactly the bytes the receipt
// extraction would transmit to the cheap-model provider — the whole log when
// it fits in the 16 KiB window budget, else the head and tail 8 KiB — for
// suspected credentials, returning the pattern name of a hit or "". A hit
// aborts the reduction before anything is archived or requested, so
// credential bytes never ride the extraction prompt. Bytes outside the
// window are never transmitted: their archive and transcript exposure is
// the status quo of the original log the model already saw.
func logReducerExtractionWindowCredential(source string) string {
	return logReducerCredentialHit(logReceiptExtractionWindow(source))
}

// logReceiptExtraction is the cheap model's answer: the evidence it chose and
// its echo of the call's identity. Command and exit status are verified against
// ground truth — they catch an extraction that was not looking at this log.
type logReceiptExtraction struct {
	Command     string
	ExitStatus  int
	QuotedLines []string
	Summary     string
}

// logReceipt is the assembled receipt: the verified extraction plus the
// authoritative metadata the harness computes itself (source hash, original
// size, archive reference), so the model can never forge them.
type logReceipt struct {
	Command       string
	ExitStatus    int
	SourceHash    string
	OriginalBytes int
	QuotedLines   []string
	Summary       string
	ArchiveRef    string
}

// isLogReceiptContent reports whether content is a rendered log-reducer
// receipt. ObservationPack and the transcript projection recognize receipts
// by this prefix.
func isLogReceiptContent(content string) bool {
	return strings.HasPrefix(content, logReceiptMarker)
}

// renderLogReceipt renders the deterministic model-facing receipt text. The
// render is pure: every field comes from the verified receipt, nothing from
// the model's raw answer.
func renderLogReceipt(r *logReceipt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s full build/test log archived; original %d bytes; sha256:%s]\n", logReceiptMarker, r.OriginalBytes, r.SourceHash)
	fmt.Fprintf(&b, "command: %s\n", r.Command)
	fmt.Fprintf(&b, "exit status: %d\n", r.ExitStatus)
	fmt.Fprintf(&b, "summary: %s\n", r.Summary)
	b.WriteString("quoted evidence lines (each appears verbatim in the original log):\n")
	for _, line := range r.QuotedLines {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "full log: %s\n", r.ArchiveRef)
	fmt.Fprintf(&b, "read with: read_transcript(transcript_ref=%q)\n", r.ArchiveRef)
	return strings.TrimRight(b.String(), "\n")
}

// logReceiptSourceLineSet indexes the source's lines for quoted-line
// verification, trimming trailing whitespace so a CRLF or trailing-space
// difference does not fail an otherwise verbatim quote.
func logReceiptSourceLineSet(source string) map[string]struct{} {
	lines := strings.Split(source, "\n")
	set := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		set[strings.TrimRight(line, " \t\r")] = struct{}{}
	}
	return set
}

// verifyLogReceipt is the deterministic verifier: it checks the receipt's
// schema (every required field present and within its caps), that the command
// and exit status match the executed call, that the source hash matches the
// full log, that the original size is honest, that every quoted line appears
// verbatim as a line of the log, that the rendered receipt carries no
// suspected credentials, and that it is strictly smaller than the log. Any
// error means fall back to the original log.
func verifyLogReceipt(r *logReceipt, source, command string, exitStatus int) error {
	if r == nil {
		return errors.New("log reducer: no receipt")
	}
	if strings.TrimSpace(r.Command) == "" {
		return errors.New("log reducer: receipt command is empty")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("log reducer: receipt summary is empty")
	}
	// A line break in the summary would let model-provided text inject
	// pseudo-field lines (a bogus "full log:" or "read with:" line, say)
	// into the rendered receipt. Quoted lines cannot do this: an embedded
	// newline can never match a whole source line.
	if strings.ContainsAny(r.Summary, "\n\r") {
		return errors.New("log reducer: receipt summary contains a line break")
	}
	if utf8.RuneCountInString(r.Summary) > logReducerMaxSummaryRunes {
		return errors.New("log reducer: receipt summary exceeds the rune cap")
	}
	if len(r.QuotedLines) == 0 {
		return errors.New("log reducer: receipt quotes no lines")
	}
	if len(r.QuotedLines) > logReducerMaxQuotedLines {
		return errors.New("log reducer: receipt quotes too many lines")
	}
	if r.Command != command {
		return errors.New("log reducer: receipt command does not match the executed command")
	}
	if r.ExitStatus != exitStatus {
		return errors.New("log reducer: receipt exit status does not match the command's exit status")
	}
	sum := sha256.Sum256([]byte(source))
	if r.SourceHash != hex.EncodeToString(sum[:]) {
		return errors.New("log reducer: receipt source hash does not match the full log")
	}
	if r.OriginalBytes != len(source) {
		return errors.New("log reducer: receipt original size does not match the full log")
	}
	if r.ArchiveRef == "" {
		return errors.New("log reducer: receipt lacks the archive reference")
	}
	sourceLines := logReceiptSourceLineSet(source)
	for _, line := range r.QuotedLines {
		if strings.TrimSpace(line) == "" {
			return errors.New("log reducer: receipt quotes an empty line")
		}
		if len(line) > logReducerMaxQuotedLineBytes {
			return errors.New("log reducer: receipt quotes an oversized line")
		}
		if _, ok := sourceLines[strings.TrimRight(line, " \t\r")]; !ok {
			return errors.New("log reducer: receipt quotes a line that is not in the log")
		}
	}
	rendered := renderLogReceipt(r)
	if hit := logReducerCredentialHit(rendered); hit != "" {
		return fmt.Errorf("log reducer: receipt carries a suspected credential (%s)", hit)
	}
	if len(rendered) >= len(source) {
		return errors.New("log reducer: receipt is not smaller than the full log")
	}
	return nil
}

// logReducerCandidate is the pure eligibility predicate: only a shell call
// from the declared command set whose complete, successful-dispatch,
// foreground result reaches logReducerMinBytes is a reduction candidate. It
// returns the executed command and its exit status. Truncated results (the
// registry's output limit), windowed and backgrounded jobs (the full output
// lives in the job, not the message), and results without a structured exit
// status decline: the receipt's hash and quoted lines must describe the whole
// log the model would otherwise see.
func logReducerCandidate(call llm.ToolCallData, res tool.ExecResult) (command string, exitStatus int, ok bool) {
	if call.Name != "shell" {
		return "", 0, false
	}
	if res.IsError || res.Truncated || res.PrevalOnly {
		return "", 0, false
	}
	if len(res.Output) < logReducerMinBytes {
		return "", 0, false
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", 0, false
	}
	command = strings.TrimSpace(stringArg(args, "command"))
	if command == "" || !logReducerCommandRe.MatchString(command) {
		return "", 0, false
	}
	var state shellToolResult
	if err := json.Unmarshal(res.ToolState, &state); err != nil {
		return "", 0, false
	}
	if state.Mode != string(shellModeForeground) || state.ExitCode == nil || state.JobID != "" {
		return "", 0, false
	}
	if state.Truncated != nil && *state.Truncated {
		return "", 0, false
	}
	if state.DroppedBytes > 0 {
		return "", 0, false
	}
	return command, *state.ExitCode, true
}

// reduceLogObservation runs the reducer for one tool result and returns the
// rendered receipt, or "" to fall back to the original log. The order is the
// safety order: credential aborts (the command, then the log's extraction
// window — exactly the bytes the extraction prompt would transmit) happen
// before anything is archived or any provider request is made; the archive
// precedes the extraction so a verified receipt always has a retrievable
// original behind it.
func (s *Session) reduceLogObservation(ctx context.Context, call llm.ToolCallData, res tool.ExecResult) string {
	if !s.cfg.LogReducer {
		return ""
	}
	command, exitStatus, ok := logReducerCandidate(call, res)
	if !ok {
		return ""
	}
	if hit := logReducerCredentialHit(command); hit != "" {
		s.warnLogReducerFallback("the command carries a suspected credential: " + hit)
		return ""
	}
	if hit := logReducerExtractionWindowCredential(res.Output); hit != "" {
		s.warnLogReducerFallback("the log's extraction window carries a suspected credential: " + hit)
		return ""
	}
	profile := s.currentProfile()
	if profile == nil || profile.ConfiguredCheapModel() == "" {
		// Unconfigured cheap model: the documented inert state. No
		// extraction, no provider call, no warning — the original log is
		// this session's default.
		return ""
	}
	if s.artifactStore == nil {
		s.warnLogReducerFallback("no artifact store; the full log cannot be archived")
		return ""
	}
	ref, err := s.artifactStore.Put([]byte(res.Output))
	if err != nil {
		s.warnLogReducerFallback("archiving the full log failed")
		return ""
	}
	extraction, err := s.extractLogReceipt(ctx, profile, res.Output, command, exitStatus)
	if err != nil {
		s.warnLogReducerFallback("receipt extraction failed: " + err.Error())
		return ""
	}
	sum := sha256.Sum256([]byte(res.Output))
	r := &logReceipt{
		Command:       extraction.Command,
		ExitStatus:    extraction.ExitStatus,
		SourceHash:    hex.EncodeToString(sum[:]),
		OriginalBytes: len(res.Output),
		QuotedLines:   extraction.QuotedLines,
		Summary:       extraction.Summary,
		ArchiveRef:    ref,
	}
	if err := verifyLogReceipt(r, res.Output, command, exitStatus); err != nil {
		s.warnLogReducerFallback("receipt verification failed: " + err.Error())
		return ""
	}
	return renderLogReceipt(r)
}

// warnLogReducerFallback surfaces one fallback reason as a session warning.
// The message names only the reason — never the command, the log, or a
// matched credential (the matched text may be the secret).
func (s *Session) warnLogReducerFallback(reason string) {
	s.emit(events.EventWarning, events.WarningData{
		Message: "log reducer: kept the original build/test log (" + reason + ")",
	})
}

const logReceiptSystemPrompt = `You extract compact evidence receipts from build and test logs for a developer assistant.
The full log is archived; your receipt is what the assistant will see instead, so it
must carry the evidence the next decision needs. Return only JSON matching the requested schema.
Rules:
- Echo the command and its exit status exactly as given.
- Quote the few most decision-relevant lines of the log verbatim and in order:
  compile errors and test failures first, then the summary or verdict lines.
  Never invent, complete, or paraphrase a line.
- Keep the summary to one or two sentences naming packages, counts, and outcomes.
- Never include credentials, tokens, or secrets in any field.`

// logReceiptExtractionWindow builds the model-facing window of the log: the
// whole log when it fits, else the head and tail windows with the elided
// middle byte count marked.
func logReceiptExtractionWindow(source string) string {
	if len(source) <= 2*logReducerExtractWindowBytes {
		return source
	}
	omitted := len(source) - 2*logReducerExtractWindowBytes
	return source[:logReducerExtractWindowBytes] +
		"\n[... " + strconv.Itoa(omitted) + " middle bytes elided; quote only from the shown windows ...]\n" +
		source[len(source)-logReducerExtractWindowBytes:]
}

// extractLogReceipt makes the one cheap-model side call that extracts the
// receipt, routed to the configured cheap model (Provider:
// profile.CheapProvider, Model: profile.ConfiguredCheapModel — never the
// primary model). It mirrors the session namer's call discipline: its own
// deadline, reasoning off, the session's LLMSleep, a retry policy that does
// not spend the turn's rate-limit wall budget, and the provider adapter
// timeout. Any error means fall back.
func (s *Session) extractLogReceipt(ctx context.Context, profile *provider.Profile, source, command string, exitStatus int) (logReceiptExtraction, error) {
	if s.client == nil {
		return logReceiptExtraction{}, errors.New("log reducer: llm client is nil")
	}
	callCtx, cancel := context.WithTimeout(ctx, logReducerExtractionTimeout)
	defer cancel()

	reasoningOff := llm.ReasoningEffortNone
	// Like the session namer: receipt extraction is auxiliary work with its
	// own short deadline, so it opts out of the turn's rate-limit wall budget.
	policy := llm.DefaultRetryPolicy()
	policy.RateLimitWallBudget = 0
	prompt := "command: " + command + "\nexit status: " + strconv.Itoa(exitStatus) +
		"\nbuild/test log (" + strconv.Itoa(len(source)) + " bytes):\n" + logReceiptExtractionWindow(source)
	opts := llm.GenerateObjectOptions{
		Client:          s.client,
		Provider:        profile.CheapProvider(),
		Model:           profile.ConfiguredCheapModel(),
		System:          new(logReceiptSystemPrompt),
		Prompt:          new(prompt),
		ReasoningEffort: &reasoningOff,
		Sleep:           s.cfg.LLMSleep,
		RetryPolicy:     &policy,
		Schema:          logReceiptSchema(),
		AdapterTimeout:  s.providerAdapterTimeout(),
	}
	res, err := llm.GenerateObject(callCtx, opts)
	if err != nil {
		return logReceiptExtraction{}, fmt.Errorf("cheap-model call: %w", err)
	}
	obj, _ := res.Output.(map[string]any)
	if obj == nil {
		return logReceiptExtraction{}, errors.New("extraction answer is not a JSON object")
	}
	echoCommand, ok := obj["command"].(string)
	if !ok {
		return logReceiptExtraction{}, errors.New("extraction answer lacks the command field")
	}
	exit, err := logReceiptExitStatus(obj["exit_status"])
	if err != nil {
		return logReceiptExtraction{}, err
	}
	rawLines, ok := obj["quoted_lines"].([]any)
	if !ok {
		return logReceiptExtraction{}, errors.New("extraction answer lacks the quoted_lines field")
	}
	summary, ok := obj["summary"].(string)
	if !ok {
		return logReceiptExtraction{}, errors.New("extraction answer lacks the summary field")
	}
	var lines []string
	for _, raw := range rawLines {
		line, ok := raw.(string)
		if !ok {
			return logReceiptExtraction{}, errors.New("extraction answer quotes a non-string line")
		}
		lines = append(lines, line)
	}
	return logReceiptExtraction{
		Command:     echoCommand,
		ExitStatus:  exit,
		QuotedLines: lines,
		Summary:     summary,
	}, nil
}

// logReceiptExitStatus decodes the extraction answer's exit_status, which
// llm.GenerateObject delivers as a json.Number (its decoder uses UseNumber).
func logReceiptExitStatus(raw any) (int, error) {
	switch v := raw.(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, errors.New("extraction answer exit_status is not an integer")
		}
		return int(n), nil
	case float64:
		return int(v), nil
	default:
		return 0, errors.New("extraction answer lacks the exit_status field")
	}
}

// logReceiptSchema is the extraction call's JSON schema.
func logReceiptSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command exactly as given.",
			},
			"exit_status": map[string]any{
				"type":        "integer",
				"description": "The command's exit status exactly as given.",
			},
			"quoted_lines": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"minItems":    1,
				"description": "The most decision-relevant lines of the log, quoted verbatim and in order.",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "One or two sentences: what the command did and what its outcome means for the work.",
			},
		},
		"required": []string{"command", "exit_status", "quoted_lines", "summary"},
	}
}
