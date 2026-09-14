package dev

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureToolchainEnv is what the fixture modules are enumerated under: their
// own go.mod rather than this repo's workspace, and none of the developer's
// toolchain settings -- a GOFLAGS carrying -tags would answer the very
// question these fixtures ask.
func fixtureToolchainEnv() []string {
	return append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOENV=off")
}

func TestPackageSelectionFlagsForwardsWhatChangesTheTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
		// err is the substring a refusal must carry.
		err string
	}{
		{name: "nothing to forward", args: []string{"-short", "-count=1"}},
		{name: "the race tag selects files", args: []string{"-race", "-short"}, want: []string{"-race"}},
		{name: "and so do the other two sanitisers", args: []string{"-msan", "-asan"}, want: []string{"-msan", "-asan"}},
		{name: "tags in the two-argument form", args: []string{"-tags", "integration", "-short"}, want: []string{"-tags", "integration"}},
		{name: "tags written inline", args: []string{"--tags=integration"}, want: []string{"-tags=integration"}},
		{name: "the boolean spelling of a sanitiser", args: []string{"-race=true"}, want: []string{"-race=true"}},
		// The bug this table exists for: a regex whose text is -race is a
		// regex, and forwarding it would enumerate under a sanitiser nobody
		// asked for.
		{name: "a value is a value", args: []string{"-run", "-race", "-short"}},
		{name: "and the real one after it still counts", args: []string{"-run", "Test", "-race"}, want: []string{"-race"}},
		{name: "a build flag's value is skipped too", args: []string{"-ldflags", "-race", "-short"}},
		{name: "the module graph decides what resolves", args: []string{"-mod", "vendor"}, want: []string{"-mod", "vendor"}},
		{name: "and a different go.mod is a different graph", args: []string{"-modfile=alt.mod"}, want: []string{"-modfile=alt.mod"}},
		{name: "gc and gccgo are build tags of their own", args: []string{"-compiler=gccgo", "-short"}, want: []string{"-compiler=gccgo"}},
		{name: "an overlay can introduce a package", args: []string{"-overlay", "o.json"}, want: []string{"-overlay", "o.json"}},
		// Left out on purpose: these change how the same packages are built.
		{name: "the optimiser and the linker select nothing", args: []string{"-pgo=auto", "-trimpath", "-ldflags=-s", "-buildvcs=false"}},
		{name: "nor does coverage", args: []string{"-cover", "-covermode", "atomic", "-coverpkg", "./..."}},
		// Everything after -args is the test binary's own argument list.
		{name: "-args ends the flags", args: []string{"-args", "foo", "-race"}},
		// -C takes a directory: the word after it is that directory, whatever
		// it is spelled like.
		// -C is refused rather than consumed: the gate decides which directory
		// each module is enumerated in, and this would move one.
		{name: "-C is refused", args: []string{"-C", "/tmp"}, err: "-C is not supported here"},
		// The test binary's own spelling, consumed with its value like the
		// flag it is: -test.run is -run, and the word after it is a regex.
		{name: "the binary's spelling of a value flag", args: []string{"-test.run", "-race"}},
		{name: "and of one that selects packages", args: []string{"-test.short", "-race"}, want: []string{"-race"}},
		// -test.race is not -race: the binary has no such flag, so nothing is
		// stripped and nothing is forwarded.
		{name: "a -test. name the binary does not have", args: []string{"-test.race"}},
		{name: "a flag with nothing after it", args: []string{"-short", "-tags"}, err: "-tags was given with nothing after it"},
		{name: "and one whose value was the last word", args: []string{"-run"}, err: "-run was given with nothing after it"},
		{name: "and what came before it still counts", args: []string{"-race", "-args", "-tags", "x"}, want: []string{"-race"}},
		// -args takes no value of its own, so a trailing one is a valid end of
		// the flags rather than a flag missing its value.
		{name: "a trailing -args ends them too", args: []string{"-race", "-args"}, want: []string{"-race"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := packageSelectionFlags(tc.args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("packageSelectionFlags(%q) = %v, want a refusal naming %q", tc.args, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("packageSelectionFlags(%q) = %v", tc.args, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("packageSelectionFlags(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestListBuildFlagsPrintsOnePerLine(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := listBuildFlags([]string{"--", "-tags", "a b", "-race", "-short"}, &stdout, &stderr); code != 0 {
		t.Fatalf("listBuildFlags = %d, stderr = %q", code, stderr.String())
	}
	// One per line is what lets a shell read a value with a space in it.
	if got := stdout.String(); got != "-tags\na b\n-race\n" {
		t.Fatalf("stdout = %q", got)
	}
}

// TestPackageSelectionFlagsForwardAnOverlayThatIntroducesAPackage is the
// second selection mechanism: an overlay file can add a package that is not on
// disk at all, so an enumeration without the overlay cannot see it.
func TestPackageSelectionFlagsForwardAnOverlayThatIntroducesAPackage(t *testing.T) {
	module := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(module, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module overlayfixture\n\ngo 1.27.0\n")
	write("always/always.go", "package always\n")
	// The overlay's keys have to be the paths the toolchain will walk, which
	// on macOS means the resolved ones: /var/folders is a symlink to /private.
	resolved, err := filepath.EvalSymlinks(module)
	if err != nil {
		t.Fatalf("resolving the fixture module: %v", err)
	}
	// The added package exists only in the overlay: nothing is written under
	// the module for it.
	added := filepath.Join(t.TempDir(), "added.go")
	if err := os.WriteFile(added, []byte("package added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(t.TempDir(), "overlay.json")
	if err := os.WriteFile(overlay, fmt.Appendf(nil,
		`{"Replace":{%q:%q}}`, filepath.Join(resolved, "added", "added.go"), added), 0o644); err != nil {
		t.Fatal(err)
	}

	list := func(extra ...string) string {
		t.Helper()
		args := append([]string{"list"}, extra...)
		args = append(args, "./...")
		cmd := exec.Command("go", args...)
		cmd.Dir = resolved
		cmd.Env = fixtureToolchainEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %q in the fixture: %v\n%s", args, err, out)
		}
		return string(out)
	}

	if got := list(); strings.Contains(got, "overlayfixture/added") {
		t.Fatalf("go list without the overlay = %q, want the added package missing", got)
	}
	forwarded, err := packageSelectionFlags([]string{"-overlay", overlay, "-count=1"})
	if err != nil {
		t.Fatalf("packageSelectionFlags: %v", err)
	}
	if got := list(forwarded...); !strings.Contains(got, "overlayfixture/added") {
		t.Fatalf("go list %q = %q, want the overlay's package listed", forwarded, got)
	}
}

// TestPackageSelectionFlagsDecideWhatGoListCanSee is the reason for all of it:
// a package that exists only behind a build tag is invisible to an enumeration
// that was not given the tag, so the gate would hand `go test` a list with the
// package missing and report a pass.
func TestPackageSelectionFlagsDecideWhatGoListCanSee(t *testing.T) {
	module := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(module, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module listfixture\n\ngo 1.27.0\n")
	write("always/always.go", "package always\n")
	// Every file in this package is behind the tag, so the package itself is
	// there or not there depending on the enumeration's flags.
	write("tagged/tagged.go", "//go:build listfixture\n\npackage tagged\n")

	list := func(extra ...string) string {
		t.Helper()
		args := append([]string{"list"}, extra...)
		args = append(args, "./...")
		cmd := exec.Command("go", args...)
		cmd.Dir = module
		cmd.Env = fixtureToolchainEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %q in the fixture: %v\n%s", args, err, out)
		}
		return string(out)
	}

	if got := list(); strings.Contains(got, "listfixture/tagged") {
		t.Fatalf("go list without the tag = %q, want the tagged package missing", got)
	}
	forwarded, err := packageSelectionFlags([]string{"-tags", "listfixture", "-short", "-count=1"})
	if err != nil {
		t.Fatalf("packageSelectionFlags: %v", err)
	}
	if got := list(forwarded...); !strings.Contains(got, "listfixture/tagged") {
		t.Fatalf("go list %q = %q, want the tagged package listed", forwarded, got)
	}
}

// refusingWriter fails on the first write, which is what a closed pipe or a
// full disk looks like to a subcommand handing over its answer.
type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errors.New("the pipe is gone") }

func TestListBuildFlagsFailsWhenItsAnswerCannotBeDelivered(t *testing.T) {
	var stderr bytes.Buffer
	code := listBuildFlags([]string{"--", "-race", "-short"}, refusingWriter{}, &stderr)
	if code == 0 {
		t.Fatalf("exit code = 0 after a write that failed; stderr = %q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "list-build-flags: -race: wrote 0 of") {
		t.Fatalf("stderr = %q, want the flag that could not be written and how much of it landed", got)
	}
}

// TestEveryBuildFlagIsAccountedFor closes the class the tables above are one
// answer to. It reads `go help build` from the toolchain in use and fails when
// a flag that takes a value is not consumed with its value -- the `-run -race`
// family of bugs -- or when a flag that changes what `go list` selects is not
// forwarded.
//
// The selection list is written here rather than derived, because "changes
// which packages or files exist" is a judgement about each flag and not
// something the help text says. A flag a later toolchain adds fails this test
// until someone makes that judgement: -workfile and -godebug are the two go
// has grown recently, and neither exists on go1.27.0 -- `go list
// -workfile=...` answers `flag provided but not defined`.
func TestEveryBuildFlagIsAccountedFor(t *testing.T) {
	t.Parallel()
	// Every build flag whose value decides which packages or files exist.
	selects := map[string]bool{
		"-tags": true, "-overlay": true, "-mod": true, "-modfile": true,
		"-compiler": true, "-race": true, "-msan": true, "-asan": true,
	}
	// Exactly the predicate walkFlags uses, and nothing beside it: a second
	// term here would let a flag count as accounted for in this test while the
	// walk still read the word after it as a flag of its own.
	consumed := valueIsNextArgument
	forwarded := func(name string) bool {
		return packageSelectionValueFlags[name] || packageSelectionBareFlags[name]
	}

	seen := 0
	for _, page := range []string{"build", "testflag"} {
		seen += checkDocumentedFlags(t, page, selects, consumed, forwarded)
	}
	if seen == 0 {
		t.Fatal("go help listed no flags; the parser is broken, not the toolchain")
	}
}

// checkDocumentedFlags reads one `go help` page and reports how many flags it
// found, failing for any the tables do not account for.
func checkDocumentedFlags(t *testing.T, page string, selects map[string]bool, consumed, forwarded func(string) bool) int {
	t.Helper()
	cmd := exec.Command("go", "help", page)
	cmd.Env = fixtureToolchainEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go help %s: %v", page, err)
	}
	seen := 0
	for line := range strings.Lines(string(out)) {
		if !strings.HasPrefix(line, "\t") {
			continue
		}
		field := strings.Fields(line)
		if len(field) == 0 || !strings.HasPrefix(field[0], "-") || strings.Contains(field[0], "=") {
			continue // prose, or a spelling with its value written in
		}
		name, _, _ := strings.Cut(goFlag(field[0]), "=")
		takesValue := len(field) > 1
		seen++
		if takesValue && !consumed(name) {
			t.Errorf("go help %s: %s takes a value and no table consumes it: the word after it would be read as a flag of its own", page, name)
		}
		if selects[name] && !forwarded(name) {
			t.Errorf("go help %s: %s changes what `go list` selects and is not forwarded: the enumeration would see a different tree from the one the tests are built for", page, name)
		}
	}
	return seen
}
