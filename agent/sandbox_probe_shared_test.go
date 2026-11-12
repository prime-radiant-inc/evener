package agent

import (
	"os"
	"slices"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// sharedHostProber reports the real host's sandbox facts to test sessions that
// inject no prober of their own. The real probe forks the bwrap, uname and
// xcode-select subprocesses, and the facts it derives are the same for every
// session built under the package's test environment, so it runs once and the
// result is replayed.
//
// The probe also reads the process environment (HOME, the XDG and toolchain
// cache variables, EVENER_*). Only a session built under the environment
// TestMain pinned shares the cached facts; any other environment gets a fresh
// real probe, so a test that moves those inputs with t.Setenv still sees facts
// derived from them. A probe the environment moved under while it ran is
// returned to its caller and never cached.
type sharedHostProber struct{}

var sharedHostProbe struct {
	// environ is the sorted process environment TestMain pinned before the
	// tests ran. Nil means no environment is pinned and every probe is fresh.
	environ []string

	mu    sync.Mutex
	facts *sandbox.HostFacts
}

// sharingHostProbe returns tc with the shared host prober when tc injects no
// prober of its own; a prober a test chose is kept.
func sharingHostProbe(tc testConfig) testConfig {
	if tc.sandboxProber == nil {
		tc.sandboxProber = sharedHostProber{}
	}
	return tc
}

// pinSharedHostProbeEnvironment records the environment whose probe result is
// shared. TestMain calls it after it has finished setting up that environment.
func pinSharedHostProbeEnvironment() {
	sharedHostProbe.environ = sortedEnviron()
}

func (sharedHostProber) Probe() sandbox.HostFacts {
	if !underPinnedEnvironment() {
		return sandbox.RealProber{}.Probe()
	}
	sharedHostProbe.mu.Lock()
	defer sharedHostProbe.mu.Unlock()
	if sharedHostProbe.facts != nil {
		return *sharedHostProbe.facts
	}
	facts := sandbox.RealProber{}.Probe()
	if underPinnedEnvironment() {
		sharedHostProbe.facts = &facts
	}
	return facts
}

func underPinnedEnvironment() bool {
	return sharedHostProbe.environ != nil && slices.Equal(sortedEnviron(), sharedHostProbe.environ)
}

func sortedEnviron() []string {
	environ := os.Environ()
	slices.Sort(environ)
	return environ
}

// TestSharedHostProberFollowsTheProcessEnvironment pins the boundary of the
// shared probe: under TestMain's environment a session sees the real facts for
// that environment, and once a test moves a probe input the next session sees
// facts derived from the moved value, not the cached ones.
//
// Not parallel: it changes HOME with t.Setenv.
func TestSharedHostProberFollowsTheProcessEnvironment(t *testing.T) {
	pinnedHome := os.Getenv("HOME")
	if got := (sharedHostProber{}).Probe().Home; got != pinnedHome {
		t.Fatalf("probe under the pinned environment: Home = %q, want %q", got, pinnedHome)
	}

	movedHome := t.TempDir()
	t.Setenv("HOME", movedHome)
	if got := (sharedHostProber{}).Probe().Home; got != movedHome {
		t.Fatalf("probe after HOME moved: Home = %q, want the moved %q", got, movedHome)
	}
}
