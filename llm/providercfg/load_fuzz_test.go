package providercfg

import (
	"strings"
	"testing"
	"unicode"
)

// FuzzProvidersTOMLLoad drives providercfg.Load over arbitrary providers.toml
// bytes. The oracle is floor "no panic" plus structured-error/no-partial (a
// rejected input yields a non-nil error AND the zero Config — never a panic,
// never a half-populated value) plus success-invariants read directly off
// Load: the resolved default names some instance, every type is known, every
// name is lowercase and '/'-free, and api_style is only ever set on openai.
func FuzzProvidersTOMLLoad(f *testing.F) {
	f.Add([]byte("[instances.openai]\ntype = \"openai\"\n"))
	f.Add([]byte("default = \"a\"\n[instances.a]\ntype = \"anthropic\"\n[instances.b]\ntype = \"kimi-anthropic\"\n"))
	f.Add([]byte("[instances.x]\ntype = \"openai\"\napi_style = \"responses\"\n"))
	// Error shapes — must produce structured errors, never panics.
	f.Add([]byte("[instances.X]\ntype=\"openai\"\n"))                             // uppercase name
	f.Add([]byte("[instances.a]\ntype=\"bogus\"\n"))                              // unknown type
	f.Add([]byte("[instances.a]\ntype=\"anthropic\"\napi_style=\"responses\"\n")) // api_style on non-openai
	f.Add([]byte("default=\"nope\"\n[instances.a]\ntype=\"openai\"\n"))           // dangling default
	f.Add([]byte(""))                                                             // empty -> no instances
	f.Add([]byte("not = toml = ["))                                               // parse error
	f.Add([]byte("[instances.a/b]\ntype=\"openai\"\n"))                           // '/' in name
	// Generic TOML decoder stressors. These mirror fuzz/edgeseeds.TOML() but are
	// inlined: the llm module does not depend on the fuzz module, and wiring it
	// in just to share ~dozen byte literals would pull rapid into llm's graph.
	for _, s := range tomlEdgeSeeds() {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		cfg, err := Load(raw)
		if err != nil {
			// No-partial: a rejected input leaves the zero Config.
			if cfg.Default != "" || cfg.Instances != nil {
				t.Fatalf("Load error returned non-zero Config: %#v\n input=%q", cfg, raw)
			}
			return
		}

		// Success-invariants (each guaranteed by Load).
		if len(cfg.Instances) == 0 {
			t.Fatalf("Load succeeded with no instances\n input=%q", raw)
		}
		names := make(map[string]bool, len(cfg.Instances))
		for _, inst := range cfg.Instances {
			names[inst.Name] = true
			if !knownTypes[inst.Type] {
				t.Fatalf("instance %q has unknown type %q\n input=%q", inst.Name, inst.Type, raw)
			}
			for _, r := range inst.Name {
				if unicode.IsUpper(r) {
					t.Fatalf("instance name %q is not lowercase\n input=%q", inst.Name, raw)
				}
			}
			if strings.Contains(inst.Name, "/") {
				t.Fatalf("instance name %q contains '/'\n input=%q", inst.Name, raw)
			}
			if inst.APIStyle != "" && inst.Type != "openai" {
				t.Fatalf("instance %q has api_style %q on non-openai type %q\n input=%q",
					inst.Name, inst.APIStyle, inst.Type, raw)
			}
		}
		if !names[cfg.Default] {
			t.Fatalf("Default %q does not name an instance %v\n input=%q",
				cfg.Default, names, raw)
		}
	})
}

// tomlEdgeSeeds returns generic TOML decoder stressors (dup keys/tables,
// datetime and number extremes, multiline strings, inline tables, BOM prefix).
// Kept in lockstep with fuzz/edgeseeds.TOML().
func tomlEdgeSeeds() [][]byte {
	ss := []string{
		"a = 1\na = 2\n",                            // duplicate bare key (rejected)
		"[t]\nx = 1\n[t]\ny = 2\n",                  // duplicate table (rejected)
		"[a.b.c.d.e.f.g.h]\nx = 1\n",                // deep dotted table
		"a = 9999999999999999999999999\n",           // integer overflow
		"a = nan\n",                                 // float NaN
		"a = inf\n",                                 // float +inf
		"a = 2020-01-01T00:00:00.999999999+05:30\n", // offset datetime, nanos
		"a = 2020-01-01\n",                          // local date
		"a = \"\"\"\nline1\nline2\n\"\"\"\n",        // multiline basic string
		"a = \"\\U0001F600\"\n",                     // astral unicode escape
		"\"a.b\" = 1\n'k k' = 2\n",                  // quoted/dotted keys
		"a = {b = 1, c = [1, 2]}\n",                 // inline table
		"[[t]]\nx = 1\n[[t]]\nx = 2\n",              // array of tables
		"a = [1, \"x\", true]\n",                    // mixed-type array (rejected)
		"\ufeff" + "a = 1\n",                        // BOM prefix
		"\"\" = 1\n",                                // empty key
	}
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}
