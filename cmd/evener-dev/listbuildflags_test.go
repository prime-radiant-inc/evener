package dev

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPackageSelectionFlagsForwardsWhatChangesTheTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
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
		// Everything after -args is the test binary's own argument list.
		{name: "-args ends the flags", args: []string{"-args", "foo", "-race"}},
		{name: "and what came before it still counts", args: []string{"-race", "-args", "-tags", "x"}, want: []string{"-race"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := packageSelectionFlags(tc.args); !reflect.DeepEqual(got, tc.want) {
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
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %q in the fixture: %v\n%s", args, err, out)
		}
		return string(out)
	}

	if got := list(); strings.Contains(got, "listfixture/tagged") {
		t.Fatalf("go list without the tag = %q, want the tagged package missing", got)
	}
	forwarded := packageSelectionFlags([]string{"-tags", "listfixture", "-short", "-count=1"})
	if got := list(forwarded...); !strings.Contains(got, "listfixture/tagged") {
		t.Fatalf("go list %q = %q, want the tagged package listed", forwarded, got)
	}
}
