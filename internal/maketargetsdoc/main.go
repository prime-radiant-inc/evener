// main.go drives ParseFamily, Render and RewriteRegion end to end: it
// walks make/*.mk, maps each family's stem to the docs/developing-evener
// doc that owns it, and regenerates that doc's marked region in place.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// stemToDoc maps a make/<stem>.mk family file to the docs/developing-evener
// doc whose "## Targets" region it feeds (spec §3). repo.mk is the one
// exception: its targets (tools, generate, clean, refresh-model-catalog,
// help) belong in the directory index rather than a family doc of its own.
var stemToDoc = map[string]string{
	"building": "building.md",
	"testing":  "testing.md",
	"linting":  "linting.md",
	"coverage": "coverage.md",
	"fuzzing":  "fuzzing.md",
	"repo":     "README.md",
}

func main() {
	mode := flag.String("mode", "generate", `"generate" (default) rewrites the doc tables; "help" prints make help's grouped target listing to stdout`)
	// go generate runs a directive with the cwd set to the package's own
	// directory, not the repo root, so doc.go's directive passes -root ../..
	// to point back at the tree that holds make/ and docs/. The default suits
	// a hand-run `go run ./internal/maketargetsdoc` from the repo root.
	root := flag.String("root", ".", "repository root holding make/ and docs/developing-evener/")
	flag.Parse()

	var err error
	switch *mode {
	case "generate":
		err = generate(*root)
	case "help":
		err = printHelp(os.Stdout, *root)
	default:
		fmt.Fprintf(os.Stderr, "maketargetsdoc: unknown -mode %q; want \"generate\" or \"help\"\n", *mode)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "maketargetsdoc:", err)
		os.Exit(1)
	}
}

// globFamilyFiles returns every make/*.mk family file under root, sorted by
// path — the shared file-discovery step both `generate` (doc regeneration)
// and `loadFamilies` (help.go, `-mode help`) build on, so there is one
// definition of "which files are family files" rather than two that can
// drift apart.
//
// Matching nothing is an error, not an empty result. filepath.Glob returns
// (nil, nil) for a pattern that matches no file, so a wrong root — the
// generator run from its own package directory, or from anywhere but the
// repo root — would otherwise regenerate nothing, write nothing, and exit
// zero. lint-generated would then diff six unchanged docs and report green
// forever while checking nothing, which is the exact hollow-gate failure
// this generator exists to close.
func globFamilyFiles(root string) ([]string, error) {
	pattern := filepath.Join(root, "make", "*.mk")
	mkPaths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if len(mkPaths) == 0 {
		return nil, fmt.Errorf("%s matched no family files; run this from the repository root or pass -root", pattern)
	}
	sort.Strings(mkPaths)
	return mkPaths, nil
}

// generate regenerates every family doc's marked region under root: it
// reads root/make/*.mk and rewrites the matching doc under
// root/docs/developing-evener.
//
// It processes every family it can rather than stopping at the first
// problem: a family that fails to parse must not prevent the other
// families' docs from being regenerated. Every problem found — a family
// that fails to parse, a .mk with no destination doc, or a doc region
// naming a .mk that does not exist — is collected and returned together;
// generate returns nil only if nothing went wrong anywhere.
func generate(root string) error {
	mkPaths, err := globFamilyFiles(root)
	if err != nil {
		return err
	}

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
