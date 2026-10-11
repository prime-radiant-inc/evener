package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// processRuntimeSessionTmpError reports why a spawned session's TMPDIR shape is
// wrong, or nil when it matches the platform's contract. Where a world-usable
// session temp container exists (sandbox.SessionTmpSupported), TMPDIR names an
// existing 1777 sticky <host-temp>/evener-sandbox-*/tmp container distinct from
// the private scratch. Where none can exist — Windows — production keeps TMPDIR
// on that scratch (local.go's tmpDirNamesScratch), so the two are equal.
func processRuntimeSessionTmpError(name, tmpDir, scratch string, tmpSupported bool) error {
	if !tmpSupported {
		if tmpDir != scratch {
			return fmt.Errorf("%s TMPDIR = %q, want the session scratch %q on this platform", name, tmpDir, scratch)
		}
		return nil
	}
	if tmpDir == "" || tmpDir == scratch {
		return fmt.Errorf("%s TMPDIR = %q, want a world-usable temp container distinct from the private scratch %q",
			name, tmpDir, scratch)
	}
	if filepath.Base(tmpDir) != "tmp" ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(tmpDir)), sandboxSessionScratchPrefixForTest) {
		return fmt.Errorf("%s TMPDIR = %q, want <host-temp>/evener-sandbox-*/tmp", name, tmpDir)
	}
	info, err := os.Stat(tmpDir)
	if err != nil {
		return fmt.Errorf("%s TMPDIR %q: %w", name, tmpDir, err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("%s TMPDIR %q mode = %v, want an existing 1777 sticky directory", name, tmpDir, info.Mode())
	}
	return nil
}

// TestProcessRuntimeSessionTmpError exercises processRuntimeSessionTmpError
// directly so both platform answers are covered on any host. Only the accepting
// supported case needs a real sticky container, which a platform that never
// reports os.ModeSticky cannot supply; every rejection arm is platform
// independent and runs everywhere.
func TestProcessRuntimeSessionTmpError(t *testing.T) {
	t.Parallel()
	scratch := t.TempDir()
	container := sessionTmpContainerForTest(t)
	wrongLeaf := sessionTmpDirForTest(t, sandboxSessionScratchPrefixForTest+"test", "not-tmp", 0o777|os.ModeSticky)
	wrongPrefix := sessionTmpDirForTest(t, "plain-dir", "tmp", 0o777|os.ModeSticky)
	wrongMode := sessionTmpDirForTest(t, sandboxSessionScratchPrefixForTest+"test", "tmp", 0o755)
	nonDir := sessionTmpFileForTest(t)

	cases := []struct {
		name         string
		tmpDir       string
		scratch      string
		tmpSupported bool
		needsSticky  bool
		wantErr      bool
	}{
		{"supported accepts container", container, scratch, true, true, false},
		{"supported rejects scratch", scratch, scratch, true, false, true},
		{"supported rejects empty", "", scratch, true, false, true},
		{"supported rejects wrong leaf", wrongLeaf, scratch, true, false, true},
		{"supported rejects unprefixed parent", wrongPrefix, scratch, true, false, true},
		{"supported rejects non-world mode", wrongMode, scratch, true, false, true},
		{"supported rejects non-directory", nonDir, scratch, true, false, true},
		{"unsupported accepts scratch", scratch, scratch, false, false, false},
		{"unsupported rejects container", container, scratch, false, false, true},
		{"unsupported rejects empty", "", scratch, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsSticky && !sandbox.SessionTmpSupported {
				t.Skip("this platform never reports os.ModeSticky, so it cannot mint the container this case accepts")
			}
			err := processRuntimeSessionTmpError("env", tc.tmpDir, tc.scratch, tc.tmpSupported)
			if (err != nil) != tc.wantErr {
				t.Fatalf("processRuntimeSessionTmpError(%q, %q, supported=%t) = %v, wantErr=%t",
					tc.tmpDir, tc.scratch, tc.tmpSupported, err, tc.wantErr)
			}
		})
	}
}

// sessionTmpDirForTest mints a directory under a fresh base whose parent is named
// parentName and whose leaf is named leaf, with the leaf in mode perm. It stands
// in for a container of any shape so the predicate's rejection arms are exercised.
func sessionTmpDirForTest(t *testing.T, parentName, leaf string, perm os.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), parentName, leaf)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, perm); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sessionTmpContainerForTest mints a directory shaped like the world-usable temp
// container production creates: <base>/evener-sandbox-*/tmp with 1777 sticky mode.
func sessionTmpContainerForTest(t *testing.T) string {
	t.Helper()
	return sessionTmpDirForTest(t, sandboxSessionScratchPrefixForTest+"test", "tmp", 0o777|os.ModeSticky)
}

// sessionTmpFileForTest mints a non-directory at the container leaf path so the
// predicate's IsDir rejection arm is exercised.
func sessionTmpFileForTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), sandboxSessionScratchPrefixForTest+"test", "tmp")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
