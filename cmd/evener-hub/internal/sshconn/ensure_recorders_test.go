package sshconn

import (
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// testRestartRecorder wires a restart recorder into m so a restart-only Ensure
// attempt runs in tests: the finish is a no-op (the fake runner spawns nothing,
// so no store is written). Tests that intend the attempt to fail closed leave
// the hook unwired.
func testRestartRecorder(t *testing.T, m *Manager) {
	t.Helper()
	m.SetEnsureRestartHook(func(hostreg.Host) (func(error), error) {
		return func(error) {}, nil
	})
}

// testDeployRecorder wires a deploy recorder into m so an Ensure-triggered
// deploy runs in tests: the finish is a no-op (the fake runner spawns nothing,
// so no store is written). Tests that intend the attempt to fail closed leave
// the hook unwired.
func testDeployRecorder(t *testing.T, m *Manager) {
	t.Helper()
	m.SetEnsureDeployHook(func(hostreg.Host) (func(error), error) {
		return func(error) {}, nil
	})
}
