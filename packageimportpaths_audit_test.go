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
// The fixtures are trees, not strings: the script sweeps three directories
// whole, and the only thing it does not read is a resolver config named by
// filename, so where a line sits decides the verdict as much as what it says.

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

// A tree where nothing names the package by path -- the scripts/*.mts tools
// included -- alongside the one spelling that is allowed to: the resolver
// config that maps the name onto the path in the first place.
func cleanPackageImportTree() map[string]string {
	return map[string]string{
		"cmd/evener-hub/frontend/src/app.ts":  "import { WireError } from \"@evener/appwire-client\";\n",
		"mobile-native/src/screen.tsx":        "import { AppwireClient } from \"@evener/appwire-client\";\n",
		"mobile/src/state.ts":                 "import { hasItemFailure } from \"@evener/appwire-client\";\n",
		"mobile-native/scripts/check-hub.mts": "import type { WebSocketLike } from \"@evener/appwire-client\";\n",
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
			name:    "the directory the package moved out of",
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
			name:    "a specifier on the line after the call that opens it",
			path:    "cmd/evener-hub/frontend/src/app.test.ts",
			line:    "vi.mock(\n\t\"../../protocol/reducer\",\n\t() => ({}),\n);\n",
			expects: "by path",
		},
		{
			name:    "a backtick specifier with nothing to interpolate",
			path:    "cmd/evener-hub/frontend/src/app.test.ts",
			line:    "vi.mock(`../../protocol/reducer`, () => ({}));\n",
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
		{
			name:    "the seam directory itself, with the quote right after it",
			path:    "cmd/evener-hub/frontend/src/app.ts",
			line:    "import { errorText } from \"../protocol\";\n",
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

// A tree that is not there sweeps nothing and says nothing, which reads as a
// pass. The gate has to refuse to run instead.
func TestPackageImportPathsCheckRefusesAMissingTree(t *testing.T) {
	for _, missing := range []string{"cmd/evener-hub/frontend/src", "mobile-native", "mobile/src"} {
		t.Run(missing, func(t *testing.T) {
			files := cleanPackageImportTree()
			for path := range files {
				if strings.HasPrefix(path, missing+"/") {
					delete(files, path)
				}
			}
			passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
			if passed {
				t.Fatalf("the gate passed a tree with no %s:\n%s", missing, output)
			}
			if !strings.Contains(output, "does not exist") {
				t.Fatalf("the gate failed for the wrong reason; wanted a missing-tree message in:\n%s", output)
			}
		})
	}
}

// `cd ""` succeeds and changes nothing, so an empty --root once swept whatever
// directory the caller was in and reported on a tree nobody asked about.
func TestPackageImportPathsCheckRefusesAnUnusableRoot(t *testing.T) {
	for name, root := range map[string]string{"empty": "", "missing": filepath.Join(t.TempDir(), "absent")} {
		t.Run(name, func(t *testing.T) {
			passed, output := runPackageImportCheck(t, root)
			if passed {
				t.Fatalf("the gate passed with a %s --root:\n%s", name, output)
			}
		})
	}
}

// The resolver-config exemption is by filename. Matching *.config.* instead
// would excuse any app module someone happened to call something.config.ts.
func TestPackageImportPathsCheckExemptsResolverConfigsByName(t *testing.T) {
	files := cleanPackageImportTree()
	files["cmd/evener-hub/frontend/src/panes/spawn/launch.config.ts"] =
		"import { errorText } from \"../../../protocol/errors\";\n"
	passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
	if passed {
		t.Fatalf("the gate excused an app module for being named like a config:\n%s", output)
	}
	if !strings.Contains(output, "launch.config.ts") {
		t.Fatalf("the gate named no offending file; wanted launch.config.ts in:\n%s", output)
	}
}
