package doctor

import (
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
)

func TestParseSelector(t *testing.T) {
	tests := []struct {
		in       string
		wantHash string
		wantSID  string
		wantErr  bool
	}{
		{in: sidA, wantSID: sidA},
		{in: "local:" + sidA, wantSID: sidA},
		{in: "proj:" + hash1 + ":" + sidA, wantHash: hash1, wantSID: sidA},
		// A legacy- or foreign-named bucket directory is addressable by the
		// refs Locate itself emits for it; only traversal shapes are rejected.
		{in: "proj:0123456789abcdef:" + sidA, wantHash: "0123456789abcdef", wantSID: sidA},
		// Colons are legal in directory names, so the sid boundary in a ref
		// is the last colon, not the first.
		{in: "proj:a:b:" + sidA, wantHash: "a:b", wantSID: sidA},
		{in: "", wantErr: true},
		{in: "current", wantErr: true},
		{in: "proj:onlyonepart", wantErr: true},    // missing the :<id>
		{in: "proj::" + sidA, wantErr: true},       // empty hash
		{in: "proj:" + hash1 + ":", wantErr: true}, // empty sid
		{in: "local:", wantErr: true},
		{in: "../escape", wantErr: true},
		{in: "a/b", wantErr: true},
		{in: "has.dot", wantErr: true},
		// The relaxed project-id policy must still refuse everything that
		// could turn the token into a path component escape.
		{in: "proj:../h:" + sidA, wantErr: true},
		{in: "proj:..:" + sidA, wantErr: true},
		{in: "proj:.:" + sidA, wantErr: true},
		{in: "proj:a/b:" + sidA, wantErr: true},
		{in: "proj:a\\b:" + sidA, wantErr: true},
		{in: "proj:a\x00b:" + sidA, wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseSelector(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSelector(%q) = %+v, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSelector(%q) error: %v", tt.in, err)
			continue
		}
		if got.projectID != tt.wantHash || got.sid != tt.wantSID {
			t.Errorf("parseSelector(%q) = {projectID:%q sid:%q}, want {projectID:%q sid:%q}",
				tt.in, got.projectID, got.sid, tt.wantHash, tt.wantSID)
		}
	}
}

// TestSessionIDGrammarCarriesNoColon pins the property the last-colon proj:
// parse leans on: a session id is exactly the base62 payload, so it can never
// contain a colon and the final colon of a ref is always the sid boundary.
func TestSessionIDGrammarCarriesNoColon(t *testing.T) {
	if strings.ContainsAny(sidA, ":") {
		t.Fatalf("fixture sid %q contains a colon", sidA)
	}
	// Same length, one payload char swapped for a colon: rejection proves the
	// colon is what fails, not the length.
	colonized := strings.Replace(sidA, "l", ":", 1)
	if identifier.ValidateSessionID(colonized) == nil {
		t.Errorf("ValidateSessionID(%q) accepted a colon inside the payload", colonized)
	}
	for i := range sidA {
		if identifier.ValidateSessionID(sidA[:i]+":"+sidA[i:]) == nil {
			t.Errorf("ValidateSessionID accepted a colon inserted at %d", i)
		}
	}
}
