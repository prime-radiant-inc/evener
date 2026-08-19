package google

import (
	"os"
	"strings"
	"testing"
)

// extractFuncBody returns the body text (excluding the outermost braces) of the
// top-level function named funcName in src. It returns ("", false) if the
// function declaration is not found.
func extractFuncBody(t *testing.T, src, funcName string) (string, bool) {
	t.Helper()

	needle := "func " + funcName + "("
	idx := strings.Index(src, needle)
	if idx < 0 {
		return "", false
	}

	// Scan from idx to the opening brace of the function body, skipping over
	// the parameter list and (optional) return signature. We track brace/paren
	// nesting so signatures containing parens are handled.
	depth := 0
	start := -1
	for i := idx; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if start >= 0 && depth == 0 {
				return src[start+1 : i], true
			}
		}
	}
	return "", false
}

// snippetAround returns a few lines of context around the first occurrence of
// needle in body, for readable failure output.
func snippetAround(t *testing.T, body, needle string) string {
	t.Helper()
	idx := strings.Index(body, needle)
	if idx < 0 {
		return needle
	}
	start := idx
	for i := idx; i >= 0 && i > idx-200; i-- {
		if body[i] == '\n' {
			start = i + 1
			break
		}
	}
	end := idx + len(needle)
	for i := end; i < len(body) && i < end+200; i++ {
		if body[i] == '\n' {
			end = i
			break
		}
	}
	return strings.TrimSpace(body[start:end])
}

// TestCancelHandlerHasNoFixedSleep pins issue #164: the
// TestComplete_WrapsContextCanceled handler must not contain a fixed-duration
// time.Sleep (which guards nothing because the context is cancelled BEFORE the
// request), and must instead wait on <-r.Context().Done() like its sibling
// deadline test.
//
// This test is RED until the 5s handler sleep in
// TestComplete_WrapsContextCanceled is replaced with <-r.Context().Done().
func TestCancelHandlerHasNoFixedSleep(t *testing.T) {
	b, err := os.ReadFile("adapter_test.go")
	if err != nil {
		t.Fatalf("read adapter_test.go: %v", err)
	}
	src := string(b)

	body, ok := extractFuncBody(t, src, "TestComplete_WrapsContextCanceled")
	if !ok {
		t.Fatalf("TestComplete_WrapsContextCanceled not found in adapter_test.go")
	}

	red := false

	if strings.Contains(body, "time.Sleep(") {
		red = true
		t.Errorf("issue #164: TestComplete_WrapsContextCanceled handler contains a fixed-duration time.Sleep;\nthis sleep guards nothing because the context is cancelled before the request and adds ~5s of wall time per run via srv.Close().\noffending snippet:\n%s", snippetAround(t, body, "time.Sleep("))
	}

	if !strings.Contains(body, "<-r.Context().Done()") {
		red = true
		t.Errorf("issue #164: TestComplete_WrapsContextCanceled handler must wait on <-r.Context().Done() (mirroring TestComplete_WrapsContextDeadlineExceeded) instead of a fixed-duration sleep; <-r.Context().Done() not found in function body")
	}

	if red {
		t.Fatalf("issue #164 red: cancel-test handler still uses a fixed-duration sleep and/or lacks <-r.Context().Done()")
	}
}
