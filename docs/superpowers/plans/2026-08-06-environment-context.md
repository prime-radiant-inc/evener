# Environment Context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Diff-rendered per-turn environment context (cwd, date+hour, sandbox, git branch, resource pressure) injected as an append-only user-role message, so the model tracks session drift without busting provider prompt caches.

**Architecture:** A pure `agent/internal/envctx` package (Snapshot struct, throttled Collector with injected probes, Tracker with field-by-field RenderDiff) plus a new `schema.TurnEnvironment` turn kind that follows the existing `TurnSteering` pattern through `expandHistory`. The session collects + diffs at turn start and appends a `TurnEnvironment` turn before the user's input; tracker state persists in session meta.

**Tech Stack:** Go (agent module). No new dependencies — `golang.org/x/sys/unix` is already in `agent/go.sum` for the disk probe.

**Spec:** `docs/superpowers/specs/2026-08-06-environment-context-design.md` — read it first.

## Global Constraints

- TDD strictly: failing test before implementation, every task.
- Work happens in the `agent/` Go module (`cd agent` for `go test`).
- `gofmt` clean; `make lint-gofmt` from the repo root must pass.
- Full gate: `make test` from the repo root (multi-module; the compiler is the completeness net — never grep-verify a refactor).
- Test output must be pristine. Probe failures render as nominal, never as log noise.
- Rendering format (spec, verbatim): one outer `<environment_context>` tag, plain `label: value` lines, cwd double-quoted, no inner XML, struct-declaration field order.
- Probe throttle floor: 5 minutes. Thresholds: load1 > 2× NumCPU, darwin memory pressure level ≥ 2 (warn), linux MemAvailable < 5% of MemTotal, disk > 90% full. Constants, no config surface.
- Never widen a timeout or sleep to make a test pass; inject the clock.

---

### Task 1: envctx types, Tracker, RenderDiff

**Files:**
- Create: `agent/internal/envctx/envctx.go`
- Test: `agent/internal/envctx/envctx_test.go`

**Interfaces:**
- Produces (later tasks and the session depend on these exact names):
  - `type Snapshot struct { Cwd, LocalDateHour, Sandbox, GitBranch string; Pressure Pressure }`
  - `type Pressure struct { Load, Memory, Disk string }`
  - `type State struct { Last Snapshot; HasSent bool }` (the persistable form)
  - `func (t *Tracker) RenderDiff(cur Snapshot) string` — "" when nothing to say
  - `func NewTracker(st State) *Tracker` / `func (t *Tracker) State() State`

- [ ] **Step 1: Write the failing tests**

```go
package envctx

import (
	"strings"
	"testing"
)

func fullSnap() Snapshot {
	return Snapshot{
		Cwd:           "/Users/jesse/work",
		LocalDateHour: "2026-08-06 14:00 PDT",
		Sandbox:       "off",
		GitBranch:     "main",
	}
}

func TestRenderDiffFirstEmissionRendersAllNonEmptyFields(t *testing.T) {
	tr := NewTracker(State{})
	got := tr.RenderDiff(fullSnap())
	want := "<environment_context>\n" +
		"cwd: \"/Users/jesse/work\"\n" +
		"date: 2026-08-06 14:00 PDT\n" +
		"sandbox: off\n" +
		"git branch: main\n" +
		"</environment_context>"
	if got != want {
		t.Fatalf("first emission:\ngot  %q\nwant %q", got, want)
	}
	if st := tr.State(); !st.HasSent || st.Last != fullSnap() {
		t.Fatalf("state after emission: %+v", st)
	}
}

func TestRenderDiffNoChangeRendersNothing(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	if got := tr.RenderDiff(fullSnap()); got != "" {
		t.Fatalf("unchanged snapshot rendered %q", got)
	}
}

func TestRenderDiffSingleFieldChange(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	cur := fullSnap()
	cur.Cwd = "/Users/jesse/work/.worktrees/lane"
	got := tr.RenderDiff(cur)
	want := "<environment_context>\n" +
		"cwd: \"/Users/jesse/work/.worktrees/lane\"\n" +
		"</environment_context>"
	if got != want {
		t.Fatalf("cwd change:\ngot  %q\nwant %q", got, want)
	}
}

func TestRenderDiffGitBranchGoneRendersPlaceholder(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	cur := fullSnap()
	cur.GitBranch = ""
	got := tr.RenderDiff(cur)
	if !strings.Contains(got, "git branch: (not in a git repository)") {
		t.Fatalf("branch->empty must render placeholder, got %q", got)
	}
}

func TestRenderDiffPressureAppearAndClear(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})

	over := fullSnap()
	over.Pressure.Memory = "memory pressure: warn level"
	got := tr.RenderDiff(over)
	if !strings.Contains(got, "memory pressure: warn level") {
		t.Fatalf("pressure onset not rendered: %q", got)
	}

	// Clearing renders the back-to-normal line exactly once.
	got = tr.RenderDiff(fullSnap())
	if !strings.Contains(got, "memory pressure: back to normal") {
		t.Fatalf("pressure clear not rendered: %q", got)
	}
	if got := tr.RenderDiff(fullSnap()); got != "" {
		t.Fatalf("steady nominal must render nothing, got %q", got)
	}
}

func TestRenderDiffFirstEmissionSkipsNominalPressure(t *testing.T) {
	tr := NewTracker(State{})
	got := tr.RenderDiff(fullSnap())
	if strings.Contains(got, "pressure") {
		t.Fatalf("nominal pressure must not appear on first emission: %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test ./internal/envctx/ -v`
Expected: FAIL to build — `undefined: Snapshot` etc.

- [ ] **Step 3: Write the implementation**

```go
// Package envctx renders per-turn environment context as an append-only
// diff: only facts that changed since the last emission are rendered, so
// the injected message stays cache-safe (appended, never edited) and
// near-zero tokens on a quiet environment. See
// docs/superpowers/specs/2026-08-06-environment-context-design.md.
package envctx

import (
	"fmt"
	"strings"
)

// Pressure holds human-readable resource-pressure warnings; "" means the
// resource is nominal. A non-empty→empty transition renders a one-time
// "back to normal" line so the model never believes stale pressure.
type Pressure struct {
	Load   string `json:"load,omitempty"`
	Memory string `json:"memory,omitempty"`
	Disk   string `json:"disk,omitempty"`
}

// Snapshot is one observation of the session environment. All fields are
// strings so Snapshot is comparable with == (the nothing-changed fast path)
// and marshals directly into session meta.
type Snapshot struct {
	Cwd           string   `json:"cwd,omitempty"`             // absolute working directory
	LocalDateHour string   `json:"local_date_hour,omitempty"` // "2026-08-06 14:00 PDT"
	Sandbox       string   `json:"sandbox,omitempty"`         // always populated; "off" included
	GitBranch     string   `json:"git_branch,omitempty"`      // "" outside a git repo
	Pressure      Pressure `json:"pressure,omitzero"`
}

// State is the Tracker's persistable form, stored in session meta so resume
// stays silent when nothing changed across a restart.
type State struct {
	Last    Snapshot `json:"last"`
	HasSent bool     `json:"has_sent"`
}

// Tracker diffs successive Snapshots into rendered context blocks.
type Tracker struct {
	st State
}

func NewTracker(st State) *Tracker { return &Tracker{st: st} }

// State returns the persistable tracker state.
func (t *Tracker) State() State { return t.st }

// RenderDiff renders the changed fields of cur against the last emission,
// or every non-empty field on the first emission. It returns "" when there
// is nothing to say. A non-empty return updates the tracker state, so the
// caller must deliver the rendered block to the model.
func (t *Tracker) RenderDiff(cur Snapshot) string {
	first := !t.st.HasSent
	if !first && cur == t.st.Last {
		return ""
	}
	prev := t.st.Last

	var lines []string
	add := func(changed bool, line string) {
		if line != "" && (first || changed) {
			lines = append(lines, line)
		}
	}
	add(cur.Cwd != prev.Cwd, fmt.Sprintf("cwd: %q", cur.Cwd))
	add(cur.LocalDateHour != prev.LocalDateHour, "date: "+cur.LocalDateHour)
	add(cur.Sandbox != prev.Sandbox, "sandbox: "+cur.Sandbox)
	switch {
	case cur.GitBranch != "":
		add(cur.GitBranch != prev.GitBranch, "git branch: "+cur.GitBranch)
	case !first && prev.GitBranch != "":
		lines = append(lines, "git branch: (not in a git repository)")
	}
	for _, p := range []struct{ label, cur, prev string }{
		{"load", cur.Pressure.Load, prev.Pressure.Load},
		{"memory", cur.Pressure.Memory, prev.Pressure.Memory},
		{"disk", cur.Pressure.Disk, prev.Pressure.Disk},
	} {
		switch {
		case p.cur != "":
			add(p.cur != p.prev, p.cur)
		case !first && p.prev != "":
			lines = append(lines, p.label+" pressure: back to normal")
		}
	}

	if len(lines) == 0 {
		return ""
	}
	t.st = State{Last: cur, HasSent: true}
	return "<environment_context>\n" + strings.Join(lines, "\n") + "\n</environment_context>"
}
```

Note: pressure probe strings are self-labeled full lines (e.g.
`"memory pressure: warn level"`) produced by Task 2's probes; RenderDiff
emits them verbatim and only synthesizes the clear lines.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test ./internal/envctx/ -v`
Expected: PASS, all six tests.

- [ ] **Step 5: gofmt and commit**

```bash
gofmt -l agent/internal/envctx/   # must print nothing
git add agent/internal/envctx/
git commit -m "feat(envctx): Snapshot, Tracker, and diff renderer"
```

---

### Task 2: Collector with throttled, injected probes

**Files:**
- Create: `agent/internal/envctx/collector.go`
- Test: `agent/internal/envctx/collector_test.go`

**Interfaces:**
- Consumes: `Snapshot`, `Pressure` from Task 1.
- Produces:
  - `type Inputs struct { Cwd, Sandbox string }` (session-owned facts)
  - `type Probes struct { Now func() time.Time; GitBranch func(cwd string) string; Load, Memory func() string; Disk func(path string) string }`
  - `func NewCollector(p Probes) *Collector`
  - `func (c *Collector) Collect(in Inputs) Snapshot`
  - `const probeInterval = 5 * time.Minute`

- [ ] **Step 1: Write the failing tests**

```go
package envctx

import (
	"testing"
	"time"
)

// fakeProbes counts invocations and returns canned values.
type fakeProbes struct {
	now                time.Time
	loadCalls, memCalls, diskCalls int
	load, mem, disk    string
	branch             string
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
	if got.LocalDateHour != "2026-08-06 14:00 PDT" {
		t.Fatalf("hour truncation: %q", got.LocalDateHour)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test ./internal/envctx/ -run TestCollect -v`
Expected: FAIL to build — `undefined: Probes`.

- [ ] **Step 3: Write the implementation**

```go
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
	value   string
	probed  time.Time
	hasRun  bool
}

// Collector assembles Snapshots, rate-limiting the pressure probes.
type Collector struct {
	p                Probes
	load, mem, disk  cachedProbe
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
	return Snapshot{
		Cwd:           in.Cwd,
		LocalDateHour: now.Local().Format("2006-01-02 15:00 MST"),
		Sandbox:       sandbox,
		GitBranch:     branch,
		Pressure: Pressure{
			Load:   c.refresh(&c.load, now, c.p.Load),
			Memory: c.refresh(&c.mem, now, c.p.Memory),
			Disk:   c.refresh(&c.disk, now, func() string { return c.p.Disk(in.Cwd) }),
		},
	}
}
```

Careful: the `Disk` closure wraps a possibly-nil `c.p.Disk` — guard it:
in `Collect`, pass `nil` instead of the closure when `c.p.Disk == nil`
(the `TestCollectNilProbesReadNominal` test catches this if forgotten).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test ./internal/envctx/ -v`
Expected: PASS (Task 1 tests still green).

- [ ] **Step 5: gofmt and commit**

```bash
gofmt -l agent/internal/envctx/
git add agent/internal/envctx/
git commit -m "feat(envctx): throttled Collector with injected probes"
```

---

### Task 3: platform pressure probes

**Files:**
- Create: `agent/internal/envctx/probes_darwin.go`
- Create: `agent/internal/envctx/probes_linux.go`
- Create: `agent/internal/envctx/probes_other.go`
- Create: `agent/internal/envctx/probes_unix.go` (disk, shared darwin+linux)
- Test: `agent/internal/envctx/probes_test.go`

**Interfaces:**
- Produces: `func DefaultProbes() Probes` — Now/Load/Memory/Disk wired for
  the platform; `GitBranch` left nil (the session wires it, Task 5).
- Thresholds (Global Constraints): load1 > 2× NumCPU; darwin memory
  pressure level ≥ 2; linux MemAvailable < 5% of MemTotal; disk > 90%.

- [ ] **Step 1: Write the failing tests**

Pure parsing/threshold helpers get real tests; the syscall/exec edges are
thin and best-effort (error → ""). Put the helpers in the per-platform
files but keep their signatures platform-independent so this test file has
no build tag:

```go
package envctx

import "testing"

func TestLoadWarningThreshold(t *testing.T) {
	if got := loadWarning(3.9, 4); got != "" {
		t.Fatalf("below threshold must be nominal, got %q", got)
	}
	if got := loadWarning(8.5, 4); got != "load pressure: 8.5 (4 cores)" {
		t.Fatalf("above threshold: %q", got)
	}
}

func TestParseLoadAvgOutput(t *testing.T) {
	// darwin `sysctl -n vm.loadavg` prints "{ 2.16 3.57 4.34 }";
	// linux /proc/loadavg starts "2.16 3.57 4.34 ...".
	for _, in := range []string{"{ 2.16 3.57 4.34 }", "2.16 3.57 4.34 1/234 5678"} {
		got, ok := parseLoad1(in)
		if !ok || got != 2.16 {
			t.Fatalf("parseLoad1(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := parseLoad1("nonsense"); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestDiskWarningThreshold(t *testing.T) {
	if got := diskWarning(0.89); got != "" {
		t.Fatalf("below threshold must be nominal, got %q", got)
	}
	if got := diskWarning(0.93); got != "disk pressure: volume 93% full" {
		t.Fatalf("above threshold: %q", got)
	}
}

func TestDefaultProbesNeverPanic(t *testing.T) {
	p := DefaultProbes()
	_ = p.Load()
	_ = p.Memory()
	_ = p.Disk("/")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test ./internal/envctx/ -run 'TestLoad|TestParse|TestDisk|TestDefault' -v`
Expected: FAIL to build — `undefined: loadWarning`.

- [ ] **Step 3: Write the implementations**

Shared pure helpers — put these in `probes_unix.go` under
`//go:build darwin || linux`, alongside the disk probe (and duplicate the
tiny threshold helpers into `probes_other.go` guarded `//go:build !darwin
&& !linux` only if the test build demands it; simpler: put the pure
helpers in `envctx.go` with no build tag so the tests run everywhere):

```go
// In envctx.go (no build tag) — pure helpers shared by all platforms.

// parseLoad1 extracts the 1-minute load average from either darwin
// sysctl output ("{ 2.16 3.57 4.34 }") or /proc/loadavg ("2.16 3.57 ...").
func parseLoad1(s string) (float64, bool) {
	for _, f := range strings.Fields(strings.Trim(strings.TrimSpace(s), "{}")) {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

func loadWarning(load1 float64, cores int) string {
	if cores <= 0 || load1 <= float64(2*cores) {
		return ""
	}
	return fmt.Sprintf("load pressure: %.1f (%d cores)", load1, cores)
}

func diskWarning(frac float64) string {
	if frac <= 0.90 {
		return ""
	}
	return fmt.Sprintf("disk pressure: volume %d%% full", int(frac*100+0.5))
}
```

`probes_unix.go` (`//go:build darwin || linux`):

```go
package envctx

import "golang.org/x/sys/unix"

func diskProbe(path string) string {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return ""
	}
	used := float64(st.Blocks-st.Bavail) / float64(st.Blocks)
	return diskWarning(used)
}
```

`probes_darwin.go` (`//go:build darwin`):

```go
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
```

`probes_linux.go` (`//go:build linux`):

```go
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
```

`probes_other.go` (`//go:build !darwin && !linux`):

```go
package envctx

import "time"

// DefaultProbes on unsupported platforms reads everything as nominal.
func DefaultProbes() Probes { return Probes{Now: time.Now} }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test ./internal/envctx/ -v`
Expected: PASS. Also sanity-check the live darwin probes on this machine:
`cd agent && go run` a throwaway main is unnecessary — the
`TestDefaultProbesNeverPanic` test exercises them; additionally verify
`sysctl -n kern.memorystatus_vm_pressure_level` prints an integer in a
shell. If it does not exist on the implementation machine, the probe's
error path already reads nominal — leave it.

- [ ] **Step 5: gofmt and commit**

```bash
gofmt -l agent/internal/envctx/
git add agent/internal/envctx/
git commit -m "feat(envctx): platform pressure probes (darwin, linux, stub)"
```

---

### Task 4: schema.TurnEnvironment + expandHistory + meta state

**Files:**
- Modify: `agent/schema/turn.go` (TurnKind consts, ~line 51)
- Modify: `agent/schema/snapshot.go` (SessionMeta, after `PinnedNote` ~line 75)
- Modify: `agent/session_model_call.go` (`expandHistory`, the `TurnSteering` case ~line 1095)
- Test: `agent/session_model_call_env_test.go` (new)

**Interfaces:**
- Consumes: `envctx.State` from Task 1.
- Produces:
  - `schema.TurnEnvironment TurnKind = "ENVIRONMENT"`
  - `SessionMeta.EnvContext *envctx.State` json tag `env_context,omitempty`
  - expandHistory emits the turn's user-role message in position.

- [ ] **Step 1: Write the failing test**

```go
package agent

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestExpandHistoryEmitsEnvironmentTurnAsUserMessage(t *testing.T) {
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnEnvironment, llm.User("<environment_context>\ncwd: \"/w\"\n</environment_context>")),
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
	}
	got := expandHistory(turns, replayScope{})
	if len(got) != 2 {
		t.Fatalf("expanded %d messages, want 2: %+v", len(got), got)
	}
	if got[0].Role != llm.RoleUser || got[0].Text() == "" {
		t.Fatalf("environment message: %+v", got[0])
	}
	if got[1].Text() != "hello" {
		t.Fatalf("user input must follow environment context: %+v", got[1])
	}
}
```

(Check `schema.NewTurn`'s exact signature in `agent/schema/turn.go` before
writing — if it takes only kind+message this is right as written.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test . -run TestExpandHistoryEmitsEnvironmentTurn -v`
Expected: FAIL to build — `undefined: schema.TurnEnvironment`.

- [ ] **Step 3: Implement**

In `agent/schema/turn.go`, after `TurnHookCompleted`:

```go
	// TurnEnvironment is a harness-injected environment-context update (cwd,
	// date, sandbox, git branch, resource pressure) rendered as a diff by
	// agent/internal/envctx. Unlike the presentational kinds above it IS
	// model-bound: expandHistory passes its user-role message through, and
	// because it is only ever appended (never edited) it preserves
	// provider prompt caches. UIs render it as harness chrome, not user speech.
	TurnEnvironment TurnKind = "ENVIRONMENT"
```

In `agent/schema/snapshot.go`, import `primeradiant.com/serf/agent/internal/envctx`
and add to `SessionMeta` after `PinnedNote`:

```go
	// EnvContext is the environment-context tracker state (last emitted
	// snapshot), persisted so resume stays silent when nothing changed.
	EnvContext *envctx.State `json:"env_context,omitempty"`
```

In `agent/session_model_call.go` `expandHistory`, extend the steering case:

```go
	case schema.TurnSteering:
		...existing body unchanged...
	case schema.TurnEnvironment:
		// Environment context only ever lands at a turn boundary, so no
		// mid-tool-round deferral: pass the message straight through.
		history = append(history, t.Message)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test . -run TestExpandHistory -v && go build ./...`
Expected: PASS, whole module builds (schema→envctx import cycle would
surface here; envctx imports nothing from agent, so none exists).

- [ ] **Step 5: Commit**

```bash
gofmt -l agent/
git add agent/schema/turn.go agent/schema/snapshot.go agent/session_model_call.go agent/session_model_call_env_test.go
git commit -m "feat(agent): TurnEnvironment turn kind, model-bound via expandHistory"
```

---

### Task 5: session wiring — collect, inject, persist, resume

**Files:**
- Modify: `agent/session.go` (Session struct fields; `appendTurn` is at ~line 1119)
- Modify: `agent/session_lifecycle.go` (turn start, before the `TurnUserInput` appends at ~lines 1280/1282)
- Modify: `agent/session_client_mutation_queue.go` (the queued-input `TurnUserInput` append at ~line 997)
- Modify: `agent/session_init.go` (tracker/collector construction; restore from meta)
- Test: `agent/session_envctx_test.go` (new)

**Interfaces:**
- Consumes: `envctx.NewCollector/DefaultProbes/NewTracker/RenderDiff/State`,
  `schema.TurnEnvironment`, `SessionMeta.EnvContext`.
- Produces: `func (s *Session) maybeAppendEnvironmentContext()` — the single
  choke point every user-input path calls immediately before appending its
  `TurnUserInput` turn.

- [ ] **Step 1: Write the failing test**

Follow the constructor pattern of an existing session test
(`agent/session_test.go` has the canonical setup — copy its harness
helper rather than inventing one; most session tests build a Session with
a scripted provider). The behavioral assertions:

```go
package agent

// (imports per the copied harness)

func TestFirstUserTurnIsPrecededByEnvironmentContext(t *testing.T) {
	s := newTestSessionForEnvctx(t) // adapt the harness helper from session_test.go
	sendOneUserInput(t, s, "hello")

	turns := s.HistoryTurns() // use the accessor the harness exposes; check session.go for the exact name
	var envIdx, userIdx = -1, -1
	for i, tn := range turns {
		switch tn.Kind {
		case schema.TurnEnvironment:
			envIdx = i
		case schema.TurnUserInput:
			userIdx = i
		}
	}
	if envIdx == -1 || userIdx == -1 || envIdx != userIdx-1 {
		t.Fatalf("want ENVIRONMENT immediately before USER_INPUT, got env=%d user=%d", envIdx, userIdx)
	}
	if !strings.Contains(turns[envIdx].Message.Text(), "<environment_context>") {
		t.Fatalf("environment turn content: %q", turns[envIdx].Message.Text())
	}
}

func TestSecondUserTurnEmitsNoEnvironmentContextWhenUnchanged(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	sendOneUserInput(t, s, "hello")
	sendOneUserInput(t, s, "again")

	count := 0
	for _, tn := range s.HistoryTurns() {
		if tn.Kind == schema.TurnEnvironment {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("unchanged environment must emit exactly once, got %d", count)
	}
}

func TestEnvContextStatePersistsToMeta(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	sendOneUserInput(t, s, "hello")
	meta := loadMetaForTest(t, s) // schema.LoadSessionMeta on the session's state dir
	if meta.EnvContext == nil || !meta.EnvContext.HasSent {
		t.Fatalf("EnvContext not persisted: %+v", meta.EnvContext)
	}
}
```

The helpers (`newTestSessionForEnvctx`, `sendOneUserInput`,
`loadMetaForTest`) are thin adapters over whatever
`agent/session_test.go` already does — do not build a parallel harness.
For determinism, construct the session's collector with fake probes
(nil pressure probes, fixed `Now`) — expose that via the session config
seam added in Step 3.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test . -run TestFirstUserTurnIsPreceded -v`
Expected: FAIL — no `TurnEnvironment` turn found (`envIdx == -1`).

- [ ] **Step 3: Implement**

Session struct (`agent/session.go`), near the other unexported runtime
fields:

```go
	envCollector *envctx.Collector
	envTracker   *envctx.Tracker
```

Construction/restore (`agent/session_init.go`, where the Session is
assembled and meta is loaded): build the collector with `DefaultProbes()`
plus the git-branch probe wired through the session's execution
environment, and seed the tracker from meta:

```go
	probes := envctx.DefaultProbes()
	probes.GitBranch = func(cwd string) string {
		_, branch, _, _, _ := snapshotGit(s.env, cwd) // reuse agent/git_snapshot.go
		return branch
	}
	s.envCollector = envctx.NewCollector(probes)
	st := envctx.State{}
	if meta.EnvContext != nil {
		st = *meta.EnvContext
	}
	s.envTracker = envctx.NewTracker(st)
```

`snapshotGit` runs several git commands (status, log) beyond the branch
lookup; if profiling of `make test` shows it matters, extract a
branch-only helper in `git_snapshot.go` — otherwise reuse it as-is.
Check `snapshotGit`'s actual receiver/args at `agent/git_snapshot.go:47`
and the session's env field name before writing this.

For test determinism add a config seam mirroring how the session already
injects fakes (look for existing `func` fields on the session config /
deps struct — e.g. the `newWriter` pattern in `fork.go`): a
`envProbes *envctx.Probes` override that, when non-nil, replaces
`DefaultProbes()`.

The choke point (`agent/session.go`, near `appendTurn`):

```go
// maybeAppendEnvironmentContext records an ENVIRONMENT turn when the
// observed environment differs from the last emission. Called immediately
// before every TurnUserInput append so the context precedes the prompt it
// applies to; append-only by construction, so provider prompt caches
// survive it.
func (s *Session) maybeAppendEnvironmentContext() {
	if s.envCollector == nil || s.envTracker == nil {
		return
	}
	snap := s.envCollector.Collect(envctx.Inputs{
		Cwd:     s.env.WorkingDirectory(),
		Sandbox: s.cfg.Sandbox,
	})
	block := s.envTracker.RenderDiff(snap)
	if block == "" {
		return
	}
	s.appendTurn(schema.TurnEnvironment, llm.User(block))
	// Persist tracker state so resume stays silent when nothing changed.
	s.mutateMeta(func(m *schema.SessionMeta) {
		st := s.envTracker.State()
		m.EnvContext = &st
	})
}
```

`s.env`, `s.cfg.Sandbox`, and the meta-mutation helper: verify the exact
field/method names in `session.go` / `session_init.go` before writing
(the session persists meta somewhere on every turn already — find that
helper and use it; do not invent `mutateMeta` if a differently-named one
exists).

Call sites — immediately before each `TurnUserInput` append:
- `agent/session_lifecycle.go:1280` and `:1282` (one call above the
  `if queuedIdentity.ClientMutationID == ""` branch covers both)
- `agent/session_client_mutation_queue.go:997`

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test . -run 'TestFirstUserTurn|TestSecondUserTurn|TestEnvContextState' -v`
Expected: PASS.

Then the whole module: `cd agent && go test -short -count=1 ./...`
Expected: PASS. Existing session tests that assert exact turn sequences
will break — each breakage is a real call-site of the new behavior;
update those tests to expect the ENVIRONMENT turn rather than disabling
the feature in tests. If the number of breakages is large, say so in the
task report before bulk-updating.

- [ ] **Step 5: Commit**

```bash
gofmt -l agent/
git add -u agent/ && git add agent/session_envctx_test.go
git commit -m "feat(agent): inject diff-rendered environment context before user turns"
```

---

### Task 6: transcript rendering + full gate

**Files:**
- Modify: `agent/transcript_render.go` (the turn-kind switch, `TurnSteering` case at ~line 773)
- Test: extend `agent/transcript_render_test.go` (follow its existing table/golden pattern)
- Check: `cmd/serf-tui/internal/transcript/reducer.go` and the hub web
  projection for turn-kind switches (grep `TurnSteering` across `cmd/`);
  add the equivalent case wherever one exists so the block renders as
  harness chrome, not user speech.

**Interfaces:**
- Consumes: `schema.TurnEnvironment`.

- [ ] **Step 1: Write the failing test**

In `agent/transcript_render_test.go`, following the file's existing
pattern for steering (find the steering render test and mirror it):

```go
func TestRenderEnvironmentTurn(t *testing.T) {
	// Mirror the TurnSteering render test's setup; assert the heading and
	// compact-note treatment:
	// "## Turn N — Environment" and the block body present under full render.
}
```

Write it concretely against the file's real helpers — the steering test
shows the exact harness (entry construction, options, golden vs inline
expectations). The assertion: heading `— Environment`, body rendered via
`writeCompactNote(b, "Environment", ...)`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test . -run TestRenderEnvironmentTurn -v`
Expected: FAIL — unknown kind falls through to the switch's default.

- [ ] **Step 3: Implement**

In `agent/transcript_render.go` after the `TurnSteering` case:

```go
	case schema.TurnEnvironment:
		fmt.Fprintf(b, "\n## Turn %d — Environment\n", seq)
		writeCompactNote(b, "Environment", e.Turn, wantFullTurn(opt, seq))
```

Then `grep -rn "TurnSteering" cmd/ agent/ --include="*.go" | grep -v _test`
and add a `TurnEnvironment` arm to every projection switch found (TUI
reducer, hub web formatting, `transcript_read.go` if it filters by kind).
Any switch with a sane default that already renders unknown kinds
legibly may be left alone — decide per site and note the decision in the
commit message.

- [ ] **Step 4: Run the full gate**

```bash
make test          # from repo root; all modules + frontend gate
make lint-gofmt
```
Expected: PASS, pristine output.

- [ ] **Step 5: Commit**

```bash
git add -u
git commit -m "feat(agent): render ENVIRONMENT turns as harness chrome"
```

---

## Self-review notes (already applied)

- Spec coverage: types/diff (Task 1), collection+throttle (Task 2), probes
  (Task 3), turn kind + history + meta (Task 4), injection + resume
  (Task 5), UI rendering + gate (Task 6). Out-of-scope items from the spec
  have no tasks, correctly.
- Line numbers are anchors from 2026-08-06; re-locate by symbol if drifted.
