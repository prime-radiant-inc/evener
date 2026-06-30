package mcpconfig

import (
	"strings"
	"testing"
	"unicode"
)

// FuzzMCPInlineMerge drives the two pure config combinators that FuzzMCPConfigLoad
// does not touch: ParseInline (the "name:command args..." CLI spec parser) and
// Merge (the layered, last-wins shadow-by-name combiner). Both shape how MCP
// servers are assembled from CLI flags yet only unit tests exercised them.
//
// The fuzzer parses each newline-separated spec, then Merges the accepted configs
// (grouped into layers) and checks Merge's shadowing contract.
//
// Oracles (never bare no-panic):
//
// ParseInline — for every accepted spec:
//   - Type is always "stdio"; Name and Command are non-empty and carry no leading
//     or trailing whitespace; Args carry no empty elements.
//   - reconstruction: Name == trimmed text before the first colon, and
//     Command+Args == Fields(trimmed text after the colon).
//   - determinism: parsing the same spec twice yields an identical config.
//   - rejection is total: a non-nil error comes with a zero-value config.
//
// Merge:
//   - every distinct server name across all layers appears exactly once;
//   - last-wins: the surviving config for a name is the last one supplied with
//     that name across the layer sequence;
//   - first-appearance order is preserved;
//   - no name is invented or dropped (result count == distinct-name count).
//
// SAFETY: pure string parsing and slice merging — no env lookup, no file I/O,
// no process spawn.
func FuzzMCPInlineMerge(f *testing.F) {
	f.Add("fs:npx @modelcontextprotocol/server-filesystem /tmp\nweb:curl http://x")
	f.Add("a:cmd\n  b  :  run  --flag  \n:nocolon\nempty:")
	f.Add("dup:one\ndup:two\nx:y")
	f.Add("no-colon-at-all")
	f.Add("")

	f.Fuzz(func(t *testing.T, blob string) {
		var accepted []ServerConfig
		for _, spec := range strings.Split(blob, "\n") {
			cfg, err := ParseInline(spec)
			if err != nil {
				if !configEqual(cfg, ServerConfig{}) {
					t.Fatalf("ParseInline rejected %q but returned a non-zero config %+v", spec, err)
				}
				continue
			}
			assertInlineConfig(t, spec, cfg)

			// Determinism: a second parse of the same spec must match.
			if again, err2 := ParseInline(spec); err2 != nil || !configEqual(again, cfg) {
				t.Fatalf("ParseInline not deterministic for %q: %+v / %v vs %+v", spec, again, err2, cfg)
			}
			accepted = append(accepted, cfg)
		}

		// Split the accepted configs into up to three layers to exercise both
		// in-layer and cross-layer shadowing in Merge.
		layers := splitLayers(accepted)
		merged := Merge(layers...)
		assertMerge(t, layers, merged)
	})
}

func assertInlineConfig(t *testing.T, spec string, cfg ServerConfig) {
	t.Helper()
	if cfg.Type != "stdio" {
		t.Fatalf("ParseInline %q: Type = %q, want stdio", spec, cfg.Type)
	}
	if cfg.Name == "" || cfg.Command == "" {
		t.Fatalf("ParseInline %q: empty Name/Command in accepted config %+v", spec, cfg)
	}
	if strings.TrimSpace(cfg.Name) != cfg.Name {
		t.Fatalf("ParseInline %q: Name %q has surrounding whitespace", spec, cfg.Name)
	}
	for _, a := range cfg.Args {
		if a == "" {
			t.Fatalf("ParseInline %q: empty arg element in %v", spec, cfg.Args)
		}
		if strings.IndexFunc(a, unicode.IsSpace) >= 0 {
			t.Fatalf("ParseInline %q: arg %q contains whitespace (Fields should have split it)", spec, a)
		}
	}

	// Reconstruct from the raw spec and confirm the split matches.
	trimmed := strings.TrimSpace(spec)
	colon := strings.Index(trimmed, ":")
	wantName := strings.TrimSpace(trimmed[:colon])
	if cfg.Name != wantName {
		t.Fatalf("ParseInline %q: Name = %q, want %q", spec, cfg.Name, wantName)
	}
	fields := strings.Fields(strings.TrimSpace(trimmed[colon+1:]))
	if len(fields) == 0 {
		t.Fatalf("ParseInline %q: accepted but command half has no fields", spec)
	}
	if cfg.Command != fields[0] {
		t.Fatalf("ParseInline %q: Command = %q, want %q", spec, cfg.Command, fields[0])
	}
	if len(cfg.Args) != len(fields)-1 {
		t.Fatalf("ParseInline %q: %d args, want %d", spec, len(cfg.Args), len(fields)-1)
	}
	for i, a := range cfg.Args {
		if a != fields[i+1] {
			t.Fatalf("ParseInline %q: arg[%d] = %q, want %q", spec, i, a, fields[i+1])
		}
	}
}

// splitLayers partitions configs into up to three layers, round-robin, so a name
// can recur both within one layer and across layers.
func splitLayers(configs []ServerConfig) [][]ServerConfig {
	layers := make([][]ServerConfig, 3)
	for i, c := range configs {
		layers[i%3] = append(layers[i%3], c)
	}
	return layers
}

func assertMerge(t *testing.T, layers [][]ServerConfig, merged []ServerConfig) {
	t.Helper()

	// Independent model of Merge: last config wins per name; first-appearance
	// order of names is preserved.
	var order []string
	last := map[string]ServerConfig{}
	seen := map[string]bool{}
	for _, layer := range layers {
		for _, c := range layer {
			if !seen[c.Name] {
				seen[c.Name] = true
				order = append(order, c.Name)
			}
			last[c.Name] = c
		}
	}

	if len(merged) != len(order) {
		t.Fatalf("Merge returned %d configs, want %d distinct names", len(merged), len(order))
	}
	resultSeen := map[string]bool{}
	for i, c := range merged {
		if resultSeen[c.Name] {
			t.Fatalf("Merge result has duplicate name %q", c.Name)
		}
		resultSeen[c.Name] = true
		if c.Name != order[i] {
			t.Fatalf("Merge order: position %d is %q, want %q", i, c.Name, order[i])
		}
		if !configEqual(c, last[c.Name]) {
			t.Fatalf("Merge did not keep the last config for %q: got %+v, want %+v", c.Name, c, last[c.Name])
		}
	}
}

// configEqual compares two ServerConfigs by value. ServerConfig holds slices and
// maps, so it is not comparable with ==; this helper covers every field.
func configEqual(a, b ServerConfig) bool {
	if a.Name != b.Name || a.Type != b.Type || a.Command != b.Command || a.URL != b.URL {
		return false
	}
	if len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	return mapEqual(a.Env, b.Env) && mapEqual(a.Headers, b.Headers)
}

func mapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
