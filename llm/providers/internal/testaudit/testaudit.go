// Package testaudit provides source-level audits shared by provider test
// suites. It is only imported from _test files, so it links into test
// binaries only.
package testaudit

import (
	"os"
	"strings"
	"testing"
)

// RequireHandlerWaitsOnContextDone pins issue #164 for the named test in the
// given file: its httptest handler must not contain a fixed-duration
// time.Sleep (which guards nothing when the context is cancelled BEFORE the
// request, and misleads readers into thinking timing matters), and must
// instead wait on <-r.Context().Done() like the sibling deadline tests.
// The path is resolved relative to the calling test's package directory.
func RequireHandlerWaitsOnContextDone(t *testing.T, file, funcName string) {
	t.Helper()

	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(b)

	body, ok := extractFuncBody(src, funcName)
	if !ok {
		t.Fatalf("%s not found in %s", funcName, file)
	}

	red := false

	if strings.Contains(body, "time.Sleep(") {
		red = true
		t.Errorf("issue #164: %s handler contains a fixed-duration time.Sleep;\nthe context is cancelled before the request, so the sleep guards nothing and misleads readers.\noffending snippet:\n%s", funcName, snippetAround(body, "time.Sleep("))
	}

	if !strings.Contains(body, "<-r.Context().Done()") {
		red = true
		t.Errorf("issue #164: %s handler must wait on <-r.Context().Done() (mirroring the sibling deadline tests) instead of a fixed-duration sleep; <-r.Context().Done() not found in function body", funcName)
	}

	if red {
		t.Fatalf("issue #164: %s handler still uses a fixed-duration sleep and/or lacks <-r.Context().Done()", funcName)
	}
}

// extractFuncBody returns the body text (excluding the outermost braces) of the
// top-level function named funcName in src. It returns ("", false) if the
// function declaration is not found.
func extractFuncBody(src, funcName string) (string, bool) {
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
func snippetAround(body, needle string) string {
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
