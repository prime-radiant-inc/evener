package envctx

import "time"

// probeInterval is the floor between expensive pressure probes; between
// probes Collect serves the cached reading.
const probeInterval = 5 * time.Minute

// Inputs carries the session-owned facts the Collector cannot observe
// itself. Sandbox "" is normalized to "off" so the field never empties
// (a change must never render as silence).
type Inputs struct {
	Cwd     string
	Sandbox string
}

// Probes are the Collector's injected observation functions. Pressure
// probes return a self-labeled warning line (e.g. "memory pressure: warn
// level") or "" when nominal; a nil probe always reads nominal. GitBranch
// returns "" outside a git repo.
type Probes struct {
	Now       func() time.Time
	GitBranch func(cwd string) string
	Load      func() string
	Memory    func() string
	Disk      func(path string) string
}

type cachedProbe struct {
	value  string
	probed time.Time
	hasRun bool
}

// Collector assembles Snapshots, rate-limiting the pressure probes.
type Collector struct {
	p               Probes
	load, mem, disk cachedProbe
}

func NewCollector(p Probes) *Collector {
	if p.Now == nil {
		p.Now = time.Now
	}
	return &Collector{p: p}
}

func (c *Collector) refresh(cp *cachedProbe, now time.Time, probe func() string) string {
	if probe == nil {
		return ""
	}
	if !cp.hasRun || now.Sub(cp.probed) >= probeInterval {
		*cp = cachedProbe{value: probe(), probed: now, hasRun: true}
	}
	return cp.value
}

// Collect observes the environment. Cheap facts (cwd, sandbox, clock, git)
// are read every call; pressure probes at most every probeInterval.
func (c *Collector) Collect(in Inputs) Snapshot {
	now := c.p.Now()
	sandbox := in.Sandbox
	if sandbox == "" {
		sandbox = "off"
	}
	var branch string
	if c.p.GitBranch != nil {
		branch = c.p.GitBranch(in.Cwd)
	}

	// Guard the Disk probe: pass nil to refresh when c.p.Disk is nil
	var diskProbe func() string
	if c.p.Disk != nil {
		diskProbe = func() string { return c.p.Disk(in.Cwd) }
	}

	return Snapshot{
		Cwd:           in.Cwd,
		LocalDateHour: now.Local().Format("2006-01-02 15:00 MST"),
		Sandbox:       sandbox,
		GitBranch:     branch,
		Pressure: Pressure{
			Load:   c.refresh(&c.load, now, c.p.Load),
			Memory: c.refresh(&c.mem, now, c.p.Memory),
			Disk:   c.refresh(&c.disk, now, diskProbe),
		},
	}
}
