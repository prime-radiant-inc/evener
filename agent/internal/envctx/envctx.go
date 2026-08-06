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
