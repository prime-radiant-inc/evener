package testredundancy

import (
	"fmt"
	"io"
	"strings"
)

// CoverageDiff compares a package's whole-suite profiles from before and after
// a deletion batch.
type CoverageDiff struct {
	BeforeCovered, BeforeTotal int
	AfterCovered, AfterTotal   int
	Lost, Gained               []BlockRef // covered on one side only
	LostStmts, GainedStmts     int
	Noise                      int // statements two identical runs can differ by
}

// DiffCoverage lists every block covered before and not after, and the
// reverse. noise is the run-to-run variation the batch may lose.
func DiffCoverage(before, after Profile, noise int) CoverageDiff {
	d := CoverageDiff{
		BeforeCovered: before.Covered(), BeforeTotal: before.Total(),
		AfterCovered: after.Covered(), AfterTotal: after.Total(),
		Noise: noise,
	}
	d.Lost, d.LostStmts = before.coveredMinus(after)
	d.Gained, d.GainedStmts = after.coveredMinus(before)
	return d
}

// WithinNoise holds when the batch lost no more statements than two runs of
// the same suite differ by, and the production statement count is unchanged.
func (d CoverageDiff) WithinNoise() bool {
	return d.LostStmts <= d.Noise && d.BeforeTotal == d.AfterTotal
}

// WriteProof renders the coverage diff and mutation results as the Markdown a
// deletion PR's description carries. display maps profile file names to
// source paths.
func WriteProof(w io.Writer, d CoverageDiff, muts []MutationResult, display func(string) string) {
	verdict := "within"
	if !d.WithinNoise() {
		verdict = "OUTSIDE"
	}
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	p("## Coverage\n\n")
	p("| | Covered | Total | Percent |\n|---|---|---|---|\n")
	p("| Before | %d | %d | %.2f%% |\n", d.BeforeCovered, d.BeforeTotal, percent(d.BeforeCovered, d.BeforeTotal))
	p("| After | %d | %d | %.2f%% |\n\n", d.AfterCovered, d.AfterTotal, percent(d.AfterCovered, d.AfterTotal))
	p("Lost %d statements, gained %d: %s the noise band of %d.\n\n", d.LostStmts, d.GainedStmts, verdict, d.Noise)
	writeBlocks(w, "Covered before and not after", d.Lost, display)
	writeBlocks(w, "Covered after and not before", d.Gained, display)

	p("## Mutations\n\n")
	if len(muts) == 0 {
		p("No mutation results.\n")
		return
	}
	counts := map[Outcome]int{}
	p("| Mutation | File | Change | Tests run | Outcome | Failing tests |\n|---|---|---|---|---|---|\n")
	for _, m := range muts {
		counts[m.Outcome]++
		outcome := string(m.Outcome)
		if m.Detail != "" {
			outcome += ": " + m.Detail
		}
		p("| %s | %s | `%s` → `%s` | `%s` | %s | %s |\n", m.Name, m.File, cell(m.Old), cell(m.New), m.Run, cell(outcome), strings.Join(m.Failed, ", "))
	}
	p("\n%d caught, %d survived, %d invalid.\n", counts[OutcomeCaught], counts[OutcomeSurvived], counts[OutcomeInvalid])
}

func writeBlocks(w io.Writer, title string, blocks []BlockRef, display func(string) string) {
	if len(blocks) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "%s:\n\n", title)
	for _, b := range blocks {
		b.File = display(b.File)
		_, _ = fmt.Fprintf(w, "- %s\n", b)
	}
	_, _ = fmt.Fprintln(w)
}

func percent(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

// cell keeps a value on one Markdown table row.
func cell(s string) string {
	return strings.NewReplacer("\n", " ", "|", `\|`, "`", "'").Replace(s)
}
