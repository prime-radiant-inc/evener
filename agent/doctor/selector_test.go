package doctor

import "testing"

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
