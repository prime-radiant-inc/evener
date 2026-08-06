package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// truncationBanner is the substring the tool registry stamps onto a result
// whose output it truncated (agent/internal/tool/registry.go's
// truncateChars, ~:596): the tail-only and head/tail marker sentences differ
// after this point, so matching the shared prefix catches both.
const truncationBanner = "[WARNING: Tool output was truncated."

// errorClassMarkers is the doctor's own coarse error-class heuristic over a
// failed tool result's text — a marker table, not a schema, so a tool's
// novel wording lands in "other" until a marker is added here. Checked top
// to bottom; the first case-insensitive substring match wins, so a more
// specific class must be listed above a more generic one.
//
// | class             | markers (case-insensitive substring)                       |
// |-------------------|--------------------------------------------------------------|
// | schema-rejection  | "schema validation failed", "invalid tool arguments json"    |
// | not-found         | "not found", "no such file", "does not exist"                |
// | denied            | "permission denied", "access denied", "denied"               |
// | timeout           | "timed out", "timeout"                                       |
// | other             | (fallback — none of the above matched)                       |
var errorClassMarkers = []struct {
	class   string
	markers []string
}{
	{"schema-rejection", []string{"schema validation failed", "invalid tool arguments json"}},
	{"not-found", []string{"not found", "no such file", "does not exist"}},
	{"denied", []string{"permission denied", "access denied", "denied"}},
	{"timeout", []string{"timed out", "timeout"}},
}

// classifyToolError maps a failed tool result's text to one of
// errorClassMarkers' classes, or "other" when nothing matches.
func classifyToolError(text string) string {
	lower := strings.ToLower(text)
	for _, m := range errorClassMarkers {
		for _, marker := range m.markers {
			if strings.Contains(lower, marker) {
				return m.class
			}
		}
	}
	return "other"
}

// IdenticalRun describes the longest run of consecutive, identically
// signatured structural tool calls in the transcript. The signature formula
// matches the runtime's own loop detector's (agent/session_tool_round.go's
// injectPostToolSteering: tool name + SHA256[:8]-hex of arguments —
// agent/runtime_dir.go's shortHash and agent/internal/tool/registry.go's
// shortHash are the identical formula under two names) — but this is only
// the formula, not the detector: it reports period-1 (immediate) repeats
// only, while detectLoop also fires on period-2/3 cycles and never checks
// error status, so a session can trip the live loop-detector steering
// without this run being long, or without AllErrors being true.
type IdenticalRun struct {
	Tool string `json:"tool,omitempty"`
	// Length is the run's call count; zero means no tool calls at all.
	Length int `json:"length"`
	// AllErrors is true only when every call in the run has a recorded
	// result and every one of those results is an error. A call whose result
	// is missing (round still open, or result never paired) is not assumed
	// to be an error, so AllErrors is false in that case.
	AllErrors bool `json:"all_errors"`
}

// JobsHealth is the jobs.jsonl-derived slice of session health: terminal
// jobs grouped by the reason that ended them, plus how many terminal jobs
// produced zero output bytes — the run_timeout-with-nothing-to-show shape
// the 2026-07-31 study named as wasted budget.
type JobsHealth struct {
	ByTerminalReason   map[string]int `json:"by_terminal_reason"`
	ZeroOutputTerminal int            `json:"zero_output_terminal"`
}

// HealthResult is TranscriptHealth's mechanical, per-session verdict: every
// metric the 2026-07-31 fleet study re-derived by hand, computed once from
// the canonical transcript + jobs.jsonl readers so a batch study never has
// to re-derive it with LLM effort again.
type HealthResult struct {
	SessionID string `json:"session_id"`

	// ToolCalls is the structural invocation count per tool name (mirrors
	// Count's definition: a content part of kind tool_call, never a textual
	// mention).
	ToolCalls map[string]int `json:"tool_calls"`
	// ToolErrors sub-keys each tool's error count by the coarse class
	// classifyToolError assigns.
	ToolErrors map[string]map[string]int `json:"tool_errors"`

	LongestIdenticalRun IdenticalRun `json:"longest_identical_run"`
	// TruncationWarnings counts tool results whose content carries the
	// registry's truncation banner.
	TruncationWarnings int `json:"truncation_warnings"`

	// Steering counts steering turns by kind (events.SteeringKind*, or
	// "unknown" for a steering turn recorded with no kind at all).
	Steering map[string]int `json:"steering"`

	Jobs JobsHealth `json:"jobs"`

	// StaleNotifications counts notification-kind steering turns recorded
	// after the transcript's FINAL end_turn=true result-tool call — a
	// notification injected once the session had already declared itself
	// done and could no longer act on it.
	StaleNotifications int `json:"stale_notifications"`
	// UserCorrections is a PROXY metric, not a verified defect count: every
	// USER_INPUT/STEERING turn recorded after the final end_turn=true
	// result-tool call, whatever its content. Named "_corrections" because
	// that is the dominant real-world cause (the agent declared done and the
	// user pushed back), but a benign turn (e.g. the same stale notification
	// StaleNotifications counts) inflates it too — read it as "activity
	// after done", not a confirmed correction count.
	UserCorrections int `json:"user_corrections"`
}

// toolCallOccurrence is one structural tool call in transcript order, kept
// only long enough to compute the identical-run signature and pair it back
// to its result by ToolCallID.
type toolCallOccurrence struct {
	signature string
	tool      string
	callID    string
}

// TranscriptHealth computes HealthResult for one session: mechanical,
// deterministic metrics over the canonical transcript and jobs.jsonl
// readers. It never re-simulates the runtime (e.g. it does not re-run loop
// detection) — it reports recorded structure using the runtime's own
// signature formula.
func TranscriptHealth(stateBase, selector string) (HealthResult, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return HealthResult{}, err
	}
	doc, err := loadTranscript(paths.TranscriptPath)
	if err != nil {
		return HealthResult{}, err
	}
	resultTool := resolveResultTool(paths)

	res := HealthResult{
		SessionID:  paths.SessionID,
		ToolCalls:  map[string]int{},
		ToolErrors: map[string]map[string]int{},
		Steering:   map[string]int{},
		Jobs:       JobsHealth{ByTerminalReason: map[string]int{}},
	}

	var occurrences []toolCallOccurrence
	resultSeen := map[string]bool{}
	resultErrors := map[string]bool{}

	for _, e := range doc.Entries {
		for _, part := range e.Turn.Message.Content {
			switch part.Kind {
			case llm.ContentToolCall:
				if part.ToolCall == nil {
					continue
				}
				name := part.ToolCall.Name
				res.ToolCalls[name]++
				occurrences = append(occurrences, toolCallOccurrence{
					signature: toolCallSignature(name, part.ToolCall.Arguments),
					tool:      name,
					callID:    part.ToolCall.ID,
				})
			case llm.ContentToolResult:
				if part.ToolResult == nil {
					continue
				}
				text := toolResultContentText(part.ToolResult.Content)
				if strings.Contains(text, truncationBanner) {
					res.TruncationWarnings++
				}
				if part.ToolResult.ToolCallID != "" {
					resultSeen[part.ToolResult.ToolCallID] = true
					resultErrors[part.ToolResult.ToolCallID] = part.ToolResult.IsError
				}
				if part.ToolResult.IsError {
					if res.ToolErrors[part.ToolResult.Name] == nil {
						res.ToolErrors[part.ToolResult.Name] = map[string]int{}
					}
					res.ToolErrors[part.ToolResult.Name][classifyToolError(text)]++
				}
			}
		}
		if e.Turn.Kind == schema.TurnSteering {
			kind := e.Turn.SteeringKind
			if kind == "" {
				kind = "unknown"
			}
			res.Steering[kind]++
		}
	}

	res.LongestIdenticalRun = longestIdenticalRun(occurrences, resultSeen, resultErrors)

	if final := finalEndTurnIndex(doc.Entries, resultTool); final >= 0 {
		for _, e := range doc.Entries[final+1:] {
			switch e.Turn.Kind {
			case schema.TurnUserInput:
				res.UserCorrections++
			case schema.TurnSteering:
				res.UserCorrections++
				if e.Turn.SteeringKind == events.SteeringKindNotification {
					res.StaleNotifications++
				}
			}
		}
	}

	jobEvents, err := jobstore.ReadEvents(paths.JobsPath)
	if err != nil {
		return HealthResult{}, err
	}
	for _, rec := range jobstore.FoldOrdered(jobEvents) {
		if !rec.Status.IsTerminal() {
			continue
		}
		if rec.Reason != "" {
			res.Jobs.ByTerminalReason[rec.Reason]++
		}
		if rec.OutputBytes == 0 {
			res.Jobs.ZeroOutputTerminal++
		}
	}

	return res, nil
}

// toolCallSignature mirrors the runtime loop detector's own tool-call
// signature (agent/session_tool_round.go's injectPostToolSteering:
// call.Name+":"+shortHash(call.Arguments)). It reimplements the SHA256[:8]
// -hex formula rather than importing the agent package: the doctor package
// deliberately imports only durable-format packages, never the agent
// session/runtime (see doctor.go's package doc).
func toolCallSignature(name string, args json.RawMessage) string {
	sum := sha256.Sum256(args)
	return name + ":" + hex.EncodeToString(sum[:8])
}

// longestIdenticalRun scans structural tool-call occurrences in transcript
// order and returns the longest run of consecutive calls sharing a
// signature. Ties keep the first (earliest) run encountered.
func longestIdenticalRun(occ []toolCallOccurrence, resultSeen, resultErrors map[string]bool) IdenticalRun {
	var best IdenticalRun
	for i := 0; i < len(occ); {
		j := i + 1
		for j < len(occ) && occ[j].signature == occ[i].signature {
			j++
		}
		if length := j - i; length > best.Length {
			allErrors := true
			for k := i; k < j; k++ {
				id := occ[k].callID
				if !resultSeen[id] || !resultErrors[id] {
					allErrors = false
					break
				}
			}
			best = IdenticalRun{Tool: occ[i].tool, Length: length, AllErrors: allErrors}
		}
		i = j
	}
	return best
}

// finalEndTurnIndex returns the doc.Entries index of the LAST entry whose
// ASSISTANT turn carries a resultTool call with end_turn=true, or -1 if none
// exists. This is the anchor "the session declared itself done" point that
// StaleNotifications and UserCorrections measure trailing activity against.
func finalEndTurnIndex(entries []transcript.Entry, resultTool string) int {
	last := -1
	for i, e := range entries {
		if e.Turn.Kind != schema.TurnAssistant {
			continue
		}
		for _, part := range e.Turn.Message.Content {
			if part.Kind != llm.ContentToolCall || part.ToolCall == nil || part.ToolCall.Name != resultTool {
				continue
			}
			if callArgsEndTurn(part.ToolCall.Arguments) {
				last = i
			}
		}
	}
	return last
}

// callArgsEndTurn reads end_turn off a result-tool call's arguments. A
// missing or unparseable field reads as false — never a guessed true.
func callArgsEndTurn(rawArgs json.RawMessage) bool {
	var args struct {
		EndTurn bool `json:"end_turn"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return false
	}
	return args.EndTurn
}

// RenderHealth renders a HealthResult as a compact human table.
func RenderHealth(r HealthResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s\n", r.SessionID)
	if len(r.ToolCalls) == 0 {
		b.WriteString("tool_calls: (none)\n")
	} else {
		b.WriteString("tool_calls:\n")
		for _, name := range sortedKeys(r.ToolCalls) {
			fmt.Fprintf(&b, "  %-20s calls=%-4d errors=%s\n", name, r.ToolCalls[name], renderCounts(r.ToolErrors[name]))
		}
	}
	fmt.Fprintf(&b, "longest_identical_run: tool=%s length=%d all_errors=%t\n",
		dash(r.LongestIdenticalRun.Tool), r.LongestIdenticalRun.Length, r.LongestIdenticalRun.AllErrors)
	fmt.Fprintf(&b, "truncation_warnings: %d\n", r.TruncationWarnings)
	fmt.Fprintf(&b, "steering: %s\n", renderCounts(r.Steering))
	fmt.Fprintf(&b, "jobs: by_terminal_reason=%s zero_output_terminal=%d\n",
		renderCounts(r.Jobs.ByTerminalReason), r.Jobs.ZeroOutputTerminal)
	fmt.Fprintf(&b, "stale_notifications: %d\n", r.StaleNotifications)
	fmt.Fprintf(&b, "user_corrections (proxy): %d\n", r.UserCorrections)
	return b.String()
}

// renderCounts renders a name->count map as "a=1 b=2", sorted by name, or
// "-" when empty.
func renderCounts(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	keys := sortedKeys(m)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
