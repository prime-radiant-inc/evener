package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
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

// failureEntry tracks two independent streaks for the ledger key it is stored
// under: consecutive failures sharing an error class, and consecutive calls
// returning a byte-identical result body. The exact-call store uses both; the
// semantic store uses only the failure streak.
type failureEntry struct {
	class    string
	count    int
	snippets []string

	bodyHash  string
	bodyCount int
}

// failureLedger records consecutive identical-failure streaks per dispatch
// fingerprint so the breaker can nudge and then park runaway tool calls. It is
// one-per-session (owned by a *Registry) and safe for concurrent use, since
// tool batches can dispatch in parallel.
type failureLedger struct {
	mu sync.Mutex
	// entries is keyed by exactSignature, preserving the original exact-call
	// fast path: byte-identical calls share an entry, so both the body-hash
	// repetition streak and the exact failure streak behave as they always did.
	entries map[string]*failureEntry
	order   []string // entries LRU, most-recently-used last

	// semantic is keyed by failureFingerprint: calls that differ only in
	// free-text or neutral/default arguments share a failure run here, so a
	// semantic loop is caught even though its exact bytes keep changing. It
	// carries its own bounded history so a fingerprint's run never evicts
	// another's, and it is deliberately separate from entries so the exact-call
	// repetition fast path is untouched.
	semantic      map[string]*failureEntry
	semanticOrder []string // semantic LRU, most-recently-used last
}

func newFailureLedger() *failureLedger {
	return &failureLedger{
		entries:  make(map[string]*failureEntry),
		semantic: make(map[string]*failureEntry),
	}
}

// exactSignature returns the ledger key for a dispatch under the exact-call
// fast path: the tool name plus a hash of its raw argument bytes. Two calls
// with byte-identical arguments share a signature. It is also the fallback
// fingerprint for arguments that cannot be canonicalized, so a malformed call
// still gets exact-call detection.
func exactSignature(name string, args []byte) string {
	return name + ":" + shortHash(args)
}

// failureFingerprint returns the ledger key for a dispatch's repeated-failure
// run. Unlike exactSignature it hashes a normalized view of the arguments:
// free-text fields no tool executes on (intent, and the shell tool's
// presentation-only job description) are dropped and JSON key order and
// whitespace are canonicalized. A call that changes only those is the same
// failing operation, while any change to a field the tool executes on (target
// ref, mode, offset, regex, or a presence-sensitive default such as
// offset_bytes=0 or depends_on: []) keeps its own fingerprint and bounded
// history: values are never judged to be "defaults" by their content alone.
//
// Arguments that are not a single well-formed JSON value fall back to
// exactSignature, preserving the original byte-exact behavior.
func failureFingerprint(name string, args []byte) string {
	canonical, ok := canonicalArgumentBytes(name, args)
	if !ok {
		return exactSignature(name, args)
	}
	return name + ":sem:" + shortHash(canonical)
}

// canonicalArgumentBytes returns the canonical byte view of a tool call's
// arguments used by the semantic failure fingerprint. The bool is false when
// args is not a single well-formed JSON value, in which case the caller must
// fall back to the exact signature.
func canonicalArgumentBytes(name string, args []byte) ([]byte, bool) {
	// The size and UTF-8 limits are the pre-parse boundary: a body the registry
	// would reject must not be decoded and re-encoded here first. Falling back
	// to the exact signature handles it as opaque bytes, which is both cheaper
	// and what the dispatch path will do with it anyway.
	if err := ValidateRawArguments(args); err != nil {
		return nil, false
	}
	if len(bytes.TrimSpace(args)) == 0 {
		return []byte("{}"), true
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	// Reject trailing tokens: only one JSON value is a valid call body.
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	encoded, err := json.Marshal(canonicalizeValue(v, name == "shell"))
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// canonicalizeValue recursively prunes fields that no tool executes on from a
// decoded JSON value. Maps are re-encoded by json.Marshal with sorted keys, so
// key order and whitespace cannot change the fingerprint.
//
// Fields are dropped by NAME only. A value that looks like a default is
// deliberately kept, because whether a value means "omitted" is a property of
// the field's contract, not of the value: a present read_transcript
// offset_bytes=0 selects the retained-page operation while omitting it selects
// the default view, and a present task_list depends_on: [] clears a task's
// dependencies while omitting it leaves them alone. Pruning by value folded
// those meaningful calls into the omitted form and could park a call the model
// legitimately changed. The cost of not pruning is only that a caller which
// materializes a real default gets its own fingerprint, which delays the
// breaker rather than refusing a call that was meant to change.
func canonicalizeValue(v any, dropDescription bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k == "intent" {
				continue // free text; the registry strips it before dispatch
			}
			if dropDescription && k == "description" {
				continue // the shell tool's job label, presentation only
			}
			out[k] = canonicalizeValue(val, false)
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, canonicalizeValue(item, false))
		}
		return out
	case json.Number:
		return canonicalNumber(x)
	default:
		return v
	}
}

// canonicalNumber folds a JSON number into a canonical Go representation so
// equivalent literals (1, 1.0, 1e0) share a fingerprint.
func canonicalNumber(n json.Number) any {
	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
		return f
	}
	return n
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
	b.WriteString(" with equivalent arguments has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach.")
	if len(snippets) > 0 {
		b.WriteString("\n\nThe failures so far:")
		for i, snippet := range snippets {
			fmt.Fprintf(&b, "\n%d. %s", i+1, snippet)
		}
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

// check is the pre-dispatch read: the normalized fingerprint's current
// consecutive-failure streak and recorded failure snippets, plus the exact
// call's consecutive-identical-body streak, without mutating the ledger.
func (l *failureLedger) check(name string, args []byte) (failStreak int, repeatStreak int, snippets []string) {
	if l == nil { // a zero-value Registry has no ledger and judges nothing
		return 0, 0, nil
	}
	// Both fingerprints hash the argument body, so compute them before taking
	// the lock (as record and clearFailures already do): canonicalizing a large
	// call under l.mu would stall every other dispatch in the batch.
	exactKey := exactSignature(name, args)
	semKey := failureFingerprint(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.semantic[semKey]; ok {
		failStreak = e.count
		snippets = append([]string(nil), e.snippets...)
	}
	if e, ok := l.entries[exactKey]; ok {
		repeatStreak = e.bodyCount
	}
	return failStreak, repeatStreak, snippets
}

// record is the post-dispatch write for both triggers. The body-hash streak
// tracks byte-identical result bodies regardless of error status: repetition
// itself is the signal, since a tool's error flag cannot be trusted (a
// failing call can report isErr=false with the failure as plain body text).
// It is tracked on the exact call, so the repetition nudge is unchanged. The
// returned failure streak is the semantic fingerprint's run; the exact entry's
// own failure streak is still maintained so byte-identical calls keep their
// original history. A success zeroes a failure streak and clears the class and
// snippets, but the entry survives so the body hash persists.
func (l *failureLedger) record(name string, args []byte, isErr bool, output string) (failStreak int, repeatStreak int) {
	if l == nil { // a zero-value Registry has no ledger and judges nothing
		return 0, 0
	}
	exactKey := exactSignature(name, args)
	semKey := failureFingerprint(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[exactKey]
	if !ok {
		e = &failureEntry{}
		l.entries[exactKey] = e
	}
	l.order = l.touch(l.order, l.entries, exactKey)

	bodyHash := shortHash([]byte(output))
	if e.bodyHash == bodyHash {
		e.bodyCount++
	} else {
		e.bodyHash = bodyHash
		e.bodyCount = 1
	}
	observeFailure(e, isErr, output)
	repeatStreak = e.bodyCount

	s, ok := l.semantic[semKey]
	if !ok {
		s = &failureEntry{}
		l.semantic[semKey] = s
	}
	l.semanticOrder = l.touch(l.semanticOrder, l.semantic, semKey)
	failStreak = observeFailure(s, isErr, output)

	return failStreak, repeatStreak
}

// observeFailure advances an entry's consecutive-failure streak for one
// result, returning the new count (0 for a success). A failure of a different
// error class restarts the streak at 1 with that failure's snippet; matching
// failures extend it, keeping only the most recent maxFailureSnippets.
func observeFailure(e *failureEntry, isErr bool, output string) int {
	if !isErr {
		e.class = ""
		e.count = 0
		e.snippets = nil
		return 0
	}
	class := errorClass(output)
	snippet := TruncateRunes(output, maxFailureSnippetRunes)

	if e.class != class {
		e.class = class
		e.count = 1
		e.snippets = []string{snippet}
		return 1
	}

	e.count++
	if len(e.snippets) >= maxFailureSnippets {
		e.snippets = e.snippets[1:]
	}
	e.snippets = append(e.snippets, snippet)
	return e.count
}

// clearFailures retires a call's failure evidence — both the semantic
// fingerprint's run and the exact call's own streak: the streaks, their error
// classes, and the retained snippets. A human who authorizes a dispatch has
// judged the refusals that preceded it, so they may no longer park a later
// equivalent call; if the authorized call fails again, the next ordinary one
// records a fresh streak of 1.
//
// The body-hash streak is deliberately left alone. Repetition only ever nudges,
// and approving a call says nothing about whether its output changed.
func (l *failureLedger) clearFailures(name string, args []byte) {
	if l == nil { // a zero-value Registry has no ledger and judges nothing
		return
	}
	exactKey := exactSignature(name, args)
	semKey := failureFingerprint(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.semantic[semKey]; ok {
		e.class = ""
		e.count = 0
		e.snippets = nil
		l.semanticOrder = l.touch(l.semanticOrder, l.semantic, semKey)
	}
	if e, ok := l.entries[exactKey]; ok {
		e.class = ""
		e.count = 0
		e.snippets = nil
		l.order = l.touch(l.order, l.entries, exactKey)
	}
}

// touch moves key to the most-recently-used end of order and evicts the
// least-recently-used key from entries if the store has grown past its bound.
// Recency, not age since first sight, decides what survives: a key that keeps
// recurring is exactly the one the breaker must not forget, however much
// unrelated one-off traffic flows around it. Must be called with l.mu held.
func (l *failureLedger) touch(order []string, entries map[string]*failureEntry, key string) []string {
	for i, k := range order {
		if k == key {
			order = append(order[:i], order[i+1:]...)
			break
		}
	}
	order = append(order, key)
	if len(order) <= maxFailureLedgerEntries {
		return order
	}
	oldest := order[0]
	order = order[1:]
	delete(entries, oldest)
	return order
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
