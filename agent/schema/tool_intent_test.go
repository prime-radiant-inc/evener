package schema

import (
	"encoding/json"
	"testing"
)

func TestToolIntent(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"intent", `{"intent":"do it"}`, "do it"},
		{"intent wins over purpose", `{"intent":"a","purpose":"b"}`, "a"},
		{"purpose fallback for pre-rename transcripts", `{"purpose":"inspect"}`, "inspect"},
		{"blank intent falls back to purpose", `{"intent":"  ","purpose":"inspect"}`, "inspect"},
		{"surrounding space trimmed", `{"intent":"  do it \n"}`, "do it"},
		{"non-string intent ignored", `{"intent":123}`, ""},
		{"boolean intent ignored", `{"intent":false}`, ""},
		{"non-string purpose ignored", `{"purpose":123}`, ""},
		{"empty object", `{}`, ""},
		{"invalid json", `not json`, ""},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolIntent(json.RawMessage(tc.raw)); got != tc.want {
				t.Errorf("ToolIntent(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
