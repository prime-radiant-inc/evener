package appwire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDiagnosticCauseJSONRoundTrip (kata cmfz) verifies the wire shape of
// DiagnosticCause: camelCase JSON tags (per the appwire camelCase
// carve-out) and omitempty on all optional fields so a nil provider
// payload encodes as an empty object rather than spurious zero fields.
func TestDiagnosticCauseJSONRoundTrip(t *testing.T) {
	in := DiagnosticCause{
		Kind:     "provider",
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		Status:   503,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"kind":"provider"`, `"provider":"anthropic"`, `"model":"claude-opus-4-7"`, `"status":503`} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out DiagnosticCause
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip=%+v, want %+v", out, in)
	}
}

// TestInstanceListResponseJSONRoundTrip verifies the wire shape of
// InstanceListResponse and InstanceEntry: camelCase JSON tags and correct
// field round-trip for a populated entry.
func TestInstanceListResponseJSONRoundTrip(t *testing.T) {
	in := InstanceListResponse{
		Instances: []InstanceEntry{
			{
				Name:           "my-openai",
				Type:           "openai",
				APIStyle:       "openai",
				BaseURL:        "https://api.openai.com/v1",
				IsDefault:      true,
				AuthModes:      []string{"apiKey"},
				ActiveSource:   "file",
				HasStoredFile:  true,
				HasStoredOAuth: false,
				EnvVar:         "OPENAI_API_KEY",
				StoredEmail:    "",
			},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"instances"`,
		`"name":"my-openai"`,
		`"type":"openai"`,
		`"apiStyle":"openai"`,
		`"baseUrl":"https://api.openai.com/v1"`,
		`"isDefault":true`,
		`"authModes":["apiKey"]`,
		`"activeSource":"file"`,
		`"hasStoredFile":true`,
		`"hasStoredOAuth":false`,
		`"envVar":"OPENAI_API_KEY"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out InstanceListResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Instances) != 1 {
		t.Fatalf("roundtrip instances len=%d, want 1", len(out.Instances))
	}
	e := out.Instances[0]
	if e.Name != "my-openai" || e.Type != "openai" || e.APIStyle != "openai" ||
		e.BaseURL != "https://api.openai.com/v1" || !e.IsDefault ||
		len(e.AuthModes) != 1 || e.AuthModes[0] != "apiKey" ||
		e.ActiveSource != "file" || !e.HasStoredFile || e.HasStoredOAuth ||
		e.EnvVar != "OPENAI_API_KEY" {
		t.Fatalf("roundtrip entry=%+v", e)
	}
}

// TestInstanceCreateParamsJSONRoundTrip verifies the wire shape of
// InstanceCreateParams: camelCase JSON tags and field preservation.
func TestInstanceCreateParamsJSONRoundTrip(t *testing.T) {
	in := InstanceCreateParams{
		Type:     "openai",
		Name:     "my-openai",
		APIStyle: "openai",
		BaseURL:  "https://api.openai.com/v1",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"type":"openai"`,
		`"name":"my-openai"`,
		`"apiStyle":"openai"`,
		`"baseUrl":"https://api.openai.com/v1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out InstanceCreateParams
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip=%+v, want %+v", out, in)
	}
}

// TestDiagnosticCauseOmitEmpty (kata cmfz) verifies that the optional
// fields drop out of the JSON encoding when zero, so kind-only causes
// stay compact on the wire.
func TestDiagnosticCauseOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(DiagnosticCause{Kind: "provider"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, banned := range []string{`"provider":`, `"model":`, `"status":`} {
		if strings.Contains(got, banned) {
			t.Fatalf("marshal=%s should have omitted %s", got, banned)
		}
	}
	if !strings.Contains(got, `"kind":"provider"`) {
		t.Fatalf("marshal=%s missing kind", got)
	}
}
