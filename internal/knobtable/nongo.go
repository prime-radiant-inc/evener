package knobtable

// Non-Go consumers: a registered knob read only by a shell script, a Makefile,
// the frontend or another non-Go tree is still consumed. These are reference
// reads, not an interpreter: a mention in a comment counts, like a Go message
// or list that names a variable counts in the Go scan. A name computed from a
// runtime value is not found.

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// nonGoConsumerRoots are the trees a knob's non-Go consumer can live in: the
// gate and release scripts, the host installer, the hub frontend, the native
// frontend and its tooling, the docs site, the TypeScript client, and the dev
// tools.
var nonGoConsumerRoots = []string{
	"scripts",
	"make",
	"Makefile",
	"install.sh",
	"internal/remoteinstall/install.sh",
	"cmd/evener-hub/frontend",
	"mobile-native",
	"docs-site",
	"appwire-client/typescript",
	"tools",
}

// nonGoReadForms matches the ways the supported non-Go languages read a
// variable: $NAME / ${NAME} / $(NAME) in shell and Make, process.env.NAME and
// process.env["NAME"] in JS/TS, import.meta.env.NAME in Vite, Deno.env.get and
// Python's os.getenv/os.environ reads.
var nonGoReadForms = regexp.MustCompile(
	`\$[\{\(]?([A-Z][A-Z0-9_]*)\b` +
		`|process\.env\.([A-Z][A-Z0-9_]*)\b` +
		`|process\.env\[\s*["']([A-Z][A-Z0-9_]*)["']\s*\]` +
		`|import\.meta\.env\.([A-Z][A-Z0-9_]*)\b` +
		`|Deno\.env\.get\(\s*["']([A-Z][A-Z0-9_]*)["']\s*\)` +
		`|os\.(?:getenv|environ\.get)\(\s*["']([A-Z][A-Z0-9_]*)["']\s*\)` +
		`|os\.environ\[\s*["']([A-Z][A-Z0-9_]*)["']\s*\]`,
)

// nonGoEnvConsumers maps each registered variable name to the files outside Go
// that read it.
func nonGoEnvConsumers(root string, names map[string]bool) (map[string]map[string]bool, error) {
	paths, err := repoFiles(root, nonGoConsumerRoots...)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, rel := range paths {
		if !isNonGoConsumerFile(rel) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		for _, match := range nonGoReadForms.FindAllSubmatch(data, -1) {
			for _, capture := range match[1:] {
				if len(capture) == 0 {
					continue
				}
				name := string(capture)
				if names[name] {
					addTo(out, name, rel)
				}
			}
		}
	}
	return out, nil
}

// isNonGoConsumerFile keeps the scan to source files and out of tests, test
// support and testdata: a test is not a consumer, matching the Go scan.
func isNonGoConsumerFile(rel string) bool {
	base := path.Base(rel)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return false
	}
	for dir := range strings.SplitSeq(path.Dir(rel), "/") {
		if dir == "testdata" || dir == "testing" {
			return false
		}
	}
	switch path.Ext(rel) {
	case ".sh", ".bash", ".mjs", ".js", ".ts", ".tsx", ".py", ".mk":
		return true
	}
	return base == "Makefile" || base == "GNUmakefile"
}
