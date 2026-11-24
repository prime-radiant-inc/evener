package schema

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestQuietCommunicateCommentary(t *testing.T) {
	t.Parallel()
	tr := true
	fa := false

	rawQuiet, err := json.Marshal(CommunicateToolState{
		Accepted:        &tr,
		EndTurn:         &tr,
		Quiet:           &tr,
		QuietNote:       "waiting on CI",
		QuietCommentary: "  Running the suite now.  ",
	})
	if err != nil {
		t.Fatalf("Marshal quiet state: %v", err)
	}
	if got := QuietCommunicateCommentary(rawQuiet); got != "Running the suite now." {
		t.Fatalf("QuietCommunicateCommentary(quiet) = %q, want trimmed commentary", got)
	}
	if got := QuietCommunicateNote(rawQuiet); got != "waiting on CI" {
		t.Fatalf("QuietCommunicateNote(quiet) = %q, want %q", got, "waiting on CI")
	}

	rawNoCommentary, err := json.Marshal(CommunicateToolState{
		Accepted: &tr,
		EndTurn:  &tr,
		Quiet:    &tr,
	})
	if err != nil {
		t.Fatalf("Marshal quiet state without commentary: %v", err)
	}
	if got := QuietCommunicateCommentary(rawNoCommentary); got != "" {
		t.Fatalf("QuietCommunicateCommentary(no commentary) = %q, want empty", got)
	}

	rawLoud, err := json.Marshal(CommunicateToolState{
		Accepted:        &tr,
		EndTurn:         &tr,
		Quiet:           &fa,
		QuietCommentary: "ignored when not quiet",
	})
	if err != nil {
		t.Fatalf("Marshal non-quiet state: %v", err)
	}
	if got := QuietCommunicateCommentary(rawLoud); got != "" {
		t.Fatalf("QuietCommunicateCommentary(non-quiet) = %q, want empty", got)
	}

	if got := QuietCommunicateCommentary(nil); got != "" {
		t.Fatalf("QuietCommunicateCommentary(nil) = %q, want empty", got)
	}
	if got := QuietCommunicateCommentary(json.RawMessage(`{invalid`)); got != "" {
		t.Fatalf("QuietCommunicateCommentary(invalid) = %q, want empty", got)
	}
}

func TestTurnQuietCompanionJSONRoundTrip(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Turn{Kind: TurnAssistant, QuietCompanion: true})
	if err != nil {
		t.Fatalf("Marshal Turn: %v", err)
	}
	var got Turn
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal Turn: %v", err)
	}
	if !got.QuietCompanion {
		t.Fatalf("QuietCompanion = false after round-trip, raw=%s", data)
	}
	if got.QuietYield {
		t.Fatalf("QuietYield = true after round-trip, raw=%s", data)
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

func TestNormalizeQuietNote(t *testing.T) {
	t.Parallel()
	if got := NormalizeQuietNote("  waiting\non\t\tbuild\n  step 2  "); got != "waiting on build step 2" {
		t.Fatalf("NormalizeQuietNote whitespace = %q", got)
	}
	exact := strings.Repeat("x", CommunicateQuietNoteMaxRunes)
	if got := NormalizeQuietNote(exact); got != exact {
		t.Fatalf("NormalizeQuietNote exact boundary truncated: len=%d", utf8.RuneCountInString(got))
	}
	over := strings.Repeat("y", CommunicateQuietNoteMaxRunes-1) + "  " + "trailing"
	got := NormalizeQuietNote(over)
	if utf8.RuneCountInString(got) > CommunicateQuietNoteMaxRunes || !strings.HasSuffix(got, "…") || strings.Contains(got, " …") {
		t.Fatalf("NormalizeQuietNote over = %q (runes=%d)", got, utf8.RuneCountInString(got))
	}
}
