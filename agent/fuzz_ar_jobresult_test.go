package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file fuzzes jobResultBody (transcript_render.go), the renderer that turns a
// job lifecycle tool-result JSON body into a status line ("job_id=… status=… …")
// followed by the de-escaped output. It reports false (falling back to generic
// JSON rendering) when the body does not decode as a jobResult or carries keys
// beyond the known set. Input is an arbitrary body string.

// FuzzArJobResultBody drives jobResultBody over fuzzed result bodies. Oracles:
//
//   - never panics on arbitrary/malformed JSON bytes;
//   - DETERMINISM: (rendered, ok) is a pure function of the body;
//   - STATUS-LINE SHAPE: whenever it renders (ok), the body carries the "job_id=",
//     "status=", and "transcript_ref=" fields the audit pivot relies on. (Field
//     VALUES may themselves contain newlines, so the fields are matched against the
//     whole rendered body, not a single physical line.)
//   - VALID UTF-8 preservation for valid-UTF-8 input.
func FuzzArJobResultBody(f *testing.F) {
	seeds := []string{
		"",
		`{"job_id":"j1","status":"completed","transcript_ref":"local:x","output":"hi\nthere"}`,
		`{"job_id":"j1","status":"running","transcript_ref":"","reason":"waiting"}`,
		`{"delegate_id":"d1","status":"completed","structured_result":{"n":1}}`,
		`{"job_id":"j1","status":"ok","unexpected_key":true}`, // extra key → fall back
		`{"transcript_ref":"local:only"}`,
		`not json`,
		`{"job_id":123}`,
		`{"job_id":"j","status":"s","output":"世界 🌍 é"}`,
		`[1,2,3]`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, body string) {
		rendered, ok := jobResultBody(body)

		if r2, ok2 := jobResultBody(body); r2 != rendered || ok2 != ok {
			t.Fatalf("jobResultBody non-deterministic")
		}

		if !ok {
			return
		}
		for _, field := range []string{"job_id=", "status=", "transcript_ref="} {
			if !strings.Contains(rendered, field) {
				t.Fatalf("jobResultBody rendering missing %q: %q", field, rendered)
			}
		}
		if utf8.ValidString(body) && !utf8.ValidString(rendered) {
			t.Fatalf("jobResultBody emitted invalid UTF-8 from valid input")
		}
	})
}
