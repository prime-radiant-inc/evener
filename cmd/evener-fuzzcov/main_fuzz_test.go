package fuzzcov

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// gapInputSeeds are both the fuzz corpus seeds and the deterministic cases
// TestGapInputsOracles pins. The last two name the regression scenarios #2372
// calls out: a coverpkg-bearing registry and a registry whose lone target
// leaves a universe entry uncovered.
var gapInputSeeds = []struct{ name, registry, ignore string }{
	{"valid ignore", "native:llm:.:FuzzParseSSE", "example.com/x  # reason"},
	{"reasonless ignore", "rapid:.:./internal/appserver:TestRouterSeqFuzz", "example.com/y"},
	{"header only", "# comment\n\nnative:agent:.:FuzzToolArgsValidate:./internal/tool,.", "# header only"},
	{"registry coverpkg", "native:agent:.:FuzzToolArgsValidate:./internal/tool,.", "example.com/x  # reason"},
	{"uncovered universe entry", "native:.:./parser:FuzzParse", "example.com/x  # reason"},
}

// fuzzModulePaths is the fixed module-path map the fuzz oracle resolves registry
// targets against.
var fuzzModulePaths = map[string]string{".": "m", "agent": "m/agent", "llm": "m/llm"}

// FuzzGapInputs drives the two file parsers the gap gate trusts with arbitrary
// content, then round-trips every accepted parse through the consumers the gate
// relies on: each registry target must reach staticFuzzedPackages as its
// expected import path, and the ignore map must clear gapMap. A no-op parser or
// a gap calculation that drops a cleared package reddens this target rather than
// passing merely because nothing panicked.
func FuzzGapInputs(f *testing.F) {
	for _, s := range gapInputSeeds {
		f.Add(s.registry, s.ignore)
	}
	f.Fuzz(func(t *testing.T, registry, ignore string) {
		if len(registry)+len(ignore) > 8192 {
			return
		}
		checkGapInputs(t, registry, ignore)
	})
}

// TestGapInputsOracles pins the parser/consumer oracles the fuzz target enforces
// on its named seeds: exact parser errors/maps and the gap status gapMap derives
// from those outputs.
func TestGapInputsOracles(t *testing.T) {
	for _, s := range gapInputSeeds {
		t.Run(s.name, func(t *testing.T) {
			checkGapInputs(t, s.registry, s.ignore)
		})
	}
}

// TestGapInputsOracleRejectsNoOpParsers mutation-tests the ignored-result paths:
// a parser regressed to a no-op must be reported, not silently accepted.
func TestGapInputsOracleRejectsNoOpParsers(t *testing.T) {
	if msg := ignoreOracle(map[string]bool{}, nil, "example.com/x  # reason"); msg == "" {
		t.Fatal("ignore oracle accepted a no-op parser")
	}
	if msg := registryOracle(nil, nil, "native:llm:.:FuzzParseSSE"); msg == "" {
		t.Fatal("registry oracle accepted a no-op parser")
	}
}

// checkGapInputs asserts the oracles FuzzGapInputs advertises for one
// (registry, ignore) input.
func checkGapInputs(t *testing.T, registry, ignore string) {
	t.Helper()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.txt")
	ignPath := filepath.Join(dir, "ignore.txt")
	if err := os.WriteFile(regPath, []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignPath, []byte(ignore), 0o600); err != nil {
		t.Fatal(err)
	}

	gotIgnore, ignErr := readIgnore(ignPath)
	if msg := ignoreOracle(gotIgnore, ignErr, ignore); msg != "" {
		t.Fatal(msg)
	}

	gotTargets, regErr := readRegistry(regPath)
	if msg := registryOracle(gotTargets, regErr, registry); msg != "" {
		t.Fatal(msg)
	}
	if regErr != nil {
		return
	}

	fuzzed := staticFuzzedPackages(gotTargets, fuzzModulePaths)
	if want := modelFuzzedPackages(gotTargets); !reflect.DeepEqual(fuzzed, want) {
		t.Fatalf("staticFuzzedPackages = %v, want %v", fuzzed, want)
	}
	if msg := gapRoundTripOracle(fuzzed, gotIgnore); msg != "" {
		t.Fatal(msg)
	}
}

// ignoreOracle returns a mismatch message when a readIgnore result disagrees
// with modelIgnore. A no-op parser returns an empty map and a nil error, which
// disagrees with any non-empty valid input.
func ignoreOracle(parsed map[string]bool, err error, src string) string {
	want, ok := modelIgnore(src)
	if (err == nil) != ok {
		return fmt.Sprintf("readIgnore err = %v, want accepted=%v for %q", err, ok, src)
	}
	if ok && !reflect.DeepEqual(parsed, want) {
		return fmt.Sprintf("readIgnore = %v, want %v for %q", parsed, want, src)
	}
	return ""
}

// registryOracle returns a mismatch message when a readRegistry result disagrees
// with modelRegistry.
func registryOracle(parsed []target, err error, src string) string {
	want, ok := modelRegistry(src)
	if (err == nil) != ok {
		return fmt.Sprintf("readRegistry err = %v, want accepted=%v for %q", err, ok, src)
	}
	if ok && !reflect.DeepEqual(parsed, want) {
		return fmt.Sprintf("readRegistry = %+v, want %+v for %q", parsed, want, src)
	}
	return ""
}

// gapRoundTripOracle feeds parsed fuzzed/ignore sets through gapMap over a
// universe built from them plus one deliberately uncovered entry, and returns a
// mismatch message unless that entry is the only gap.
func gapRoundTripOracle(fuzzed, ignore map[string]bool) string {
	const uncovered = "m/__deliberately_uncovered__"
	universe := map[string]string{}
	for imp := range fuzzed {
		universe[imp] = "fuzzed"
	}
	for imp := range ignore {
		universe[imp] = "ignored"
	}
	universe[uncovered] = "gap"
	gaps := gapMap(universe, fuzzed, ignore)
	if len(gaps) != 1 || gaps[0][0] != uncovered {
		return fmt.Sprintf("gapMap = %v, want only %q (universe=%v fuzzed=%v ignore=%v)", gaps, uncovered, universe, fuzzed, ignore)
	}
	return ""
}

// modelFuzzedPackages is the oracle's expected package set: each parsed target
// contributes its coverpkg (or pkg) resolved against its module import path. It
// is deliberately separate from staticFuzzedPackages' aggregation, so a target
// the production path drops or mis-maps turns the fuzz target red.
func modelFuzzedPackages(targets []target) map[string]bool {
	out := map[string]bool{}
	for _, tg := range targets {
		modulePath := fuzzModulePaths[tg.module]
		if modulePath == "" {
			continue
		}
		claimed := tg.coverpkg
		if claimed == "" {
			claimed = tg.pkg
		}
		for part := range strings.SplitSeq(claimed, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out[joinImport(modulePath, pkgSubdir(part))] = true
		}
	}
	return out
}

// modelIgnore is an independent reference for the documented ignore-list format:
// non-blank, non-comment lines must be "<import-path>  # <reason>". ok is false
// when any entry is malformed.
func modelIgnore(src string) (map[string]bool, bool) {
	out := map[string]bool{}
	ok := true
	for raw := range strings.SplitSeq(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		imp, reason, found := strings.Cut(line, "#")
		imp = strings.TrimSpace(imp)
		reason = strings.TrimSpace(reason)
		if !found || imp == "" || reason == "" {
			ok = false
			continue
		}
		out[imp] = true
	}
	return out, ok
}

// modelRegistry is an independent reference for the documented registry format:
// non-blank, non-comment lines must be "tag:module:pkg:name[:coverpkg]". ok is
// false when any entry has fewer than four fields.
func modelRegistry(src string) ([]target, bool) {
	var out []target
	for raw := range strings.SplitSeq(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			return nil, false
		}
		tg := target{tag: fields[0], module: fields[1], pkg: fields[2], name: fields[3]}
		if len(fields) > 4 {
			tg.coverpkg = fields[4]
		}
		out = append(out, tg)
	}
	return out, true
}
