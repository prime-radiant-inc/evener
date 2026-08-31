package registry

import (
	"reflect"
	"testing"
)

func TestOptionalCapReadersAndEffortCapable(t *testing.T) {
	if BoolValue(nil) || !BoolValue(new(true)) || BoolValue(new(false)) {
		t.Fatal("BoolValue")
	}
	if StringValue(nil) != "" || StringValue(new("x")) != "x" {
		t.Fatal("StringValue")
	}
	if !(Caps{ReasoningControls: []string{"effort"}}).EffortCapable() || (Caps{ReasoningControls: []string{"toggle"}}).EffortCapable() {
		t.Fatal("effort ∈ ReasoningControls decides")
	}
	if !(Caps{}).EffortCapable() || (Caps{Reasoning: new(true)}).EffortCapable() || (Caps{Reasoning: new(false)}).EffortCapable() {
		t.Fatal("only a row with no verdict and no controls passes an effort through")
	}
}

func TestApplyBodyConstantsCreatesParentsAndOverrides(t *testing.T) {
	body := map[string]any{"text": map[string]any{"format": map[string]any{"type": "text"}}, "parallel_tool_calls": true}
	ApplyBodyConstants(body, map[string]any{
		"reasoning.context":   "all_turns",
		"text.verbosity":      "low",
		"parallel_tool_calls": false,
		"anthropic_version":   "vertex-2023-10-16",
	})
	want := map[string]any{
		"text":                map[string]any{"format": map[string]any{"type": "text"}, "verbosity": "low"},
		"reasoning":           map[string]any{"context": "all_turns"},
		"parallel_tool_calls": false,
		"anthropic_version":   "vertex-2023-10-16",
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %#v\nwant %#v", body, want)
	}
}

func TestApplyBodyConstantsSurvivesPrune(t *testing.T) {
	// Spec §8.2: constants run after the prune, so a constant under a pruned
	// parent still lands (the Codex rows prune nothing under reasoning, but
	// the ordering contract must hold for any row).
	caps := Caps{Fields: map[string]bool{"metadata": false}}
	body := map[string]any{"metadata": map[string]any{"a": "b"}}
	Prune(body, caps)
	ApplyBodyConstants(body, map[string]any{"metadata.trace": "on"})
	if got := body["metadata"]; !reflect.DeepEqual(got, map[string]any{"trace": "on"}) {
		t.Fatalf("metadata = %#v", got)
	}
	ApplyBodyConstants(body, nil)
}
