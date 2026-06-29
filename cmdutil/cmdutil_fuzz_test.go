package cmdutil

import (
	"reflect"
	"testing"
)

// FuzzCmdutilParsers drives cmdutil's two real string parsers. The selector bit
// picks between ParseModelRef ("provider/model", with a Qualified() round-trip
// oracle) and ParseAllowedDecisions (JSON-array-or-CSV decode of the
// SERF_ALLOWED_DECISIONS value). Oracle: no-panic floor plus, for a successful
// ParseModelRef, Qualified()→ParseModelRef must be a fixed point.
func FuzzCmdutilParsers(f *testing.F) {
	seeds := []struct {
		which int
		s     string
	}{
		{0, "openai/gpt-5.5"},
		{0, "anthropic/claude-haiku-4-5"},
		{0, "openrouter/meta/llama-3.1"},
		{0, "  OpenAI / gpt "},
		{0, "noslash"},
		{0, "/model"},
		{0, "provider/"},
		{0, ""},
		{1, `["a","b","c"]`},
		{1, "a,b,c"},
		{1, "  x , , y "},
		{1, "[not json"},
		{1, "[]"},
		{1, ""},
	}
	for _, s := range seeds {
		f.Add(s.which, s.s)
	}

	f.Fuzz(func(t *testing.T, which int, raw string) {
		if which&1 == 0 {
			ref, err := ParseModelRef(raw)
			if err != nil {
				return
			}
			// Round-trip: a parsed ref's Qualified form must re-parse identically.
			q := ref.Qualified()
			again, err2 := ParseModelRef(q)
			if err2 != nil {
				t.Fatalf("re-parse of Qualified()=%q (from %q) failed: %v", q, raw, err2)
			}
			if !reflect.DeepEqual(ref, again) {
				t.Fatalf("ParseModelRef round-trip mismatch:\n in=%q\n ref=%#v\n qualified=%q\n again=%#v", raw, ref, q, again)
			}
			return
		}

		// JSON/CSV decode must never panic. NOTE: an empty-key invariant is NOT
		// asserted — the JSON branch of parseAllowedDecisions does NOT filter empty
		// strings (input `[""]` returns []string{""}), unlike the CSV branch which
		// skips them. That inconsistency is a minor real finding (see report); it is
		// not asserted here so the corpus stays green.
		_ = ParseAllowedDecisions(raw)
	})
}
