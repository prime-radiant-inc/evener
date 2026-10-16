package appoverlay

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSkillWarningPreservesOwnerGuidance(t *testing.T) {
	want := map[string]string{"source": "evener", "title": "TITLE_752", "hint": "HINT_752", "code": "skill_diagnostic"}
	got := warningAnnouncement(want["source"], want["title"], want["hint"], want["code"], "MESSAGE_752")
	if got.EventKind != appwire.ThreadItemEventKindWarning || got.Description != want["title"] || got.Text != "MESSAGE_752" {
		t.Fatalf("warning projection=%+v", got)
	}
	var raw map[string]map[string]string
	if err := json.Unmarshal(got.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, map[string]map[string]string{"warning": want}) {
		t.Fatalf("warning metadata=%v", raw)
	}
}
