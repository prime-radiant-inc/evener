package envctx

import (
	"slices"
	"testing"
	"time"
)

// fakeProbes counts invocations and returns canned values.
type fakeProbes struct {
	now                            time.Time
	loadCalls, memCalls, diskCalls int
	load, mem, disk                string
	branch                         string
}

func (f *fakeProbes) probes() Probes {
	return Probes{
		Now:       func() time.Time { return f.now },
		GitBranch: func(string) string { return f.branch },
		Load:      func() string { f.loadCalls++; return f.load },
		Memory:    func() string { f.memCalls++; return f.mem },
		Disk:      func(string) string { f.diskCalls++; return f.disk },
	}
}

func TestCollectFillsAllFields(t *testing.T) {
	f := &fakeProbes{
		now:    time.Date(2026, 8, 6, 14, 37, 0, 0, time.FixedZone("PDT", -7*3600)),
		branch: "main",
		load:   "load pressure: 9.1 (8 cores)",
	}
	c := NewCollector(f.probes())
	got := c.Collect(Inputs{Cwd: "/w", Sandbox: ""})
	if got.Cwd != "/w" || got.Sandbox != "off" || got.GitBranch != "main" {
		t.Fatalf("collect: %+v", got)
	}
	want := f.now.Local().Format("2006-01-02 15:00 MST")
	if got.LocalDateHour != want {
		t.Fatalf("hour truncation: got %q, want %q", got.LocalDateHour, want)
	}
	if got.Pressure.Load != "load pressure: 9.1 (8 cores)" {
		t.Fatalf("pressure: %+v", got.Pressure)
	}
}

func TestCollectThrottlesProbesToFiveMinutes(t *testing.T) {
	f := &fakeProbes{now: time.Unix(1_754_000_000, 0)}
	c := NewCollector(f.probes())

	c.Collect(Inputs{Cwd: "/w"})
	c.Collect(Inputs{Cwd: "/w"}) // 0s later: cached
	f.now = f.now.Add(4 * time.Minute)
	c.Collect(Inputs{Cwd: "/w"}) // 4m later: still cached
	if f.loadCalls != 1 || f.memCalls != 1 || f.diskCalls != 1 {
		t.Fatalf("probes not throttled: load=%d mem=%d disk=%d", f.loadCalls, f.memCalls, f.diskCalls)
	}

	f.now = f.now.Add(2 * time.Minute) // 6m after first probe
	f.load = "load pressure: high"
	got := c.Collect(Inputs{Cwd: "/w"})
	if f.loadCalls != 2 {
		t.Fatalf("probe not re-run after interval: %d", f.loadCalls)
	}
	if got.Pressure.Load != "load pressure: high" {
		t.Fatalf("fresh reading not used: %+v", got.Pressure)
	}
}

func TestCollectCachedReadingServedBetweenProbes(t *testing.T) {
	f := &fakeProbes{now: time.Unix(1_754_000_000, 0), mem: "memory pressure: warn level"}
	c := NewCollector(f.probes())
	c.Collect(Inputs{Cwd: "/w"})
	f.mem = "CHANGED" // must not be seen until re-probe
	got := c.Collect(Inputs{Cwd: "/w"})
	if got.Pressure.Memory != "memory pressure: warn level" {
		t.Fatalf("cached reading not served: %+v", got.Pressure)
	}
}

func TestCollectNilProbesReadNominal(t *testing.T) {
	c := NewCollector(Probes{Now: time.Now, GitBranch: func(string) string { return "" }})
	got := c.Collect(Inputs{Cwd: "/w"})
	if got.Pressure != (Pressure{}) {
		t.Fatalf("nil probes must read nominal: %+v", got.Pressure)
	}
}

// TestCollectReProbesDiskOnWorkspaceChange pins that a change of cwd
// invalidates the disk-pressure cache, while the host-wide load and memory
// caches keep their bounded cadence. Disk pressure depends on the cwd's
// filesystem, so a reading taken in one workspace must never be served for
// another.
func TestCollectReProbesDiskOnWorkspaceChange(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_754_000_000, 0)
	diskByCwd := map[string]string{
		"/full":    "disk pressure: volume 95% full",
		"/healthy": "",
	}
	var diskPaths []string
	var loadCalls, memCalls int
	p := Probes{
		Now:       func() time.Time { return now },
		GitBranch: func(string) string { return "" },
		Load:      func() string { loadCalls++; return "load pressure: high" },
		Memory:    func() string { memCalls++; return "memory pressure: warn level" },
		Disk: func(path string) string {
			diskPaths = append(diskPaths, path)
			return diskByCwd[path]
		},
	}
	c := NewCollector(p)

	// A full volume at /full is observed.
	if got := c.Collect(Inputs{Cwd: "/full"}); got.Pressure.Disk != diskByCwd["/full"] {
		t.Fatalf("first disk reading: %+v", got.Pressure)
	}
	// Same cwd within the interval reuses the cached disk reading.
	c.Collect(Inputs{Cwd: "/full"})
	if len(diskPaths) != 1 {
		t.Fatalf("same-cwd disk reading not reused: %v", diskPaths)
	}

	// Switching to a healthy workspace within the interval must refresh disk
	// rather than serve /full's warning, while load/memory stay cached.
	got := c.Collect(Inputs{Cwd: "/healthy"})
	if got.Pressure.Disk != "" {
		t.Fatalf("stale disk pressure survived workspace change: %+v", got.Pressure)
	}
	if got.Pressure.Load != "load pressure: high" || got.Pressure.Memory != "memory pressure: warn level" {
		t.Fatalf("host-wide readings lost on workspace change: %+v", got.Pressure)
	}
	if loadCalls != 1 || memCalls != 1 {
		t.Fatalf("load/memory must stay cached: load=%d mem=%d", loadCalls, memCalls)
	}

	// Returning to /full must refresh disk again, not serve the healthy cache.
	got = c.Collect(Inputs{Cwd: "/full"})
	if got.Pressure.Disk != diskByCwd["/full"] {
		t.Fatalf("disk not re-probed on return to full volume: %+v", got.Pressure)
	}
	if want := []string{"/full", "/healthy", "/full"}; !slices.Equal(diskPaths, want) {
		t.Fatalf("disk probe paths: got %v, want %v", diskPaths, want)
	}
}
