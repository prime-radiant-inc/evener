package worktree

import "testing"

func TestFormatSessionMarker(t *testing.T) {
	got := FormatSessionMarker("01HXYZABCD0123456789ABCDEF")
	want := "evener:01HXYZABCD0123456789ABCDEF"
	if got != want {
		t.Errorf("FormatSessionMarker() = %q, want %q", got, want)
	}
}

func TestFormatDelegateMarker(t *testing.T) {
	got := FormatDelegateMarker("dlg_01JXYZABCD0123456789ABCDEF", "01HXYZABCD0123456789ABCDEF")
	want := "evener:dlg:dlg_01JXYZABCD0123456789ABCDEF:01HXYZABCD0123456789ABCDEF"
	if got != want {
		t.Errorf("FormatDelegateMarker() = %q, want %q", got, want)
	}
}

func TestParseMarker_SessionMarker(t *testing.T) {
	m, ok := ParseMarker("evener:01HXYZABCD0123456789ABCDEF")
	if !ok {
		t.Fatalf("ParseMarker() ok = false, want true")
	}
	want := Marker{SessionID: "01HXYZABCD0123456789ABCDEF"}
	if m != want {
		t.Errorf("ParseMarker() = %+v, want %+v", m, want)
	}
}

func TestParseMarker_DelegateMarker(t *testing.T) {
	m, ok := ParseMarker("evener:dlg:dlg_01JXYZABCD0123456789ABCDEF:01HXYZABCD0123456789ABCDEF")
	if !ok {
		t.Fatalf("ParseMarker() ok = false, want true")
	}
	want := Marker{SessionID: "01HXYZABCD0123456789ABCDEF", DelegateID: "dlg_01JXYZABCD0123456789ABCDEF"}
	if m != want {
		t.Errorf("ParseMarker() = %+v, want %+v", m, want)
	}
}

func TestParseMarker_RoundTrip(t *testing.T) {
	sessionReason := FormatSessionMarker("01HXYZABCD0123456789ABCDEF")
	m, ok := ParseMarker(sessionReason)
	if !ok || m.DelegateID != "" || m.SessionID != "01HXYZABCD0123456789ABCDEF" {
		t.Fatalf("session round-trip: got %+v, %v", m, ok)
	}
	if FormatSessionMarker(m.SessionID) != sessionReason {
		t.Errorf("session round-trip did not reproduce original reason")
	}

	delegateReason := FormatDelegateMarker("dlg_01JXYZABCD0123456789ABCDEF", "01HXYZABCD0123456789ABCDEF")
	m, ok = ParseMarker(delegateReason)
	if !ok || m.DelegateID != "dlg_01JXYZABCD0123456789ABCDEF" || m.SessionID != "01HXYZABCD0123456789ABCDEF" {
		t.Fatalf("delegate round-trip: got %+v, %v", m, ok)
	}
	if FormatDelegateMarker(m.DelegateID, m.SessionID) != delegateReason {
		t.Errorf("delegate round-trip did not reproduce original reason")
	}
}

func TestParseMarker_Foreign(t *testing.T) {
	cases := []string{
		"",                                 // reasonless lock (git's bare `locked`)
		"evener:",                            // trailing colon, no session id
		"evener",                             // no colon at all
		"evener:dlg:x",                       // missing the parent-session segment
		"evener:dlg::",                       // both dlg and parent segments empty
		"evener:dlg:x:",                      // parent segment present but empty
		"evener:dlg::y",                      // dlg segment present but empty
		"evener:dlg:a:b:c",                   // five segments — over-long, not guessed at
		"evener:a:b",                         // three segments, not a "dlg" marker
		"random text",                      // not a evener marker at all
		"evenerx:01HXYZABCD0123456789ABCDEF", // looks close but wrong prefix token
		" evener:01HXYZABCD0123456789ABCDEF", // leading whitespace corrupts the prefix
		"SERF:01HXYZABCD0123456789ABCDEF",  // case-sensitive: not a match
	}
	for _, reason := range cases {
		if m, ok := ParseMarker(reason); ok {
			t.Errorf("ParseMarker(%q) = %+v, true; want foreign (false)", reason, m)
		}
	}
}
