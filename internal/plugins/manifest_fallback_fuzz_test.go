//go:build serffuzz

package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzDetectNPMBinServer drives detectNPMBinServer — the package.json bin
// parser behind manifest-less MCP auto-wiring — with arbitrary package.json
// bytes. Invariants: it never panics; it never returns both servers and a
// note; any servers it returns are a valid JSON object whose single entry is
// a node command with exactly one arg rooted at ${CLAUDE_PLUGIN_ROOT} and no
// path escape; and it never wires anything when the plugin dir also has a
// .mcp.json (exercised on alternating runs via a sibling file toggle byte).
func FuzzDetectNPMBinServer(f *testing.F) {
	for _, s := range []string{
		`{"name":"private-journal-mcp","version":"2.0.1","bin":{"private-journal-mcp":"./dist/index.js"}}`,
		`{"bin":{"p":"./cli.js"}}`,
		`{"bin":{"a":"./a.js","b":"./b.js"}}`,
		`{"bin":{"p":"../../evil.js"}}`,
		`{"bin":{"p":"/abs/path.js"}}`,
		`{"bin":"./cli.js"}`,
		`{"bin":{}}`,
		`{"bin":null}`,
		`{"bin":123}`,
		`{"bin":{"p":""}}`,
		`{"bin":{"":"./x.js"}}`,
		`{"bin":{"p":"./nested/../cli.js"}}`,
		`{"bin": nope`,
		`{}`,
		``,
		`null`,
	} {
		f.Add([]byte(s), false)
	}
	f.Add([]byte(`{"bin":{"p":"./cli.js"}}`), true)

	f.Fuzz(func(t *testing.T, pkg []byte, withMCPJSON bool) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), pkg, 0o644); err != nil {
			t.Fatal(err)
		}
		// Materialize a plausible bin target so existence checks can pass for
		// simple relative paths the fuzzer discovers.
		_ = os.WriteFile(filepath.Join(dir, "cli.js"), []byte("x"), 0o644)
		if withMCPJSON {
			_ = os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644)
		}

		servers, note := detectNPMBinServer(dir, "p")
		if servers != nil && note != "" {
			t.Fatalf("both servers and note returned: %s / %q", servers, note)
		}
		if withMCPJSON && (servers != nil || note != "") {
			t.Fatalf("a plugin with its own .mcp.json must be left alone, got %s / %q", servers, note)
		}
		if servers == nil {
			return
		}
		var m map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if err := json.Unmarshal(servers, &m); err != nil || len(m) != 1 {
			t.Fatalf("wired servers not a single-entry object: %s (%v)", servers, err)
		}
		for name, cfg := range m {
			if name == "" {
				t.Fatalf("wired an empty server name: %s", servers)
			}
			if cfg.Command != "node" {
				t.Fatalf("wired command %q, want node: %s", cfg.Command, servers)
			}
			if len(cfg.Args) != 1 || !strings.HasPrefix(cfg.Args[0], "${CLAUDE_PLUGIN_ROOT}/") {
				t.Fatalf("wired args %v, want one ${CLAUDE_PLUGIN_ROOT}-rooted path", cfg.Args)
			}
			rel := strings.TrimPrefix(cfg.Args[0], "${CLAUDE_PLUGIN_ROOT}/")
			if rel == "" || strings.HasPrefix(rel, "/") || rel == ".." || strings.HasPrefix(rel, "../") {
				t.Fatalf("wired path escapes the plugin dir: %q", cfg.Args[0])
			}
		}
	})
}
