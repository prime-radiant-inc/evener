// help.go implements `-mode help`: `make help`'s entire implementation.
// It groups every make/*.mk family's annotated targets and prints one line
// per target. It builds directly on ParseFamily (parse.go) — the same
// parser Render (render.go) feeds the doc tables from — so `make help` and
// the generated docs can never disagree about what a target's summary is.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// family pairs one make/<stem>.mk's annotated targets, in file order, with
// the stem that names it.
type family struct {
	Stem    string
	Targets []Target
}

// loadFamilies parses every make/*.mk file under root (via familyFiles,
// the same file-discovery generate uses), returning one family per file in
// sorted-stem order.
//
// Like generate (main.go), it processes every family it can rather than
// stopping at the first parse failure: the families that did parse are
// still returned alongside a joined error naming every family that didn't,
// so one bad annotation does not hide `make help`'s output for everything
// else.
func loadFamilies(root string) ([]family, error) {
	mkPaths, err := familyFiles(root)
	if err != nil {
		return nil, err
	}

	var families []family
	var errs []error
	for _, mkPath := range mkPaths {
		stem := strings.TrimSuffix(filepath.Base(mkPath), ".mk")
		src, err := os.ReadFile(mkPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		targets, err := ParseFamily(src)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mkPath, err))
			continue
		}
		families = append(families, family{Stem: stem, Targets: targets})
	}
	return families, errors.Join(errs...)
}

// renderHelp turns families into make help's listing: one family per
// block, separated by a blank line, each target rendered as its name
// (left-padded to the widest name in that family) followed by its summary.
// The padding width is computed per family so one family's unusually long
// name does not stretch every other family's column.
func renderHelp(families []family) string {
	var b strings.Builder
	for i, fam := range families {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s:\n", fam.Stem)

		width := 0
		for _, t := range fam.Targets {
			if len(t.Name) > width {
				width = len(t.Name)
			}
		}
		for _, t := range fam.Targets {
			fmt.Fprintf(&b, "  %-*s  %s\n", width, t.Name, t.Summary)
		}
	}
	return b.String()
}

// helpHeader introduces make help's listing: plain usage, then how the
// targets below are grouped.
const helpHeader = "usage: make [target]\n\n" +
	"Targets are grouped by family (make/<family>.mk); run `make <target>`\n" +
	"for any name below.\n\n"

// printHelp implements `-mode help`: load every family under root and
// write the header plus the grouped listing to w.
func printHelp(w io.Writer, root string) error {
	families, err := loadFamilies(root)
	if _, writeErr := io.WriteString(w, helpHeader+renderHelp(families)); writeErr != nil {
		return writeErr
	}
	return err
}
