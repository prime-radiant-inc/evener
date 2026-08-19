package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// FuzzFluencyParsers exercises only local decoding and normalization paths.
func FuzzFluencyParsers(f *testing.F) {
	for _, seed := range []struct {
		which    uint8
		modelRef string
		raw      string
	}{
		{0, "openai/gpt-5.4-mini", `{"kind":"tool_call_start","data":{"tool_name":"shell"}}`},
		{0, "not-a-model", "not json\n"},
		{1, "openai/gpt-5.4-mini", "schema: 1\nid: shell_probe\nprompt: hello\n"},
		{1, "openai/gpt-5.4-mini", "schema: [\n"},
	} {
		f.Add(seed.which, seed.modelRef, seed.raw)
	}

	f.Fuzz(func(t *testing.T, which uint8, modelRef, raw string) {
		if len(modelRef) > 4096 || len(raw) > 64<<10 {
			t.Skip()
		}

		provider, model, err := splitModelRef(modelRef)
		if err == nil {
			againProvider, againModel, againErr := splitModelRef(provider + "/" + model)
			if againErr != nil || provider != againProvider || model != againModel {
				t.Fatalf("splitModelRef canonical round-trip failed for %q", modelRef)
			}
		}

		counts, errorsByTool, messages := parseEvents([]byte(raw))
		againCounts, againErrors, againMessages := parseEvents([]byte(raw))
		if !reflect.DeepEqual(counts, againCounts) || !reflect.DeepEqual(errorsByTool, againErrors) || !reflect.DeepEqual(messages, againMessages) {
			t.Fatalf("parseEvents is not deterministic for %q", raw)
		}
		for tool, count := range counts {
			if tool == "" || count <= 0 {
				t.Fatalf("parseEvents returned invalid tool count %q=%d", tool, count)
			}
		}
		for tool, count := range errorsByTool {
			if tool == "" || count <= 0 {
				t.Fatalf("parseEvents returned invalid error count %q=%d", tool, count)
			}
		}
		if safe := safeName(modelRef); safe == "" || (strings.TrimSpace(modelRef) == "" && safe != "unnamed") {
			t.Fatalf("safeName(%q) = %q", modelRef, safe)
		}

		if which&1 == 0 {
			return
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "fuzz.yaml")
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		probes, err := loadProbes(dir, "all")
		if err != nil {
			return
		}
		againProbes, err := loadProbes(dir, "all")
		if err != nil {
			t.Fatalf("second loadProbes(%q) failed: %v", dir, err)
		}
		if !reflect.DeepEqual(probes, againProbes) {
			t.Fatalf("loadProbes is not deterministic: first=%#v second=%#v", probes, againProbes)
		}
		for _, probe := range probes {
			if strings.TrimSpace(probe.ID) == "" {
				t.Fatalf("loadProbes accepted an empty id: %#v", probe)
			}
		}
	})
}
