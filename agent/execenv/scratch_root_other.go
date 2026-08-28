//go:build !linux && !darwin

package execenv

// pinScratchSandboxRoot is intentionally a no-op on platforms without the
// supported descriptor-based enforcement implementation. scratchSandboxFor
// returns nil on those platforms, so the ordinary non-sandboxed path remains
// responsible for its existing best-effort confinement contract without
// pretending that a race-safe root pin exists.
func pinScratchSandboxRoot(_ *sandboxFS, _ string) error {
	return nil
}
