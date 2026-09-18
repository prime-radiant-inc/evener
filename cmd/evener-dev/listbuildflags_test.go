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

// fixtureEnv is the environment every fixture command runs with: the ambient
// GOFLAGS and GOENV are cleared so a developer's own settings cannot change
// which tree the fixture's `go list` sees.
func fixtureEnv() []string {
	return append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOENV=off")
}

func TestListBuildFlagsIsARegisteredSubcommand(t *testing.T) {
	if subcommands["list-build-flags"] == nil {
		t.Fatal("list-build-flags is not registered; `go run ./cmd/evener-dev/bin dev list-build-flags` would be unknown to the gate")
	}
}

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
		// The flags that change the module graph or the file set move with
		// their value, so the enumeration resolves the same tree.
		{name: "an overlay moves with its file", args: []string{"-overlay", "o.json"}, want: []string{"-overlay", "o.json"}},
		{name: "and so do the module flags", args: []string{"-mod", "mod", "-modfile", "alt.mod"}, want: []string{"-mod", "mod", "-modfile", "alt.mod"}},
		{name: "and the compiler", args: []string{"--compiler=gccgo"}, want: []string{"-compiler=gccgo"}},
		// -C cannot be applied to an enumeration the gate anchors to the
		// module's own directory, so it is dropped -- but its value is still
		// consumed, not read as a flag.
		{name: "-C is not forwarded", args: []string{"-C", "somewhere"}},
		// The bug this table exists for: a regex whose text is -race is a
		// regex, and forwarding it would enumerate under a sanitiser nobody
		// asked for.
		{name: "a value is a value", args: []string{"-run", "-race", "-short"}},
		{name: "and the real one after it still counts", args: []string{"-run", "Test", "-race"}, want: []string{"-race"}},
		{name: "a build flag's value is skipped too", args: []string{"-ldflags", "-race", "-short"}},
		// The test binary's own spelling is normalized first, so its value is
		// consumed from the same walk the shard runner uses.
		{name: "the test-side spelling of run", args: []string{"-test.run", "-race", "-short"}},
		{name: "and its inline form still forwards a real flag", args: []string{"-test.run=foo", "-race"}, want: []string{"-race"}},
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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write refused") }

// TestListBuildFlagsFailsOnAShortWrite pins the guard: a truncated flag list
// would make the gate enumerate under flags the caller never set, so the
// subcommand must not report success when its output cannot be delivered.
func TestListBuildFlagsFailsOnAShortWrite(t *testing.T) {
	var stderr bytes.Buffer
	if code := listBuildFlags([]string{"--", "-race"}, failingWriter{}, &stderr); code == 0 {
		t.Fatalf("listBuildFlags with a failing writer = 0, want nonzero")
	}
	if !strings.Contains(stderr.String(), "writing flags") {
		t.Fatalf("stderr = %q, want a write diagnostic", stderr.String())
	}
}

// writeFixture lays out a module in a temp dir and returns the dir.
func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	module := t.TempDir()
	for name, body := range files {
		path := filepath.Join(module, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return module
}

// TestPackageSelectionFlagsDecideWhatGoListCanSee is the reason for all of it:
// a package that exists only behind a build tag is invisible to an enumeration
// that was not given the tag, so the gate would hand `go test` a list with the
// package missing and report a pass.
func TestPackageSelectionFlagsDecideWhatGoListCanSee(t *testing.T) {
	module := writeFixture(t, map[string]string{
		"go.mod":           "module listfixture\n\ngo 1.27.0\n",
		"always/always.go": "package always\n",
		// Every file in this package is behind the tag, so the package itself
		// is there or not there depending on the enumeration's flags.
		"tagged/tagged.go": "//go:build listfixture\n\npackage tagged\n",
	})
	list := func(extra ...string) string {
		t.Helper()
		args := append([]string{"list"}, extra...)
		args = append(args, "./...")
		cmd := exec.Command("go", args...)
		cmd.Dir = module
		cmd.Env = fixtureEnv()
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

// TestPackageSelectionFlagsEnumerateWhatGoTestBuilds proves the contract for
// the flags that are neither tags nor sanitisers: an -overlay can introduce a
// package, and an enumeration that does not carry it hands `go test` a list
// with that package missing. The test asserts the forwarded enumeration and
// `go test` agree on the package set, not merely that the flag was passed.
func TestPackageSelectionFlagsEnumerateWhatGoTestBuilds(t *testing.T) {
	module := writeFixture(t, map[string]string{
		"go.mod":                "module ovfixture\n\ngo 1.27.0\n",
		"always/always.go":      "package always\n",
		"maybe/placeholder.txt": "not go source\n",
		// A Go source file that is not itself a package (its name is not .go),
		// used as the overlay's replacement for maybe/o.go.
		"source.txt": "package maybe\n",
	})
	overlay := filepath.Join(module, "overlay.json")
	overlayBody := fmt.Sprintf(`{"Replace": {%q: %q}}`+"\n",
		filepath.Join(module, "maybe", "o.go"), filepath.Join(module, "source.txt"))
	if err := os.WriteFile(overlay, []byte(overlayBody), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir = module
		cmd.Env = fixtureEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %q in the fixture: %v\n%s", args, err, out)
		}
		return string(out)
	}

	if got := run("list", "./..."); strings.Contains(got, "ovfixture/maybe") {
		t.Fatalf("plain go list = %q, want the overlay-only package missing", got)
	}
	forwarded := packageSelectionFlags([]string{"-overlay", overlay, "-short", "-count=1"})
	listed := run(append([]string{"list"}, append(forwarded, "./...")...)...)
	if !strings.Contains(listed, "ovfixture/maybe") {
		t.Fatalf("go list %q = %q, want the overlay-only package listed", forwarded, listed)
	}
	// The same tree `go test` builds: it must see the package the enumeration
	// now lists, or the two disagree on what the gate covers.
	tested := run("test", "-overlay", overlay, "-list", ".*", "./...")
	if !strings.Contains(tested, "ovfixture/maybe") {
		t.Fatalf("go test -overlay sees %q, want the package the enumeration listed", tested)
	}
}
