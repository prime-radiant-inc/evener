//go:build serffuzz

package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/sandbox"
)

// FuzzJobDelegateSandboxHostFactsSeed100 covers the nil, injected, and default
// prober paths. Session probes must be memoized even when the returned value is
// the zero HostFacts value.
func FuzzJobDelegateSandboxHostFactsSeed100(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 3, 255} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		switch selector % 3 {
		case 0:
			_ = (*Session)(nil).sandboxHostFacts()
		case 1:
			want := sandbox.HostFacts{OS: "fixture", Home: "/fixture/home", BwrapPath: "/fixture/bwrap", BwrapCapable: true}
			prober := &jdSandboxSchemaCountingProber{facts: want}
			s := &Session{}
			s.cfg.testOnly.sandboxProber = prober
			first := s.sandboxHostFacts()
			second := s.sandboxHostFacts()
			if !reflect.DeepEqual(first, want) || !reflect.DeepEqual(second, want) {
				t.Fatalf("injected host facts = (%+v, %+v), want %+v", first, second, want)
			}
			if prober.calls != 1 {
				t.Fatalf("injected prober calls = %d, want 1", prober.calls)
			}
		case 2:
			s := &Session{}
			first := s.sandboxHostFacts()
			second := s.sandboxHostFacts()
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("default host facts changed after memoization: first=%+v second=%+v", first, second)
			}
		}
	})
}

// FuzzJobDelegateRestoreSandboxFloorSeed100 exercises restore-time network
// no-escalation before host probing. A net-off parent must reject both an
// explicit net-on snapshot and the snapshot's legacy nil-network form.
func FuzzJobDelegateRestoreSandboxFloorSeed100(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 255} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		root := t.TempDir()
		parent := execenv.NewLocalExecutionEnvironment(root)
		parent.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeReadOnly, Network: false}
		prober := &jdSandboxSchemaCountingProber{}
		s := &Session{env: parent}
		s.cfg.testOnly.sandboxProber = prober

		var network *bool
		if selector&1 != 0 {
			on := true
			network = &on
		}
		desc := &jobstore.DelegateRestoreDescriptor{Sandbox: &jobstore.SandboxSnapshot{
			Mode:    sandbox.ModeReadOnly.String(),
			Network: network,
		}}
		resolved, reason := s.resolveRestoredDelegateSandbox(desc, root)
		if resolved != nil || reason != notResumableSandboxUnsatisfiable {
			t.Fatalf("network escalation = (%+v, %q), want nil/%q", resolved, reason, notResumableSandboxUnsatisfiable)
		}
		if prober.calls != 0 {
			t.Fatalf("network escalation probed host %d times, want 0", prober.calls)
		}
	})
}

// FuzzJobDelegateRestorePureGuardsSeed100 keeps corrupt persisted inputs on the
// pure validation side of the restore boundary and covers schema clone fallbacks.
func FuzzJobDelegateRestorePureGuardsSeed100(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 3, 4, 255} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		switch selector % 5 {
		case 0:
			if !hasValidDelegateRestoreSandbox(nil) {
				t.Fatal("nil restore descriptor rejected")
			}
		case 1:
			if !hasValidDelegateRestoreSandbox(&jobstore.DelegateRestoreDescriptor{}) {
				t.Fatal("absent restore sandbox rejected")
			}
		case 2:
			desc := &jobstore.DelegateRestoreDescriptor{Sandbox: &jobstore.SandboxSnapshot{Mode: "corrupt"}}
			if hasValidDelegateRestoreSandbox(desc) {
				t.Fatal("corrupt restore sandbox accepted")
			}
		case 3:
			value := make(chan int)
			if got := cloneDelegateResultSchema(value); got != value {
				t.Fatalf("marshal failure clone = %#v, want original", got)
			}
		case 4:
			if got := cloneDelegateResultSchema(map[string]any{}); got != nil {
				t.Fatalf("empty schema clone = %#v, want nil", got)
			}
		}
	})
}

type jdSandboxSchemaCountingProber struct {
	facts sandbox.HostFacts
	calls int
}

func (p *jdSandboxSchemaCountingProber) Probe() sandbox.HostFacts {
	p.calls++
	return p.facts
}
