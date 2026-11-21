package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// pinTestSnapshotConfigHome records the config home this test binary inherits,
// so snapshotConfigHomeForkError can tell a test's own per-test override from the
// process-wide isolation the binary established. TestMain calls it once, after it
// has pointed HOME and XDG_CONFIG_HOME at a directory that outlives the tests and
// before m.Run.
func pinTestSnapshotConfigHome() {
	if dir, err := os.UserConfigDir(); err == nil {
		testSnapshotConfigHome = dir
	}
}

// TestSnapshotConfigHomeForkRefusesAConfigHomeTheTestMoved is the runtime
// replacement for the source-scanning config-home audit (#1527, #6110): a test
// that moves the config home to its own temp dir and leaves the launch snapshot
// on is refused where the snapshot forks `go env`, whatever shape its options
// took. The test binary pins its own config home in TestMain, so a difference
// here is this test's own doing.
//
// It cannot run in parallel: it moves process-wide HOME and XDG_CONFIG_HOME with
// t.Setenv. Go's test driver runs every non-parallel top-level test to
// completion, Setenv's restore included, before any parallel test body runs, so
// omitting t.Parallel() is what keeps this safe rather than racy.
func TestSnapshotConfigHomeForkRefusesAConfigHomeTheTestMoved(t *testing.T) {
	moved := t.TempDir()
	t.Setenv("HOME", moved)
	t.Setenv("XDG_CONFIG_HOME", moved)

	if err := snapshotConfigHomeForkError(); err == nil {
		t.Fatal("the snapshot guard must refuse a config home this test moved")
	}

	_, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{testOnly: testConfig{minimalSystemPrompt: true, noSyncJobStore: true}})
	if err == nil {
		t.Fatal("NewSession must refuse the launch snapshot in a config home the test moved to its own temp dir")
	}
	if !strings.Contains(err.Error(), "config home") {
		t.Errorf("refusal must name the config home, got %q", err)
	}
}

// TestSnapshotConfigHomeForkAllowsAGatedFixtureWithAMovedHome proves the guard
// costs nothing to the tests the audit used to steer: a session built through
// newSession skips the launch snapshot, so a moved config home is never forked
// into and the construction succeeds.
func TestSnapshotConfigHomeForkAllowsAGatedFixtureWithAMovedHome(t *testing.T) {
	moved := t.TempDir()
	t.Setenv("HOME", moved)
	t.Setenv("XDG_CONFIG_HOME", moved)

	if sess := newSession(t); sess == nil {
		t.Fatal("newSession must build a session in a moved config home: its fixture skips the snapshot")
	}
}

// TestSnapshotConfigHomeForkAllowsThePinnedConfigHome proves the other side: the
// config home the test binary pinned outlives the tests, so a session that takes
// the snapshot in it still forks and still measures go.
func TestSnapshotConfigHomeForkAllowsThePinnedConfigHome(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withEnvironmentSnapshot())
	if !sess.capabilities.toolsProbed || !sess.capabilities.onPath["go"] {
		t.Fatalf("the snapshot in the pinned config home must still measure the toolchain: %+v", sess.capabilities)
	}
}

// TestConfigHomeForkRefusal covers the decision itself: only a config home that
// differs from the pinned one is refused, and no pinned home means no opinion.
func TestConfigHomeForkRefusal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		baseline string
		current  string
		refused  bool
	}{
		{name: "same home", baseline: "/home/u/.config", current: "/home/u/.config", refused: false},
		{name: "same home spelled differently", baseline: "/home/u/.config/", current: "/home/u/./.config", refused: false},
		{name: "no pinned home", baseline: "", current: "/tmp/TestX/001", refused: false},
		{name: "moved to a temp home", baseline: "/home/u/.config", current: "/tmp/TestX/001", refused: true},
		{name: "moved within the same parent", baseline: "/home/u/.config", current: "/home/u/other", refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := configHomeForkRefusal(tc.baseline, tc.current)
			if tc.refused && err == nil {
				t.Fatalf("configHomeForkRefusal(%q, %q) must refuse", tc.baseline, tc.current)
			}
			if !tc.refused && err != nil {
				t.Fatalf("configHomeForkRefusal(%q, %q) must allow: %v", tc.baseline, tc.current, err)
			}
		})
	}
	// A symlinked spelling of one directory is the same home; on a host whose
	// temp dir has no symlink this degrades to the exact-string case above.
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil {
		if err := configHomeForkRefusal(dir, link); err != nil {
			t.Errorf("a symlinked spelling of the pinned home must be allowed: %v", err)
		}
	}
}
