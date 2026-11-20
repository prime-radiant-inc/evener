// Package envctx renders per-turn environment context as an append-only
// diff: only facts that changed since the last emission are rendered, so
// the injected message stays cache-safe (appended, never edited) and
// near-zero tokens on a quiet environment. See
// docs/superpowers/specs/2026-08-06-environment-context-design.md.
package envctx

import (
	"cmp"
	"fmt"
	"math"
	"slices"
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

	Model           string `json:"model,omitempty"`
	KnowledgeCutoff string `json:"knowledge_cutoff,omitempty"`
	CommitTrailer   string `json:"commit_trailer,omitempty"`
	ScratchDir      string `json:"scratch_dir,omitempty"`
	CallableTools   string `json:"callable_tools,omitempty"`
	AvailableSkills string `json:"available_skills,omitempty"`
	RunLifetime     string `json:"run_lifetime,omitempty"`
}

// State is the Tracker's persistable form, stored in session meta so resume
// stays silent when nothing changed across a restart.
type State struct {
	Last    Snapshot `json:"last"`
	HasSent bool     `json:"has_sent"`
}

// Tracker diffs successive Snapshots into rendered context blocks.
type Tracker struct {
	st            State
	base          Snapshot // the runtime facts the model already knows, so State replays purely
	fullInventory bool     // emit authoritative tools:/skills: once (legacy restore without baseline)
}

func NewTracker(st State) *Tracker { return &Tracker{st: st} }

func (t *Tracker) ResetState() { t.st = State{} }

// RequireFullInventory asks the next RenderDiff to emit authoritative tools:
// and skills: lines, superseding a legacy launch prompt whose baseline was
// never recorded. Cleared once emitted or replayed.
func (t *Tracker) RequireFullInventory() {
	if t != nil {
		t.fullInventory = true
	}
}

// State returns the persistable tracker state.
func (t *Tracker) State() State { return t.st }

func (t *Tracker) SeedRuntime(rt Snapshot) {
	b := &t.base
	b.GitBranch = cmp.Or(b.GitBranch, rt.GitBranch)
	b.Model, b.KnowledgeCutoff = cmp.Or(b.Model, rt.Model), cmp.Or(b.KnowledgeCutoff, rt.KnowledgeCutoff)
	b.CommitTrailer, b.ScratchDir = cmp.Or(b.CommitTrailer, rt.CommitTrailer), cmp.Or(b.ScratchDir, rt.ScratchDir)
	b.CallableTools, b.AvailableSkills = cmp.Or(b.CallableTools, rt.CallableTools), cmp.Or(b.AvailableSkills, rt.AvailableSkills)
	b.RunLifetime = cmp.Or(b.RunLifetime, rt.RunLifetime)
}

// RuntimeDrifted reports whether cur holds a runtime fact base does not.
func (t *Tracker) RuntimeDrifted(cur Snapshot) bool {
	return (&Tracker{st: State{Last: cur, HasSent: true}, base: t.base, fullInventory: t.fullInventory}).RenderDiff(cur) != ""
}

// ReplayBlock folds one durable environment block into the tracker state.
// Blocks are diffs, so callers replay them in transcript order starting from
// the effective history boundary. This closes the write-before-metadata crash window
// without treating the rendered model text as a new observation.
func (t *Tracker) ReplayBlock(block string) bool {
	if t == nil {
		return false
	}
	body, ok := strings.CutPrefix(strings.TrimSpace(block), "<environment_context>")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, "</environment_context>")
	if !ok {
		return false
	}
	var cwd, date, sandbox bool
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "cwd: "):
			if value, err := strconv.Unquote(strings.TrimPrefix(line, "cwd: ")); err == nil {
				t.st.Last.Cwd = value
				cwd = true
			}
		case strings.HasPrefix(line, "date: "):
			t.st.Last.LocalDateHour = strings.TrimPrefix(line, "date: ")
			date = true
		case strings.HasPrefix(line, "sandbox: "):
			t.st.Last.Sandbox = strings.TrimPrefix(line, "sandbox: ")
			sandbox = true
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
		default:
			t.applyNameLine(line)
		}
	}
	// Compaction may retain a changed-field block while removing its full
	// baseline. Such a tail still needs a full emission before the next input.
	t.st.HasSent = t.st.HasSent || (cwd && date && sandbox)
	return true
}

func (t *Tracker) applyNameLine(line string) {
	fields := map[string]*string{"model": &t.base.Model, "knowledge cutoff": &t.base.KnowledgeCutoff, "commit trailer": &t.base.CommitTrailer, "scratch dir": &t.base.ScratchDir, "tools": &t.base.CallableTools, "skills": &t.base.AvailableSkills, "run lifetime": &t.base.RunLifetime}
	if label, value, ok := strings.Cut(line, ": "); ok && fields[label] != nil {
		if (label == "tools" || label == "skills") && value == "(none)" {
			value = ""
		}
		*fields[label] = value // a one-value fact, not a delta
		if label == "tools" || label == "skills" {
			t.fullInventory = false
		}
		return
	}
	verb, rest, _ := strings.Cut(line, " ")
	noun, listed, _ := strings.Cut(rest, ": ")
	if field := fields[noun]; field != nil {
		added := splitNames(listed) // split once: DeleteFunc asks per existing name
		switch verb {
		case "added":
			*field = JoinNames(append(splitNames(*field), added...))
		case "removed":
			*field = JoinNames(slices.DeleteFunc(splitNames(*field), func(n string) bool { return slices.Contains(added, n) }))
		}
	}
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
	full := t.fullInventory
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
	case prev.GitBranch != "" || (first && t.base.GitBranch != ""):
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

	say := func(label, v, blank string, dst *string) {
		if v == "" && *dst != "" {
			v = blank
		}
		if v != *dst && v != "" {
			lines, *dst = append(lines, label+v), v
		}
	}
	say("model: ", cur.Model, "(none)", &t.base.Model)
	say("knowledge cutoff: ", cur.KnowledgeCutoff, "(none)", &t.base.KnowledgeCutoff)
	say("scratch dir: ", cur.ScratchDir, "(none)", &t.base.ScratchDir)
	say("commit trailer: ", cur.CommitTrailer, "(disabled)", &t.base.CommitTrailer)
	say("run lifetime: ", cur.RunLifetime, "(none)", &t.base.RunLifetime)
	if full && cur.CommitTrailer == "" && t.base.CommitTrailer == "" {
		lines = append(lines, "commit trailer: (disabled)")
		t.base.CommitTrailer = "(disabled)"
	}
	for _, n := range []struct {
		label, cur string
		dst        *string
	}{{"tools", cur.CallableTools, &t.base.CallableTools}, {"skills", cur.AvailableSkills, &t.base.AvailableSkills}} {
		if t.fullInventory {
			val := n.cur
			if val == "" {
				val = "(none)"
			}
			lines, *n.dst = append(lines, n.label+": "+val), n.cur
			continue
		}
		if d := nameDelta(n.label, n.cur, *n.dst); len(d) > 0 {
			lines, *n.dst = append(lines, d...), n.cur
		}
	}
	t.fullInventory = false

	if len(lines) == 0 {
		return ""
	}
	t.st.Last.Cwd, t.st.Last.LocalDateHour, t.st.Last.Sandbox = cur.Cwd, cur.LocalDateHour, cur.Sandbox
	t.st.Last.GitBranch, t.st.Last.Pressure, t.st.HasSent = cur.GitBranch, cur.Pressure, true
	return "<environment_context>\n" + strings.Join(lines, "\n") + "\n</environment_context>"
}

func JoinNames(names []string) string {
	return strings.Join(slices.Compact(slices.Sorted(slices.Values(names))), ", ")
}
func splitNames(list string) []string {
	return slices.DeleteFunc(strings.Split(list, ", "), func(n string) bool { return n == "" })
}
func nameDelta(label, cur, prev string) (lines []string) {
	for _, d := range []struct{ new, old, verb string }{{cur, prev, "added"}, {prev, cur, "removed"}} {
		have := splitNames(d.old)
		for _, n := range splitNames(d.new) {
			if !slices.Contains(have, n) {
				lines = append(lines, d.verb+" "+label+": "+n)
			}
		}
	}
	return lines
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
