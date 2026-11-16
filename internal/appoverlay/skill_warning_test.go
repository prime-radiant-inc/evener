package appoverlay

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSkillWarningPreservesOwnerGuidance(t *testing.T) {
	want := map[string]string{"source": "evener", "title": "TITLE_752", "hint": "HINT_752", "code": "skill_diagnostic"}
	got := warningAnnouncement(warningFields{source: want["source"], title: want["title"], hint: want["hint"], code: want["code"], message: "MESSAGE_752"})
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

// A delegate-scoped warning keeps its delegate identity, and an attention
// refusal its refusing predicate, through the overlay's projection: without
// the fields, warnings for different delegates held by the same predicate
// collapse into one anonymous line, and a full-detail client cannot key on
// the structured predicate the event carries.
func TestWarningProjectionCarriesDelegateIdentityAndRefusedPredicate(t *testing.T) {
	got := warningAnnouncement(warningFields{
		source: "delegate", title: "TITLE_753", hint: "HINT_753",
		code: "delegate_attention_drive_refused", message: "MESSAGE_753",
		delegateID:       "dlg_753",
		refusedPredicate: "binding",
	})
	var raw map[string]map[string]string
	if err := json.Unmarshal(got.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	warning := raw["warning"]
	for field, want := range map[string]string{"delegate_id": "dlg_753", "refused_predicate": "binding"} {
		if warning[field] != want {
			t.Fatalf("warning %q = %q, want %q: the overlay dropped a structured field the event carries: %v", field, warning[field], want, warning)
		}
	}
}
