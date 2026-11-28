//go:build evenerfuzz

package sandbox_test

import (
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

func FuzzSandboxContractMaterializers(f *testing.F) {
	for _, seed := range []byte{0, 1, 2} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, _ byte) {
		for _, kind := range []sandbox.WorkspaceKind{sandbox.NonGit, sandbox.MainCheckout, sandbox.LinkedWorktree} {
			cwd := sandboxtest.MaterializeWorkspace(t, kind)
			layout, err := sandbox.ClassifyWorkspace(cwd, nil)
			if err != nil {
				t.Fatalf("MaterializeWorkspace(%s): %v", kind, err)
			}
			if layout.Kind != kind {
				t.Fatalf("MaterializeWorkspace(%s) kind = %s", kind, layout.Kind)
			}
		}
		sandboxtest.AssertReRoot(t, sandboxtest.ReRootCases())
	})
}

func FuzzSandboxPublicContractHarness(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		sandboxtest.AssertResolve(t, sandbox.Resolve)
		sandboxtest.AssertReRoot(t, sandboxtest.ReRootCases())

		rec := &recordingT{T: t}
		if got := sandboxtest.MaterializeWorkspace(rec, sandbox.WorkspaceKind(99)); got != "" || rec.errors == 0 {
			t.Fatalf("MaterializeWorkspace unsupported kind = (%q, %d errors)", got, rec.errors)
		}
	})
}
