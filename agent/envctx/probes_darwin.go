//go:build darwin

package envctx

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// sysctlN runs `sysctl -n name` with a short timeout; "" on any failure.
// Exec keeps this cgo-free; the Collector throttles calls to once per
// probeInterval so the subprocess cost is negligible.
func sysctlN(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func loadProbe() string {
	l1, ok := parseLoad1(sysctlN("vm.loadavg"))
	if !ok {
		return ""
	}
	return loadWarning(l1, runtime.NumCPU())
}

// memoryProbe reads kern.memorystatus_vm_pressure_level: 1 normal,
// 2 warn, 4 critical. Verified present on macOS 15 (Darwin 25).
func memoryProbe() string {
	lvl, err := strconv.Atoi(sysctlN("kern.memorystatus_vm_pressure_level"))
	if err != nil || lvl < 2 {
		return ""
	}
	if lvl >= 4 {
		return "memory pressure: critical"
	}
	return "memory pressure: warn level"
}

func DefaultProbes() Probes {
	return Probes{Now: time.Now, Load: loadProbe, Memory: memoryProbe, Disk: diskProbe}
}
