package hub

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm/registry"
)

func TestInstances_ModelGetProvenanceAndAliasLockstep(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant",
		"OPENAI_API_KEY":    "sk-oai",
	})
	cfg := `
[models."claude-*"]
max_output_tokens = 32000

[providers.work]
base = "anthropic"
api_key = "sk-work"
compaction_threshold = 180000

[providers.work.models."claude-opus-4-6"]
compaction_threshold = 250000
wire_id = "claude-opus-4-6-custom"
disabled = true

[providers.work.models."same-alias"]
alias_of = "claude-sonnet-4-6"

[providers.work.models."cross-alias"]
alias_of = "openai/gpt-5.4"

[providers.work.models."clamped-row"]
context_window = 128000
compaction_threshold = 200000

[providers.work.models."custom-only-row"]
context_window = 500000
`
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	f.ctl.reg.Get().ApplyLive("work", []registry.Model{
		{ID: "claude-sonnet-4-6", Caps: registry.Caps{ContextWindow: new(400_000)}},
		{ID: "claude-opus-4-6"},
		{ID: "clamped-row"},
	})

	opus, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "work", Model: "claude-opus-4-6"})
	if err != nil {
		t.Fatalf("GetModel(claude-opus-4-6): %v", err)
	}
	if opus.Name != "work" || opus.Model != "claude-opus-4-6" || !opus.Disabled || opus.DisabledLockstepTarget != "" || opus.AuthoredOnly {
		t.Fatalf("opus metadata = %+v", opus)
	}
	if opus.Effective.CompactionThreshold == nil || *opus.Effective.CompactionThreshold != 250_000 {
		t.Fatalf("opus Effective.CompactionThreshold = %v, want 250000", opus.Effective.CompactionThreshold)
	}
	if opus.Authored.CompactionThreshold == nil || *opus.Authored.CompactionThreshold != 250_000 || opus.Authored.WireID != "claude-opus-4-6-custom" {
		t.Fatalf("opus Authored = %+v", opus.Authored)
	}
	if opus.Provenance["CompactionThreshold"] != "config" {
		t.Fatalf("opus Provenance[CompactionThreshold] = %q, want config", opus.Provenance["CompactionThreshold"])
	}
	if opus.Provenance["MaxOutputTokens"] != "glob" {
		t.Fatalf("opus Provenance[MaxOutputTokens] = %q, want glob", opus.Provenance["MaxOutputTokens"])
	}
	if opus.Provenance["ContextWindow"] != "catalog" && opus.Provenance["ContextWindow"] != "overlay" {
		t.Fatalf("opus Provenance[ContextWindow] = %q, want catalog or overlay", opus.Provenance["ContextWindow"])
	}
	if !reflect.DeepEqual(opus.ThinkingFormats, registry.ThinkingFormats()) {
		t.Fatalf("ThinkingFormats = %v, want %v", opus.ThinkingFormats, registry.ThinkingFormats())
	}
	if !reflect.DeepEqual(opus.ThinkingShapes, registry.ThinkingShapes()) {
		t.Fatalf("ThinkingShapes = %v, want %v", opus.ThinkingShapes, registry.ThinkingShapes())
	}

	sonnet, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "work", Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("GetModel(claude-sonnet-4-6): %v", err)
	}
	if sonnet.Provenance["ContextWindow"] != "live" {
		t.Fatalf("sonnet Provenance[ContextWindow] = %q, want live", sonnet.Provenance["ContextWindow"])
	}
	if sonnet.Provenance["CompactionThreshold"] != "provider" {
		t.Fatalf("sonnet Provenance[CompactionThreshold] = %q, want provider", sonnet.Provenance["CompactionThreshold"])
	}

	sameAlias, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "work", Model: "same-alias"})
	if err != nil {
		t.Fatalf("GetModel(same-alias): %v", err)
	}
	if sameAlias.DisabledLockstepTarget != "claude-sonnet-4-6" {
		t.Fatalf("sameAlias.DisabledLockstepTarget = %q, want claude-sonnet-4-6", sameAlias.DisabledLockstepTarget)
	}
	if sameAlias.Provenance["Surface"] != "alias" {
		t.Fatalf("sameAlias.Provenance[Surface] = %q, want alias", sameAlias.Provenance["Surface"])
	}

	crossAlias, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "work", Model: "cross-alias"})
	if err != nil {
		t.Fatalf("GetModel(cross-alias): %v", err)
	}
	if crossAlias.DisabledLockstepTarget != "" {
		t.Fatalf("crossAlias.DisabledLockstepTarget = %q, want empty", crossAlias.DisabledLockstepTarget)
	}

	clamped, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "work", Model: "clamped-row"})
	if err != nil {
		t.Fatalf("GetModel(clamped-row): %v", err)
	}
	if clamped.Effective.CompactionThreshold != nil || clamped.Provenance["CompactionThreshold"] != "derived" || len(clamped.Warnings) == 0 {
		t.Fatalf("clamped detail = %+v", clamped)
	}

	customOnly, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "work", Model: "custom-only-row"})
	if err != nil {
		t.Fatalf("GetModel(custom-only-row): %v", err)
	}
	if !customOnly.AuthoredOnly {
		t.Fatalf("customOnly.AuthoredOnly = false, want true")
	}
}

func TestInstances_ModelGetReadableWhileWritesRefusedAndRefusesInvalidTargets(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": "sk-ant"})
	writeMinimalProvidersToml(t, f.tomlPath)
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	requireWireError(t, func() error {
		_, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "unknown-instance", Model: "claude-opus-4-6"})
		return err
	}(), appwire.CodeInvalidParams)
	requireWireError(t, func() error {
		_, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "base", Model: "claude-*"})
		return err
	}(), appwire.CodeInvalidParams)
	requireWireError(t, func() error {
		_, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "base", Model: "unauthored-mystery-model"})
		return err
	}(), appwire.CodeInvalidParams)

	if err := os.WriteFile(f.tomlPath, []byte("invalid = [toml\n"), 0o600); err != nil {
		t.Fatalf("WriteFile broken: %v", err)
	}
	if err := f.ctl.reg.Reload(); err == nil {
		t.Fatal("Reload succeeded on malformed providers.toml")
	}
	if !f.ctl.reg.WritesRefused() {
		t.Fatal("WritesRefused = false after failed reload")
	}

	detail, err := f.ctl.GetModel(appwire.InstanceModelGetParams{Name: "base", Model: "claude-opus-4-6"})
	if err != nil {
		t.Fatalf("GetModel while WritesRefused: %v", err)
	}
	if detail.Model != "claude-opus-4-6" || detail.Effective.ContextWindow == nil {
		t.Fatalf("GetModel while WritesRefused returned incomplete detail: %+v", detail)
	}
}

func TestInstances_ModelEditLifecycleImplicitExplicitSlashedAndBracketed(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	initCfg := `[providers.work]
base = "openai-compatible"
base_url = "https://gw.example.test/v1"
api_key = "sk-work"
`
	if err := os.WriteFile(tomlPath, []byte(initCfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	credsStore := newTestCredentialsStore(t)
	reg := newTestRegistry(t, t.TempDir(), tomlPath, credsStore, map[string]string{"ANTHROPIC_API_KEY": "sk-ant"})
	reg.Get().ApplyLive("work", []registry.Model{
		{ID: "deepseek/deepseek-4.1-flash", Caps: registry.Caps{ContextWindow: new(1_000_000)}},
		{ID: "sibling-live-model", Caps: registry.Caps{ContextWindow: new(128_000)}},
	})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            reg,
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	ct := 250_000
	var editWorkResp appwire.InstanceModelEditResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceModelEdit, appwire.InstanceModelEditParams{
		Name:                "work",
		Model:               "deepseek/deepseek-4.1-flash",
		CompactionThreshold: &ct,
		Protocol:            "openai-chat",
		InputModalities:     []string{"text", "image"},
		EffortValues:        []string{"low", "high"},
		OriginClientId:      "tab-model-edit",
	}, &editWorkResp); err != nil {
		t.Fatalf("modelEdit(work, deepseek/deepseek-4.1-flash): %v", err)
	}
	assertInstanceBroadcastShape(t, appwire.MethodEvenerInstanceModelEdit, waitForAuthUpdatedRaw(t, client), "tab-model-edit")

	if editWorkResp.Detail == nil || editWorkResp.Detail.Effective.CompactionThreshold == nil || *editWorkResp.Detail.Effective.CompactionThreshold != 250_000 {
		t.Fatalf("editWorkResp.Detail = %+v", editWorkResp.Detail)
	}
	workEntry := entry(t, editWorkResp.List, "work")
	if !slices.ContainsFunc(workEntry.Models, func(m appwire.InstanceModelEntry) bool {
		return m.ID == "deepseek/deepseek-4.1-flash" && m.Customized && m.CompactionThreshold != nil && *m.CompactionThreshold == 250_000
	}) {
		t.Fatalf("workEntry.Models missing customized deepseek/deepseek-4.1-flash: %+v", workEntry.Models)
	}
	if !slices.ContainsFunc(workEntry.Models, func(m appwire.InstanceModelEntry) bool {
		return m.ID == "sibling-live-model"
	}) {
		t.Fatalf("workEntry.Models lost cached live sibling across reload: %+v", workEntry.Models)
	}

	resolvedFlash, err := reg.Get().Resolve("work/deepseek/deepseek-4.1-flash")
	if err != nil {
		t.Fatalf("Resolve(work/deepseek/deepseek-4.1-flash): %v", err)
	}
	desc := cmdutil.ModelDescriptorFromResolved(resolvedFlash)
	if desc.CompactionThreshold == nil || *desc.CompactionThreshold != 250_000 || desc.SupportsVision == nil || !*desc.SupportsVision {
		t.Fatalf("ModelDescriptorFromResolved = %+v", desc)
	}

	// Edit bracketed alias model ID on implicit anthropic instance without touching base claude-opus-4-5.
	var editAntResp appwire.InstanceModelEditResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceModelEdit, appwire.InstanceModelEditParams{
		Name:                "anthropic",
		Model:               "claude-opus-4-5[1m]",
		CompactionThreshold: &ct,
		OriginClientId:      "tab-ant",
	}, &editAntResp); err != nil {
		t.Fatalf("modelEdit(anthropic, claude-opus-4-5[1m]): %v", err)
	}
	assertInstanceBroadcastShape(t, appwire.MethodEvenerInstanceModelEdit, waitForAuthUpdatedRaw(t, client), "tab-ant")

	parsedLayer, exists, err := registry.ReadConfigFile(tomlPath)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile: exists=%v err=%v", exists, err)
	}
	antProv, ok := parsedLayer.Providers["anthropic"]
	if !ok {
		t.Fatalf("providers.toml missing [providers.anthropic]: %+v", parsedLayer.Providers)
	}
	if _, touchedBase := antProv.Models["claude-opus-4-5"]; touchedBase {
		t.Fatalf("editing claude-opus-4-5[1m] touched base alias target claude-opus-4-5: %+v", antProv.Models)
	}
	bracketRow, ok := antProv.Models["claude-opus-4-5[1m]"]
	if !ok || bracketRow.Caps.CompactionThreshold == nil || *bracketRow.Caps.CompactionThreshold != 250_000 {
		t.Fatalf("providers.toml claude-opus-4-5[1m] row = %+v", bracketRow)
	}
}

func TestInstances_ModelEditPreservesHandAuthoredFieldsAndCleansBareRows(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": "sk-ant"})
	cfg := `[providers.work]
base = "anthropic"
api_key = "sk-work"

[providers.work.models."claude-opus-4-6"]
headers = { "X-Custom" = "keep-me" }
compaction_threshold = 200000
fields = { "temperature" = true }
chat_template_kwargs = { "enable_thinking" = true }

[providers.work.models."claude-opus-4-6".cost]
input = 1.5
output = 7.5
`
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	var resp appwire.InstanceModelEditResponse
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:     "work",
		Model:    "claude-opus-4-6",
		ResetAll: true,
	}, &resp); err != nil {
		t.Fatalf("EditModel(ResetAll): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "work")
	row, ok := p.Models["claude-opus-4-6"]
	if !ok {
		t.Fatal("claude-opus-4-6 row deleted despite hand-authored headers/cost/fields/chat_template_kwargs")
	}
	if row.Caps.CompactionThreshold != nil {
		t.Fatalf("CompactionThreshold survived ResetAll: %v", row.Caps.CompactionThreshold)
	}
	if row.Headers["X-Custom"] != "keep-me" || row.Caps.Cost == nil || row.Caps.Cost.Input != 1.5 || !row.Caps.Fields["temperature"] || row.Caps.ChatTemplateKwargs["enable_thinking"] != true {
		t.Fatalf("hand-authored fields lost on ResetAll: %+v", row)
	}

	// Shadow implicit anthropic by setting CompactionThreshold on claude-opus-4-6, then clear it.
	ct := 150_000
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                "anthropic",
		Model:               "claude-opus-4-6",
		CompactionThreshold: &ct,
	}, &resp); err != nil {
		t.Fatalf("EditModel(anthropic set): %v", err)
	}
	authoredEntry(t, f.tomlPath, "anthropic")

	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                     "anthropic",
		Model:                    "claude-opus-4-6",
		ClearCompactionThreshold: true,
	}, &resp); err != nil {
		t.Fatalf("EditModel(anthropic clear): %v", err)
	}
	l, exists, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile: exists=%v err=%v", exists, err)
	}
	if _, stillShadowed := l.Providers["anthropic"]; stillShadowed {
		t.Fatalf("clearing last model override on implicit anthropic left bare [providers.anthropic] shadow: %+v", l.Providers)
	}
	if _, workExists := l.Providers["work"]; !workExists {
		t.Fatal("[providers.work] was unexpectedly removed")
	}
}

func TestInstances_ModelEditCreateAuthoredOnlyAndRemoveCustomSymmetry(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"OPENAI_API_KEY": "sk-oai"})
	if err := f.ctl.auth.saveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", "user@example.com")); err != nil {
		t.Fatalf("saveAuth: %v", err)
	}
	cfg := `[providers.work]
base = "openai"
api_key = "sk-work"
`
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// Apply live listing on work so upstream snapshot rows not in live/overlay are hidden.
	f.ctl.reg.Get().ApplyLive("work", []registry.Model{{ID: "gpt-live-only"}})

	// Refused on live and overlay rows.
	for _, nonCustom := range []string{"gpt-live-only", "gpt-5.4"} {
		err := f.ctl.EditModel(appwire.InstanceModelEditParams{
			Name:         "work",
			Model:        nonCustom,
			RemoveCustom: true,
		}, nil)
		requireWireError(t, err, appwire.CodeInvalidParams)
	}

	// Create custom model on work (when live listing is active) and verify AuthoredOnly + RemoveCustom.
	cw := 512_000
	ct := 128_000
	var createResp appwire.InstanceModelEditResponse
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                "work",
		Model:               "deepseek/deepseek-r2-custom",
		Create:              true,
		ContextWindow:       &cw,
		CompactionThreshold: &ct,
	}, &createResp); err != nil {
		t.Fatalf("EditModel(Create on work): %v", err)
	}
	if createResp.Detail == nil || !createResp.Detail.AuthoredOnly {
		t.Fatalf("created model Detail = %+v, want AuthoredOnly=true", createResp.Detail)
	}

	var removeResp appwire.InstanceModelEditResponse
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:         "work",
		Model:        "deepseek/deepseek-r2-custom",
		RemoveCustom: true,
	}, &removeResp); err != nil {
		t.Fatalf("EditModel(RemoveCustom on work): %v", err)
	}
	if removeResp.Detail != nil {
		t.Fatalf("RemoveCustom Detail = %+v, want nil", removeResp.Detail)
	}

	// Create custom model on oauth-openai-codex and remove it cleanly.
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:          "openai-codex",
		Model:         "gpt-5.6-custom",
		Create:        true,
		ContextWindow: &cw,
	}, &createResp); err != nil {
		t.Fatalf("EditModel(Create on openai-codex): %v", err)
	}
	if createResp.Detail == nil || !createResp.Detail.AuthoredOnly {
		t.Fatalf("openai-codex created Detail = %+v, want AuthoredOnly=true", createResp.Detail)
	}
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:         "openai-codex",
		Model:        "gpt-5.6-custom",
		RemoveCustom: true,
	}, &removeResp); err != nil {
		t.Fatalf("EditModel(RemoveCustom on openai-codex): %v", err)
	}
	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if _, stillShadowed := l.Providers["openai-codex"]; stillShadowed {
		t.Fatalf("removing only custom model on implicit openai-codex left shadow: %+v", l.Providers)
	}
}

func TestInstances_ModelEditValidationSetClearPairsAndRollback(t *testing.T) {
	f := newInstancesFixture(t, nil)
	cfg := `[providers.work]
base = "anthropic"
api_key = "sk-work"

[providers.work.models."chained-alias"]
alias_of = "claude-opus-4-6"
`
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	valInt := 100_000
	valBool := true
	invalidCases := []appwire.InstanceModelEditParams{
		{Name: "work", Model: "claude-opus-4-6", RemoveCustom: true, Create: true},
		{Name: "work", Model: "claude-opus-4-6", RemoveCustom: true, ResetAll: true},
		{Name: "work", Model: "claude-opus-4-6", RemoveCustom: true, CompactionThreshold: &valInt},
		{Name: "work", Model: "claude-opus-4-6", RemoveCustom: true, ClearCompactionThreshold: true},
		{Name: "work", Model: "claude-opus-4-6", Create: true, ResetAll: true},
		{Name: "work", Model: "claude-opus-4-6", CompactionThreshold: &valInt, ClearCompactionThreshold: true},
		{Name: "work", Model: "claude-opus-4-6", ContextWindow: &valInt, ClearContextWindow: true},
		{Name: "work", Model: "claude-opus-4-6", MaxOutputTokens: &valInt, ClearMaxOutputTokens: true},
		{Name: "work", Model: "claude-opus-4-6", Reasoning: &valBool, ClearReasoning: true},
		{Name: "work", Model: "claude-opus-4-6", DefaultEffort: "high", ClearDefaultEffort: true},
		{Name: "work", Model: "claude-opus-4-6", EffortValues: []string{"high"}, ClearEffortValues: true},
		{Name: "work", Model: "claude-opus-4-6", InputModalities: []string{"text"}, ClearInputModalities: true},
		{Name: "work", Model: "claude-opus-4-6", Tools: &valBool, ClearTools: true},
		{Name: "work", Model: "claude-opus-4-6", Protocol: "anthropic", ClearProtocol: true},
		{Name: "work", Model: "claude-opus-4-6", Surface: "anthropic", ClearSurface: true},
		{Name: "work", Model: "claude-opus-4-6", ThinkingFormat: "anthropic", ClearThinkingFormat: true},
		{Name: "work", Model: "claude-opus-4-6", ThinkingShape: "budget", ClearThinkingShape: true},
		{Name: "work", Model: "claude-opus-4-6", WireID: "w", ClearWireID: true},
		{Name: "work", Model: "claude-opus-4-6", AliasOf: "claude-sonnet-4-6", ClearAliasOf: true},
		{Name: "work", Model: "claude-opus-4-6", EffortValues: []string{}},
		{Name: "work", Model: "claude-opus-4-6", InputModalities: []string{}},
		{Name: "work", Model: "claude-opus-4-6", AliasOf: "claude-opus-4-6"},
		{Name: "work", Model: "claude-opus-4-6", AliasOf: "claude-*"},
		{Name: "work", Model: "claude-opus-4-6", AliasOf: "nonexistent-target"},
		{Name: "work", Model: "claude-opus-4-6", AliasOf: "chained-alias"},
	}
	for i, tc := range invalidCases {
		err := f.ctl.EditModel(tc, nil)
		if err == nil {
			t.Fatalf("case %d (%+v) succeeded, want InvalidParams", i, tc)
		}
		requireWireError(t, err, appwire.CodeInvalidParams)
	}

	// Setting CompactionThreshold = 250_000 on a 1M window succeeds.
	cw1M := 1_000_000
	ct250k := 250_000
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                "work",
		Model:               "claude-opus-4-6",
		ContextWindow:       &cw1M,
		CompactionThreshold: &ct250k,
	}, nil); err != nil {
		t.Fatalf("EditModel(valid 250k on 1M): %v", err)
	}
	savedBytes, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// 1. Setting CompactionThreshold >= ContextWindow is rejected and leaves providers.toml unchanged.
	ctTooHigh := 1_000_000
	err = f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                "work",
		Model:               "claude-opus-4-6",
		CompactionThreshold: &ctTooHigh,
	}, nil)
	requireWireError(t, err, appwire.CodeInvalidParams)

	// 2. Lowering ContextWindow to 200_000 below existing CompactionThreshold (250_000) is rejected and leaves providers.toml unchanged.
	cw200k := 200_000
	err = f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:          "work",
		Model:         "claude-opus-4-6",
		ContextWindow: &cw200k,
	}, nil)
	requireWireError(t, err, appwire.CodeInvalidParams)

	// 3. Clearing ContextWindow when underlying catalog window (200_000 on claude-haiku-4-5) is <= CompactionThreshold (250_000) rolls back via writeAndReload validator.
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                "work",
		Model:               "claude-haiku-4-5",
		ContextWindow:       &cw1M,
		CompactionThreshold: &ct250k,
	}, nil); err != nil {
		t.Fatalf("EditModel(seed haiku 1M/250k): %v", err)
	}
	haikuBytes, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	err = f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:               "work",
		Model:              "claude-haiku-4-5",
		ClearContextWindow: true,
	}, nil)
	requireWireError(t, err, appwire.CodeInvalidParams)
	afterBytes, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile after rollback: %v", err)
	}
	if string(afterBytes) != string(haikuBytes) {
		t.Fatalf("providers.toml changed after rollback:\ngot:\n%s\nwant:\n%s", afterBytes, haikuBytes)
	}
	_ = savedBytes
}

func TestInstances_ModelEditEndpointFingerprintAndEffectiveAccess(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "anthropic",
		BaseURL: "https://gw.example.test/v1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-work"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}

	workEntry := entry(t, f.ctl.List(), "work")
	if workEntry.EndpointFingerprint == "" {
		t.Fatal("expected non-empty EndpointFingerprint")
	}

	// Stale fingerprint is refused with CodeConflict.
	ct := 120_000
	err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                        "work",
		Model:                       "claude-opus-4-6",
		CompactionThreshold:         &ct,
		ExpectedEndpointFingerprint: "stale-fp",
	}, nil)
	requireWireError(t, err, appwire.CodeConflict)

	// Record a credential rejection on work; verify a non-access model edit keeps it, while changing the default model's protocol clears it.
	resPresence, err := f.ctl.reg.Get().ResolveInstancePresence("work")
	if err != nil {
		t.Fatalf("ResolveInstancePresence: %v", err)
	}
	f.ctl.auth.rejections.byName = map[string]credentialRejection{
		"work": {revision: workEntry.ConfigRevision, message: "401 Unauthorized"},
	}
	if st, err := f.ctl.auth.Status(appwire.AuthStatusParams{Provider: "work"}); err != nil || st.Error == "" {
		t.Fatalf("precondition: expected credential rejection recorded on work, got st=%+v err=%v", st, err)
	}

	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                        "work",
		Model:                       "claude-opus-4-6",
		CompactionThreshold:         &ct,
		ExpectedEndpointFingerprint: workEntry.EndpointFingerprint,
	}, nil); err != nil {
		t.Fatalf("EditModel(CompactionThreshold with matching fingerprint): %v", err)
	}
	if st, err := f.ctl.auth.Status(appwire.AuthStatusParams{Provider: "work"}); err != nil || st.Error == "" {
		t.Fatalf("model threshold edit should not clear credential rejection, got st=%+v err=%v", st, err)
	}
	if f.ctl.auth.rejections.writes["work"] != 0 {
		t.Fatalf("model threshold edit incremented writes fence to %d, want 0", f.ctl.auth.rejections.writes["work"])
	}

	defaultModel := resPresence.DefaultModel
	if defaultModel == "" {
		defaultModel = "claude-sonnet-4-6"
	}
	if err := f.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                        "work",
		Model:                       defaultModel,
		Protocol:                    "openai-chat",
		ExpectedEndpointFingerprint: workEntry.EndpointFingerprint,
	}, nil); err != nil {
		t.Fatalf("EditModel(default model protocol change): %v", err)
	}
	if st, err := f.ctl.auth.Status(appwire.AuthStatusParams{Provider: "work"}); err != nil || st.Error != "" {
		t.Fatalf("default model protocol change must call forgetCredentialRejection, got st=%+v err=%v", st, err)
	}
	if f.ctl.auth.rejections.writes["work"] == 0 {
		t.Fatal("default model protocol change did not call forgetCredentialRejection")
	}

	// Unkeyable hub: empty ExpectedEndpointFingerprint is allowed, non-empty is refused with CodeConflict.
	unkeyable := newInstancesFixture(t, nil)
	if err := unkeyable.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	unkeyableStateRoot(t, unkeyable.stateDir)
	if err := unkeyable.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                "work",
		Model:               "claude-opus-4-6",
		CompactionThreshold: &ct,
	}, nil); err != nil {
		t.Fatalf("EditModel(empty fingerprint on unkeyable hub): %v", err)
	}
	err = unkeyable.ctl.EditModel(appwire.InstanceModelEditParams{
		Name:                        "work",
		Model:                       "claude-opus-4-6",
		CompactionThreshold:         &ct,
		ExpectedEndpointFingerprint: "asserted-fp",
	}, nil)
	requireWireError(t, err, appwire.CodeConflict)
}
