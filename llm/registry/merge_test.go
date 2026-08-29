package registry

import (
	"reflect"
	"testing"
)

func TestMergeCaps_LaterSetFieldWins_NilInherits(t *testing.T) {
	prov := map[string]string{}
	dst := Caps{ContextWindow: intp(1000), Tools: boolp(true), EffortValues: []string{"low"}}
	src := Caps{ContextWindow: intp(2000), EffortValues: []string{"high", "max"}}
	mergeCaps(&dst, src, "overlay/provider", prov)
	if *dst.ContextWindow != 2000 {
		t.Fatalf("ContextWindow = %d, want 2000", *dst.ContextWindow)
	}
	if dst.Tools == nil || !*dst.Tools {
		t.Fatalf("Tools should be inherited (nil in src)")
	}
	if !reflect.DeepEqual(dst.EffortValues, []string{"high", "max"}) {
		t.Fatalf("EffortValues should replace wholesale, got %v", dst.EffortValues)
	}
	if prov["ContextWindow"] != "overlay/provider" || prov["EffortValues"] != "overlay/provider" {
		t.Fatalf("provenance not recorded: %v", prov)
	}
	if _, ok := prov["Tools"]; ok {
		t.Fatalf("provenance must not be recorded for an inherited field")
	}
}

func TestMergeCaps_MapsMergeKeyWise(t *testing.T) {
	prov := map[string]string{}
	dst := Caps{Fields: map[string]bool{"store": false, "temperature": true}}
	src := Caps{Fields: map[string]bool{"store": true}, ChatTemplateKwargs: map[string]any{"a": 1}}
	mergeCaps(&dst, src, "config/row", prov)
	if !dst.Fields["store"] || !dst.Fields["temperature"] {
		t.Fatalf("Fields must merge key-wise: %v", dst.Fields)
	}
	if dst.ChatTemplateKwargs["a"] != 1 {
		t.Fatalf("ChatTemplateKwargs not merged")
	}
	if prov["Fields.store"] != "config/row" {
		t.Fatalf("map provenance is per key: %v", prov)
	}
}

func TestMergeCaps_CostAndFinishReasonMapReplaceWholesale(t *testing.T) {
	prov := map[string]string{}
	dst := Caps{Cost: &Cost{Input: 1, Output: 2}, FinishReasonMap: map[string]string{"a": "b", "c": "d"}}
	src := Caps{Cost: &Cost{Input: 5}, FinishReasonMap: map[string]string{"x": "y"}}
	mergeCaps(&dst, src, "live", prov)
	if dst.Cost.Input != 5 || dst.Cost.Output != 0 {
		t.Fatalf("Cost must replace wholesale: %+v", *dst.Cost)
	}
	if len(dst.FinishReasonMap) != 1 || dst.FinishReasonMap["x"] != "y" {
		t.Fatalf("FinishReasonMap must replace wholesale: %v", dst.FinishReasonMap)
	}
}

func TestMergeTransport_FieldWise(t *testing.T) {
	dst := Transport{Auth: "bearer", BaseURL: "https://a/v1", Vars: map[string]string{"X": "1"}, Body: map[string]any{"k": "v"}}
	src := Transport{AuthHeader: "x-api-key", Vars: map[string]string{"Y": "2"}, Body: map[string]any{"k2": true}}
	mergeTransport(&dst, src)
	if dst.Auth != "bearer" || dst.AuthHeader != "x-api-key" || dst.BaseURL != "https://a/v1" {
		t.Fatalf("scalar merge wrong: %+v", dst)
	}
	if dst.Vars["X"] != "1" || dst.Vars["Y"] != "2" || dst.Body["k"] != "v" || dst.Body["k2"] != true {
		t.Fatalf("map merge wrong: %+v", dst)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, id string
		want        bool
	}{
		{"gpt-5*", "gpt-5.6", true},
		{"gpt-5*", "gpt-4.1", false},
		{"*claude-opus-4-5*", "us.anthropic.claude-opus-4-5-20251101-v1:0", true},
		{"*claude-opus-4-5*", "claude-opus-4-6", false},
		{"minimax/*", "minimax/MiniMax-M2.7", true},
		{"minimax/*", "MiniMax-M2.7", false},
		{"*", "anything", true},
		{"MiniMax-M3", "minimax-m3", false}, // case-sensitive
		{"gpt-5.6", "gpt-5.6", true},        // exact key is not a glob but must still match itself
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.id); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.id, got, c.want)
		}
	}
}

func TestSortGlobs_ShorterThenLexical(t *testing.T) {
	got := sortGlobs([]string{"gpt-5.6*", "gpt-5*", "*", "gpt-4.1*"})
	want := []string{"*", "gpt-5*", "gpt-4.1*", "gpt-5.6*"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortGlobs = %v, want %v", got, want)
	}
}

func TestIsGlob(t *testing.T) {
	if !isGlob("gpt-5*") || isGlob("gpt-5.6") {
		t.Fatalf("isGlob wrong")
	}
}
