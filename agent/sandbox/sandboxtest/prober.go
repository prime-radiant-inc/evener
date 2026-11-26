package sandboxtest

import "primeradiant.com/evener/agent/sandbox"

// FakeProber returns fixed HostFacts. Unit tests inject it as sandbox.Prober
// so no unit test shells out to bwrap.
type FakeProber struct {
	Facts sandbox.HostFacts
}

var _ sandbox.Prober = FakeProber{}

// Probe returns the canned facts.
func (f FakeProber) Probe() sandbox.HostFacts { return f.Facts }
