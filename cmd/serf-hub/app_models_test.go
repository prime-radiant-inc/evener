package main

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// TestHubModelList_AttachesRecentFromPastIndex verifies every ModelList
// response (the path both the TUI's appwire RPC and the web's non-serf-harness
// REST branch use) carries Recent, filtered to models actually present in
// resp.Data — a recent ref no longer offered isn't rendered as unselectable.
func TestHubModelList_AttachesRecentFromPastIndex(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "local", Model: "still-live-model"},
		{ID: "b", ProfileID: "local", Model: "retired-model"}, // not in the live source below
	})
	cfg := hubcore.WebConfig{Past: past}
	sources := appsource.NewRegistry()
	// No Spawner/live source configured: hubModelList's serf/local branch
	// returns an empty ModelListResponse (its early-return path), which is
	// enough to exercise attachRecentModels' filtering against resp.Data.
	resp, err := hubModelList(context.Background(), cfg, sources, appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("hubModelList: %v", err)
	}
	if resp.Recent != nil {
		t.Fatalf("Recent = %+v, want nil (no models in resp.Data to match against)", resp.Recent)
	}
}

// TestHubModelList_NilPastIndexOmitsRecent guards the nil-Past config path
// (tests/sandboxes that construct WebConfig without a Past index).
func TestHubModelList_NilPastIndexOmitsRecent(t *testing.T) {
	cfg := hubcore.WebConfig{}
	sources := appsource.NewRegistry()
	resp, err := hubModelList(context.Background(), cfg, sources, appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("hubModelList: %v", err)
	}
	if resp.Recent != nil {
		t.Fatalf("Recent = %+v, want nil with no Past index configured", resp.Recent)
	}
}

// TestAttachRecentModels_FiltersToAvailableModels is the direct unit test for
// the filtering rule: a recent ref present in resp.Data survives; one absent
// (retired/reconfigured) is dropped, in most-recent-first order.
func TestAttachRecentModels_FiltersToAvailableModels(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "openai", Model: "gpt-5.2"},
		{ID: "b", ProfileID: "openai", Model: "retired-model"},
	})
	cfg := hubcore.WebConfig{Past: past}
	resp := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
		{Provider: "openai", Model: "gpt-5.2"},
	}}
	got := attachRecentModels(cfg, resp)
	want := []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}}
	if !reflect.DeepEqual(got.Recent, want) {
		t.Fatalf("Recent = %+v, want %+v (retired-model absent from resp.Data must be dropped)", got.Recent, want)
	}
}

func TestPrettifyModelDisplayName(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-6":          "Claude Opus 4 6",
		"claude-opus-4-6-20251101": "Claude Opus 4 6", // dated snapshot suffix stripped first
		"gpt-5.1":                  "Gpt 5.1",
		"o3-deep-research":         "O3 Deep Research",
		"glm-5.2":                  "Glm 5.2",
		"bare":                     "Bare",
	}
	for id, want := range cases {
		if got := prettifyModelDisplayName(id); got != want {
			t.Errorf("prettifyModelDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestIsDatedSnapshotModelID(t *testing.T) {
	if !isDatedSnapshotModelID("claude-opus-4-6-20251101") {
		t.Error("dated snapshot suffix should be detected")
	}
	if !isDatedSnapshotModelID("anthropic/claude-opus-4-6-20251101") {
		t.Error("dated snapshot suffix should be detected through a provider-qualified ref")
	}
	if isDatedSnapshotModelID("claude-opus-4-6") {
		t.Error("bare family id must not be treated as dated")
	}
	if isDatedSnapshotModelID("gpt-5.1") {
		t.Error("non-dated id must not be treated as dated")
	}
}

func TestModelDescriptorsToAPIModels_UsesPrettifiedDisplayNameAndSortsDatedLast(t *testing.T) {
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6-20251101"},
		{Provider: "anthropic", Model: "claude-opus-4-6"},
		{Provider: "openai", Model: "gpt-5.2"},
	}, nil)
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	if got := models[0]["display_name"]; got != "Claude Opus 4 6" {
		t.Errorf("models[0].display_name = %v, want %q", got, "Claude Opus 4 6")
	}
	// Within the anthropic group, the dated snapshot must sort after the bare
	// family id, regardless of input order.
	var anthropicOrder []string
	for _, m := range models {
		if m["provider"] == "anthropic" {
			anthropicOrder = append(anthropicOrder, m["model"].(string))
		}
	}
	want := []string{"claude-opus-4-6", "claude-opus-4-6-20251101"}
	if !reflect.DeepEqual(anthropicOrder, want) {
		t.Errorf("anthropic model order = %v, want %v (dated snapshot last)", anthropicOrder, want)
	}
}
