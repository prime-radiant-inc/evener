//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyRuntimePairRefusesUnsafePairs drives the guard run-ui-tests.sh
// calls before it copies a pair the workflow's setup job built: a pair from
// another revision, a manifest that omits a binary, or bytes that changed after
// the manifest was written must be refused, and a matching pair accepted.
func TestVerifyRuntimePairRefusesUnsafePairs(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq", "shasum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	script, err := filepath.Abs("verify-runtime-pair.sh")
	if err != nil {
		t.Fatal(err)
	}

	const revision = "0123456789abcdef0123456789abcdef01234567"
	const otherRevision = "fedcba9876543210fedcba9876543210fedcba98"

	writePair := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"evener", "fixture-provider"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		writeManifest(t, dir)
		writeFile(t, filepath.Join(dir, "revision"), revision+"\n")
	}
	// newPair writes the directory the setup job uploads and the identity the
	// qualification runner records for the run.
	newPair := func(t *testing.T) (dir, identity string) {
		t.Helper()
		dir = t.TempDir()
		writePair(t, dir)
		identity = filepath.Join(t.TempDir(), "identity.json")
		writeFile(t, identity, fmt.Sprintf("{\"sourceRevision\":%q}\n", revision))
		return dir, identity
	}
	refused := func(t *testing.T, dir, identity string) {
		t.Helper()
		out, err := exec.Command("bash", script, dir, identity).CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("verify-runtime-pair.sh exit = %v, want exit 1; output:\n%s", err, out)
		}
	}

	t.Run("matching pair accepted", func(t *testing.T) {
		t.Parallel()
		dir, identity := newPair(t)
		out, err := exec.Command("bash", script, dir, identity).CombinedOutput()
		if err != nil {
			t.Fatalf("verify-runtime-pair.sh refused a matching pair: %v\n%s", err, out)
		}
	})
	t.Run("revision mismatch refused", func(t *testing.T) {
		t.Parallel()
		dir, identity := newPair(t)
		writeFile(t, filepath.Join(dir, "revision"), otherRevision+"\n")
		refused(t, dir, identity)
	})
	t.Run("identity without a revision refused", func(t *testing.T) {
		t.Parallel()
		dir, identity := newPair(t)
		writeFile(t, identity, "{}\n")
		refused(t, dir, identity)
	})
	t.Run("manifest entry missing refused", func(t *testing.T) {
		t.Parallel()
		dir, identity := newPair(t)
		manifest, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		for _, line := range strings.Split(string(manifest), "\n") {
			if strings.HasSuffix(line, "fixture-provider") {
				continue
			}
			kept = append(kept, line)
		}
		writeFile(t, filepath.Join(dir, "SHA256SUMS"), strings.Join(kept, "\n"))
		refused(t, dir, identity)
	})
	t.Run("changed bytes refused", func(t *testing.T) {
		t.Parallel()
		dir, identity := newPair(t)
		writeFile(t, filepath.Join(dir, "fixture-provider"), "#!/bin/sh\nexit 1\n")
		refused(t, dir, identity)
	})
	t.Run("missing manifest refused", func(t *testing.T) {
		t.Parallel()
		dir, identity := newPair(t)
		if err := os.Remove(filepath.Join(dir, "SHA256SUMS")); err != nil {
			t.Fatal(err)
		}
		refused(t, dir, identity)
	})
	t.Run("relative pair is not resolved through CDPATH", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cwd := filepath.Join(root, "cwd")
		writePair(t, filepath.Join(cwd, "pair"))
		decoy := filepath.Join(root, "decoy")
		writePair(t, filepath.Join(decoy, "pair"))
		// The real pair's bytes no longer match its manifest; the decoy's do.
		writeFile(t, filepath.Join(cwd, "pair", "fixture-provider"), "#!/bin/sh\nexit 1\n")
		identity := filepath.Join(root, "identity.json")
		writeFile(t, identity, fmt.Sprintf("{\"sourceRevision\":%q}\n", revision))
		cmd := exec.Command("bash", script, "pair", identity)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "CDPATH="+decoy)
		out, err := cmd.CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("a relative pair resolved through CDPATH instead of the caller's directory: exit = %v; output:\n%s", err, out)
		}
	})
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunUITestsVerifiesThePairBeforeUse pins the call site: the journeys
// runner must verify a supplied pair before copying either binary out of it,
// or the guard below is dead code and the pair runs unverified.
func TestRunUITestsVerifiesThePairBeforeUse(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile(filepath.Join("..", "run-ui-tests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	verify := strings.Index(text, "qualification/verify-runtime-pair.sh")
	copied := strings.Index(text, `cp "$runtime_pair/evener" "$evener_binary"`)
	if verify == -1 || copied == -1 || verify > copied {
		t.Fatalf("run-ui-tests.sh verify call at %d, copy at %d; want the verifier called before either binary is copied", verify, copied)
	}
}

// writeManifest records the two binaries' checksums the way the workflow's
// packaging step does.
func writeManifest(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("shasum", "-a", "256", "evener", "fixture-provider")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("shasum: %v", err)
	}
	writeFile(t, filepath.Join(dir, "SHA256SUMS"), string(out))
}
