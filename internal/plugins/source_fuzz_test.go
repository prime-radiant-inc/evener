//go:build serffuzz

package plugins

import (
	"encoding/json"
	"testing"
)

// FuzzSourceUnmarshalJSON drives Source.UnmarshalJSON — the plugin marketplace
// entry's custom source decoder (bare-string and object forms) — with
// arbitrary bytes. Invariants: it never panics on any input, and every source
// it accepts round-trips through MarshalJSON/UnmarshalJSON with a stable Kind
// (the decode normalizes legacy aliases like "git" -> url, so re-decoding the
// marshaled form must reproduce the same normalized Kind).
func FuzzSourceUnmarshalJSON(f *testing.F) {
	for _, s := range []string{
		`"./subdir"`,
		`{"source":"github","repo":"owner/repo"}`,
		`{"source":"url","url":"https://example.com/x.git"}`,
		`{"source":"git-subdir","url":"https://example.com/x.git","path":"sub"}`,
		`{"source":"directory","path":"./d","rel":true}`,
		`{"source":"git","url":"https://example.com/x.git"}`, // legacy alias -> url
		`{"source":"npm","package":"whatever"}`,              // unknown -> error
		`{}`,
		``,
		`null`,
		`123`,
		`"`,
		`{"source":`,
		`{"source":"github","repo":123}`,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		var s Source
		if err := s.UnmarshalJSON(b); err != nil {
			return // rejecting malformed input is expected; the contract is "no panic"
		}
		out, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("Marshal after a successful UnmarshalJSON(%q) failed: %v", b, err)
		}
		var s2 Source
		if err := s2.UnmarshalJSON(out); err != nil {
			t.Fatalf("re-Unmarshal of the marshaled form failed: %v (input=%q marshaled=%s)", err, b, out)
		}
		if s2.Kind != s.Kind {
			t.Fatalf("Kind not stable across round-trip for %q: %q -> %q", b, s.Kind, s2.Kind)
		}
	})
}
