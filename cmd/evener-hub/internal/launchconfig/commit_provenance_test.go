package launchconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCommitProvenanceSchema(t *testing.T) {
	var found *LaunchOption
	for _, opt := range LaunchOptionSchema() {
		if opt.Field == "commit_provenance" {
			o := opt
			found = &o
			break
		}
	}
	if found == nil {
		t.Fatal("commit_provenance is not in the launch option schema")
	}
	if found.Group != LaunchGroupAgent || found.Kind != LaunchControlSelect {
		t.Fatalf("field shape = %s/%s, want agent/select", found.Group, found.Kind)
	}
	if !found.PerLaunch {
		t.Fatal("commit_provenance must be offered per launch")
	}
	if !slices.Equal(found.DefaultableLayers, []LaunchLayerSupport{LaunchLayerGlobal}) {
		t.Fatalf("defaultable layers = %v, want [global]", found.DefaultableLayers)
	}
	var values []string
	for _, choice := range found.Choices {
		values = append(values, choice.Value)
	}
	if !slices.Equal(values, []string{"", "coauthor", "off"}) {
		t.Fatalf("choices = %v, want [ coauthor off ]", values)
	}
	if !AllowsLayer("commit_provenance", LayerGlobal) {
		t.Fatal("global layer should allow commit_provenance")
	}
	if !AllowsLayer("commit_provenance", LayerLaunch) {
		t.Fatal("launch layer should allow commit_provenance")
	}
	if AllowsLayer("commit_provenance", LayerRepo) || AllowsLayer("commit_provenance", LayerProject) {
		t.Fatal("repo/project layers must not allow commit_provenance")
	}
}

func TestMergeCommitProvenance(t *testing.T) {
	got, diags := mergeLayers(map[LayerName]Layer{
		LayerGlobal: {CommitProvenance: "coauthor"},
		LayerLaunch: {CommitProvenance: "off"},
	})
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got.Effective.CommitProvenance != "off" {
		t.Fatalf("effective = %q, want off", got.Effective.CommitProvenance)
	}
	if got.Provenance["commit_provenance"] != LayerLaunch {
		t.Fatalf("provenance = %q, want launch", got.Provenance["commit_provenance"])
	}

	got, diags = mergeLayers(map[LayerName]Layer{
		LayerRepo: {CommitProvenance: "off"},
	})
	if got.Effective.CommitProvenance != "" {
		t.Fatalf("repo value leaked into effective: %q", got.Effective.CommitProvenance)
	}
	if len(diags) != 1 || diags[0].Field != "commit_provenance" {
		t.Fatalf("repo value diagnostics = %v, want one commit_provenance diagnostic", diags)
	}

	got, diags = mergeLayers(map[LayerName]Layer{
		LayerGlobal: {CommitProvenance: "bogus"},
	})
	if got.Effective.CommitProvenance != "" {
		t.Fatalf("invalid value leaked into effective: %q", got.Effective.CommitProvenance)
	}
	if len(diags) != 1 || diags[0].Field != "commit_provenance" {
		t.Fatalf("invalid value diagnostics = %v, want one commit_provenance diagnostic", diags)
	}
}

func TestCommitProvenanceToArgs(t *testing.T) {
	args := ToArgs(Resolved{Effective: Layer{CommitProvenance: "off"}})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--commit-provenance off") {
		t.Fatalf("args = %q, want --commit-provenance off", joined)
	}
}

func TestCommitProvenanceWireRoundTrip(t *testing.T) {
	wire := ToWire(Layer{CommitProvenance: "off"})
	if wire.CommitProvenance != "off" {
		t.Fatalf("wire value = %q, want off", wire.CommitProvenance)
	}
	if got := FromWire(wire).CommitProvenance; got != "off" {
		t.Fatalf("round trip = %q, want off", got)
	}
}

func TestCommitProvenanceSavesToLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.toml")
	if err := SaveLayer(path, Layer{CommitProvenance: "off"}); err != nil {
		t.Fatalf("SaveLayer: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `commit_provenance = "off"`) {
		t.Fatalf("saved layer does not carry commit_provenance: %s", raw)
	}
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	if got.CommitProvenance != "off" {
		t.Fatalf("loaded = %q, want off", got.CommitProvenance)
	}
}
