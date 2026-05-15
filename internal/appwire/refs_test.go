package appwire

import "testing"

func TestRefRoundTrip(t *testing.T) {
	ref := Ref{SourceID: "local", ThreadID: "th_01HX"}
	if ref.String() != "local:th_01HX" {
		t.Fatalf("String=%q", ref.String())
	}
	parsed, err := ParseRef("local:th_01HX")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if parsed.SourceID != "local" || parsed.ThreadID != "th_01HX" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseRefRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"", "local", "local:", ":th_1", "local:../x", "local:with space"} {
		if _, err := ParseRef(raw); err == nil {
			t.Fatalf("ParseRef(%q) succeeded", raw)
		}
	}
}
