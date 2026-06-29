package hooks

import (
	"testing"
)

// FuzzParseHookOutput drives parseHookOutput — the package's real hook-stdout
// decode seam (json.Unmarshal of the hook JSON protocol, with plain-text
// fallback). Input is the hook's stdout plus its exit code. Beyond no-panic it
// asserts the exit-code contract: a non-zero exit is always reported as an error
// carrying the raw output, and the recorded RawExitCode always echoes the input.
func FuzzParseHookOutput(f *testing.F) {
	seeds := []struct {
		out  string
		code int
	}{
		{`{"continue":false,"systemMessage":"stop"}`, 0},
		{`{"decision":"block","reason":"nope"}`, 0},
		{`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"x","updatedInput":{"a":1},"additionalContext":"ctx"}}`, 0},
		{`{"suppressOutput":true,"terminalSequence":"\u001b[0m"}`, 0},
		{"plain text message", 0},
		{"error happened", 2},
		{"", 0},
		{"not json {", 0},
		{`{"decision":"approve"}`, 0},
	}
	for _, s := range seeds {
		f.Add(s.out, s.code)
	}

	f.Fuzz(func(t *testing.T, out string, code int) {
		result := parseHookOutput(out, code)

		if result.RawExitCode != code {
			t.Fatalf("RawExitCode = %d, want echoed input %d", result.RawExitCode, code)
		}
		if code != 0 {
			if !result.IsError {
				t.Fatalf("non-zero exit %d not reported as error", code)
			}
			if result.SystemMessage != out {
				t.Fatalf("non-zero exit dropped raw output: got %q want %q", result.SystemMessage, out)
			}
		}
	})
}
