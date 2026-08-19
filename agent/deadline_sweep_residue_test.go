package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// deadline_sweep_residue_test.go contains RED guards for the deadline-sweep
// residue tracked by GitHub issue #186:
//
//   1. agent/session_model_test.go still keeps 26x 5s and 2x 10s context
//      timeouts labeled "only fires on a genuine hang" — load-flaky under
//      `make test` parallelism. The sweep raised identical shapes elsewhere
//      to 30s; these were left behind.
//   2. agent/job_watch_events_test.go:TestProgressTimerFiresPeriodically only
//      waits for ONE timer fire, so a test named "FiresPeriodically" passes
//      even if the timer fires once and stops.
//
// Both tests are source-level guards: they read the offending test files and
// assert on their contents. They FAIL RED today and pass GREEN once the
// GREEN phase raises the bounds to 30s and makes the periodicity test collect
// multiple fires.

// residueBounds are the load-flaky timeout bounds left behind by the
// deadline sweep in session_model_test.go.
var residueBounds = []string{
	"5*time.Second",
	"10*time.Second",
}

// TestSessionModelTestNoFiveOrTenSecondBounds is the RED guard for defect 1 of
// issue #186: session_model_test.go must not retain 5s/10s context bounds.
func TestSessionModelTestNoFiveOrTenSecondBounds(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("session_model_test.go")
	if err != nil {
		t.Fatalf("read session_model_test.go: %v", err)
	}

	var offenders []string
	for line := range strings.SplitSeq(string(src), "\n") {
		for _, bound := range residueBounds {
			if strings.Contains(line, bound) {
				offenders = append(offenders, line)
				break
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("issue #186: session_model_test.go still has %d lines with 5s/10s "+
			"deadline-sweep residue bounds (want them raised to 30*time.Second).\n"+
			"offending lines:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// TestProgressTimerFiresPeriodicallyTestsPeriodicity is the RED guard for
// defect 2 of issue #186: a test named FiresPeriodically must verify MULTIPLE
// timer fires, not just one. It reads the body of
// TestProgressTimerFiresPeriodically and asserts it receives from `fired` at
// least three times.
func TestProgressTimerFiresPeriodicallyTestsPeriodicity(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("job_watch_events_test.go")
	if err != nil {
		t.Fatalf("read job_watch_events_test.go: %v", err)
	}

	body, ok := extractFuncBody(string(src), "TestProgressTimerFiresPeriodically")
	if !ok {
		t.Fatalf("issue #186: could not locate TestProgressTimerFiresPeriodically " +
			"function body in job_watch_events_test.go")
	}

	// Count receives from the `fired` channel. A periodicity test must collect
	// at least three fires; the existing test has only one `<-fired` receive.
	re := regexp.MustCompile(`<-\s*fired`)
	receives := len(re.FindAllString(body, -1))
	const want = 3
	if receives < want {
		t.Fatalf("issue #186: TestProgressTimerFiresPeriodically receives from "+
			"`fired` only %d time(s); a test named FiresPeriodically must verify "+
			"multiple fires (want >= %d receives). A timer that fires once and "+
			"stops would pass the existing test, defeating its name.",
			receives, want)
	}
}

// extractFuncBody locates `func <name>(` in src, then returns the brace-balanced
// body of that function (including the outer braces). It is a minimal
// Go-aware scanner: it skips string literals, raw string literals, line
// comments, and block comments so braces inside them do not affect depth.
// The second return is false if the function cannot be located.
func extractFuncBody(src, name string) (string, bool) {
	sig := "func " + name + "("
	idx := strings.Index(src, sig)
	if idx < 0 {
		return "", false
	}
	// Find the opening brace of the body, which follows the closing paren of
	// the parameter list. Scan from the `func` keyword.
	depth := 0
	braceStart := -1
	for i := idx; i < len(src); i++ {
		c := src[i]
		if c == '(' {
			depth++
			continue
		}
		if c == ')' {
			depth--
			if depth == 0 {
				// Rest of the signature is the return list then '{'; find '{'.
				braceStart = strings.Index(src[i:], "{")
				if braceStart < 0 {
					return "", false
				}
				braceStart += i
				break
			}
			continue
		}
	}
	if braceStart < 0 {
		return "", false
	}

	// Walk from the opening brace, tracking depth and skipping strings/comments.
	i := braceStart
	balance := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			// line comment
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				return "", false
			}
			i += end
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i:], "*/")
			if end < 0 {
				return "", false
			}
			i += end + 2
			continue
		case c == '"':
			i++
			for i < len(src) {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		case c == '`':
			i++
			for i < len(src) {
				if src[i] == '`' {
					i++
					break
				}
				i++
			}
			continue
		case c == '\'':
			i++
			for i < len(src) {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == '\'' {
					i++
					break
				}
				i++
			}
			continue
		case c == '{':
			balance++
		case c == '}':
			balance--
			if balance == 0 {
				return src[braceStart : i+1], true
			}
		}
		i++
	}
	return "", false
}
