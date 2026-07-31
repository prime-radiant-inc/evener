package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureWithLegacyReservedTurnID lays out two sessions: one whose persisted
// reserved turn id sits in the transcript's entry-index namespace ("turn_11"),
// and one whose reservation is namespaced ("turn_m2"). The cmd layer writes the
// on-disk bytes directly — the fold semantics are covered by agent/doctor tests.
func fixtureWithLegacyReservedTurnID(t *testing.T) (base, affected, clean string) {
	t.Helper()
	base = t.TempDir()
	affected = "02wLIRxqmq3AUo6vl2OW37"
	clean = "02wLIRxqmq3AUo6vl2OW38"
	sess := filepath.Join(base, "serf", "projects", "project-test-0123456789", "sessions")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(sid, stableTurnID string) {
		mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"),
			`{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n"+
				`{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"ask"}]},"client_mutation_id":"cm-1","stable_turn_id":"`+stableTurnID+`"}}`+"\n")
	}
	write(affected, "turn_11")
	write(clean, "turn_m2")
	return base, affected, clean
}

func TestRun_TurnIDsHuman(t *testing.T) {
	base, affected, clean := fixtureWithLegacyReservedTurnID(t)
	var out, errb bytes.Buffer
	if code := run([]string{"turnids", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"scanned 2 sessions", affected, "turn_11", "entry 1", "USER_INPUT"} {
		if !strings.Contains(got, want) {
			t.Errorf("turnids output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, clean) {
		t.Errorf("turnids named the session whose reservation is namespaced:\n%s", got)
	}
}

func TestRun_TurnIDsJSON(t *testing.T) {
	base, affected, _ := fixtureWithLegacyReservedTurnID(t)
	var out, errb bytes.Buffer
	if code := run([]string{"turnids", "--json", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var res struct {
		SessionsScanned int `json:"sessions_scanned"`
		Sessions        []struct {
			SessionID     string `json:"session_id"`
			ReservedTurns []struct {
				TurnID     string `json:"turn_id"`
				EntryIndex int    `json:"entry_index"`
				Kind       string `json:"kind"`
			} `json:"reserved_turns"`
		} `json:"sessions"`
		Unreadable []struct {
			SessionID string `json:"session_id"`
		} `json:"unreadable"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if res.SessionsScanned != 2 {
		t.Errorf("sessions_scanned = %d, want 2", res.SessionsScanned)
	}
	if len(res.Unreadable) != 0 {
		t.Errorf("unreadable = %#v, want none", res.Unreadable)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %#v, want exactly the affected one\n%s", res.Sessions, out.String())
	}
	if res.Sessions[0].SessionID != affected {
		t.Errorf("session_id = %q, want %q", res.Sessions[0].SessionID, affected)
	}
	if len(res.Sessions[0].ReservedTurns) != 1 {
		t.Fatalf("reserved_turns = %#v, want one", res.Sessions[0].ReservedTurns)
	}
	reserved := res.Sessions[0].ReservedTurns[0]
	if reserved.TurnID != "turn_11" || reserved.EntryIndex != 1 || reserved.Kind != "USER_INPUT" {
		t.Errorf("reserved turn = %+v, want turn_11 on entry 1 of a USER_INPUT turn", reserved)
	}
}

// Every other session subcommand takes a selector in this position, so a
// selector here has to be refused out loud: silently sweeping the whole state
// root would answer a question the caller did not ask.
func TestRun_TurnIDsRefusesASelector(t *testing.T) {
	base, affected, _ := fixtureWithLegacyReservedTurnID(t)
	var out, errb bytes.Buffer
	if code := run([]string{"turnids", affected, "--state-dir", base}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "serf-doctor turnids: takes no selector") {
		t.Errorf("stderr should explain that turnids takes no selector; got %q", errb.String())
	}
}

// The subcommand has to be reachable from the usage index, or nobody finds it.
func TestRun_UsageListsTurnIDs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--help"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "turnids") {
		t.Errorf("usage does not list turnids:\n%s", out.String())
	}
}
