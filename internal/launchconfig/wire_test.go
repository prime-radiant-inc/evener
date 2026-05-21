package launchconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestFromWire(t *testing.T) {
	in := appwire.LaunchConfigLayer{
		Model:          "openai/gpt-5",
		FastCheapModel: "openai/gpt-5-mini",
		Schema:         ptrInt(1),
		MCPs:           []appwire.MCPServerSpec{{Name: "x", Command: "y", Args: []string{"z"}}},
		MaxRounds:      ptrInt(50),
	}
	got := FromWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
	}
	if got.FastCheapModel != "openai/gpt-5-mini" {
		t.Errorf("FastCheapModel = %q, want openai/gpt-5-mini", got.FastCheapModel)
	}
	if got.Schema != 1 {
		t.Errorf("Schema = %d, want 1", got.Schema)
	}
	if got.MaxRounds == nil || *got.MaxRounds != 50 {
		t.Errorf("MaxRounds = %v, want 50", got.MaxRounds)
	}
	if len(got.MCPs) != 1 || got.MCPs[0].Name != "x" {
		t.Errorf("MCPs = %v", got.MCPs)
	}
}

func TestToWire(t *testing.T) {
	in := Layer{Model: "openai/gpt-5", FastCheapModel: "openai/gpt-5-mini", MaxRounds: ptrInt(50)}
	got := ToWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
	}
	if got.FastCheapModel != "openai/gpt-5-mini" {
		t.Errorf("FastCheapModel = %q, want openai/gpt-5-mini", got.FastCheapModel)
	}
	if got.MaxRounds == nil || *got.MaxRounds != 50 {
		t.Errorf("MaxRounds = %v", got.MaxRounds)
	}
}

func TestToWirePreservesExplicitEmptyModelFallbacks(t *testing.T) {
	got := ToWire(Layer{ModelFallbacksSet: true, ModelFallbacks: []string{}})
	if got.ModelFallbacks == nil || len(got.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %#v, want explicit empty slice", got.ModelFallbacks)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !jsonContains(raw, `"modelFallbacks":[]`) {
		t.Fatalf("json = %s, want explicit empty modelFallbacks array", raw)
	}
}

func TestWire_SystemPromptAndDebugFieldsRoundTrip(t *testing.T) {
	verbose := true
	in := Layer{
		SystemPromptMode:       "inline",
		SystemPromptText:       "base",
		SystemPromptAppendMode: "file",
		SystemPromptAppendFile: "/append.md",
		Verbose:                &verbose,
		TraceFile:              "/trace",
		CPUProfile:             "/cpu",
		ExportATIFPath:         "/atif",
	}
	got := FromWire(ToWire(in))
	if got.SystemPromptMode != in.SystemPromptMode || got.SystemPromptText != in.SystemPromptText {
		t.Fatalf("system prompt round trip = %#v", got)
	}
	if got.Verbose == nil || *got.Verbose != true || got.TraceFile != "/trace" || got.CPUProfile != "/cpu" || got.ExportATIFPath != "/atif" {
		t.Fatalf("debug round trip = %#v", got)
	}
}

func TestWireOmitsUnsetModelFallbacks(t *testing.T) {
	raw, err := json.Marshal(appwire.LaunchConfigLayer{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if jsonContains(raw, `"modelFallbacks"`) {
		t.Fatalf("json = %s, want modelFallbacks omitted when unset", raw)
	}
}

func TestResolvedToWire(t *testing.T) {
	r := Resolved{
		Effective:  Layer{Model: "m"},
		Layers:     map[LayerName]Layer{LayerGlobal: {Model: "m"}},
		Provenance: map[string]LayerName{"model": LayerGlobal},
		Repo:       &RepoStatus{Path: "/p", Trust: TrustTrusted, Hash: "sha256:abc"},
	}
	got := ResolvedToWire(r)
	if got.Effective.Model != "m" {
		t.Errorf("Effective.Model")
	}
	if got.Layers["global"].Model != "m" {
		t.Errorf("Layers[global]")
	}
	if got.Provenance["model"] != "global" {
		t.Errorf("Provenance[model] = %q", got.Provenance["model"])
	}
	if got.Repo == nil || got.Repo.Trust != "trusted" {
		t.Errorf("Repo = %v", got.Repo)
	}
	_ = reflect.TypeOf(got)
}

func jsonContains(raw []byte, needle string) bool {
	return strings.Contains(string(raw), needle)
}
