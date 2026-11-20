//go:build linux || darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pairRevision = "0123456789abcdef0123456789abcdef01234567"

// TestInstallRuntimePairRefusesUnsafePairs drives the code path
// run-ui-tests.sh uses when the workflow supplies a pair: a pair from another
// revision, a manifest that omits a binary, or bytes that changed after the
// manifest was written must be refused without copying either binary, and a
// matching pair must be installed into the run directory.
func TestInstallRuntimePairRefusesUnsafePairs(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq", "shasum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	script, err := filepath.Abs("install-runtime-pair.sh")
	if err != nil {
		t.Fatal(err)
	}

	writePair := func(t *testing.T, dir, revision string) {
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
	writeIdentity := func(t *testing.T, revision string) string {
		t.Helper()
		identity := filepath.Join(t.TempDir(), "identity.json")
		writeFile(t, identity, fmt.Sprintf("{\"sourceRevision\":%q}\n", revision))
		return identity
	}
	newFixture := func(t *testing.T) (pair, identity, dest string) {
		t.Helper()
		pair = t.TempDir()
		writePair(t, pair, pairRevision)
		return pair, writeIdentity(t, pairRevision), t.TempDir()
	}
	run := func(t *testing.T, dir string, env []string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	refused := func(t *testing.T, out string, err error, dest string) {
		t.Helper()
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("install-runtime-pair.sh exit = %v, want exit 1; output:\n%s", err, out)
		}
		for _, name := range []string{"evener", "fixture-provider"} {
			if _, err := os.Stat(filepath.Join(dest, name)); err == nil {
				t.Errorf("a refused pair still copied %s into the run directory", name)
			}
		}
	}

	t.Run("matching pair installed", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		out, err := run(t, "", nil, pair, identity, dest)
		if err != nil {
			t.Fatalf("install-runtime-pair.sh refused a matching pair: %v\n%s", err, out)
		}
		for _, name := range []string{"evener", "fixture-provider"} {
			installed := filepath.Join(dest, name)
			info, err := os.Stat(installed)
			if err != nil {
				t.Fatalf("%s was not copied into the run directory: %v", name, err)
			}
			if info.Mode()&0o111 == 0 {
				t.Errorf("%s is not executable", installed)
			}
			want, err := os.ReadFile(filepath.Join(pair, name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(installed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s in the run directory differs from the verified pair", name)
			}
		}
	})
	t.Run("revision mismatch refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		writeFile(t, filepath.Join(pair, "revision"), "fedcba9876543210fedcba9876543210fedcba98\n")
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("identity without a revision refused", func(t *testing.T) {
		t.Parallel()
		pair, _, dest := newFixture(t)
		out, err := run(t, "", nil, pair, writeIdentity(t, ""), dest)
		refused(t, out, err, dest)
	})
	t.Run("manifest entry missing refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		manifest, err := os.ReadFile(filepath.Join(pair, "SHA256SUMS"))
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		for line := range strings.SplitSeq(string(manifest), "\n") {
			if strings.HasSuffix(line, "fixture-provider") {
				continue
			}
			kept = append(kept, line)
		}
		writeFile(t, filepath.Join(pair, "SHA256SUMS"), strings.Join(kept, "\n"))
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("changed bytes refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		writeFile(t, filepath.Join(pair, "fixture-provider"), "#!/bin/sh\nexit 1\n")
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("missing manifest refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		if err := os.Remove(filepath.Join(pair, "SHA256SUMS")); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("relative pair is not resolved through CDPATH", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cwd := filepath.Join(root, "cwd")
		writePair(t, filepath.Join(cwd, "pair"), pairRevision)
		decoyRoot := filepath.Join(root, "decoy")
		writePair(t, filepath.Join(decoyRoot, "pair"), pairRevision)
		// The real pair's bytes no longer match its manifest; the decoy's do.
		writeFile(t, filepath.Join(cwd, "pair", "fixture-provider"), "#!/bin/sh\nexit 1\n")
		dest := t.TempDir()
		out, err := run(t, cwd, []string{"CDPATH=" + decoyRoot}, "pair", writeIdentity(t, pairRevision), dest)
		refused(t, out, err, dest)
	})
	t.Run("symlink and dot-dot are resolved physically", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cwd := filepath.Join(root, "cwd")
		writePair(t, filepath.Join(cwd, "pair"), pairRevision)
		other := filepath.Join(root, "other")
		writePair(t, filepath.Join(other, "pair"), pairRevision)
		// The path sym/../pair is cwd/pair to a logical cd and other/pair to
		// the kernel that copies; only the physical directory may be trusted.
		writeFile(t, filepath.Join(other, "pair", "evener"), "#!/bin/sh\nexit 1\n")
		if err := os.MkdirAll(filepath.Join(other, "deep"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(other, "deep"), filepath.Join(cwd, "sym")); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
		dest := t.TempDir()
		out, err := run(t, cwd, nil, "sym/../pair", writeIdentity(t, pairRevision), dest)
		refused(t, out, err, dest)
	})
}

// TestRunUITestsInstallsThePairBeforeBuilding pins the runner's side of the
// handoff: it must install a supplied pair through the script that verifies it
// before the local build, and it must not copy from the pair itself.
func TestRunUITestsInstallsThePairBeforeBuilding(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile(filepath.Join("..", "run-ui-tests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	installed := strings.Index(text, "qualification/install-runtime-pair.sh")
	built := strings.Index(text, "make build-runtime")
	if installed == -1 || built == -1 || installed > built {
		t.Fatalf("run-ui-tests.sh install call at %d, local build at %d; want the pair installed before the build", installed, built)
	}
	if strings.Contains(text, `cp "$runtime_pair/`) {
		t.Error("run-ui-tests.sh copies from the pair itself instead of the installer that verified it")
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
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
