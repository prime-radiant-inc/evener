package appwire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFlagDayProtocolVersion(t *testing.T) {
	if ProtocolVersion != "evener-appwire-v4" {
		t.Fatalf("protocol = %q, want %q", ProtocolVersion, "evener-appwire-v4")
	}
}

func TestFlagDayRejectsRetiredTranscriptPagingFields(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "thread read page unit", raw: `{"ref":"local:thread","pageUnit":"turn"}`},
		{name: "thread read turn limit", raw: `{"ref":"local:thread","turnLimit":1}`},
		{name: "turns list page unit", raw: `{"ref":"local:thread","cursor":"opaque","pageUnit":"turn"}`},
		{name: "turns list limit", raw: `{"ref":"local:thread","cursor":"opaque","limit":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if strings.Contains(test.name, "turns list") {
				var params ThreadTurnsListParams
				err = json.Unmarshal([]byte(test.raw), &params)
			} else {
				var params ThreadReadParams
				err = json.Unmarshal([]byte(test.raw), &params)
			}
			if err == nil {
				t.Fatalf("retired paging field accepted: %s", test.raw)
			}
		})
	}
}

func TestFlagDayTurnsListRequiresOpaqueCursor(t *testing.T) {
	for _, cursor := range []string{"", "42", "99999999999999999999999999999999999999999999999999", " 42 ", "+42", "-42"} {
		t.Run("cursor="+cursor, func(t *testing.T) {
			if err := ValidateThreadTurnsListParams(ThreadTurnsListParams{Cursor: cursor}); err == nil {
				t.Fatalf("cursor %q accepted; want nonempty opaque cursor", cursor)
			}
		})
	}
	if err := ValidateThreadTurnsListParams(ThreadTurnsListParams{Cursor: "opaque"}); err != nil {
		t.Fatalf("opaque cursor rejected: %v", err)
	}
}
