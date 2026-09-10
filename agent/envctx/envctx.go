// Package envctx renders per-turn environment context as an append-only
// diff: only facts that changed since the last emission are rendered, so
// the injected message stays cache-safe (appended, never edited) and
// near-zero tokens on a quiet environment. See
// docs/superpowers/specs/2026-08-06-environment-context-design.md.
package envctx

import (
	"fmt"
	"math"
	"strconv"
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

// ReplayBlock folds one durable environment block into the tracker state.
// Blocks are diffs, so callers replay them in transcript order starting from
// the effective history boundary. This closes the write-before-metadata crash window
// without treating the rendered model text as a new observation.
func (t *Tracker) ReplayBlock(block string) bool {
	if t == nil || !strings.Contains(block, "<environment_context>") {
		return false
	}
	start := strings.Index(block, "<environment_context>") + len("<environment_context>")
	end := strings.Index(block[start:], "</environment_context>")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(block[start:start+end], "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "cwd: "):
			if value, err := strconv.Unquote(strings.TrimPrefix(line, "cwd: ")); err == nil {
				t.st.Last.Cwd = value
			}
		case strings.HasPrefix(line, "date: "):
			t.st.Last.LocalDateHour = strings.TrimPrefix(line, "date: ")
		case strings.HasPrefix(line, "sandbox: "):
			t.st.Last.Sandbox = strings.TrimPrefix(line, "sandbox: ")
		case strings.HasPrefix(line, "git branch: "):
			value := strings.TrimPrefix(line, "git branch: ")
			if value == "(not in a git repository)" {
				value = ""
			}
			t.st.Last.GitBranch = value
		case strings.HasPrefix(line, "load pressure: "):
			t.st.Last.Pressure.Load = pressureValue(line, "load")
		case strings.HasPrefix(line, "memory pressure: "):
			t.st.Last.Pressure.Memory = pressureValue(line, "memory")
		case strings.HasPrefix(line, "disk pressure: "):
			t.st.Last.Pressure.Disk = pressureValue(line, "disk")
		}
	}
	t.st.HasSent = true
	return true
}

func pressureValue(line, name string) string {
	prefix := name + " pressure: "
	value := strings.TrimPrefix(line, prefix)
	if value == "back to normal" {
		return ""
	}
	return line
}

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

// parseLoad1 extracts the 1-minute load average from either darwin
// sysctl output ("{ 2.16 3.57 4.34 }") or /proc/loadavg ("2.16 3.57 ...").
func parseLoad1(s string) (float64, bool) {
	fields := strings.Fields(strings.Trim(strings.TrimSpace(s), "{}"))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	// ParseFloat accepts "NaN" and "Inf", which are not load averages. Reporting
	// ok for them cannot be recovered downstream: every comparison against NaN
	// is false, so loadWarning's `load1 <= 2*cores` guard falls through and
	// renders "load pressure: NaN (N cores)" into the model's context.
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
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
