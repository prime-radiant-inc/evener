package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

func TestModelPickerItems_SetsGroupAndPrettifiedDisplay(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, false)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Group != "anthropic" {
		t.Errorf("Group = %q, want %q", items[0].Group, "anthropic")
	}
	if items[0].Display != "Claude Opus 4 6" {
		t.Errorf("Display = %q, want %q", items[0].Display, "Claude Opus 4 6")
	}
	if items[0].ID != "anthropic/claude-opus-4-6" {
		t.Errorf("ID = %q, want the qualified ref unchanged", items[0].ID)
	}
}

func TestModelInfoMetaTail_FromDescriptor(t *testing.T) {
	got := modelInfoMetaTail(appwire.ModelDescriptor{
		Provider:             "anthropic",
		Model:                "claude-opus-4-6",
		ContextWindow:        new(200_000),
		SupportsTools:        new(true),
		SupportsVision:       new(true),
		SupportsReasoning:    new(true),
		InputCostPerMillion:  new(3.0),
		OutputCostPerMillion: new(15.0),
	})
	if want := "200K ctx · $3.00/$15.00 · tools,vision,reasoning"; got != want {
		t.Errorf("modelInfoMetaTail = %q, want %q", got, want)
	}
}

func TestModelPickerItems_MetaTailFromDescriptor(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{
			Provider:             "anthropic",
			Model:                "claude-opus-4-6",
			ContextWindow:        new(1_000_000),
			SupportsTools:        new(true),
			SupportsVision:       new(true),
			SupportsReasoning:    new(true),
			InputCostPerMillion:  new(5.0),
			OutputCostPerMillion: new(25.0),
		},
	}, false)
	if !strings.Contains(items[0].Meta, "1M ctx") {
		t.Errorf("Meta = %q, want it to contain %q", items[0].Meta, "1M ctx")
	}
	if !strings.Contains(items[0].Meta, "$5.00/$25.00") {
		t.Errorf("Meta = %q, want it to contain price", items[0].Meta)
	}
	if !strings.Contains(items[0].Meta, "tools") || !strings.Contains(items[0].Meta, "vision") || !strings.Contains(items[0].Meta, "reasoning") {
		t.Errorf("Meta = %q, want tools/vision/reasoning capability flags", items[0].Meta)
	}
}

func TestModelPickerItems_DescriptorWithoutMetadataStillRendersEmptyMeta(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{Provider: "mycompany", Model: "totally-unknown-model-xyz"},
	}, false)
	if len(items) != 1 {
		t.Fatalf("bare descriptor was dropped: got %d items, want 1", len(items))
	}
	if items[0].Meta != "" {
		t.Errorf("Meta = %q, want empty for a descriptor carrying no metadata", items[0].Meta)
	}
	if items[0].Display != "Totally Unknown Model Xyz" {
		t.Errorf("Display = %q, want the prettified id even without metadata", items[0].Display)
	}
}

// TestModelInfoMetaTail_PricelessRowRendersNoCost pins the flag-day rule
// (spec §14.1): a registry row with no cost yields no cost string anywhere,
// including the picker, rather than a fabricated "$0.00/$0.00".
func TestModelInfoMetaTail_PricelessRowRendersNoCost(t *testing.T) {
	got := modelInfoMetaTail(appwire.ModelDescriptor{
		Provider:      "mycompany",
		Model:         "priceless",
		ContextWindow: new(128_000),
		SupportsTools: new(true),
	})
	if want := "128K ctx · tools"; got != want {
		t.Errorf("modelInfoMetaTail = %q, want %q", got, want)
	}
}

func TestModelPickerItems_SortsDatedSnapshotLastWithinProvider(t *testing.T) {
	items := modelPickerItems([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6-20251101"},
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, false)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != "anthropic/claude-opus-4-6" || items[1].ID != "anthropic/claude-opus-4-6-20251101" {
		t.Fatalf("order = [%s, %s], want bare family id before its dated snapshot", items[0].ID, items[1].ID)
	}
}

// TestModelPickerItemProvider_ReadsGroupNotDisplay pins a regression this
// task would otherwise introduce: modelPickerItemProvider used to parse the
// provider out of item.Display (which used to be "provider/model"). Now that
// Display is the prettified bare name (no "/"), the diagnostics
// disabled-reason overlay in modelPickerItemsFromResponse would silently stop
// matching any provider. Group is now the authoritative source.
func TestModelPickerItemProvider_ReadsGroupNotDisplay(t *testing.T) {
	item := tuipick.ModelPickerItem{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Group: "anthropic"}
	if got := modelPickerItemProvider(item); got != "anthropic" {
		t.Fatalf("modelPickerItemProvider = %q, want %q (from Group, not the prettified Display)", got, "anthropic")
	}
}

func TestModelPickerItemsFromResponse_PrependsRecentGroup(t *testing.T) {
	resp := appwire.ModelListResponse{
		Data: []appwire.ModelDescriptor{
			{Provider: "anthropic", Model: "claude-opus-4-6"},
			{Provider: "openai", Model: "gpt-5.2"},
		},
		Recent: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}},
	}
	items := modelPickerItemsFromResponse(resp, false)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (1 recent + 2 catalog, recent duplicated per design)", len(items))
	}
	if items[0].Group != "Recent" || items[0].ID != "openai/gpt-5.2" {
		t.Fatalf("items[0] = %+v, want the Recent-grouped gpt-5.2 first", items[0])
	}
	// The catalog copy of gpt-5.2 (under its provider group) still exists —
	// Recent is a shortcut, not a removal from the browsable catalog.
	var providerCopyFound bool
	for _, it := range items[1:] {
		if it.ID == "openai/gpt-5.2" && it.Group == "openai" {
			providerCopyFound = true
		}
	}
	if !providerCopyFound {
		t.Fatal("gpt-5.2 should still appear under its provider group, not just under Recent")
	}
}

func TestModelPickerItemsFromResponse_RecentPreservesServerRecencyOrderAcrossProviders(t *testing.T) {
	resp := appwire.ModelListResponse{
		Data: []appwire.ModelDescriptor{
			{Provider: "anthropic", Model: "claude-opus-4-6"},
			{Provider: "openai", Model: "gpt-5.5"},
		},
		Recent: []appwire.ModelDescriptor{
			{Provider: "openai", Model: "gpt-5.5"},
			{Provider: "anthropic", Model: "claude-opus-4-6"},
		},
	}
	items := modelPickerItemsFromResponse(resp, false)
	if len(items) < 2 || items[0].ID != "openai/gpt-5.5" || items[1].ID != "anthropic/claude-opus-4-6" {
		t.Fatalf("Recent group order = %+v, want server recency order (openai first) preserved, not provider-alphabetical", items[:2])
	}
}

func TestModelPickerItemsFromResponse_NoRecentOmitsGroup(t *testing.T) {
	resp := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}}}
	items := modelPickerItemsFromResponse(resp, false)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (fresh install, no Recent)", len(items))
	}
	if items[0].Group == "Recent" {
		t.Fatal("no Recent group should render when resp.Recent is empty")
	}
}

func TestVisionModelPickerItemsPrependPseudoEntriesAndFilterDescriptors(t *testing.T) {
	models := []appwire.ModelDescriptor{
		{Provider: "openai", Model: "gpt-3.5-turbo", SupportsVision: new(false)},
		{Provider: "openai", Model: "gpt-4o", SupportsVision: new(true)},
		// No SupportsVision at all: unknown is not vision-capable.
		{Provider: "openai", Model: "gpt-unknown"},
	}
	items := visionModelPickerItems(models, []tuipick.ModelPickerItem{
		{ID: "openai/gpt-3.5-turbo", Display: "GPT 3.5 Turbo"},
		{ID: "openai/gpt-4o", Display: "GPT 4o"},
		{ID: "openai/gpt-unknown", Display: "GPT Unknown"},
	})
	if len(items) != 3 {
		t.Fatalf("got %d items, want Current, Off, and the one vision-capable descriptor: %#v", len(items), items)
	}
	if items[0].ID != "" || items[0].Display != "Current model" {
		t.Fatalf("first item = %#v, want Current model with empty ID", items[0])
	}
	if items[1].ID != "off" || items[1].Display != "Off" {
		t.Fatalf("second item = %#v, want Off", items[1])
	}
	if items[2].ID != "openai/gpt-4o" {
		t.Fatalf("filtered catalog item = %#v, want vision-capable gpt-4o only", items[2])
	}
}
