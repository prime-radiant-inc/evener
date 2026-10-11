//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/envvars"
)

// worldUsableBaseForTest creates a directory with /tmp's own mode (world-
// writable, world-traversable, sticky) that a test can name as a host temp base.
func worldUsableBaseForTest(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "host-temp")
	if err := os.Mkdir(base, sessionTmpLeafMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, sessionTmpLeafMode); err != nil {
		t.Fatal(err)
	}
	return base
}

// retainedAgedSessionTmpIn mints a session temp container in base, retains it
// exactly as a session close does, and ages it past the reclaim window, so the
// sweep removes it if and only if it walks base.
func retainedAgedSessionTmpIn(t *testing.T, base string) string {
	t.Helper()
	restore := SetWorldTempBasesForTesting([]string{base})
	tmp, err := NewSessionTmp()
	restore()
	if err != nil {
		t.Fatalf("NewSessionTmp in %q: %v", base, err)
	}
	if err := tmp.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	container := filepath.Dir(tmp.Dir)
	aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
	if err := os.Chtimes(container, aged, aged); err != nil {
		t.Fatal(err)
	}
	return container
}

// requireOnlyBases stops a test before it mints or sweeps anything unless the
// bases in force are exactly want. Without it, an implementation that ignored
// the variable would fall back to the machine's real /tmp and /var/tmp, and the
// test would create containers there or reclaim other sessions' scratch.
func requireOnlyBases(t *testing.T, want ...string) {
	t.Helper()
	got, err := worldTempBaseCandidates()
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("bases in force = %v, %v; want exactly %v before touching any of them", got, err, want)
	}
}

// TestHostTempBasesEnvUnsetKeepsTheDefaults pins that an unset variable leaves
// the bases exactly as they were: /tmp, then /var/tmp.
func TestHostTempBasesEnvUnsetKeepsTheDefaults(t *testing.T) {
	t.Setenv(envvars.EVENERHostTempBases.Name, "")
	if err := envvars.EVENERHostTempBases.Unsetenv(); err != nil {
		t.Fatal(err)
	}
	got, err := worldTempBaseCandidates()
	if err != nil {
		t.Fatalf("worldTempBaseCandidates with the variable unset: %v", err)
	}
	if want := []string{"/tmp", "/var/tmp"}; !slices.Equal(got, want) {
		t.Fatalf("bases with the variable unset = %v, want %v", got, want)
	}
}

// TestHostTempBasesEnvReplacesTheDefaults pins the list parse: an OS path list,
// in order, replacing the defaults rather than adding to them.
func TestHostTempBasesEnvReplacesTheDefaults(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	t.Setenv(envvars.EVENERHostTempBases.Name, first+string(os.PathListSeparator)+second)
	got, err := worldTempBaseCandidates()
	if err != nil {
		t.Fatalf("worldTempBaseCandidates: %v", err)
	}
	if want := []string{first, second}; !slices.Equal(got, want) {
		t.Fatalf("bases = %v, want %v", got, want)
	}
}

// TestHostTempBasesEnvRefusesAMalformedValue pins the loud refusal: a value
// that is set but names no usable list is an error at every consumer, and is
// never quietly replaced by /tmp and /var/tmp. Falling back would send the
// startup sweep to exactly the bases the variable was set to keep it out of.
func TestHostTempBasesEnvRefusesAMalformedValue(t *testing.T) {
	abs := t.TempDir()
	sep := string(os.PathListSeparator)
	for name, value := range map[string]string{
		"empty":          "",
		"blank":          "  ",
		"relative entry": "relative/tmp",
		"empty entry":    abs + sep + sep + abs,
		"trailing sep":   abs + sep,
	} {
		t.Run(name, func(t *testing.T) {
			isolateScratchBases(t)
			t.Setenv(envvars.EVENERHostTempBases.Name, value)

			bases, err := worldTempBaseCandidates()
			if err == nil || !strings.Contains(err.Error(), envvars.EVENERHostTempBases.Name) {
				t.Fatalf("worldTempBaseCandidates(%q) = %v, %v; want an error naming %s", value, bases, err, envvars.EVENERHostTempBases.Name)
			}
			if bases != nil {
				t.Fatalf("worldTempBaseCandidates(%q) returned bases %v alongside its error", value, bases)
			}
			if tmp, err := NewSessionTmp(); err == nil || !strings.Contains(err.Error(), envvars.EVENERHostTempBases.Name) {
				if tmp != nil {
					_ = tmp.Remove()
				}
				t.Fatalf("NewSessionTmp under %q = %v; want an error naming %s", value, err, envvars.EVENERHostTempBases.Name)
			}
			if err := SweepCrashedSessionScratch(t.TempDir()); err == nil || !strings.Contains(err.Error(), envvars.EVENERHostTempBases.Name) {
				t.Fatalf("SweepCrashedSessionScratch under %q = %v; want an error naming %s", value, err, envvars.EVENERHostTempBases.Name)
			}
		})
	}
}

// TestNewSessionTmpMintsInTheHostTempBasesEnv: with the variable set, a session
// temp container is created in the base it names.
func TestNewSessionTmpMintsInTheHostTempBasesEnv(t *testing.T) {
	base := worldUsableBaseForTest(t)
	t.Setenv(envvars.EVENERHostTempBases.Name, base)
	requireOnlyBases(t, base)

	tmp, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	t.Cleanup(func() { _ = tmp.Remove() })
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(filepath.Dir(tmp.Dir)); got != canonical {
		t.Fatalf("container base = %q, want the one %s names, %q", got, envvars.EVENERHostTempBases.Name, canonical)
	}
}

// TestNewSessionTmpValidatesHostTempBasesEnvLikeTheDefaults: a named base that
// is not a world-writable sticky directory is refused the same way /tmp would
// be, and a later entry that serves is used instead.
func TestNewSessionTmpValidatesHostTempBasesEnvLikeTheDefaults(t *testing.T) {
	private := t.TempDir() // 0700, not sticky: no other uid could use it
	base := worldUsableBaseForTest(t)
	t.Setenv(envvars.EVENERHostTempBases.Name, private+string(os.PathListSeparator)+base)
	requireOnlyBases(t, private, base)

	tmp, err := NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	t.Cleanup(func() { _ = tmp.Remove() })
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(filepath.Dir(tmp.Dir)); got != canonical {
		t.Fatalf("container base = %q, want the world-usable %q rather than the private %q", got, canonical, private)
	}

	t.Setenv(envvars.EVENERHostTempBases.Name, private)
	requireOnlyBases(t, private)
	if tmp, err := NewSessionTmp(); err == nil {
		_ = tmp.Remove()
		t.Fatalf("NewSessionTmp minted %q in a base that is not world-usable", tmp.Dir)
	}
}

// TestSweepWalksOnlyTheHostTempBasesEnv is the property the variable exists
// for: a process started with it set reclaims abandoned containers in the base
// it names and leaves every other host temp alone. The decoy base stands in for
// the machine's /tmp.
func TestSweepWalksOnlyTheHostTempBasesEnv(t *testing.T) {
	isolateScratchBases(t)
	named := worldUsableBaseForTest(t)
	decoyBase := worldUsableBaseForTest(t)
	abandoned := retainedAgedSessionTmpIn(t, named)
	decoy := retainedAgedSessionTmpIn(t, decoyBase)
	t.Setenv(envvars.EVENERHostTempBases.Name, named)
	requireOnlyBases(t, named)

	if err := SweepCrashedSessionScratch(t.TempDir()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("abandoned container %q in the named base survived the sweep: %v", abandoned, err)
	}
	if _, err := os.Stat(decoy); err != nil {
		t.Errorf("the sweep reached %q, outside the bases %s names: %v", decoy, envvars.EVENERHostTempBases.Name, err)
	}
}

// TestSetWorldTempBasesForTestingOutranksTheHostTempBasesEnv: a test binary's
// TestMain exports the variable for the child processes it starts, and a test
// inside that binary that points the bases at its own fixture must still get
// that fixture.
func TestSetWorldTempBasesForTestingOutranksTheHostTempBasesEnv(t *testing.T) {
	t.Setenv(envvars.EVENERHostTempBases.Name, t.TempDir())
	fixture := t.TempDir()
	t.Cleanup(SetWorldTempBasesForTesting([]string{fixture}))
	got, err := worldTempBaseCandidates()
	if err != nil {
		t.Fatalf("worldTempBaseCandidates: %v", err)
	}
	if want := []string{fixture}; !slices.Equal(got, want) {
		t.Fatalf("bases = %v, want the testing override %v", got, want)
	}
}
