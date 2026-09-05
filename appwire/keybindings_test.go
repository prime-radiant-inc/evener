package appwire

import (
	"encoding/json"
	"testing"
)

func TestKeybindingsShippedDefaults(t *testing.T) {
	got := KeybindingsShippedDefaults()
	if got.Version != 1 {
		t.Errorf("defaults version = %d, want 1", got.Version)
	}
	if got.Revision != 0 {
		t.Errorf("defaults revision = %d, want 0", got.Revision)
	}
	if got.Rules == nil || len(got.Rules) != 0 {
		t.Errorf("defaults rules = %#v, want empty non-nil slice", got.Rules)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"version":1,"revision":0,"rules":[]}`; string(encoded) != want {
		t.Fatalf("defaults JSON = %s, want %s", encoded, want)
	}
}

func TestDecodeKeybindingsPatchAcceptsRebindAndUnbind(t *testing.T) {
	raw := json.RawMessage(`{"expectedRevision":3,"config":{"version":1,"rules":[{"action":"thread.new","chord":"ctrl+n"},{"action":"thread.close","chord":null}]}}`)
	got, err := DecodeKeybindingsPatchParams(raw)
	if err != nil {
		t.Fatalf("valid patch rejected: %v", err)
	}
	if got.ExpectedRevision != 3 || got.Config.Version != 1 || len(got.Config.Rules) != 2 {
		t.Fatalf("decoded = %#v", got)
	}
	rebind := got.Config.Rules[0]
	if rebind.Action != "thread.new" || rebind.Chord == nil || *rebind.Chord != "ctrl+n" {
		t.Fatalf("rebind rule = %#v", rebind)
	}
	unbind := got.Config.Rules[1]
	if unbind.Action != "thread.close" || unbind.Chord != nil {
		t.Fatalf("unbind rule = %#v", unbind)
	}
}

func TestDecodeKeybindingsPatchAcceptsEmptyRules(t *testing.T) {
	raw := json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[]}}`)
	got, err := DecodeKeybindingsPatchParams(raw)
	if err != nil {
		t.Fatalf("empty rules rejected: %v", err)
	}
	if got.Config.Rules == nil || len(got.Config.Rules) != 0 {
		t.Fatalf("decoded rules = %#v, want empty non-nil slice", got.Config.Rules)
	}
}

func TestDecodeKeybindingsPatchRejectsUnknownFields(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"top level": json.RawMessage(`{"expectedRevision":0,"unexpected":true,"config":{"version":1,"rules":[]}}`),
		"config":    json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[],"unexpected":true}}`),
		"rule":      json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"action":"thread.new","chord":"ctrl+n","unexpected":true}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeKeybindingsPatchParams(raw); err == nil {
				t.Fatal("unknown field accepted")
			}
		})
	}
}

func TestDecodeKeybindingsPatchRejectsMissingFields(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"expectedRevision": json.RawMessage(`{"config":{"version":1,"rules":[]}}`),
		"config":           json.RawMessage(`{"expectedRevision":0}`),
		"version":          json.RawMessage(`{"expectedRevision":0,"config":{"rules":[]}}`),
		"rules":            json.RawMessage(`{"expectedRevision":0,"config":{"version":1}}`),
		"rule action":      json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"chord":"ctrl+n"}]}}`),
		"rule chord":       json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"action":"thread.new"}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeKeybindingsPatchParams(raw); err == nil {
				t.Fatal("missing field accepted")
			}
		})
	}
}

func TestDecodeKeybindingsPatchRejectsNullScalars(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"expectedRevision": json.RawMessage(`{"expectedRevision":null,"config":{"version":1,"rules":[]}}`),
		"version":          json.RawMessage(`{"expectedRevision":0,"config":{"version":null,"rules":[]}}`),
		"rules":            json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":null}}`),
		"rule action":      json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"action":null,"chord":"ctrl+n"}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeKeybindingsPatchParams(raw); err == nil {
				t.Fatal("null scalar accepted")
			}
		})
	}
}

func TestDecodeKeybindingsPatchRejectsTrailingJSON(t *testing.T) {
	raw := json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[]}} {}`)
	if _, err := DecodeKeybindingsPatchParams(raw); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestDecodeKeybindingsPatchRejectsInvalidValues(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"version":           json.RawMessage(`{"expectedRevision":0,"config":{"version":2,"rules":[]}}`),
		"empty action":      json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"action":"","chord":"ctrl+n"}]}}`),
		"whitespace action": json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"action":"  ","chord":"ctrl+n"}]}}`),
		"empty chord":       json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"action":"thread.new","chord":""}]}}`),
		"whitespace chord":  json.RawMessage(`{"expectedRevision":0,"config":{"version":1,"rules":[{"action":"thread.new","chord":" \t "}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeKeybindingsPatchParams(raw); err == nil {
				t.Fatal("invalid value accepted")
			}
		})
	}
}

func TestValidateKeybindingsConfigRejectsInvalidShapes(t *testing.T) {
	chord := "ctrl+n"
	empty := ""
	tests := []struct {
		name   string
		config KeybindingsConfig
	}{
		{name: "version", config: KeybindingsConfig{Version: 2, Rules: []KeybindingsRule{}}},
		{name: "empty action", config: KeybindingsConfig{Version: 1, Rules: []KeybindingsRule{{Action: "", Chord: &chord}}}},
		{name: "whitespace action", config: KeybindingsConfig{Version: 1, Rules: []KeybindingsRule{{Action: " ", Chord: &chord}}}},
		{name: "empty chord", config: KeybindingsConfig{Version: 1, Rules: []KeybindingsRule{{Action: "thread.new", Chord: &empty}}}},
		{name: "whitespace chord", config: KeybindingsConfig{Version: 1, Rules: []KeybindingsRule{{Action: "thread.new", Chord: &[]string{"\n"}[0]}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateKeybindingsConfig(tc.config); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if err := ValidateKeybindingsConfig(KeybindingsConfig{Version: 1, Rules: []KeybindingsRule{{Action: "thread.new", Chord: &chord}, {Action: "thread.close"}}}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
