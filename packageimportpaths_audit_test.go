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

// scripts/sdk/package-import-paths-check.sh is the gate that keeps the AppWire
// TypeScript package addressed by name. Its whole job is a negative -- finding
// a spelling nothing else in the tree would notice -- so it needs fixtures
// that carry each spelling, or it is a gate that could not fail.
//
// The fixtures are trees, not strings: the script sweeps directories and
// exempts one of them (mobile-native/scripts, the plan's tsx carve-out), so
// where a line sits decides the verdict as much as what it says.

// packageImportFixture writes files (repo-relative path -> contents) into a new
// temporary tree and returns its root.
func packageImportFixture(t *testing.T, files map[string]string) string {
	t.Helper()
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
	return root
}

// runPackageImportCheck runs the gate against root and reports whether it
// passed, along with everything it printed.
func runPackageImportCheck(t *testing.T, root string) (bool, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/sdk/package-import-paths-check.sh requires a Unix shell")
	}
	for _, tool := range []string{"bash", "grep", "xargs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH: %v", tool, err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(wd, "scripts", "sdk", "package-import-paths-check.sh")
	output, err := exec.Command("bash", script, "--root", root).CombinedOutput()
	if err != nil {
		exitErr := &exec.ExitError{}
		if !errors.As(err, &exitErr) {
			t.Fatalf("running %s: %v", script, err)
		}
		return false, string(output)
	}
	return true, string(output)
}

// A tree where nothing names the package by path, including the two spellings
// that are allowed to: the carve-out's relative import and the resolver config
// that maps the name onto the path in the first place.
func cleanPackageImportTree() map[string]string {
	return map[string]string{
		"cmd/evener-hub/frontend/src/app.ts":  "import { WireError } from \"@evener/appwire-client\";\n",
		"mobile-native/src/screen.tsx":        "import { AppwireClient } from \"@evener/appwire-client\";\n",
		"mobile/src/state.ts":                 "import { hasItemFailure } from \"@evener/appwire-client\";\n",
		"mobile-native/scripts/check-hub.mts": "import type { WebSocketLike } from \"../../appwire-client/typescript/transport\";\n",
		"mobile-native/vitest.config.mts":     "const shared = new URL(\"../appwire-client/typescript/index.ts\", import.meta.url);\n",
	}
}

func TestPackageImportPathsCheckPassesACleanTree(t *testing.T) {
	passed, output := runPackageImportCheck(t, packageImportFixture(t, cleanPackageImportTree()))
	if !passed {
		t.Fatalf("the clean fixture must pass; the gate said:\n%s", output)
	}
}

// Each case is one spelling that reaches the package by path. Four of them --
// the vitest mocking helpers, single quotes, and the package directory named
// without a trailing slash -- bypassed the gate's first implementation, which
// matched only double-quoted specifiers after from/import/require.
func TestPackageImportPathsCheckRejectsEveryPathSpelling(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		path    string
		line    string
		expects string
	}{
		{
			name:    "relative import of the package directory",
			path:    "mobile/src/state.ts",
			line:    "import type { WireError } from \"../../appwire-client/typescript/errors\";\n",
			expects: "by path",
		},
		{
			name:    "the old relative protocol specifier inside the web tree",
			path:    "cmd/evener-hub/frontend/src/app.ts",
			line:    "import type { WireError } from \"./protocol/errors\";\n",
			expects: "by path",
		},
		{
			name:    "the directory the package moved out of, from the carve-out",
			path:    "mobile-native/scripts/check-hub.mts",
			line:    "import type { W } from \"../../cmd/evener-hub/frontend/src/protocol/transport\";\n",
			expects: "no longer exists",
		},
		{
			name:    "vi.mock naming the seam",
			path:    "cmd/evener-hub/frontend/src/app.test.ts",
			line:    "vi.mock(\"../../protocol/reducer\", () => ({}));\n",
			expects: "by path",
		},
		{
			name:    "vi.importActual naming the seam",
			path:    "cmd/evener-hub/frontend/src/app.test.ts",
			line:    "const actual = await vi.importActual(\"../../protocol/reducer\");\n",
			expects: "by path",
		},
		{
			name:    "a single-quoted specifier",
			path:    "cmd/evener-hub/frontend/src/app.ts",
			line:    "import { errorText } from '../../protocol/errors';\n",
			expects: "by path",
		},
		{
			name:    "the package directory with no trailing slash",
			path:    "mobile-native/src/screen.tsx",
			line:    "import { AppwireClient } from \"../../appwire-client/typescript\";\n",
			expects: "by path",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			files := cleanPackageImportTree()
			files[testCase.path] = testCase.line
			passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
			if passed {
				t.Fatalf("the gate passed a tree containing %s\n  %s", testCase.path, testCase.line)
			}
			if !strings.Contains(output, testCase.expects) {
				t.Fatalf("the gate failed for the wrong reason; wanted %q in:\n%s", testCase.expects, output)
			}
			if !strings.Contains(output, testCase.path) {
				t.Fatalf("the gate named no offending file; wanted %q in:\n%s", testCase.path, output)
			}
		})
	}
}

// The word "protocol" appears in this app as a terminal-reason literal and
// inside @modelcontextprotocol specifiers. Matching it there would make the
// gate fire on code that has nothing to do with the package.
func TestPackageImportPathsCheckIgnoresProtocolThatIsNotTheSeam(t *testing.T) {
	files := cleanPackageImportTree()
	files["cmd/evener-hub/frontend/src/banner.ts"] = strings.Join([]string{
		"import { spawn } from \"@modelcontextprotocol/server-everything\";",
		"setClosedReason(\"protocol\");",
		"// The moved profile lives in appwire-client/typescript/tokenFlood.bench.ts.",
		"",
	}, "\n")
	passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
	if !passed {
		t.Fatalf("the gate fired on a tree that never names the package by path:\n%s", output)
	}
}
