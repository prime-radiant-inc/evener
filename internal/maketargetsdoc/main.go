// main.go drives ParseFamily, Render and RewriteRegion end to end: it
// walks make/*.mk, maps each family's stem to the docs/developing-evener
// doc that owns it, and regenerates that doc's marked region in place.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// stemToDoc maps a make/<stem>.mk family file to the docs/developing-evener
// doc whose "## Targets" region it feeds (spec §3). repo.mk is the one
// exception: its targets (build, help, clean, generate) belong in the
// directory index rather than a family doc of its own.
var stemToDoc = map[string]string{
	"building": "building.md",
	"testing":  "testing.md",
	"linting":  "linting.md",
	"coverage": "coverage.md",
	"fuzzing":  "fuzzing.md",
	"repo":     "README.md",
}

func main() {
	if err := generate("."); err != nil {
		fmt.Fprintln(os.Stderr, "maketargetsdoc:", err)
		os.Exit(1)
	}
}

// generate regenerates every family doc's marked region under root: it
// reads root/make/*.mk and rewrites the matching doc under
// root/docs/developing-evener.
//
// It processes every family it can rather than stopping at the first
// problem: as of this writing make/repo.mk carries no ## annotations yet
// (a later change owns it), and that must not prevent the other five
// families' docs from being regenerated. Every problem found — a family
// that fails to parse, a .mk with no destination doc, or a doc region
// naming a .mk that does not exist — is collected and returned together;
// generate returns nil only if nothing went wrong anywhere.
func generate(root string) error {
	mkDir := filepath.Join(root, "make")
	mkPaths, err := filepath.Glob(filepath.Join(mkDir, "*.mk"))
	if err != nil {
		return err
	}
	sort.Strings(mkPaths)

	docDir := filepath.Join(root, "docs", "developing-evener")

	var errs []error
	stems := make(map[string]bool, len(mkPaths))
	for _, mkPath := range mkPaths {
		stem := strings.TrimSuffix(filepath.Base(mkPath), ".mk")
		stems[stem] = true

		if err := generateOne(docDir, mkPath, stem); err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, checkOrphanRegions(docDir, stems)...)

	return errors.Join(errs...)
}

// generateOne regenerates the one family doc that make/<stem>.mk (at
// mkPath) feeds.
func generateOne(docDir, mkPath, stem string) error {
	docName, ok := stemToDoc[stem]
	if !ok {
		return fmt.Errorf("%s has no destination doc; add %q to stemToDoc", mkPath, stem)
	}

	src, err := os.ReadFile(mkPath)
	if err != nil {
		return err
	}
	targets, err := ParseFamily(src)
	if err != nil {
		return fmt.Errorf("%s: %w", mkPath, err)
	}
	body := Render(targets)

	docPath := filepath.Join(docDir, docName)
	docSrc, err := os.ReadFile(docPath)
	if err != nil {
		return fmt.Errorf("%s: %w", docPath, err)
	}
	newDoc, err := RewriteRegion(docSrc, stem, body)
	if err != nil {
		return fmt.Errorf("%s: %w", docPath, err)
	}
	return os.WriteFile(docPath, newDoc, 0o644)
}

// regionFamilyPattern extracts the family name a doc's GENERATED marker
// names — "linting" from "Edit make/linting.mk, then run ...".
var regionFamilyPattern = regexp.MustCompile(`<!-- BEGIN GENERATED: make targets\. Edit make/([A-Za-z0-9_-]+)\.mk, then run`)

// checkOrphanRegions reverses generateOne's direction: every doc under
// docDir that carries a GENERATED marker must name a family present in
// stems (a make/*.mk file that actually exists), so a stale or misspelled
// marker left behind by a rename or deletion is caught rather than quietly
// generating nothing forever.
func checkOrphanRegions(docDir string, stems map[string]bool) []error {
	docPaths, err := filepath.Glob(filepath.Join(docDir, "*.md"))
	if err != nil {
		return []error{err}
	}

	var errs []error
	for _, docPath := range docPaths {
		docSrc, err := os.ReadFile(docPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m := regionFamilyPattern.FindSubmatch(docSrc)
		if m == nil {
			continue // no GENERATED region in this doc at all: not every doc has one.
		}
		family := string(m[1])
		if !stems[family] {
			errs = append(errs, fmt.Errorf("%s: marked region references make/%s.mk, which does not exist", docPath, family))
		}
	}
	return errs
}
