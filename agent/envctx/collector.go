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
// reports a known empty branch only for a directory outside a worktree.
type Probes struct {
	Now       func() time.Time
	GitBranch func(cwd string) (branch string, known bool)
	Load      func() string
	Memory    func() string
	Disk      func(path string) string
}

type cachedProbe struct {
	value  string
	key    string
	probed time.Time
	hasRun bool
}

// Collector assembles Snapshots, rate-limiting the pressure probes.
type Collector struct {
	p               Probes
	load, mem, disk cachedProbe
	gitBranches     map[string]string
}

func NewCollector(p Probes) *Collector {
	if p.Now == nil {
		p.Now = time.Now
	}
	return &Collector{p: p, gitBranches: make(map[string]string)}
}

// RememberGitBranch records a confirmed observation for one checkout. An
// empty branch records that the directory was outside a worktree.
func (c *Collector) RememberGitBranch(cwd, branch string) {
	if cwd != "" {
		c.gitBranches[cwd] = branch
	}
}

// ReconcileGitBranch retains a definitive probe and supplies this checkout's
// last confirmed value on an inconclusive one. It reports whether to probe again.
// The caller serializes this step with workspace changes and publication.
func (c *Collector) ReconcileGitBranch(snap *Snapshot) (retry bool) {
	if !snap.gitBranchUnknown {
		c.RememberGitBranch(snap.Cwd, snap.GitBranch)
		return false
	}
	if branch, known := c.gitBranches[snap.Cwd]; known {
		snap.GitBranch = branch
		snap.gitBranchUnknown = false
	}
	return c.p.GitBranch != nil
}

// refresh serves cp's cached value until its key changes or probeInterval
// elapses. key names the subject the observation depends on: host-wide probes
// pass "" (never invalidated), disk passes the cwd whose filesystem it reads.
func (c *Collector) refresh(cp *cachedProbe, now time.Time, key string, probe func() string) string {
	if probe == nil {
		return ""
	}
	if !cp.hasRun || cp.key != key || now.Sub(cp.probed) >= probeInterval {
		*cp = cachedProbe{value: probe(), key: key, probed: now, hasRun: true}
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
	var branchKnown bool
	if c.p.GitBranch != nil {
		branch, branchKnown = c.p.GitBranch(in.Cwd)
	}

	// Guard the Disk probe: pass nil to refresh when c.p.Disk is nil
	var diskProbe func() string
	if c.p.Disk != nil {
		diskProbe = func() string { return c.p.Disk(in.Cwd) }
	}

	return Snapshot{
		Cwd:              in.Cwd,
		LocalDateHour:    now.Local().Format("2006-01-02 15:00 MST"),
		Sandbox:          sandbox,
		GitBranch:        branch,
		gitBranchUnknown: !branchKnown,
		Pressure: Pressure{
			Load:   c.refresh(&c.load, now, "", c.p.Load),
			Memory: c.refresh(&c.mem, now, "", c.p.Memory),
			Disk:   c.refresh(&c.disk, now, in.Cwd, diskProbe),
		},
	}
}
