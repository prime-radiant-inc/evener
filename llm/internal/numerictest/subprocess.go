// Package numerictest contains subprocess containment for numeric regression tests.
// It is test support, not production input admission policy.
package numerictest

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/metrics"
	"testing"
	"time"
)

type report struct {
	Started        bool
	Completed      bool
	AllocBytes     uint64
	CPUSeconds     float64
	UserCPUSeconds float64
	GCCPUSeconds   float64
}

func callbackCPU(t *testing.T) (user, gc float64) {
	t.Helper()
	samples := []metrics.Sample{
		{Name: "/cpu/classes/user:cpu-seconds"},
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
	}
	metrics.Read(samples)
	for _, sample := range samples {
		if sample.Value.Kind() != metrics.KindFloat64 {
			t.Fatalf("CPU metric %s unavailable: kind=%v", sample.Name, sample.Value.Kind())
		}
		value := sample.Value.Float64()
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			t.Fatalf("CPU metric %s invalid: %v", sample.Name, value)
		}
	}
	return samples[0].Value.Float64(), samples[1].Value.Float64()
}

// Run executes exactly one witness in a fresh copy of the current test binary.
// Package initialization precedes the measured callback and the hard limits.
func Run(t *testing.T, witness string, check func(*testing.T)) {
	t.Helper()
	key := t.Name() + ":" + witness
	if childWitness := os.Getenv("EVENER_NUMERIC_WITNESS"); childWitness != "" {
		if childWitness != key {
			return
		}
		limitChild(t)
		path := os.Getenv("EVENER_NUMERIC_REPORT")
		write := func(r report) {
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		var before, after runtime.MemStats
		// CPU class metrics are GC snapshots on supported Go runtimes. Flush
		// both boundaries so a callback without a GC still gets measured.
		runtime.GC()
		runtime.ReadMemStats(&before)
		userStart, gcStart := callbackCPU(t)
		write(report{Started: true})
		completed := false
		defer func() {
			runtime.GC()
			runtime.ReadMemStats(&after)
			userEnd, gcEnd := callbackCPU(t)
			userDelta, gcDelta := userEnd-userStart, gcEnd-gcStart
			if userDelta < 0 || gcDelta < 0 {
				t.Fatalf("CPU metrics decreased: user=%g->%g gc=%g->%g", userStart, userEnd, gcStart, gcEnd)
			}
			r := report{Started: true, Completed: completed, AllocBytes: after.TotalAlloc - before.TotalAlloc,
				CPUSeconds: userDelta + gcDelta, UserCPUSeconds: userDelta, GCCPUSeconds: gcDelta}
			write(r)
			t.Logf("runtime CPU metrics: /cpu/classes/user:cpu-seconds=%g->%g /cpu/classes/gc/total:cpu-seconds=%g->%g", userStart, userEnd, gcStart, gcEnd)
			if r.AllocBytes > 64<<20 || r.CPUSeconds > 5 {
				t.Errorf("callback budget exceeded: %+v", r)
			}
		}()
		check(t)
		completed = true
		return
	}
	path := filepath.Join(t.TempDir(), "report.json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1", "EVENER_NUMERIC_WITNESS="+key, "EVENER_NUMERIC_REPORT="+path)
	out, err := cmd.CombinedOutput()
	var r report
	b, readErr := os.ReadFile(path)
	if readErr == nil {
		readErr = json.Unmarshal(b, &r)
	}
	t.Logf("witness=%s report=%+v child=%v\n%s", witness, r, err, out)
	if ctx.Err() != nil || err != nil || readErr != nil || !r.Started || !r.Completed {
		t.Fatalf("witness did not complete successfully: child=%v deadline=%v report=%v started=%v completed=%v", err, ctx.Err(), readErr, r.Started, r.Completed)
	}
	if r.AllocBytes > 64<<20 || r.CPUSeconds > 5 {
		t.Fatalf("callback budget exceeded: %+v", r)
	}
}
