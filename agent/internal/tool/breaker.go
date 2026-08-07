package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"unicode"
)

// maxFailureLedgerEntries bounds the ledger's memory: only failing
// signatures are stored, and a runaway session must not grow this
// unboundedly.
const maxFailureLedgerEntries = 256

// maxFailureSnippets caps how many failure outputs are retained per
// signature, enough to show the parked-call intervention its evidence
// without accumulating unbounded text.
const maxFailureSnippets = 2

// maxFailureSnippetLen truncates each retained failure output.
const maxFailureSnippetLen = 500

// failureEntry tracks the consecutive-failure streak for one dispatch
// signature (tool name + argument hash).
type failureEntry struct {
	class    string
	count    int
	snippets []string
}

// failureLedger records consecutive identical-failure streaks per dispatch
// signature so the breaker can nudge and then park runaway tool calls. It is
// one-per-session (owned by a *Registry) and safe for concurrent use, since
// tool batches can dispatch in parallel.
type failureLedger struct {
	mu      sync.Mutex
	entries map[string]*failureEntry
	order   []string // insertion order, for FIFO eviction
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

// check is the pre-dispatch read: the signature's current consecutive-
// failure count and recorded snippets, without mutating the ledger.
func (l *failureLedger) check(name string, args []byte) (streak int, snippets []string) {
	key := signature(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return 0, nil
	}
	return e.count, append([]string(nil), e.snippets...)
}

// record is the post-dispatch write. A success deletes the entry and
// returns streak 0. A failure whose error class matches the recorded class
// increments the streak; a failure with a different class starts a fresh
// streak of 1. It returns the new streak.
func (l *failureLedger) record(name string, args []byte, isErr bool, output string) int {
	key := signature(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()

	if !isErr {
		if _, ok := l.entries[key]; ok {
			delete(l.entries, key)
			l.removeFromOrder(key)
		}
		return 0
	}

	class := errorClass(output)
	snippet := truncateRunes(output, maxFailureSnippetLen)

	e, ok := l.entries[key]
	if !ok || e.class != class {
		l.entries[key] = &failureEntry{class: class, count: 1, snippets: []string{snippet}}
		if !ok {
			l.insert(key)
		}
		return 1
	}

	e.count++
	if len(e.snippets) >= maxFailureSnippets {
		e.snippets = e.snippets[1:]
	}
	e.snippets = append(e.snippets, snippet)
	return e.count
}

// insert appends a newly-created key to the insertion order and evicts the
// oldest signature if the ledger has grown past its bound. Must be called
// with l.mu held.
func (l *failureLedger) insert(key string) {
	l.order = append(l.order, key)
	if len(l.order) <= maxFailureLedgerEntries {
		return
	}
	oldest := l.order[0]
	l.order = l.order[1:]
	delete(l.entries, oldest)
}

// removeFromOrder drops key from the insertion-order slice, keeping it in
// sync with l.entries so a later eviction never targets a key whose entry
// was already deleted (and possibly replaced by a newer one under the same
// key). Must be called with l.mu held.
func (l *failureLedger) removeFromOrder(key string) {
	for i, k := range l.order {
		if k == key {
			l.order = append(l.order[:i], l.order[i+1:]...)
			return
		}
	}
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
	line = truncateRunes(line, 200)

	sum := sha256.Sum256([]byte(line))
	return hex.EncodeToString(sum[:4])
}

func firstNonBlankLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
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

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
