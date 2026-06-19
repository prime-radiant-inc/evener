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
		{in: "", wantErr: true},
		{in: "current", wantErr: true},
		{in: "proj:onlyonepart", wantErr: true}, // missing the :<id>
		{in: "proj::" + sidA, wantErr: true},     // empty hash
		{in: "proj:" + hash1 + ":", wantErr: true}, // empty sid
		{in: "local:", wantErr: true},
		{in: "../escape", wantErr: true},
		{in: "a/b", wantErr: true},
		{in: "has.dot", wantErr: true},
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
		if got.hash != tt.wantHash || got.sid != tt.wantSID {
			t.Errorf("parseSelector(%q) = {hash:%q sid:%q}, want {hash:%q sid:%q}",
				tt.in, got.hash, got.sid, tt.wantHash, tt.wantSID)
		}
	}
}
