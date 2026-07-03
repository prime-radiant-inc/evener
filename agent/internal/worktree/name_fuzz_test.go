package worktree

import (
	"os/exec"
	"strings"
	"testing"
)

// FuzzValidateName drives ValidateName with arbitrary strings. Two
// invariants hold regardless of input: ValidateName never panics, and any
// name it accepts is also accepted by the real `git check-ref-format
// --branch <name>` (the source of truth the create path itself consults,
// spec §2) whenever git is on PATH. The git oracle is skipped under
// testing.Short() so short/CI-fast runs don't pay for a subprocess per
// input; the fuzz engine (-fuzz=...) does not set -short, so exploratory
// fuzzing still gets the oracle. The name is always passed as a single argv
// element to exec.Command — never through a shell — so no fuzz input can
// escape into shell syntax.
func FuzzValidateName(f *testing.F) {
	seeds := []string{
		"feature/foo",
		"dlg_01HXYZ",
		"my_feature",
		"a.json/b",
		"..",
		"-x",
		"a/",
		"a@{1}",
		"a b",
		"",
		".",
		"a//b",
		"a.lock",
		"a/.b",
		"foo.",
		strings.Repeat("a", 101),
		strings.Repeat("a", 100),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	gitPath, gitErr := exec.LookPath("git")

	f.Fuzz(func(t *testing.T, name string) {
		err := ValidateName(name) // must not panic regardless of err
		if err != nil {
			return
		}
		if testing.Short() || gitErr != nil {
			return
		}
		out, cmdErr := exec.Command(gitPath, "check-ref-format", "--branch", name).CombinedOutput()
		if cmdErr != nil {
			t.Fatalf("ValidateName accepted %q but git check-ref-format --branch rejected it: %v\n%s", name, cmdErr, out)
		}
	})
}

// FuzzSidecarNameRoundtrip checks that, for every name ValidateName accepts,
// EncodeSidecarName produces a "/"-free string and DecodeSidecarName
// reverses it exactly (spec §6 sidecar encoding).
func FuzzSidecarNameRoundtrip(f *testing.F) {
	seeds := []string{
		"feature/foo",
		"dlg_01HXYZ",
		"my_feature",
		"a.json/b",
		"a",
		"a/b/c",
		"a%b",
		"a%2Fb",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		if ValidateName(name) != nil {
			return
		}
		encoded := EncodeSidecarName(name)
		if strings.Contains(encoded, "/") {
			t.Fatalf("EncodeSidecarName(%q) = %q still contains a slash", name, encoded)
		}
		decoded, ok := DecodeSidecarName(encoded)
		if !ok {
			t.Fatalf("DecodeSidecarName(%q) (from %q) ok=false, want true", encoded, name)
		}
		if decoded != name {
			t.Fatalf("round trip mismatch: encode(%q) = %q, decode -> %q", name, encoded, decoded)
		}
	})
}
