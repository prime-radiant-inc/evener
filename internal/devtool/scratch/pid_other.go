//go:build !linux && !darwin

package scratch

// This platform has no signal-0 liveness check (syscall.Kill is unix-only,
// and os.FindProcess succeeds for any pid on Windows), so every owner counts
// as alive and nothing is ever reclaimed here: the mistake this package must
// never make is deleting a live run's scratch. The dev tooling runs on
// linux/darwin; this keeps the ports that import this library building
// everywhere, the way cmd/serf-test-dev-tooling's wave_other.go does.

func pidAlive(int) bool { return true }
