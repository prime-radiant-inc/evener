package appwire

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTranscriptDisplayShippedDefaults(t *testing.T) {
	got := TranscriptDisplayShippedDefaults()
	if got.Desktop.Config.Content.Level != TranscriptLevelTools || got.Mobile.Config.Content.Level != TranscriptLevelIntent {
		t.Fatalf("defaults = %#v", got)
	}
	if got.Desktop.Revision != 0 || got.Mobile.Revision != 0 {
		t.Fatalf("new revisions must be zero: %#v", got)
	}
	for name, config := range map[string]TranscriptDisplayConfig{
		"desktop": got.Desktop.Config,
		"mobile":  got.Mobile.Config,
	} {
		if config.Version != 1 {
			t.Errorf("%s version = %d, want 1", name, config.Version)
		}
		if config.Content.Kind != TranscriptContentKindPreset {
			t.Errorf("%s content kind = %q, want preset", name, config.Content.Kind)
		}
		if config.Content.Custom != nil {
			t.Errorf("%s custom = %#v, want nil", name, config.Content.Custom)
		}
		if config.Advanced.RoundTimings || config.Advanced.TokenCounts || config.Advanced.EstimatedCost ||
			config.Advanced.SystemEvents || config.Advanced.PromptEvents || config.Advanced.HookExits != TranscriptHookExitDetailNone {
			t.Errorf("%s advanced fields are not all off: %#v", name, config.Advanced)
		}
	}
}

func TestDecodeTranscriptDisplayPatchAcceptsPreset(t *testing.T) {
	raw := json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`)
	got, err := DecodeTranscriptDisplayDefaultsPatchParams(raw)
	if err != nil {
		t.Fatalf("valid preset rejected: %v", err)
	}
	if got.Layout != TranscriptViewportClassDesktop || got.Config.Content.Level != TranscriptLevelTools {
		t.Fatalf("decoded = %#v", got)
	}
}

func TestDecodeTranscriptDisplayPatchAcceptsCompleteCustom(t *testing.T) {
	raw := json.RawMessage(`{"layout":"mobile","expectedRevision":3,"config":{"version":1,"content":{"kind":"custom","custom":{"toolIntent":true,"toolCalls":false,"reasoning":true,"expandByDefault":false}},"advanced":{"roundTimings":true,"tokenCounts":false,"estimatedCost":true,"systemEvents":false,"promptEvents":true,"hookExits":"successful"}}}`)
	got, err := DecodeTranscriptDisplayDefaultsPatchParams(raw)
	if err != nil {
		t.Fatalf("valid custom rejected: %v", err)
	}
	if got.Layout != TranscriptViewportClassMobile || got.ExpectedRevision != 3 || got.Config.Content.Custom == nil ||
		!got.Config.Content.Custom.ToolIntent || got.Config.Content.Custom.ToolCalls || !got.Config.Content.Custom.Reasoning || got.Config.Content.Custom.ExpandByDefault {
		t.Fatalf("decoded = %#v", got)
	}
}

func TestTranscriptDisplayCustomBooleansAreAlwaysOnWire(t *testing.T) {
	encoded, err := json.Marshal(TranscriptDisplayCustomContent{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"toolIntent", "toolCalls", "reasoning", "expandByDefault"} {
		if !bytes.Contains(encoded, []byte(`"`+field+`":false`)) {
			t.Errorf("custom JSON missing false %s: %s", field, encoded)
		}
	}
}

func TestDecodeTranscriptDisplayPatchRejectsIncompleteCustom(t *testing.T) {
	raw := json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"custom","custom":{"toolIntent":true}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`)
	if _, err := DecodeTranscriptDisplayDefaultsPatchParams(raw); err == nil {
		t.Fatal("expected incomplete Custom vector to fail")
	}
}

func TestDecodeTranscriptDisplayPatchRejectsAmbiguousContent(t *testing.T) {
	tests := []string{
		`{"kind":"preset","level":"tools","custom":{"toolIntent":true,"toolCalls":true,"reasoning":true,"expandByDefault":true}}`,
		`{"kind":"custom","level":"tools","custom":{"toolIntent":true,"toolCalls":true,"reasoning":true,"expandByDefault":true}}`,
	}
	for _, content := range tests {
		t.Run(content, func(t *testing.T) {
			raw := json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":` + content + `,"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`)
			if _, err := DecodeTranscriptDisplayDefaultsPatchParams(raw); err == nil {
				t.Fatal("ambiguous content accepted")
			}
		})
	}
}

func TestDecodeTranscriptDisplayPatchRejectsUnknownFields(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"top level": json.RawMessage(`{"layout":"desktop","expectedRevision":0,"unexpected":true,"config":{"version":1,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`),
		"custom":    json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"custom","custom":{"toolIntent":true,"toolCalls":false,"reasoning":false,"expandByDefault":false,"unexpected":true}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTranscriptDisplayDefaultsPatchParams(raw); err == nil {
				t.Fatal("unknown field accepted")
			}
		})
	}
}

func TestDecodeTranscriptDisplayPatchRejectsNullScalarValues(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"revision": json.RawMessage(`{"layout":"desktop","expectedRevision":null,"config":{"version":1,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`),
		"advanced": json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":null,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`),
		"custom":   json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"custom","custom":{"toolIntent":null,"toolCalls":false,"reasoning":false,"expandByDefault":false}},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTranscriptDisplayDefaultsPatchParams(raw); err == nil {
				t.Fatal("null scalar accepted")
			}
		})
	}
}

func TestDecodeTranscriptDisplayPatchRejectsTrailingJSON(t *testing.T) {
	raw := json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}} {}`)
	if _, err := DecodeTranscriptDisplayDefaultsPatchParams(raw); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestDecodeTranscriptDisplayPatchRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "layout", field: "layout", value: "tablet"},
		{name: "version", field: "version", value: "2"},
		{name: "level", field: "level", value: "everything"},
		{name: "hook detail", field: "hookExits", value: "normal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"layout":"desktop","expectedRevision":0,"config":{"version":1,"content":{"kind":"preset","level":"tools"},"advanced":{"roundTimings":false,"tokenCounts":false,"estimatedCost":false,"systemEvents":false,"promptEvents":false,"hookExits":"none"}}}`)
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			switch tc.field {
			case "layout":
				fields[tc.field] = tc.value
			case "version":
				config := fields["config"].(map[string]any)
				config[tc.field] = json.Number(tc.value)
			case "level":
				config := fields["config"].(map[string]any)
				content := config["content"].(map[string]any)
				content[tc.field] = tc.value
			case "hookExits":
				config := fields["config"].(map[string]any)
				advanced := config["advanced"].(map[string]any)
				advanced[tc.field] = tc.value
			}
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeTranscriptDisplayDefaultsPatchParams(encoded); err == nil {
				t.Fatalf("invalid %s accepted", tc.field)
			}
		})
	}
}

func TestValidateTranscriptDisplayConfigRejectsInvalidShapes(t *testing.T) {
	valid := TranscriptDisplayConfig{
		Version:  1,
		Content:  TranscriptDisplayContent{Kind: TranscriptContentKindPreset, Level: TranscriptLevelTools},
		Advanced: TranscriptDisplayAdvanced{HookExits: TranscriptHookExitDetailNone},
	}
	tests := []struct {
		name   string
		config TranscriptDisplayConfig
	}{
		{name: "version", config: func() TranscriptDisplayConfig { c := valid; c.Version = 2; return c }()},
		{name: "kind", config: func() TranscriptDisplayConfig { c := valid; c.Content.Kind = "other"; return c }()},
		{name: "level", config: func() TranscriptDisplayConfig { c := valid; c.Content.Level = "everything"; return c }()},
		{name: "preset custom", config: func() TranscriptDisplayConfig {
			c := valid
			c.Content.Custom = &TranscriptDisplayCustomContent{}
			return c
		}()},
		{name: "custom nil", config: func() TranscriptDisplayConfig {
			c := valid
			c.Content.Kind = TranscriptContentKindCustom
			c.Content.Level = ""
			return c
		}()},
		{name: "hook detail", config: func() TranscriptDisplayConfig { c := valid; c.Advanced.HookExits = "normal"; return c }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTranscriptDisplayConfig(tc.config); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
