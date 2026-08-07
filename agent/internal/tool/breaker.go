package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// breakerThreshold is how many times a signature may produce the same answer
// before the breaker intervenes. The second such result carries a nudge. For
// repeated failures the call that would be the third is not executed at all;
// repeated identical successes only ever draw the nudge, since the next one
// may be the call that finally sees a changed world.
const breakerThreshold = 2

// The intervention texts. Parked results all begin with parkPrefix, which is
// the stable marker other layers count them by.
const (
	parkPrefix          = "serf did not execute this call: "
	failureNudgeText    = "You just ran the same tool twice with the same arguments and got the same failure. Consider an alternate approach"
	repetitionNudgeText = "You have now made this same call twice and received the identical result. Repeating it will not change the answer — use the result you already have, or change your approach."
)

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
