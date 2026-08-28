package cmdutil

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

func TestResolveSessionMetaRejectsMismatchedIDWithoutMutation(t *testing.T) {
	const requestedID = "02wMz5Txv1C3Hut0M8GCeB"
	stateDir := t.TempDir()
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	meta := schema.SessionMeta{ID: "02wMz5Txv2enqVTitaig6F", ProfileID: "openai", Model: "gpt-5.2"}
	contents, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path := filepath.Join(sessionsDir, requestedID+".meta.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = ResolveSessionMeta(stateDir, requestedID, false)
	if err == nil {
		t.Fatal("ResolveSessionMeta accepted metadata for another session")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if !bytes.Equal(got, contents) {
		t.Fatalf("failed resolution mutated metadata: got %q want %q", got, contents)
	}
}

func TestMaxRoundsToConfig(t *testing.T) {
	tests := []struct {
		name     string
		cli      int
		wantConf int
	}{
		{"not specified", -1, 0},   // 0 → applyDefaults sets to 200
		{"unlimited", 0, -1},       // -1 → no limit in session loop
		{"explicit limit", 50, 50}, // pass through
		{"explicit limit 1", 1, 1}, // edge case
		{"very negative", -999, 0}, // any negative → not specified
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxRoundsToConfig(tt.cli)
			if got != tt.wantConf {
				t.Fatalf("MaxRoundsToConfig(%d) = %d, want %d", tt.cli, got, tt.wantConf)
			}
		})
	}
}

func TestResolveReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		cli     string
		env     string
		wantSet bool
		wantVal string
		wantErr bool
	}{
		{name: "unset", cli: "", env: "", wantSet: false},
		{name: "env medium", cli: "", env: "medium", wantSet: true, wantVal: "medium"},
		{name: "cli overrides env", cli: "HIGH", env: "low", wantSet: true, wantVal: "high"},
		{name: "cli none means off", cli: "none", env: "high", wantSet: true, wantVal: "none"},
		{name: "env none means off", cli: "", env: "none", wantSet: true, wantVal: "none"},
		{name: "xhigh", cli: "xhigh", env: "", wantSet: true, wantVal: "xhigh"},
		{name: "minimal", cli: "minimal", env: "", wantSet: true, wantVal: "minimal"},
		{name: "max distinct top tier", cli: "max", env: "", wantSet: true, wantVal: "max"},
		{name: "off alias means off", cli: "off", env: "", wantSet: true, wantVal: "none"},
		{name: "invalid", cli: "banana", env: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveReasoningEffort(tt.cli, tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Set != tt.wantSet {
				t.Fatalf("Set=%v want %v", got.Set, tt.wantSet)
			}
			if got.Value != tt.wantVal {
				t.Fatalf("Value=%q want %q", got.Value, tt.wantVal)
			}
		})
	}
}

func TestResolveModelRef_QualifiedModelSuppliesProvider(t *testing.T) {
	got, err := ResolveModelRef("openai/gpt-5.2", "", "", "")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.2" {
		t.Fatalf("got provider=%q model=%q, want openai/gpt-5.2", got.Provider, got.Model)
	}
	if got.Qualified() != "openai/gpt-5.2" {
		t.Fatalf("Qualified()=%q", got.Qualified())
	}
}

func TestResolveModelRef_EnvModelSuppliesProvider(t *testing.T) {
	got, err := ResolveModelRef("", "Anthropic/claude-opus-4-6", "", "")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestResolveModelRef_RejectsBareStartupModel(t *testing.T) {
	_, err := ResolveModelRef("gpt-5.2", "", "", "")
	if err == nil {
		t.Fatal("expected error for bare startup model")
	}
	if !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("error=%q, want provider/model guidance", err.Error())
	}
}

func TestResolveModelRef_ResumeMetaSuppliesBareModelProvider(t *testing.T) {
	got, err := ResolveModelRef("", "", "anthropic", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestResolveResumeModelRef_PersistedMetaBeatsEnv(t *testing.T) {
	got, err := ResolveResumeModelRef("", "openai/gpt-env", "anthropic", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveResumeModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestResolveResumeModelRef_CLIOverridesPersistedMeta(t *testing.T) {
	got, err := ResolveResumeModelRef("openai/gpt-cli", "openai/gpt-env", "anthropic", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveResumeModelRef: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-cli" {
		t.Fatalf("got provider=%q model=%q, want openai/gpt-cli", got.Provider, got.Model)
	}
}

func TestResolveResumeModelRef_UsesEnvWhenMetaMissing(t *testing.T) {
	got, err := ResolveResumeModelRef("", "openai/gpt-env", "", "")
	if err != nil {
		t.Fatalf("ResolveResumeModelRef: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-env" {
		t.Fatalf("got provider=%q model=%q, want openai/gpt-env", got.Provider, got.Model)
	}
}

func TestParseAllowedDecisions_CommaSeparated(t *testing.T) {
	got := parseAllowedDecisions("approved,changes_requested")
	if len(got) != 2 || got[0] != "approved" || got[1] != "changes_requested" {
		t.Fatalf("got %v, want [approved changes_requested]", got)
	}
}

func TestParseAllowedDecisions_JSONArray(t *testing.T) {
	got := parseAllowedDecisions(`["pass","fail"]`)
	if len(got) != 2 || got[0] != "pass" || got[1] != "fail" {
		t.Fatalf("got %v, want [pass fail]", got)
	}
}

func TestParseAllowedDecisions_Empty(t *testing.T) {
	if got := parseAllowedDecisions(""); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestParseAllowedDecisions_Whitespace(t *testing.T) {
	got := parseAllowedDecisions(" approved , changes_requested ")
	if len(got) != 2 || got[0] != "approved" || got[1] != "changes_requested" {
		t.Fatalf("got %v, want [approved changes_requested]", got)
	}
}

func TestStringSliceFlag(t *testing.T) {
	var f StringSliceFlag
	if err := f.Set("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("b"); err != nil {
		t.Fatal(err)
	}
	if f.String() != "a,b" {
		t.Fatalf("String() = %q, want %q", f.String(), "a,b")
	}
	if len(f) != 2 || f[0] != "a" || f[1] != "b" {
		t.Fatalf("values = %v, want [a b]", []string(f))
	}
}
