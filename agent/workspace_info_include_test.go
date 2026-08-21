package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseMakefileTargetsFollowsIncludes pins the split-Makefile shape: rules
// live in make/*.mk and the root holds only variables and the include line. A
// parser that reads the root alone returns variable names here, which is worse
// than returning nothing — the caller checks len(targets) > 0 and publishes
// the garbage.
func TestParseMakefileTargetsFollowsIncludes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "make"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Makefile", "LDFLAGS := -X main.X=1\nGO_MODULES := . agent\n\n.DEFAULT_GOAL := build\n\ninclude $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk\n")
	write("make/building.mk", ".PHONY: build\n\nbuild:\n\t@echo build\n")
	write("make/testing.mk", ".PHONY: test vet\n\ntest:\n\t@echo test\n\nvet:\n\t@echo vet\n")

	got := parseMakefileTargets(filepath.Join(root, "Makefile"))

	for _, want := range []string{"build", "test", "vet"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing target %q; got %v", want, got)
		}
	}
	for _, unwanted := range []string{"LDFLAGS", "GO_MODULES"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("variable %q reported as a target; got %v", unwanted, got)
		}
	}
}

// TestParseMakefileTargetsSkipsPlainEqualsAssignment pins the missing
// operator in makefileAssignment: it matched ":=", "::=", "+=" and "?=" but
// not a plain "=" (GNU make's recursive assignment). A plain-"=" line whose
// value contains a colon is then indistinguishable from a rule declaration
// — the assignment check doesn't recognize it as an assignment, so the
// target-line check finds the first ":" in the value and mines bogus
// targets out of it. make/fuzzing.mk's `FUZZ_SEED_REPLAY = for m in …`
// escaped this only because its value happens to hold no colon; this
// fixture's value does.
func TestParseMakefileTargetsSkipsPlainEqualsAssignment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "Makefile"),
		[]byte("REPLAY = for m in $(mods); do sed 's:a:b:' \"$$m\"; done\n\nbuild:\n\t@echo build\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	got := parseMakefileTargets(filepath.Join(root, "Makefile"))

	if !slices.Contains(got, "build") {
		t.Errorf("missing target %q; got %v", "build", got)
	}
	for _, unwanted := range []string{"REPLAY", "=", "for", "m", "in", "do", "sed"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("bogus target %q mined from the plain-\"=\" assignment's value; got %v", unwanted, got)
		}
	}
}
