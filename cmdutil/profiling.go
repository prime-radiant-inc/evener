package cmdutil

import (
	"fmt"
	"os"
	"runtime/pprof"
	"runtime/trace"
)

// StartCPUProfile begins CPU profiling to the given path.
// Returns a stop function that must be deferred by the caller.
func StartCPUProfile(path string) (stop func(), err error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("cannot create CPU profile: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot start CPU profile: %w", err)
	}
	return func() {
		pprof.StopCPUProfile()
		f.Close()
	}, nil
}

// StartTrace begins execution tracing to the given path.
// Returns a stop function that must be deferred by the caller.
func StartTrace(path string) (stop func(), err error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("cannot create trace file: %w", err)
	}
	if err := trace.Start(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot start trace: %w", err)
	}
	return func() {
		trace.Stop()
		f.Close()
	}, nil
}
