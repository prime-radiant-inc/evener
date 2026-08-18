// Package report is the output discipline the dev-tooling runners share: a
// run owes its reader exactly one summary line, whatever the run does, and
// failures speak a fixed five-word vocabulary so humans and log scrapers can
// read a failure by its class before its detail. The shapes here are output
// contracts consumed by the merge gate and CI log readers; changing one is an
// interface change, not a wording tweak.
package report

import (
	"fmt"
	"io"
)

// Category is the failure class a summary line leads with: setup for a run
// that never got as far as a unit, not-checked for an unusable tool, findings
// for real results, results-lost for verdicts that did not survive the run,
// interrupted for a signal.
type Category int

const (
	Setup Category = iota
	NotChecked
	Findings
	ResultsLost
	Interrupted
)

func (c Category) String() string {
	switch c {
	case Setup:
		return "setup"
	case NotChecked:
		return "not-checked"
	case Findings:
		return "findings"
	case ResultsLost:
		return "results-lost"
	case Interrupted:
		return "interrupted"
	}
	return fmt.Sprintf("category(%d)", int(c))
}

// Reporter writes a tool's single summary line. Every failing exit routes
// through Fail and the one success through Pass, so no run can end silently
// and none can summarize twice: a second summary is a control-flow bug in
// the calling tool, and it panics so the tool's own tests catch it.
type Reporter struct {
	w          io.Writer
	tool       string
	summarized bool
}

func New(w io.Writer, tool string) *Reporter {
	return &Reporter{w: w, tool: tool}
}

// Pass writes `PASS <tool> (<detail>)`.
func (r *Reporter) Pass(detail string) {
	r.summarize()
	_, _ = fmt.Fprintf(r.w, "PASS %s (%s)\n", r.tool, detail)
}

// Fail writes `FAIL <tool> (<category>: <detail>)`.
func (r *Reporter) Fail(c Category, detail string) {
	r.summarize()
	_, _ = fmt.Fprintf(r.w, "FAIL %s (%s: %s)\n", r.tool, c, detail)
}

// Summarized reports whether the run's summary line has been written.
func (r *Reporter) Summarized() bool { return r.summarized }

func (r *Reporter) summarize() {
	if r.summarized {
		panic("report: a run summarized twice; every run owes its reader exactly one summary line")
	}
	r.summarized = true
}

// Replay writes a failed unit's whole log under a `----- unit -----` fence.
func Replay(w io.Writer, unit string, log io.Reader) {
	_, _ = fmt.Fprintf(w, "----- %s -----\n", unit)
	_, _ = io.Copy(w, log)
}

// RetainedPointer names the directory a failing run kept its logs in.
func RetainedPointer(w io.Writer, dir string) {
	_, _ = fmt.Fprintf(w, "full logs: %s\n", dir)
}
