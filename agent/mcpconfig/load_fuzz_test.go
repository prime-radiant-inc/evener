package mcpconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/edgeseeds"
)

// fuzzSetVar is a deterministic environment variable the fuzz target sets so
// that ${VAR} expansion reaches its "variable is set" success branch on every
// run regardless of the ambient environment.
const (
	fuzzSetVar    = "MCP_FUZZ_SET_VAR"
	fuzzSetVal    = "expanded-value"
	fuzzUnsetVar  = "MCP_FUZZ_UNSET_VAR_DO_NOT_SET"
	fuzzUnsetName = "${" + fuzzUnsetVar + "}"
)

// FuzzMCPConfigLoad drives LoadFile — the package's real .mcp.json decode seam
// (json.Unmarshal of the config file → ParseServerMap → serverJSONToConfig with
// ${VAR} expansion). Input is the raw config file bytes written to a temp file.
//
// Because this is an internal (package mcpconfig) test, the oracle cross-checks
// the returned configs against the same bytes re-decoded into the unexported
// mcpConfigFile/mcpServerJSON shapes. For any config LoadFile accepts it asserts
// the structural invariants ParseServerMap + serverJSONToConfig guarantee:
//
//   - one ServerConfig per server entry (no entries dropped or invented);
//   - every entry's name survives verbatim and is non-empty;
//   - every ServerConfig has a non-empty Type, defaulted to "stdio" when the
//     input type is empty/whitespace, otherwise the trimmed input type;
//   - ${VAR} expansion is 1:1 — it never adds or drops an arg, env key, or
//     header key (counts and key sets are preserved).
//
// And for any config LoadFile rejects it asserts the no-partial-result
// invariant: a non-nil error comes with an empty config slice.
//
// SAFETY: this target stays strictly within parse/expand/validate. It never
// reaches mcpstatus.ProbeMCPStatus or anything that spawns an MCP subprocess or
// opens a network connection — a fuzzer must never launch real servers. The
// ${VAR} expander only consults os.LookupEnv; it does not shell out.
func FuzzMCPConfigLoad(f *testing.F) {
	// Drive expansion deterministically: a known-set variable (success branch)
	// and a guaranteed-unset variable (missing-variable error branch).
	f.Setenv(fuzzSetVar, fuzzSetVal)
	os.Unsetenv(fuzzUnsetVar)

	seeds := []string{
		// Stdio + http/sse shapes and a defaulted ${VAR}.
		`{"mcpServers":{"a":{"command":"echo","args":["hi"]}}}`,
		`{"mcpServers":{"web":{"type":"http","url":"http://x","headers":{"k":"v"}}}}`,
		`{"mcpServers":{"sse":{"type":"sse","url":"http://x/sse"}}}`,
		`{"mcpServers":{"e":{"command":"x","env":{"K":"${HOME:-/tmp}"}}}}`,
		`{"mcpServers":{}}`,
		`{}`,
		`not json`,
		``,
		// Empty / whitespace server name → rejected.
		`{"mcpServers":{"":{"command":"x"}}}`,
		`{"mcpServers":{"  ":{"command":"x"}}}`,
		// Server entry that is not an object → per-server unmarshal error.
		`{"mcpServers":{"a":42}}`,
		`{"mcpServers":{"a":"oops"}}`,
		// Missing ${VAR} with no default in each expandable field → expansion error.
		`{"mcpServers":{"a":{"command":"` + fuzzUnsetName + `"}}}`,
		`{"mcpServers":{"a":{"command":"x","args":["` + fuzzUnsetName + `"]}}}`,
		`{"mcpServers":{"a":{"command":"x","env":{"K":"` + fuzzUnsetName + `"}}}}`,
		`{"mcpServers":{"a":{"type":"http","url":"` + fuzzUnsetName + `"}}}`,
		`{"mcpServers":{"a":{"type":"http","url":"u","headers":{"H":"` + fuzzUnsetName + `"}}}}`,
		// Known-set variable → expansion success branch.
		`{"mcpServers":{"a":{"command":"${` + fuzzSetVar + `}","args":["pre-${` + fuzzSetVar + `}"]}}}`,
		// ${VAR:-default} on an unset variable → default branch.
		`{"mcpServers":{"a":{"command":"pre${` + fuzzUnsetVar + `:-def}post"}}}`,
		// Unterminated ${ → treated literally (no closing brace).
		`{"mcpServers":{"a":{"command":"tail-${b"}}}`,
		// Whitespace-only type → defaulted to stdio.
		`{"mcpServers":{"a":{"type":"  ","command":"x"}}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	// Generic JSON decoder stressors (deep nesting, surrogates, dup keys, …).
	for _, s := range edgeseeds.JSON() {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		configs, err := LoadFile(path)
		if err != nil {
			// Rejected config must not leak a partial result.
			if len(configs) != 0 {
				t.Fatalf("LoadFile rejected config (%v) but returned %d configs", err, len(configs))
			}
			return
		}

		// LoadFile accepted: the same bytes must re-decode into the config file
		// shape so we can cross-check against the intended server map.
		var cf mcpConfigFile
		if derr := json.Unmarshal(raw, &cf); derr != nil {
			t.Fatalf("LoadFile succeeded but raw bytes fail to re-decode: %v", derr)
		}

		// One config per server entry — nothing dropped or invented.
		if len(configs) != len(cf.MCPServers) {
			t.Fatalf("got %d configs for %d server entries", len(configs), len(cf.MCPServers))
		}

		byName := make(map[string]ServerConfig, len(configs))
		for _, cfg := range configs {
			if strings.TrimSpace(cfg.Name) == "" {
				t.Fatalf("accepted config has empty server name")
			}
			if cfg.Type == "" {
				t.Fatalf("server %q has empty Type after parse", cfg.Name)
			}
			if _, dup := byName[cfg.Name]; dup {
				t.Fatalf("duplicate server name %q in result", cfg.Name)
			}
			byName[cfg.Name] = cfg
		}

		for name, rawSrv := range cf.MCPServers {
			cfg, ok := byName[name]
			if !ok {
				t.Fatalf("server %q present in input but missing from result", name)
			}

			var sj mcpServerJSON
			if jerr := json.Unmarshal(rawSrv, &sj); jerr != nil {
				t.Fatalf("server %q re-decode failed though LoadFile succeeded: %v", name, jerr)
			}

			// Type defaulting: empty/whitespace → "stdio", else the trimmed input.
			wantType := strings.TrimSpace(sj.Type)
			if wantType == "" {
				wantType = "stdio"
			}
			if cfg.Type != wantType {
				t.Fatalf("server %q: Type = %q, want %q", name, cfg.Type, wantType)
			}

			// ${VAR} expansion is 1:1: it never adds or drops args/keys.
			if len(cfg.Args) != len(sj.Args) {
				t.Fatalf("server %q: arg count changed %d -> %d", name, len(sj.Args), len(cfg.Args))
			}
			assertSameKeys(t, name, "env", sj.Env, cfg.Env)
			assertSameKeys(t, name, "headers", sj.Headers, cfg.Headers)
		}
	})
}

// assertSameKeys fails if the expanded map dropped, renamed, or invented a key
// relative to the raw input map. Expansion rewrites values only, never keys.
func assertSameKeys(t *testing.T, server, field string, raw, expanded map[string]string) {
	t.Helper()
	if len(expanded) != len(raw) {
		t.Fatalf("server %q: %s key count changed %d -> %d", server, field, len(raw), len(expanded))
	}
	for k := range raw {
		if _, present := expanded[k]; !present {
			t.Fatalf("server %q: %s key %q dropped during expansion", server, field, k)
		}
	}
}
