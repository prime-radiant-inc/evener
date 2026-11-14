//go:build ignore

// catalog-startup loads one offline embedded registry, then exits reporting
// this process's executed-image peak (VmHWM): getrusage counters survive
// exec, so wait4's ru_maxrss is floored by the launcher's own image and the
// probe must self-report (#4799). Build this exact source at each revision.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// executedImagePeakKiB reads /proc/self/status's VmHWM with strict field and
// unit validation: proc reports the high-water mark as a single integer in
// "kB" (its name for KiB), and anything else is a refused measurement rather
// than a zero. VmHWM resets when exec installs a new image, so it measures
// the executed probe alone, not the launcher.
func executedImagePeakKiB() (uint64, error) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, fmt.Errorf("VmHWM line %q is not a single kB value", line)
		}
		return strconv.ParseUint(fields[1], 10, 64)
	}
	return 0, fmt.Errorf("/proc/self/status has no VmHWM line")
}

func main() {
	r, err := registry.Load(registry.WithNoUserLayer(), registry.WithOffline(true), registry.WithoutCache(), registry.WithEnv(func(string) (string, bool) { return "", false }))
	if err != nil {
		panic(err)
	}
	providers := len(r.ProviderIDs())
	if providers != 219 {
		panic(fmt.Sprintf("expected 219 embedded providers, got %d", providers))
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	peakKiB, err := executedImagePeakKiB()
	if err != nil {
		panic(err)
	}
	result := struct {
		Providers  int    `json:"providers"`
		Loads      int    `json:"registry_loads"`
		GOMAXPROCS int    `json:"gomaxprocs"`
		HeapAlloc  uint64 `json:"heap_alloc_bytes"`
		TotalAlloc uint64 `json:"total_alloc_bytes"`
		PeakKiB    uint64 `json:"vm_hwm_kib"`
	}{providers, 1, runtime.GOMAXPROCS(0), stats.HeapAlloc, stats.TotalAlloc, peakKiB}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		panic(err)
	}
	runtime.KeepAlive(r)
}
