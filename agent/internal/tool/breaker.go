package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"unicode"
)

// maxFailureLedgerEntries bounds the ledger's memory. Raised from 256: a
// success no longer deletes its entry (the body hash must survive), so
// successful signatures now accumulate too.
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
// failure streak, consecutive-identical-body streak, and recorded failure
// snippets, without mutating the ledger.
func (l *failureLedger) check(name string, args []byte) (failStreak int, repeatStreak int, snippets []string) {
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
	key := signature(name, args)
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok {
		e = &failureEntry{}
		l.entries[key] = e
		l.insert(key)
	}

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
	snippet := truncateRunes(output, maxFailureSnippetRunes)

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
