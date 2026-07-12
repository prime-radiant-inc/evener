//go:build serffuzz

package main

import "testing"

// FuzzPackageUnion replays every deterministic package scenario under fuzz coverage.

func FuzzPackageUnion(f *testing.F) {

	f.Add(uint8(0))

	f.Fuzz(func(t *testing.T, _ uint8) {

		t.Run("TestRun", TestRun)
		t.Run("TestMainAndRenderFailure", TestMainAndRenderFailure)
		t.Run("TestRegisterType", TestRegisterType)
		t.Run("TestBuild", TestBuild)
	})
}
