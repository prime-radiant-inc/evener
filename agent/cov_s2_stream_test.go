package agent

import "testing"

// bsEsc is a single backslash, built without a literal so test-source escaping
// stays unambiguous when composing raw JSON escape sequences below.
var bsEsc = string(rune(92))

// s2cov_ tests for the streaming JSON-field scanners partialJSONStringField and
// unquoteJSONUnicodeEscape. These parse partially-streamed tool-call argument
// JSON (e.g. the communicate output field) before the object is complete, so
// they must tolerate truncation and every JSON string escape.

func TestS2Cov_PartialJSONStringField(t *testing.T) {
	t.Parallel()
	nl := "\n"
	cases := []struct {
		name  string
		raw   string
		field string
		want  string
		ok    bool
	}{
		{"field missing", `{"other":"x"}`, "output", "", false},
		{"no colon after key", `{"output" x`, "output", "", false},
		{"value not a string", `{"output": 123}`, "output", "", false},
		{"plain closed string", `{"output":"hello"}`, "output", "hello", true},
		{"solidus escape", `{"output":"a` + bsEsc + `/b"}`, "output", "a/b", true},
		{"unicode escape", `{"output":"a` + bsEsc + `u0041b"}`, "output", "aAb", true},
		{"surrogate pair emoji", `{"output":"x` + bsEsc + `uD83D` + bsEsc + `uDE00y"}`, "output", "x\U0001F600y", true},
		{"truncated high surrogate", `{"output":"` + bsEsc + `uD800`, "output", "", true},
		{"go escape newline", `{"output":"a` + bsEsc + `nb"}`, "output", "a" + nl + "b", true},
		{"invalid escape stops", `{"output":"ab` + bsEsc + `x`, "output", "ab", true},
		{"unterminated string", `{"output":"abc`, "output", "abc", true},
		{"whitespace before value", "{\"output\":\t\n \"hi\"}", "output", "hi", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := partialJSONStringField(tc.raw, tc.field)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("partialJSONStringField(%q,%q) = (%q,%v), want (%q,%v)", tc.raw, tc.field, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestS2Cov_UnquoteJSONUnicodeEscape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		rest     string
		wantRune rune
		wantTail string
		ok       bool
	}{
		{"too short", bsEsc + "u12", 0, "", false},
		{"bad hex", bsEsc + "uZZZZrest", 0, "", false},
		{"basic bmp", bsEsc + "u0041tail", 'A', "tail", true},
		{"valid surrogate pair", bsEsc + "uD83D" + bsEsc + "uDE00tail", '\U0001F600', "tail", true},
		{"high surrogate no low", bsEsc + "uD800short", 0, "", false},
		{"high surrogate bad low hex", bsEsc + "uD800" + bsEsc + "uZZZZ", 0, "", false},
		{"high surrogate low out of range", bsEsc + "uD800" + bsEsc + "u0041", 0, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, tail, ok := unquoteJSONUnicodeEscape(tc.rest)
			if r != tc.wantRune || tail != tc.wantTail || ok != tc.ok {
				t.Fatalf("unquoteJSONUnicodeEscape(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.rest, r, tail, ok, tc.wantRune, tc.wantTail, tc.ok)
			}
		})
	}
}
