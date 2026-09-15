package evener_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
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
	for _, tool := range []string{"bash", "grep"} {
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

// specifierForm is one way a source file can name a module, as
// scripts/sdk/module-specifier-forms.json spells it. The AST reader's own
// tests walk the same file.
type specifierForm struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

func loadSpecifierForms(t *testing.T) []specifierForm {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "scripts", "sdk", "module-specifier-forms.json"))
	if err != nil {
		t.Fatalf("reading the shared form list: %v", err)
	}
	var forms []specifierForm
	if err := json.Unmarshal(raw, &forms); err != nil {
		t.Fatalf("parsing the shared form list: %v", err)
	}
	if len(forms) == 0 {
		t.Fatal("the shared form list is empty; this audit would be measuring nothing")
	}
	return forms
}

// The gate is a grep and the rewriter is a TypeScript AST reader, so they
// answer the same question in two languages and drifted apart form by form --
// a backtick specifier, a vi.mock, a specifier on the line after its call.
// They read one list of forms now, and this is the half that holds the grep to
// it: every form the AST reader recognizes must also be a form the gate
// catches, or a path import can be rewritten-but-unguarded again.
func TestPackageImportPathsCheckCatchesEveryFormTheReaderKnows(t *testing.T) {
	for _, form := range loadSpecifierForms(t) {
		t.Run(form.Name, func(t *testing.T) {
			files := cleanPackageImportTree()
			files["mobile/src/probe.ts"] = strings.ReplaceAll(form.Source, "@@SPEC@@", "../../appwire-client/typescript/errors")
			passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
			if passed {
				t.Fatalf("the gate passed a %s naming the package by path:\n%s", form.Name, form.Source)
			}
			if !strings.Contains(output, "mobile/src/probe.ts") {
				t.Fatalf("the gate named no offending file:\n%s", output)
			}
		})
	}
}

// The resolver-config exemption is by exact filename: a glob over
// vitest.config.* also excused a vitest.config.extra.ts, which is not one.
func TestPackageImportPathsCheckExemptsOnlyTheResolverConfigsThatExist(t *testing.T) {
	files := cleanPackageImportTree()
	// A real import, not a new URL(): the sweep reads module loaders only, so
	// the fixture has to be one for this to be about the exemption.
	files["mobile-native/vitest.config.extra.ts"] =
		"import { errorText } from \"../appwire-client/typescript/errors\";\nvoid errorText;\n"
	passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
	if passed {
		t.Fatalf("the gate excused a file for being named like a resolver config:\n%s", output)
	}
	if !strings.Contains(output, "vitest.config.extra.ts") {
		t.Fatalf("the gate named no offending file:\n%s", output)
	}
}

// Metro and Vite resolve JavaScript beside TypeScript, so a .js module in
// these trees reaches the package by path exactly as a .ts one does and
// bundles the same. The sweep's --include list named only the TypeScript
// extensions, which left every one of these invisible to it.
func TestPackageImportPathsCheckSweepsEveryExtensionTheBundlersResolve(t *testing.T) {
	for _, file := range []string{
		"mobile-native/src/legacyScreen.js",
		"mobile-native/src/legacyScreen.jsx",
		"mobile-native/scripts/seed.cjs",
		"mobile-native/scripts/seed.mjs",
		"mobile/src/legacyState.cts",
		"cmd/evener-hub/frontend/src/legacyBridge.js",
	} {
		t.Run(file, func(t *testing.T) {
			files := cleanPackageImportTree()
			files[file] = "import { errorText } from \"../appwire-client/typescript/errors\";\nvoid errorText;\n"
			passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
			if passed {
				t.Fatalf("the gate never read %s, so a path import there is free:\n%s", file, output)
			}
			if !strings.Contains(output, filepath.Base(file)) {
				t.Fatalf("the gate named no offending file; wanted %s in:\n%s", file, output)
			}
		})
	}
}

// Widening the sweep to JavaScript brings metro.config.js inside it. Mapping
// the package name onto its path is the whole job of a resolver config, so the
// exemption has to still hold for the one file the wider sweep newly reaches.
func TestPackageImportPathsCheckStillExemptsTheJavaScriptResolverConfig(t *testing.T) {
	files := cleanPackageImportTree()
	files["mobile-native/metro.config.js"] =
		"const appwire = require(\"../appwire-client/typescript/index.ts\");\nmodule.exports = appwire;\n"
	passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
	if !passed {
		t.Fatalf("the gate faulted the resolver config for naming the path it exists to map:\n%s", output)
	}
}

// The exemption is by the exact path grep prints, not the basename. A basename
// match excused every metro.config.js anywhere -- including an app file a tree
// happened to name that way in a subdirectory, which is ordinary source that
// must not reach the package by path.
func TestPackageImportPathsCheckExemptsResolverConfigsByPathNotBasename(t *testing.T) {
	files := cleanPackageImportTree()
	files["cmd/evener-hub/frontend/src/metro.config.js"] =
		"const appwire = require(\"../../appwire-client/typescript/index.ts\");\nmodule.exports = appwire;\n"
	passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
	if passed {
		t.Fatalf("the gate excused an app file for sharing a resolver config's basename:\n%s", output)
	}
	if !strings.Contains(output, "src/metro.config.js") {
		t.Fatalf("the gate named no offending file; wanted src/metro.config.js in:\n%s", output)
	}
}

// The sweep reads module LOADERS. An earlier pattern accepted any identifier
// before a parenthesis, which made an ordinary call carrying the package path
// -- reading a fixture file, building a URL -- a failure of a gate about
// imports.
func TestPackageImportPathsCheckIgnoresCallsThatLoadNothing(t *testing.T) {
	files := cleanPackageImportTree()
	files["mobile-native/src/fixtures.ts"] = strings.Join([]string{
		`const raw = readFileSync("../../appwire-client/typescript/fixtures/thread.jsonl", "utf8");`,
		`const url = new URL("../../appwire-client/typescript/docContent.ts", import.meta.url);`,
		`expect(thing).toThrow("../../appwire-client/typescript/errors");`,
		"",
	}, "\n")
	passed, output := runPackageImportCheck(t, packageImportFixture(t, files))
	if !passed {
		t.Fatalf("the gate fired on calls that load nothing:\n%s", output)
	}
}

// The gate is a grep and the reader is a TypeScript AST walk, and each carries
// its own list of the calls that take a module specifier. They are compared
// here rather than trusted to stay equal: a loader added to one and not the
// other is either a form the gate stops catching or a call it starts flagging.
func TestPackageImportPathsCheckKnowsTheSameLoaderCallsAsTheReader(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	read := func(rel string) string {
		raw, readErr := os.ReadFile(filepath.Join(wd, rel))
		if readErr != nil {
			t.Fatalf("reading %s: %v", rel, readErr)
		}
		return string(raw)
	}
	names := func(source, pattern string) []string {
		match := regexp.MustCompile(pattern).FindStringSubmatch(source)
		if match == nil {
			t.Fatalf("no loader-call list found with %q", pattern)
		}
		found := regexp.MustCompile(`[A-Za-z]+`).FindAllString(match[1], -1)
		sort.Strings(found)
		return found
	}
	gate := names(read(filepath.Join("scripts", "sdk", "package-import-paths-check.sh")), `(?m)^mock_calls='([^']*)'`)
	reader := names(read(filepath.Join("scripts", "sdk", "module-specifiers.mjs")), `MOCK_CALLS = new Set\(\[([^\]]*)\]`)
	if !slices.Equal(gate, reader) {
		t.Fatalf("the gate knows %v and the reader knows %v; they have to be the same calls", gate, reader)
	}
}

// The grep gate is a shell script, so it cannot import the shared extension and
// skipped-directory lists the rewriter and the value-import derivation read
// from scripts/sdk/source-files.mjs -- it carries its own copy in bash arrays.
// An extension one sweeps and the other skips, or a directory one descends and
// the other does not, is a file that reaches the package by path in one of
// them silently, so the two encodings are asserted equal here.
func TestPackageImportPathsCheckSweepsTheSharedExtensionsAndSkipsTheSharedDirs(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	read := func(rel string) string {
		raw, readErr := os.ReadFile(filepath.Join(wd, rel))
		if readErr != nil {
			t.Fatalf("reading %s: %v", rel, readErr)
		}
		return string(raw)
	}
	tokens := func(source, pattern string) []string {
		match := regexp.MustCompile(pattern).FindStringSubmatch(source)
		if match == nil {
			t.Fatalf("no list found with %q", pattern)
		}
		found := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`).FindAllString(match[1], -1)
		sort.Strings(found)
		return found
	}
	gate := read(filepath.Join("scripts", "sdk", "package-import-paths-check.sh"))
	shared := read(filepath.Join("scripts", "sdk", "source-files.mjs"))

	// The shell writes extensions without the leading dot; the JS list has it.
	// Tokenizing to word characters drops the dot and the quotes from both.
	gateExts := tokens(gate, `(?m)^extensions=\(([^)]*)\)`)
	sharedExts := tokens(shared, `SOURCE_EXTENSIONS = \[([^\]]*)\]`)
	if !slices.Equal(gateExts, sharedExts) {
		t.Fatalf("the gate sweeps %v and the shared list is %v; they have to be the same extensions", gateExts, sharedExts)
	}

	gateDirs := tokens(gate, `(?m)^skip_dirs=\(([^)]*)\)`)
	sharedDirs := tokens(shared, `SKIPPED_DIRS = new Set\(\[([^\]]*)\]`)
	if !slices.Equal(gateDirs, sharedDirs) {
		t.Fatalf("the gate skips %v and the shared list is %v; they have to be the same directories", gateDirs, sharedDirs)
	}
}
