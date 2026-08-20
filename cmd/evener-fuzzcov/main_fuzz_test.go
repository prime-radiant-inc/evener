package main

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzGapInputs drives the two file parsers the gap gate trusts — the target
// registry and the ignore-list — with arbitrary content. The oracle is
// "never panic, and a parse that succeeds round-trips through the consumers".
func FuzzGapInputs(f *testing.F) {
	f.Add("native:llm:.:FuzzParseSSE", "example.com/x  # reason")
	f.Add("rapid:.:./internal/appserver:TestRouterSeqFuzz", "example.com/y")
	f.Add("# comment\n\nnative:agent:.:FuzzToolArgsValidate:./internal/tool,.", "# header only")
	f.Fuzz(func(t *testing.T, registry, ignore string) {
		if len(registry)+len(ignore) > 8192 {
			return
		}
		dir := t.TempDir()
		regPath := filepath.Join(dir, "registry.txt")
		ignPath := filepath.Join(dir, "ignore.txt")
		if err := os.WriteFile(regPath, []byte(registry), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ignPath, []byte(ignore), 0o600); err != nil {
			t.Fatal(err)
		}
		targets, regErr := readRegistry(regPath)
		_, _ = readIgnore(ignPath)
		if regErr != nil {
			return
		}
		// A parsed registry feeds the gap gate: staticFuzzedPackages must not
		// panic on whatever the parser accepted.
		_ = staticFuzzedPackages(targets, map[string]string{".": "m", "agent": "m/agent"})
	})
}
