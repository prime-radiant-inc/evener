package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode"
)

// maxFailureLedgerEntries bounds the ledger's memory. It has room for
// successful signatures too, not just failing ones: a success keeps its entry
// (the body hash must survive to catch repetition), so every distinct
// signature a session dispatches accumulates.
const maxFailureLedgerEntries = 512

// maxFailureSnippets caps how many failure outputs are retained per
// signature, enough to show the parked-call intervention its evidence
// without accumulating unbounded text.
const maxFailureSnippets = 2

// maxFailureSnippetRunes truncates each retained failure output, in runes.
const maxFailureSnippetRunes = 500

// maxSemanticArgumentRunes bounds a single value participating in a semantic
// signature. The signature itself is a short hash, but omitting unbounded
// bodies before hashing prevents arbitrary prose from creating fresh retries.
const maxSemanticArgumentRunes = 256

// failureEntry tracks, for one dispatch signature (tool name + argument
// hash), two independent streaks: consecutive failures sharing an error
// class, and consecutive calls returning a byte-identical result body.
type failureEntry struct {
	class    string
	count    int
	snippets []string

	bodyHash  string
	bodyCount int
}

// failureLedger records consecutive identical-failure streaks per dispatch
// signature so the breaker can nudge and then park runaway tool calls. It is
// one-per-session (owned by a *Registry) and safe for concurrent use, since
// tool batches can dispatch in parallel.
type failureLedger struct {
	mu      sync.Mutex
	entries map[string]*failureEntry
	order   []string // most-recently-used last, for LRU eviction
}

func newFailureLedger() *failureLedger {
	return &failureLedger{
		entries: make(map[string]*failureEntry),
	}
}

// signature returns the ledger key for a dispatch: the tool name plus a
// hash of its raw argument bytes. Two calls with byte-identical arguments
// share a signature; differing JSON formatting (key order, whitespace) does
// not, matching the loop detector's existing behavior.
func signature(name string, args []byte) string {
	return name + ":" + shortHash(args)
}

// semanticCallSignature returns the call half of a semantic failure
// fingerprint. Registered-tool normalization has already happened when this
// is called, so it intentionally preserves meaningful zero values such as an
// explicit retained-output offset while inheriting a tool's own omission rules
// (notably read_transcript's #827 neutral-default normalization).
func semanticCallSignature(name string, args map[string]any) string {
	encoded, err := json.Marshal(semanticArgumentValue(args, ""))
	if err != nil {
		// Args have passed JSON parsing and schema validation, so this is a
		// defensive fallback rather than a normal path. It remains bounded and
		// never exposes the original value.
		return name + ":" + shortHash([]byte("unencodable"))
	}
	return name + ":" + shortHash(encoded)
}

// semanticArgumentValue removes presentation-only and sensitive fields from
// a signature and replaces unbounded bodies with fixed markers. Values are
// hashed only after this pass, so telemetry cannot recover a secret or a body.
func semanticArgumentValue(value any, field string) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			keyLower := strings.ToLower(key)
			switch keyLower {
			case "intent", "description":
				continue
			case "secret", "token", "password", "authorization", "api_key", "access_token":
				out[key] = "<redacted>"
				continue
			case "body", "content", "patch":
				out[key] = "<omitted-body>"
				continue
			}
			out[key] = semanticArgumentValue(item, key)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = semanticArgumentValue(item, field)
		}
		return out
	case string:
		if len([]rune(value)) > maxSemanticArgumentRunes {
			switch strings.ToLower(field) {
			case "output_match", "regex", "pattern":
				// Regex text can legitimately be large but is a meaningful
				// selector, not a presentation body. Keep only a bounded digest.
				return "<hash:" + shortHash([]byte(value)) + ">"
			}
			return "<omitted-unbounded>"
		}
		return value
	default:
		return value
	}
}

// semanticFailureLedger records failed semantic fingerprints independently of
// the exact-call ledger. It is separately bounded so interleaved semantic runs
// survive raw argument variations without changing existing exact behavior.
type semanticFailureLedger struct {
	mu      sync.Mutex
	entries map[string]*semanticFailureEntry
	order   []string
}

type semanticFailureEntry struct {
	base     string
	boundary string
	count    int
}

func newSemanticFailureLedger() *semanticFailureLedger {
	return &semanticFailureLedger{entries: make(map[string]*semanticFailureEntry)}
}

func semanticFailureSignature(base, class string) string { return base + ":" + class }

func (l *semanticFailureLedger) check(base string) (count int, boundary, fingerprint string) {
	if l == nil {
		return 0, "", ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, entry := range l.entries {
		if entry.base != base || entry.count < count || (entry.count == count && fingerprint != "" && key > fingerprint) {
			continue
		}
		count, boundary, fingerprint = entry.count, entry.boundary, key
	}
	if fingerprint != "" {
		l.touch(fingerprint)
	}
	return count, boundary, fingerprint
}

func (l *semanticFailureLedger) record(base, class, boundary string) (fingerprint string, count int) {
	if l == nil {
		return semanticFailureSignature(base, class), 0
	}
	fingerprint = semanticFailureSignature(base, class)
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[fingerprint]
	if !ok {
		entry = &semanticFailureEntry{base: base, boundary: boundary}
		l.entries[fingerprint] = entry
	}
	entry.count++
	l.touch(fingerprint)
	return fingerprint, entry.count
}

// clear removes every failure class for one semantic call. A successful call
// has established that this target/mode/meaningful-argument combination is no
// longer stuck; unrelated semantic bases retain their independent histories.
func (l *semanticFailureLedger) clear(base string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, entry := range l.entries {
		if entry.base != base {
			continue
		}
		delete(l.entries, key)
		for i, ordered := range l.order {
			if ordered == key {
				l.order = append(l.order[:i], l.order[i+1:]...)
				break
			}
		}
	}
}

func (l *semanticFailureLedger) touch(key string) {
	for i, ordered := range l.order {
		if ordered == key {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
	l.order = append(l.order, key)
	if len(l.order) <= maxFailureLedgerEntries {
		return
	}
	oldest := l.order[0]
	l.order = l.order[1:]
	delete(l.entries, oldest)
}

func (l *semanticFailureLedger) len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// breakerThreshold is how many times a signature may produce the same answer
// before the breaker intervenes. The second such result carries a nudge. For
// repeated failures the call that would be the third is not executed at all;
// repeated identical successes only ever draw the nudge, since the next one
// may be the call that finally sees a changed world.
const breakerThreshold = 2

// The intervention texts. Parked results all begin with parkPrefix, which is
// the stable marker other layers count them by.
//
// The failure nudge stays a fixed string: the park check runs before every
// dispatch and refuses a call once its failure streak reaches
// breakerThreshold, so record() can never observe a failure streak above
// that threshold and the nudge only ever fires at exactly two failures.
const (
	parkPrefix       = "evener did not execute this call: "
	failureNudgeText = "You just ran the same tool twice with the same arguments and got the same failure. Consider an alternate approach"
)

// repetitionNudgeText builds the nudge for a run of consecutive identical
// results. Repetition is never parked, so count can climb arbitrarily high
// as a session loops; stating the real count lets a long loop read as
// escalating rather than repeating the same "twice" wording forever.
func repetitionNudgeText(count int) string {
	return fmt.Sprintf("You have now made this same call and received the identical result %d times in a row. Repeating it will not change the answer — use the result you already have, or change your approach.", count)
}

// failureParkText is the body of a refused call whose signature keeps failing
// the same way. It shows the failures themselves so the model can see what it
// is being asked to stop repeating.
func failureParkText(name string, snippets []string) string {
	var b strings.Builder
	b.WriteString(parkPrefix)
	b.WriteString(name)
	b.WriteString(" with these exact arguments has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach.")
	if len(snippets) > 0 {
		b.WriteString("\n\nThe failures so far:")
		for i, snippet := range snippets {
			fmt.Fprintf(&b, "\n%d. %s", i+1, snippet)
		}
	}
	return b.String()
}

func semanticFailureNudgeText(boundary string) string {
	return fmt.Sprintf("You just ran a semantically equivalent tool call twice and hit the same normalized failure boundary (%s). Change the target, mode, or meaningful arguments before retrying.", boundary)
}

// semanticFailureParkText deliberately reports hashes and normalized boundary
// labels rather than raw arguments or error snippets. The result is retained in
// session telemetry, so this must remain useful without repeating secrets or
// arbitrary user-provided bodies.
func semanticFailureParkText(name, semanticSignature, boundary string, attempts int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%ssemantic failure loop for %s (signature %s) has already failed %d times at normalized boundary %s.", parkPrefix, name, semanticSignature, attempts, boundary)
	b.WriteString("\n\nPrior attempts:")
	for attempt := 1; attempt <= attempts; attempt++ {
		fmt.Fprintf(&b, "\n%d. %s", attempt, boundary)
	}
	b.WriteString("\n\nTake a materially different valid action: ")
	if name == "read_transcript" {
		b.WriteString("use a transcript_ref compatible with the selected mode; for job: and artifact: refs, omit session-only range and expand_turn fields.")
	} else {
		b.WriteString("change the target, operation, or meaningful arguments, or use another tool.")
	}
	return b.String()
}

// appendIntervention adds breaker text after the result body, separated by a
// blank line, on both the model-facing output and the full output.
func appendIntervention(res *ExecResult, text string) {
	res.Output += "\n\n" + text
	if res.FullOutput != "" {
		res.FullOutput += "\n\n" + text
	}
}

// breakerBypassKey marks a context whose dispatch the breaker must not judge.
type breakerBypassKey struct{}

// WithBreakerBypass exempts calls made with the returned context from the
// repeated-call breaker. It is for re-dispatches a human explicitly
// authorized, where refusing the call would override that decision.
func WithBreakerBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, breakerBypassKey{}, true)
}

func breakerBypassed(ctx context.Context) bool {
	bypass, _ := ctx.Value(breakerBypassKey{}).(bool)
	return bypass
}

// check is the pre-dispatch read: the signature's current consecutive-
// failure streak, consecutive-identical-body streak, and recorded failure
// snippets, without mutating the ledger.
func (l *failureLedger) check(name string, args []byte) (failStreak int, repeatStreak int, snippets []string) {
	if l == nil { // a zero-value Registry has no ledger and judges nothing
		return 0, 0, nil
	}
	key := signature(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return 0, 0, nil
	}
	return e.count, e.bodyCount, append([]string(nil), e.snippets...)
}

// record is the post-dispatch write for both triggers. The body-hash streak
// tracks byte-identical result bodies regardless of error status: repetition
// itself is the signal, since a tool's error flag cannot be trusted (a
// failing call can report isErr=false with the failure as plain body text).
// The failure streak keeps its original semantics, except a success no
// longer deletes the entry — it zeroes the failure streak and clears the
// class and snippets, but the entry survives so the body hash persists.
func (l *failureLedger) record(name string, args []byte, isErr bool, output string) (failStreak int, repeatStreak int) {
	if l == nil { // a zero-value Registry has no ledger and judges nothing
		return 0, 0
	}
	key := signature(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok {
		e = &failureEntry{}
		l.entries[key] = e
	}
	l.touch(key)

	bodyHash := shortHash([]byte(output))
	if e.bodyHash == bodyHash {
		e.bodyCount++
	} else {
		e.bodyHash = bodyHash
		e.bodyCount = 1
	}

	if !isErr {
		e.class = ""
		e.count = 0
		e.snippets = nil
		return 0, e.bodyCount
	}

	class := errorClass(output)
	snippet := TruncateRunes(output, maxFailureSnippetRunes)

	if e.class != class {
		e.class = class
		e.count = 1
		e.snippets = []string{snippet}
		return 1, e.bodyCount
	}

	e.count++
	if len(e.snippets) >= maxFailureSnippets {
		e.snippets = e.snippets[1:]
	}
	e.snippets = append(e.snippets, snippet)
	return e.count, e.bodyCount
}

// clearFailures retires a signature's failure evidence: the streak, its error
// class, and the retained snippets. A human who authorizes a dispatch has
// judged the refusals that preceded it, so they may no longer park a later
// identical call; if the authorized call fails again, the next ordinary one
// records a fresh streak of 1.
//
// The body-hash streak is deliberately left alone. Repetition only ever nudges,
// and approving a call says nothing about whether its output changed.
func (l *failureLedger) clearFailures(name string, args []byte) {
	if l == nil { // a zero-value Registry has no ledger and judges nothing
		return
	}
	key := signature(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return // nothing recorded, so nothing to retire — and no entry to evict for
	}
	e.class = ""
	e.count = 0
	e.snippets = nil
	l.touch(key)
}

// touch moves key to the most-recently-used end of the order and evicts the
// least-recently-used signature if the ledger has grown past its bound.
// Recency, not age since first sight, decides what survives: a signature that
// keeps recurring is exactly the one the breaker must not forget, however
// much unrelated one-off traffic flows around it. Must be called with l.mu
// held.
func (l *failureLedger) touch(key string) {
	for i, k := range l.order {
		if k == key {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
	l.order = append(l.order, key)
	if len(l.order) <= maxFailureLedgerEntries {
		return
	}
	oldest := l.order[0]
	l.order = l.order[1:]
	delete(l.entries, oldest)
}

// errorClass normalizes a tool error output into a stable 8-character
// digest so that transient details (timings, job IDs) don't defeat
// streak detection across otherwise-identical failures.
func errorClass(output string) string {
	line := firstNonBlankLine(output)
	line = strings.TrimSpace(line)
	line = collapseWhitespace(line)
	line = strings.ToLower(line)
	line = replaceDigitRuns(line, "#")
	line = TruncateRunes(line, 200)

	sum := sha256.Sum256([]byte(line))
	return hex.EncodeToString(sum[:4])
}

// failureBoundary is the stable, presentation-free category displayed by the
// semantic breaker. errorClass retains the full normalized first line as a
// hash for fingerprint separation; this short label gives the model a safe
// diagnosis without echoing attacker-controlled error prose.
func failureBoundary(output string) string {
	line := strings.ToLower(strings.TrimSpace(firstNonBlankLine(output)))
	switch {
	case strings.HasPrefix(line, "invalid_request:"):
		return "invalid_request"
	case strings.HasPrefix(line, "unknown tool:"):
		return "unknown_tool"
	case strings.HasPrefix(line, "invalid tool arguments json:"):
		return "arguments_json"
	case strings.HasPrefix(line, "tool args schema validation failed:"):
		return "schema_validation"
	case strings.HasPrefix(line, "tool arguments too large:"):
		return "arguments_too_large"
	default:
		return "tool_execution"
	}
}

func firstNonBlankLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

func replaceDigitRuns(s, replacement string) string {
	var b strings.Builder
	inDigits := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			if !inDigits {
				b.WriteString(replacement)
				inDigits = true
			}
			continue
		}
		inDigits = false
		b.WriteRune(r)
	}
	return b.String()
}

// TruncateRunes cuts s to at most maxRunes runes, never splitting a multi-byte
// rune. Exported so the agent package's steering messages truncate the same
// way tool results do.
func TruncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
