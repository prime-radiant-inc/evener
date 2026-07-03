package worktree

import "testing"

func TestFormatSessionMarker(t *testing.T) {
	got := FormatSessionMarker("ag_01HXYZ")
	want := "serf:ag_01HXYZ"
	if got != want {
		t.Errorf("FormatSessionMarker() = %q, want %q", got, want)
	}
}

func TestFormatDelegateMarker(t *testing.T) {
	got := FormatDelegateMarker("dlg_01ABC", "ag_01HXYZ")
	want := "serf:dlg:dlg_01ABC:ag_01HXYZ"
	if got != want {
		t.Errorf("FormatDelegateMarker() = %q, want %q", got, want)
	}
}

func TestParseMarker_SessionMarker(t *testing.T) {
	m, ok := ParseMarker("serf:ag_01HXYZ")
	if !ok {
		t.Fatalf("ParseMarker() ok = false, want true")
	}
	want := Marker{SessionID: "ag_01HXYZ"}
	if m != want {
		t.Errorf("ParseMarker() = %+v, want %+v", m, want)
	}
}

func TestParseMarker_DelegateMarker(t *testing.T) {
	m, ok := ParseMarker("serf:dlg:dlg_01ABC:ag_01HXYZ")
	if !ok {
		t.Fatalf("ParseMarker() ok = false, want true")
	}
	want := Marker{SessionID: "ag_01HXYZ", DelegateID: "dlg_01ABC"}
	if m != want {
		t.Errorf("ParseMarker() = %+v, want %+v", m, want)
	}
}

func TestParseMarker_RoundTrip(t *testing.T) {
	sessionReason := FormatSessionMarker("ag_01HXYZ")
	m, ok := ParseMarker(sessionReason)
	if !ok || m.DelegateID != "" || m.SessionID != "ag_01HXYZ" {
		t.Fatalf("session round-trip: got %+v, %v", m, ok)
	}
	if FormatSessionMarker(m.SessionID) != sessionReason {
		t.Errorf("session round-trip did not reproduce original reason")
	}

	delegateReason := FormatDelegateMarker("dlg_01ABC", "ag_01HXYZ")
	m, ok = ParseMarker(delegateReason)
	if !ok || m.DelegateID != "dlg_01ABC" || m.SessionID != "ag_01HXYZ" {
		t.Fatalf("delegate round-trip: got %+v, %v", m, ok)
	}
	if FormatDelegateMarker(m.DelegateID, m.SessionID) != delegateReason {
		t.Errorf("delegate round-trip did not reproduce original reason")
	}
}

func TestParseMarker_Foreign(t *testing.T) {
	cases := []string{
		"",                // reasonless lock (git's bare `locked`)
		"serf:",           // trailing colon, no session id
		"serf",            // no colon at all
		"serf:dlg:x",      // missing the parent-session segment
		"serf:dlg::",      // both dlg and parent segments empty
		"serf:dlg:x:",     // parent segment present but empty
		"serf:dlg::y",     // dlg segment present but empty
		"serf:dlg:a:b:c",  // five segments — over-long, not guessed at
		"serf:a:b",        // three segments, not a "dlg" marker
		"random text",     // not a serf marker at all
		"serfx:ag_01HXYZ", // looks close but wrong prefix token
		" serf:ag_01HXYZ", // leading whitespace corrupts the prefix
		"SERF:ag_01HXYZ",  // case-sensitive: not a match
	}
	for _, reason := range cases {
		if m, ok := ParseMarker(reason); ok {
			t.Errorf("ParseMarker(%q) = %+v, true; want foreign (false)", reason, m)
		}
	}
}
