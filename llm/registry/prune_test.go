package registry

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPrunableTable_MatchesSpec(t *testing.T) {
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
			"generationConfig.temperature": true, "generationConfig.topP": true, "generationConfig.stopSequences": true,
			"toolConfig": true, "safetySettings": true, "cachedContent": false, "labels": false,
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
	if err := ValidateFields(map[string]bool{"store": true, "text.verbosity": false}, ProtocolOpenAIResponses, "providers.x"); err != nil {
		t.Fatal(err)
	}
	err := ValidateFields(map[string]bool{"stream_options": false}, ProtocolOpenAIResponses, "providers.x")
	if err == nil || !strings.Contains(err.Error(), "stream_options") || !strings.Contains(err.Error(), ProtocolOpenAIResponses) || !strings.Contains(err.Error(), "providers.x") {
		t.Fatalf("error must name key, protocol, and record: %v", err)
	}
}

func TestSeedFields(t *testing.T) {
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
	for _, proto := range []string{ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropic, ProtocolGoogle} {
		allFalse := Caps{Fields: map[string]bool{}}
		for _, p := range PrunablePaths(proto) {
			allFalse.Fields[p] = false
		}
		body := fullBody(proto)
		pruned := Prune(body, allFalse)
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
		if got := Prune(body, allTrue); len(got) != 0 {
			t.Errorf("%s: nothing should be pruned, got %v", proto, got)
		}
	}
}

func TestPrune_NestedLeafKeepsSiblingAndDropsEmptyParent(t *testing.T) {
	body := map[string]any{"text": map[string]any{"verbosity": "low", "format": map[string]any{"type": "text"}}, "reasoning": map[string]any{"context": "all_turns"}}
	pruned := Prune(body, Caps{Fields: map[string]bool{"text.verbosity": false, "reasoning.context": false}})
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
	body := map[string]any{"max_completion_tokens": 10}
	spelling := "max_completion_tokens"
	pruned := Prune(body, Caps{MaxTokensField: &spelling, Fields: map[string]bool{"max_tokens": false}})
	if !reflect.DeepEqual(pruned, []string{"max_completion_tokens"}) || len(body) != 0 {
		t.Fatalf("pruned = %v body = %v", pruned, body)
	}
	body = map[string]any{"max_tokens": 10}
	if pruned := Prune(body, Caps{Fields: map[string]bool{"max_tokens": false}}); !reflect.DeepEqual(pruned, []string{"max_tokens"}) {
		t.Fatalf("default spelling: %v", pruned)
	}
}

func TestPrune_DeveloperRoleIsPseudoAndAbsentPathsAreNotReported(t *testing.T) {
	body := map[string]any{"messages": []any{map[string]any{"role": "developer"}}}
	if pruned := Prune(body, Caps{Fields: map[string]bool{"developer_role": false, "store": false}}); len(pruned) != 0 {
		t.Fatalf("pseudo path and absent paths must not be reported: %v", pruned)
	}
	if len(body["messages"].([]any)) != 1 {
		t.Fatal("developer_role must not touch the body")
	}
}
