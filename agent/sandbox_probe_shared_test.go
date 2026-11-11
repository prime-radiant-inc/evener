package agent

import (
	"os"
	"slices"
	"sync"

	"primeradiant.com/evener/agent/sandbox"
)

// sharedHostProber reports the real host's sandbox facts to test sessions that
// inject no prober of their own. The real probe forks bwrap, uname and go env
// subprocesses, and the facts it derives are the same for every session built
// under the package's test environment, so it runs once and the result is
// replayed.
//
// The probe also reads the process environment (HOME, the XDG and toolchain
// cache variables, EVENER_*). Only a session built under the environment
// TestMain pinned shares the cached facts; any other environment gets a fresh
// real probe, so a test that moves those inputs with t.Setenv still sees facts
// derived from them.
type sharedHostProber struct{}

var sharedHostProbe struct {
	// environ is the sorted process environment TestMain pinned before the
	// tests ran. Nil means no environment is pinned and every probe is fresh.
	environ []string
	once    sync.Once
	facts   sandbox.HostFacts
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
	if sharedHostProbe.environ == nil || !slices.Equal(sortedEnviron(), sharedHostProbe.environ) {
		return sandbox.RealProber{}.Probe()
	}
	sharedHostProbe.once.Do(func() {
		sharedHostProbe.facts = sandbox.RealProber{}.Probe()
	})
	return sharedHostProbe.facts
}

func sortedEnviron() []string {
	environ := os.Environ()
	slices.Sort(environ)
	return environ
}
