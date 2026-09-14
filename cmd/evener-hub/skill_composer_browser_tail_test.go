//go:build browserguard

package hub

import "testing"

// The tail is the only thing the guard's failure hands a reader on CI, so the
// shape it takes is worth pinning: dropping the last line, or counting a
// trailing newline as a line, would quietly cost the one line that matters.
func TestSkillGuardDriverTail(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		contents string
		lines    int
		want     string
		wantKept int
	}{
		{name: "empty log", contents: "", lines: 3, want: "", wantKept: 0},
		{name: "only newlines", contents: "\n\n", lines: 3, want: "", wantKept: 0},
		{name: "shorter than the limit", contents: "a\nb\n", lines: 3, want: "a\nb", wantKept: 2},
		{name: "exactly the limit", contents: "a\nb\nc\n", lines: 3, want: "a\nb\nc", wantKept: 3},
		{name: "longer than the limit keeps the END", contents: "a\nb\nc\nd\n", lines: 3, want: "b\nc\nd", wantKept: 3},
		{name: "no trailing newline", contents: "a\nb", lines: 3, want: "a\nb", wantKept: 2},
		{name: "blank lines inside are kept", contents: "a\n\nc\n", lines: 3, want: "a\n\nc", wantKept: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, kept := skillGuardDriverTail([]byte(testCase.contents), testCase.lines)
			if got != testCase.want {
				t.Errorf("tail = %q, want %q", got, testCase.want)
			}
			if kept != testCase.wantKept {
				t.Errorf("kept = %d, want %d", kept, testCase.wantKept)
			}
		})
	}
}
