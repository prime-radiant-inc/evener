package launchconfig

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestFromWire(t *testing.T) {
	in := appwire.LaunchConfigLayer{
		Model:     "openai/gpt-5",
		Schema:    ptrInt(1),
		MCPs:      []appwire.MCPServerSpec{{Name: "x", Command: "y", Args: []string{"z"}}},
		MaxRounds: ptrInt(50),
	}
	got := FromWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
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
	in := Layer{Model: "openai/gpt-5", MaxRounds: ptrInt(50)}
	got := ToWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
	}
	if got.MaxRounds == nil || *got.MaxRounds != 50 {
		t.Errorf("MaxRounds = %v", got.MaxRounds)
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
