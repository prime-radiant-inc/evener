package envctx

import (
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
