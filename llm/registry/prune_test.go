package registry

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPrunableTable_MatchesSpec(t *testing.T) {
	t.Parallel()
	want := map[string]map[string]bool{
		ProtocolOpenAIChat: {
			"temperature": true, "top_p": true, "stop": true, "stream_options": true, "max_tokens": true,
			"store": false, "frequency_penalty": false, "presence_penalty": false, "developer_role": false,
			"parallel_tool_calls": false, "prompt_cache_key": false, "prompt_cache_retention": false,
			"service_tier": false, "metadata": false, "logprobs": false, "n": false, "seed": false, "user": false,
		},
		ProtocolOpenAIResponses: {
			"temperature": true, "top_p": true, "max_output_tokens": true,
			"store": false, "include": false, "truncation": false, "safety_identifier": false, "service_tier": false,
			"prompt_cache_key": false, "prompt_cache_retention": false, "previous_response_id": false,
			"conversation": false, "metadata": false, "max_tool_calls": false, "background": false,
			"parallel_tool_calls": false, "text.verbosity": false, "reasoning.context": false,
		},
		ProtocolAnthropic: {
			"temperature": true, "top_p": true, "stop_sequences": true, "max_tokens": true,
			"metadata": false, "service_tier": false, "fallbacks": false, "container": false,
		},
		ProtocolGoogle: {
			"generation_config.temperature": true, "generation_config.top_p": true,
			"generation_config.stop_sequences": true, "generation_config.max_output_tokens": true,
			FieldGoogleThinkingLevel: true, FieldGoogleThinkingSummaries: true,
			"generation_config.tool_choice": true, "safety_settings": true, "store": true,
			"generation_config.seed": false, "previous_interaction_id": false,
			"service_tier": false, "labels": false,
		},
	}
	for proto, table := range want {
		if got := Baseline(proto); !reflect.DeepEqual(got, table) {
			t.Errorf("%s baseline = %v", proto, got)
		}
		paths := PrunablePaths(proto)
		if !sort.StringsAreSorted(paths) || len(paths) != len(table) {
			t.Errorf("%s PrunablePaths = %v", proto, paths)
		}
	}
	if PrunablePaths("grpc") != nil {
		t.Error("unknown protocol must yield nil")
	}
	b := Baseline(ProtocolAnthropic)
	b["metadata"] = true
	if Baseline(ProtocolAnthropic)["metadata"] {
		t.Error("Baseline must return a copy")
	}
}

func TestValidateFields(t *testing.T) {
	t.Parallel()
	if err := ValidateFields(map[string]bool{"store": true, "text.verbosity": false}, ProtocolOpenAIResponses, "providers.x"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFields(map[string]bool{FieldGoogleThinkingLevel: false, FieldGoogleThinkingSummaries: false}, ProtocolGoogle, "providers.vertex-arc.models.gemini-4-argon"); err != nil {
		t.Fatal(err)
	}
	err := ValidateFields(map[string]bool{"stream_options": false}, ProtocolOpenAIResponses, "providers.x")
	if err == nil || !strings.Contains(err.Error(), "stream_options") || !strings.Contains(err.Error(), ProtocolOpenAIResponses) || !strings.Contains(err.Error(), "providers.x") {
		t.Fatalf("error must name key, protocol, and record: %v", err)
	}
	// Retired camelCase Google fields keys must fail ValidateFields naming the quoted TOML replacement(s).
	for _, tc := range []struct {
		retired string
		wants   []string
	}{
		{"generationConfig.thinkingConfig", []string{`"generation_config.thinking_level"`, `"generation_config.thinking_summaries"`}},
		{"toolConfig", []string{`"generation_config.tool_choice"`}},
		{"cachedContent", []string{"remove"}},
	} {
		gerr := ValidateFields(map[string]bool{tc.retired: false}, ProtocolGoogle, "providers.google")
		for _, w := range tc.wants {
			if gerr == nil || !strings.Contains(gerr.Error(), tc.retired) || !strings.Contains(gerr.Error(), w) {
				t.Fatalf("retired key %q error = %v, want mention of %q", tc.retired, gerr, w)
			}
		}
	}
}

func TestSeedFields(t *testing.T) {
	t.Parallel()
	c := Caps{Fields: map[string]bool{"store": true}}
	seedFields(&c, ProtocolOpenAIResponses)
	if !c.Fields["store"] || c.Fields["include"] || !c.Fields["temperature"] || len(c.Fields) != len(PrunablePaths(ProtocolOpenAIResponses)) {
		t.Fatalf("seeded fields: %v", c.Fields)
	}
}

func fullBody(proto string) map[string]any {
	body := map[string]any{"model": "m", "input": "x"}
	for _, p := range PrunablePaths(proto) {
		if p == FieldDeveloperRole {
			continue
		}
		setPath(body, p, 1)
	}
	return body
}

func TestPrune_RemovesOnlyFalsePaths(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropic, ProtocolGoogle} {
		allFalse := Caps{Fields: map[string]bool{}}
		for _, p := range PrunablePaths(proto) {
			allFalse.Fields[p] = false
		}
		body := fullBody(proto)
		pruned := Prune(body, allFalse, ProtocolOpenAIChat)
		if !sort.StringsAreSorted(pruned) {
			t.Errorf("%s: pruned list must be sorted: %v", proto, pruned)
		}
		for _, p := range PrunablePaths(proto) {
			if p == FieldDeveloperRole {
				continue
			}
			if _, ok := getPath(body, p); ok {
				t.Errorf("%s: %s survived the prune", proto, p)
			}
		}
		if body["model"] != "m" || body["input"] != "x" {
			t.Errorf("%s: non-prunable paths must survive", proto)
		}
		allTrue := Caps{Fields: map[string]bool{}}
		for _, p := range PrunablePaths(proto) {
			allTrue.Fields[p] = true
		}
		body = fullBody(proto)
		if got := Prune(body, allTrue, ProtocolOpenAIChat); len(got) != 0 {
			t.Errorf("%s: nothing should be pruned, got %v", proto, got)
		}
	}
}

// Clearing generation_config.thinking_level drops only that leaf and leaves
// sibling generation_config fields standing.
func TestPrune_GoogleThinkingLevel(t *testing.T) {
	t.Parallel()
	body := map[string]any{"generation_config": map[string]any{"temperature": 0.2, "thinking_level": "high", "thinking_summaries": "auto"}}
	pruned := Prune(body, Caps{Fields: map[string]bool{FieldGoogleThinkingLevel: false}}, ProtocolGoogle)
	if !reflect.DeepEqual(pruned, []string{FieldGoogleThinkingLevel}) {
		t.Fatalf("pruned = %v", pruned)
	}
	gen := body["generation_config"].(map[string]any)
	if _, ok := gen["thinking_level"]; ok {
		t.Fatalf("thinking_level survived the prune: %v", gen)
	}
	if _, ok := gen["temperature"]; !ok || gen["thinking_summaries"] != "auto" {
		t.Fatalf("sibling fields must survive: %v", gen)
	}
}

func TestPrune_NestedLeafKeepsSiblingAndDropsEmptyParent(t *testing.T) {
	t.Parallel()
	body := map[string]any{"text": map[string]any{"verbosity": "low", "format": map[string]any{"type": "text"}}, "reasoning": map[string]any{"context": "all_turns"}}
	pruned := Prune(body, Caps{Fields: map[string]bool{"text.verbosity": false, "reasoning.context": false}}, ProtocolOpenAIResponses)
	if !reflect.DeepEqual(pruned, []string{"reasoning.context", "text.verbosity"}) {
		t.Fatalf("pruned = %v", pruned)
	}
	if _, ok := body["text"].(map[string]any)["format"]; !ok {
		t.Fatal("sibling must survive")
	}
	if _, ok := body["reasoning"]; ok {
		t.Fatal("emptied parent must be removed")
	}
}

func TestPrune_MaxTokensSpelling(t *testing.T) {
	t.Parallel()
	body := map[string]any{"max_completion_tokens": 10}
	spelling := "max_completion_tokens"
	pruned := Prune(body, Caps{MaxTokensField: &spelling, Fields: map[string]bool{"max_tokens": false}}, ProtocolOpenAIChat)
	if !reflect.DeepEqual(pruned, []string{"max_completion_tokens"}) || len(body) != 0 {
		t.Fatalf("pruned = %v body = %v", pruned, body)
	}
	body = map[string]any{"max_tokens": 10}
	if pruned := Prune(body, Caps{Fields: map[string]bool{"max_tokens": false}}, ProtocolOpenAIChat); !reflect.DeepEqual(pruned, []string{"max_tokens"}) {
		t.Fatalf("default spelling: %v", pruned)
	}
}

// TestPrune_MaxTokensFieldIsAChatSpelling pins that the output-limit spelling
// Cap names a path only where that path exists. A gateway's chat listing can
// advertise max_completion_tokens for a model whose row resolves to the
// anthropic protocol; following the cap there leaves the user's
// fields.max_tokens = false pruning nothing, so the field reaches a provider
// that rejects it.
func TestPrune_MaxTokensFieldIsAChatSpelling(t *testing.T) {
	t.Parallel()
	spelling := "max_completion_tokens"
	body := map[string]any{"max_tokens": 10}
	pruned := Prune(body, Caps{MaxTokensField: &spelling, Fields: map[string]bool{"max_tokens": false}}, ProtocolAnthropic)
	if !reflect.DeepEqual(pruned, []string{"max_tokens"}) || len(body) != 0 {
		t.Fatalf("pruned = %v body = %v, want the anthropic max_tokens path", pruned, body)
	}
}

func TestPrune_DeveloperRoleIsPseudoAndAbsentPathsAreNotReported(t *testing.T) {
	t.Parallel()
	body := map[string]any{"messages": []any{map[string]any{"role": "developer"}}}
	if pruned := Prune(body, Caps{Fields: map[string]bool{"developer_role": false, "store": false}}, ProtocolOpenAIChat); len(pruned) != 0 {
		t.Fatalf("pseudo path and absent paths must not be reported: %v", pruned)
	}
	if len(body["messages"].([]any)) != 1 {
		t.Fatal("developer_role must not touch the body")
	}
}

func TestTemperaturePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		protocol string
		want     string
	}{
		{ProtocolOpenAIChat, "temperature"},
		{ProtocolOpenAIResponses, "temperature"},
		{ProtocolAnthropic, "temperature"},
		{ProtocolGoogle, "generation_config.temperature"},
		{"unknown-protocol", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := TemperaturePath(tc.protocol); got != tc.want {
			t.Errorf("TemperaturePath(%q) = %q, want %q", tc.protocol, got, tc.want)
		}
	}
}

func TestTemperatureSupported(t *testing.T) {
	t.Parallel()
	boolPtr := func(v bool) *bool { return &v }
	row := func(step, model string) map[string]string {
		if model == "" {
			return map[string]string{"model": step}
		}
		return map[string]string{"model": step + ":" + model}
	}
	// catalogRow is a row lookup that also carries a snapshot-attributed fact,
	// as every real catalog row does (context window, family, …).
	catalogRow := func(step, model string) map[string]string {
		prov := row(step, model)
		prov["ContextWindow"] = LayerSnapshot + "/row"
		return prov
	}
	cases := []struct {
		name string
		res  Resolved
		want bool
	}{
		{
			name: "catalog row with fields true",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: catalogRow("row", "gpt-x"),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
			want: true,
		},
		{
			name: "region-prefixed catalog row",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: catalogRow("region", "gpt-x"),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
			want: true,
		},
		{
			name: "dated-suffix catalog row",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: catalogRow("dated", "gpt-x"),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
			want: true,
		},
		{
			name: "catalog row with fields false",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: catalogRow("row", "gpt-x"),
				Caps: Caps{Fields: map[string]bool{"temperature": false}}},
		},
		{
			name: "live-only row inherits send-by-default baseline",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: row("live", ""),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
		},
		{
			name: "synthesized row",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: row("synthesized", ""),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
		},
		{
			name: "absent model provenance",
			res: Resolved{Protocol: ProtocolOpenAIResponses,
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
		},
		{
			name: "sampling false overrides fields true",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: row("row", "o-x"),
				Caps: Caps{Sampling: boolPtr(false), Fields: map[string]bool{"temperature": true}}},
		},
		{
			name: "sampling true keeps fields verdict",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: catalogRow("row", "gpt-x"),
				Caps: Caps{Sampling: boolPtr(true), Fields: map[string]bool{"temperature": true}}},
			want: true,
		},
		{
			name: "google row keys generation_config.temperature",
			res: Resolved{Protocol: ProtocolGoogle, Provenance: catalogRow("row", "gemini-x"),
				Caps: Caps{Fields: map[string]bool{"generation_config.temperature": true}}},
			want: true,
		},
		{
			name: "google row with plain temperature key only reads as absent",
			res: Resolved{Protocol: ProtocolGoogle, Provenance: catalogRow("row", "gemini-x"),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
		},
		{
			name: "unknown protocol",
			res: Resolved{Protocol: "unknown", Provenance: row("row", "x"),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
		},
		{
			// A user-configured custom row with no catalog facts and no explicit
			// temperature field: baseline true is silence, not support.
			name: "user-configured row with no facts",
			res: Resolved{Protocol: ProtocolOpenAIResponses, Provenance: row("row", "my-model"),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
		},
		{
			// A user-configured row with facts but none catalog-attributed and no
			// explicit temperature field: still silence.
			name: "user-configured row with config-attributed facts only",
			res: Resolved{Protocol: ProtocolOpenAIResponses,
				Provenance: func() map[string]string {
					prov := row("row", "my-model")
					prov["ContextWindow"] = LayerConfig + "/row"
					return prov
				}(),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
		},
		{
			// An explicit fields.temperature = true from any layer is a deliberate
			// choice and vouches without catalog backing.
			name: "user-configured row with explicit fields temperature true",
			res: Resolved{Protocol: ProtocolOpenAIResponses,
				Provenance: func() map[string]string {
					prov := row("row", "my-model")
					prov["Fields.temperature"] = LayerConfig + "/row"
					return prov
				}(),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
			want: true,
		},
		{
			// A user-defined model on a curated base inherits provider-level
			// overlay facts (StrictTools, MaxTokensField, most Fields.*) — those
			// inherited facts must not mistake the custom row for a catalog one
			// (round-5 probe on a work/my-model row showed exactly this).
			name: "user-configured row inheriting overlay provider facts",
			res: Resolved{Protocol: ProtocolOpenAIResponses,
				Provenance: func() map[string]string {
					prov := row("row", "my-model")
					prov["StrictTools"] = LayerOverlay + "/provider"
					prov["MaxTokensField"] = LayerOverlay + "/provider"
					prov["Fields.store"] = LayerOverlay + "/provider"
					return prov
				}(),
				Caps: Caps{Fields: map[string]bool{"temperature": true, "store": false}}},
		},
		{
			// A cached catalog row fact vouches the same way a snapshot one does.
			name: "cache-backed catalog row",
			res: Resolved{Protocol: ProtocolOpenAIResponses,
				Provenance: func() map[string]string {
					prov := catalogRow("row", "gpt-x")
					prov["ContextWindow"] = LayerCache + "/row"
					return prov
				}(),
				Caps: Caps{Fields: map[string]bool{"temperature": true}}},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := TemperatureSupported(tc.res); got != tc.want {
			t.Errorf("%s: TemperatureSupported = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestApplyBodyConstants_DeepClonesNestedMapsAcrossCalls pins that a constant
// object is installed as a copy: two requests built from one shared
// Transport.Body must not share nested maps, so a builder edit on the first
// body cannot leak into the second or back into the registry's constants.
func TestApplyBodyConstants_DeepClonesNestedMapsAcrossCalls(t *testing.T) {
	t.Parallel()
	constants := map[string]any{
		"generation_config":      map[string]any{"max_output_tokens": 1000},
		"generation_config.seed": 7,
		"safety_settings":        []any{map[string]any{"type": "harassment", "threshold": "block_only_high"}},
	}
	body1 := map[string]any{}
	ApplyBodyConstants(body1, constants)
	body2 := map[string]any{}
	ApplyBodyConstants(body2, constants)

	gen1, ok := body1["generation_config"].(map[string]any)
	if !ok {
		t.Fatalf("generation_config = %T, want a map", body1["generation_config"])
	}
	gen1["max_output_tokens"] = 200
	ss1 := body1["safety_settings"].([]any)
	ss1[0].(map[string]any)["threshold"] = "block_none"
	ss1[0] = "mutated"

	if got := constants["generation_config"].(map[string]any)["max_output_tokens"]; got != 1000 {
		t.Fatalf("shared constant mutated: max_output_tokens = %v, want 1000", got)
	}
	if got := constants["generation_config.seed"]; got != 7 {
		t.Fatalf("shared constant mutated: seed = %v, want 7", got)
	}
	gen2 := body2["generation_config"].(map[string]any)
	if gen2["max_output_tokens"] != 1000 {
		t.Fatalf("second body shares the first body's map: max_output_tokens = %v, want 1000", gen2["max_output_tokens"])
	}
	if gen2["seed"] != 7 {
		t.Fatalf("second body seed = %v, want 7", gen2["seed"])
	}
	ss2 := body2["safety_settings"].([]any)
	if ss2[0].(map[string]any)["threshold"] != "block_only_high" {
		t.Fatalf("second body shares safety_settings slice/map with first: %v", ss2)
	}
}
