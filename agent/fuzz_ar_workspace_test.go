//go:build serffuzz

package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This file fuzzes the build-system extractors in workspace_info.go:
//
//   - parseMakefileTargets: scans a Makefile and returns its named targets.
//   - parsePackageJsonScripts: decodes a package.json and returns its script names.
//
// Both read a real file, so the fuzzed content is written into a t.TempDir
// sandbox (never the real workspace). They transform arbitrary file bytes into a
// small, well-formed list of names and must never panic, must be deterministic,
// and must honour the invariants their callers rely on (non-empty, de-duplicated,
// sorted where documented).

// FuzzArParseMakefileTargets drives parseMakefileTargets over fuzzed Makefile
// bytes. Oracles beyond never-panic:
//
//   - WELL-FORMED NAMES: every returned target is non-empty, contains no
//     whitespace (it comes from strings.Fields of the pre-colon text) and no '%'
//     (a pattern-rule line is skipped wholesale). NOTE: the "special target" dot
//     skip is applied to the whole pre-colon text, not per field, so a dot-target
//     that is not the first field can legitimately appear — this oracle does not
//     assert its absence.
//   - DEDUP: no target appears twice.
//   - DETERMINISM.
func FuzzArParseMakefileTargets(f *testing.F) {
	seeds := []string{
		"",
		"all: build test\n\techo hi\n",
		"build:\n\tgo build\ntest:\n\tgo test\n",
		"# comment\nVAR = value\nall: deps\n",
		".PHONY: all\n%.o: %.c\n\tcc\n",
		"a b c: dep\n",
		"weird::: colons\n",
		strings.Repeat("t: dep\n", 5000),
		"no colon here\njust text\n",
		"\x00\x01binary\x00:\ttarget\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), "Makefile")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write Makefile: %v", err)
		}

		targets := parseMakefileTargets(path)

		seen := map[string]bool{}
		for _, tg := range targets {
			if tg == "" {
				t.Fatalf("parseMakefileTargets returned an empty target")
			}
			if strings.ContainsAny(tg, " \t\n") {
				t.Fatalf("parseMakefileTargets returned a target with whitespace: %q", tg)
			}
			if strings.Contains(tg, "%") {
				t.Fatalf("parseMakefileTargets returned a pattern rule: %q", tg)
			}
			if seen[tg] {
				t.Fatalf("parseMakefileTargets returned duplicate target: %q", tg)
			}
			seen[tg] = true
		}

		// DETERMINISM (re-parse the same file).
		again := parseMakefileTargets(path)
		if len(again) != len(targets) {
			t.Fatalf("parseMakefileTargets non-deterministic length: %d vs %d", len(again), len(targets))
		}
		for i := range targets {
			if again[i] != targets[i] {
				t.Fatalf("parseMakefileTargets non-deterministic at %d: %q vs %q", i, again[i], targets[i])
			}
		}
	})
}

// FuzzArParsePackageJsonScripts drives parsePackageJsonScripts over fuzzed
// package.json bytes. Oracles beyond never-panic:
//
//   - SORTED + UNIQUE: the returned script names are in ascending order with no
//     duplicates (the function sorts a map's keys).
//   - DETERMINISM: map iteration order must not leak — two parses agree exactly.
func FuzzArParsePackageJsonScripts(f *testing.F) {
	seeds := []string{
		"",
		`{"scripts":{"build":"tsc","test":"jest","lint":"eslint"}}`,
		`{"scripts":{}}`,
		`{"name":"pkg","version":"1.0.0"}`,
		`not json`,
		`{"scripts":{"z":"1","a":"2","m":"3"}}`,
		`{"scripts":{"dup":"x","dup":"y"}}`, // JSON with a repeated key
		`{"scripts":123}`,                   // wrong type
		`{"scripts":{"🚀":"emoji","tab\ttab":"x"}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), "package.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write package.json: %v", err)
		}

		scripts := parsePackageJsonScripts(path)

		if !sort.StringsAreSorted(scripts) {
			t.Fatalf("parsePackageJsonScripts returned unsorted output: %#v", scripts)
		}
		for i := 1; i < len(scripts); i++ {
			if scripts[i] == scripts[i-1] {
				t.Fatalf("parsePackageJsonScripts returned duplicate: %q", scripts[i])
			}
		}

		// DETERMINISM — the sort must make map iteration order irrelevant.
		again := parsePackageJsonScripts(path)
		if strings.Join(again, "\x00") != strings.Join(scripts, "\x00") {
			t.Fatalf("parsePackageJsonScripts non-deterministic:\n a=%#v\n b=%#v", scripts, again)
		}
	})
}