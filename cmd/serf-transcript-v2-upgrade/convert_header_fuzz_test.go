package main

// FuzzConvertHeader covers the v1→v2 transcript header decode surface
// (json.Unmarshal of attacker/corruption-controlled transcript lines in
// convertHeader). Seeds pin both the happy path (a valid legacy header that
// must convert and re-validate) and malformed inputs (which must error, never
// panic). A successful conversion must always re-validate under
// transcript.DecodeHeader — convertHeader checks exactly this, so a violation
// would be a real contract breach.

import (
	"bytes"
	"testing"

	"primeradiant.com/serf/agent/transcript"
)

func FuzzConvertHeader(f *testing.F) {
	f.Add([]byte(`{"kind":"header","format_version":1,"session_id":"s","profile_id":"p","model":"m"}`))
	f.Add([]byte(`{"kind":"header","format_version":2}`))
	f.Add([]byte(`{"kind":"entry","format_version":1}`))
	f.Add([]byte(`{"kind":"header"}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"format_version":"1"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := convertHeader(append([]byte(nil), data...))
		if err != nil {
			return
		}
		if !bytes.Contains(out, []byte(`"format_version"`)) {
			t.Fatalf("converted header lacks format_version: %q", out)
		}
		if _, derr := transcript.DecodeHeader(out); derr != nil {
			t.Fatalf("convertHeader succeeded but DecodeHeader rejects the output: %v (%q)", derr, out)
		}
	})
}
