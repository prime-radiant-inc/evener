//go:build serffuzz

package agent

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// partialJSONStringField / unquoteJSONUnicodeEscape are the lenient, streaming
// JSON string-field extractor session_stream.go uses to pull a named field
// (e.g. an assistant text or a model id) out of a possibly-truncated response
// body while the stream is still arriving. They hand-roll a JSON string scanner
// (quote handling, `\/`, `\uXXXX` with surrogate-pair decoding, strconv escape
// fallback) rather than json.Unmarshal, because the body may be cut mid-string.
// That hand-rolled path eats untrusted bytes and had no fuzz coverage.
//
// FuzzAcPartialJSONStringField asserts three properties:
//
//   - never-panic + determinism: the extractor and the escape decoder run twice
//     on arbitrary bytes and must agree with themselves.
//   - well-formed round-trip: for a genuinely valid single-field object
//     {"<field>":<value>} produced by encoding/json, the lenient extractor must
//     recover exactly the value encoding/json put in. This pins the extractor's
//     escape handling (`\n`, `\"`, `\uXXXX`, HTML `<`, multi-byte runes,
//     astral round-trips) against the standard library as the oracle.
//
// The round-trip oracle only fires when the field name is JSON-key-safe (so its
// marshaled key is the literal `"field"` the extractor searches for) and the
// value is valid UTF-8 (so encoding/json does not silently substitute U+FFFD,
// which would make "recovered == input" false through no fault of the parser).
func FuzzAcPartialJSONStringField(f *testing.F) {
	type seed struct {
		raw, field, value string
	}
	seeds := []seed{
		{`{"text":"hello"}`, "text", "hello"},
		{`{"text":"a\"b"}`, "text", `a"b`},
		{`{"model":"gpt-4o"} trailing junk`, "model", "gpt-4o"},
		{`{"text":"line\nbreak\ttab"}`, "text", "line\nbreak\ttab"},
		{`{"text":"slash\/escaped"}`, "text", "slash/escaped"},
		{`{"text":"unicode é <"}`, "text", "unicode é <"},
		{`{"text":"astral 😀"}`, "text", "astral 😀"},
		{`{"text":"truncated`, "text", "trunc"},
		{`{"other":"x","text":"second"}`, "text", "second"},
		{`no field here`, "text", ""},
		{`{"":"emptykey"}`, "", "emptykey"},
		{`{"text":"bad \u"}`, "text", ""},
		{`{"text":"lonely surrogate \ud800"}`, "text", ""},
	}
	for _, s := range seeds {
		f.Add(s.raw, s.field, s.value)
	}

	f.Fuzz(func(t *testing.T, raw, field, value string) {
		// never-panic + determinism on the raw/field the fuzzer supplied.
		got1, ok1 := partialJSONStringField(raw, field)
		got2, ok2 := partialJSONStringField(raw, field)
		if got1 != got2 || ok1 != ok2 {
			t.Fatalf("partialJSONStringField nondeterministic for raw=%q field=%q: (%q,%v) vs (%q,%v)",
				raw, field, got1, ok1, got2, ok2)
		}
		// The escape decoder is reachable directly from the raw bytes too.
		r1, tail1, e1 := unquoteJSONUnicodeEscape(raw)
		r2, tail2, e2 := unquoteJSONUnicodeEscape(raw)
		if r1 != r2 || tail1 != tail2 || e1 != e2 {
			t.Fatalf("unquoteJSONUnicodeEscape nondeterministic for %q", raw)
		}

		// Round-trip oracle against encoding/json.
		if field == "" || !acJSONKeySafe(field) || !utf8.ValidString(value) {
			return
		}
		encoded, err := json.Marshal(map[string]string{field: value})
		if err != nil {
			t.Fatalf("marshal oracle object: %v", err)
		}
		recovered, ok := partialJSONStringField(string(encoded), field)
		if !ok {
			t.Fatalf("extractor failed to find field %q in valid object %s", field, encoded)
		}
		if recovered != value {
			t.Fatalf("round-trip mismatch for field %q:\n  input    =%q\n  recovered=%q\n  object   =%s",
				field, value, recovered, encoded)
		}
	})
}

// acJSONKeySafe reports whether s marshals as a JSON object key without any escape
// sequences, so the marshaled key is exactly `"s"` — the literal the streaming
// extractor searches for. Only unescaped, non-quote, non-backslash printable
// ASCII qualifies; any character encoding/json would escape (control chars,
// quote, backslash, HTML `<>&`, non-ASCII) disqualifies the oracle.
func acJSONKeySafe(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
		switch c {
		case '"', '\\', '<', '>', '&':
			return false
		}
	}
	return true
}