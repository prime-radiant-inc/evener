package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/sdk/js-toolchain-pins-check.mjs keeps the npm trees that share client
// sources on one toolchain. Its whole job is a negative -- a range or a skewed
// version nothing else would notice -- so each case plants one in a fixture tree.

func runToolchainPinsCheck(t *testing.T, files map[string]string) (bool, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/sdk/js-toolchain-pins-check.mjs gate runs on Unix CI")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node is not on PATH: %v", err)
	}
	root := t.TempDir()
	for path, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(wd, "scripts", "sdk", "js-toolchain-pins-check.mjs")
	output, err := exec.Command("node", script, "--root", root).CombinedOutput()
	if err != nil {
		exitErr := &exec.ExitError{}
		if !errors.As(err, &exitErr) {
			t.Fatalf("running %s: %v", script, err)
		}
		return false, string(output)
	}
	return true, string(output)
}

func pinnedToolchainTree() map[string]string {
	return map[string]string{
		"cmd/evener-hub/frontend/package.json":   `{"devDependencies":{"@biomejs/biome":"2.5.5","vitest":"4.1.11","typescript":"6.0.3","vite":"^8.1.5"},"dependencies":{"react":"19.2.7"}}`,
		"cmd/evener-hub/frontend/biome.jsonc":    `{"$schema":"https://biomejs.dev/schemas/2.5.5/schema.json"}`,
		"mobile-native/package.json":             `{"devDependencies":{"@biomejs/biome":"2.5.5","vitest":"4.1.11","typescript":"6.0.3"},"dependencies":{"react":"19.2.3","zustand":"^5.0.15"}}`,
		"mobile-native/biome.jsonc":              `{"$schema":"https://biomejs.dev/schemas/2.5.5/schema.json"}`,
		"appwire-client/typescript/package.json": `{"devDependencies":{"typescript":"6.0.3"}}`,
	}
}

func TestJSToolchainPinsPassesAgreeingExactPins(t *testing.T) {
	passed, output := runToolchainPinsCheck(t, pinnedToolchainTree())
	if !passed {
		t.Fatalf("agreeing exact pins must pass; the gate said:\n%s", output)
	}
}

func TestJSToolchainPinsRejectsSkewAndRanges(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		offends string
	}{
		{"caret range", "mobile-native/package.json", `{"devDependencies":{"@biomejs/biome":"^2.5.5"}}`, "mobile-native/package.json"},
		{"tilde range", "appwire-client/typescript/package.json", `{"devDependencies":{"typescript":"~6.0.3"}}`, "appwire-client/typescript/package.json"},
		{"different minor", "mobile-native/package.json", `{"devDependencies":{"vitest":"5.0.0"}}`, "vitest"},
		{"react range", "cmd/evener-hub/frontend/package.json", `{"dependencies":{"react":"^19.2.7"}}`, "react"},
		{"stale biome schema", "mobile-native/biome.jsonc", `{"$schema":"https://biomejs.dev/schemas/2.5.12/schema.json"}`, "mobile-native/biome.jsonc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := pinnedToolchainTree()
			files[c.file] = c.content
			passed, output := runToolchainPinsCheck(t, files)
			if passed {
				t.Fatalf("the gate passed %s:\n%s", c.name, output)
			}
			if !strings.Contains(output, c.offends) {
				t.Fatalf("the gate never named %q:\n%s", c.offends, output)
			}
		})
	}
}
