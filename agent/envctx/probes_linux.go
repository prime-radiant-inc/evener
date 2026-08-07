//go:build linux

package envctx

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func loadProbe() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	l1, ok := parseLoad1(string(b))
	if !ok {
		return ""
	}
	return loadWarning(l1, runtime.NumCPU())
}

// memoryProbe warns when MemAvailable drops below 5% of MemTotal.
func memoryProbe() string {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return ""
	}
	var totalKB, availKB int64
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			totalKB = v
		case "MemAvailable:":
			availKB = v
		}
	}
	if totalKB == 0 || availKB*20 >= totalKB {
		return ""
	}
	return "memory pressure: low available memory"
}

func DefaultProbes() Probes {
	return Probes{Now: time.Now, Load: loadProbe, Memory: memoryProbe, Disk: diskProbe}
}
