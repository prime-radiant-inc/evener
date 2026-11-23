package schema

import (
	"encoding/json"
	"testing"
)

func TestCommunicateToolStateQuietNote(t *testing.T) {
	t.Parallel()
	tr := true
	fa := false

	rawQuiet, err := json.Marshal(CommunicateToolState{
		Accepted:  &tr,
		EndTurn:   &tr,
		Quiet:     &tr,
		QuietNote: "  waiting on go test  ",
	})
	if err != nil {
		t.Fatalf("Marshal quiet state: %v", err)
	}
	if got := QuietCommunicateNote(rawQuiet); got != "waiting on go test" {
		t.Fatalf("QuietCommunicateNote(quiet) = %q, want %q", got, "waiting on go test")
	}

	rawLoud, err := json.Marshal(CommunicateToolState{
		Accepted:  &tr,
		EndTurn:   &tr,
		Quiet:     &fa,
		QuietNote: "ignored when not quiet",
	})
	if err != nil {
		t.Fatalf("Marshal non-quiet state: %v", err)
	}
	if got := QuietCommunicateNote(rawLoud); got != "" {
		t.Fatalf("QuietCommunicateNote(non-quiet) = %q, want empty", got)
	}

	if got := QuietCommunicateNote(nil); got != "" {
		t.Fatalf("QuietCommunicateNote(nil) = %q, want empty", got)
	}
	if got := QuietCommunicateNote(json.RawMessage(`{invalid`)); got != "" {
		t.Fatalf("QuietCommunicateNote(invalid) = %q, want empty", got)
	}
}

func TestTurnQuietYieldJSONRoundTrip(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Turn{Kind: TurnAssistant, QuietYield: true})
	if err != nil {
		t.Fatalf("Marshal Turn: %v", err)
	}
	var got Turn
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal Turn: %v", err)
	}
	if !got.QuietYield {
		t.Fatalf("QuietYield = false after round-trip, raw=%s", data)
	}
}

func TestCommunicateArgsOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                   string
		raw                    string
		wantEndTurn, wantQuiet bool
		wantOK                 bool
	}{
		{name: "empty", raw: ``, wantOK: false},
		{name: "malformed", raw: `{not json`, wantOK: false},
		{name: "omitted_defaults_send_and_end_turn", raw: `{"message":"hi"}`, wantEndTurn: true, wantOK: true},
		{name: "explicit_send_and_end_turn", raw: `{"message":"hi","next":"send_and_end_turn"}`, wantEndTurn: true, wantOK: true},
		{name: "send_and_continue", raw: `{"message":"hi","next":"send_and_continue"}`, wantOK: true},
		{name: "log_quietly_and_end_turn", raw: `{"message":"waiting","next":"log_quietly_and_end_turn"}`, wantQuiet: true, wantOK: true},
		{name: "legacy_end_turn_true", raw: `{"end_turn":true}`, wantEndTurn: true, wantOK: true},
		{name: "legacy_end_turn_false", raw: `{"end_turn":false}`, wantOK: true},
	} {
		endTurn, quiet, ok := CommunicateArgsOutcome(json.RawMessage(tc.raw))
		if endTurn != tc.wantEndTurn || quiet != tc.wantQuiet || ok != tc.wantOK {
			t.Errorf("%s: got (endTurn=%v quiet=%v ok=%v), want (%v, %v, %v)",
				tc.name, endTurn, quiet, ok, tc.wantEndTurn, tc.wantQuiet, tc.wantOK)
		}
	}
}
