package fuzzharvest

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// harvesterSeedName matches the content-addressed names Emitter gives its
// files (a full sha256). Files Go's fuzzing toolchain wrote (crashers and
// minimized campaign corpus entries) carry a shorter hash name and are never
// pruned, since they are regressions.
var harvesterSeedName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// coverageSet is the set of covered statement blocks, keyed by their position
// in a Go cover profile.
type coverageSet map[string]bool

// addsTo reports whether c covers a block that total does not.
func (c coverageSet) addsTo(total coverageSet) bool {
	for b := range c {
		if !total[b] {
			return true
		}
	}
	return false
}

// selectByCoverage returns the files worth keeping: walking them in the given
// order, a file stays only if it covers a block that the baseline and the
// earlier kept files do not. It also returns the union of everything covered,
// kept or not.
func selectByCoverage(files []string, baseline coverageSet, coverageOf func(file string) (coverageSet, error)) (kept []string, all coverageSet, err error) {
	total := coverageSet{}
	all = coverageSet{}
	for b := range baseline {
		total[b] = true
		all[b] = true
	}
	for _, f := range files {
		c, err := coverageOf(f)
		if err != nil {
			return nil, nil, err
		}
		for b := range c {
			all[b] = true
		}
		if !c.addsTo(total) {
			continue
		}
		for b := range c {
			total[b] = true
		}
		kept = append(kept, f)
	}
	return kept, all, nil
}

// parseCoverProfile reads the covered blocks (count > 0) from a Go cover profile.
func parseCoverProfile(path string) (coverageSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	set := coverageSet{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 3 && fields[2] != "0" {
			set[fields[0]] = true
		}
	}
	return set, sc.Err()
}

// instrumentedPackages lists, comma-joined for -coverpkg, every non-standard
// package the package in pkgDir builds with under -tags evenerfuzz: the module's own code plus third-party dependencies such as the
// schema validator that a target's oracle runs inside.
func instrumentedPackages(pkgDir string) (string, error) {
	out, err := exec.Command("go", "-C", pkgDir, "list", "-tags", "evenerfuzz", "-deps",
		"-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").Output()
	if err != nil {
		return "", fmt.Errorf("go list deps of %s: %w", pkgDir, err)
	}
	return strings.Join(strings.Fields(string(out)), ","), nil
}

// pruneCorpus drops the harvester-written seeds of one target corpus
// directory (<package>/testdata/fuzz/<FuzzName>) that add no coverage over the
// seeds in the target's source, the toolchain-written files, and the smaller
// seeds kept before them, so the covered blocks are the same before and after.
// Coverage is measured across the module and its third-party dependencies
// under -tags evenerfuzz. Under dryRun it deletes nothing. It returns a one-line report.
func pruneCorpus(corpusDir string, dryRun bool) (string, error) {
	corpusDir, err := filepath.Abs(corpusDir)
	if err != nil {
		return "", err
	}
	target := filepath.Base(corpusDir)
	pkgDir := filepath.Dir(filepath.Dir(filepath.Dir(corpusDir)))

	tmp, err := os.MkdirTemp("", "fuzz-prune-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) //nolint:errcheck

	coverPkgs, err := instrumentedPackages(pkgDir)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(tmp, "target.test")
	build := exec.Command("go", "-C", pkgDir, "test", "-tags", "evenerfuzz", "-c", "-cover",
		"-coverpkg", coverPkgs, "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build %s: %w\n%s", pkgDir, err, out)
	}

	profile := filepath.Join(tmp, "cover.out")
	// replay runs the subtests matching pattern and returns what they covered.
	// A corpus file must actually run: a directory left behind by a renamed or
	// removed target would otherwise replay nothing, look redundant, and be
	// pruned.
	replay := func(pattern string, mustRun bool) (coverageSet, error) {
		run := exec.Command(bin, "-test.v", "-test.run", "^"+target+"$/^"+pattern+"$", "-test.coverprofile", profile)
		run.Dir = pkgDir
		out, err := run.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("replay %s: %w\n%s", pattern, err, out)
		}
		if mustRun && !strings.Contains(string(out), "--- PASS: "+target+"/"+pattern) {
			return nil, fmt.Errorf("replay %s: no %s subtest ran; is the corpus directory stale?", pattern, target)
		}
		return parseCoverProfile(profile)
	}
	coverageOf := func(file string) (coverageSet, error) { return replay(file, true) }

	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return "", err
	}
	baseline, err := replay("seed#[0-9]+", false)
	if err != nil {
		return "", err
	}
	var prunable []string
	for _, e := range entries {
		if harvesterSeedName.MatchString(e.Name()) {
			prunable = append(prunable, e.Name())
			continue
		}
		c, err := coverageOf(e.Name())
		if err != nil {
			return "", err
		}
		for b := range c {
			baseline[b] = true
		}
	}
	// Smaller seeds first, so the kept set is the cheapest to read and replay.
	size := func(name string) int64 {
		info, err := os.Stat(filepath.Join(corpusDir, name))
		if err != nil {
			return 0
		}
		return info.Size()
	}
	sort.Slice(prunable, func(i, j int) bool {
		if si, sj := size(prunable[i]), size(prunable[j]); si != sj {
			return si < sj
		}
		return prunable[i] < prunable[j]
	})

	kept, all, err := selectByCoverage(prunable, baseline, coverageOf)
	if err != nil {
		return "", err
	}
	keep := map[string]bool{}
	for _, k := range kept {
		keep[k] = true
	}
	removed := 0
	for _, name := range prunable {
		if keep[name] {
			continue
		}
		removed++
		if !dryRun {
			if err := os.Remove(filepath.Join(corpusDir, name)); err != nil {
				return "", err
			}
		}
	}
	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	return fmt.Sprintf("%s %d of %d harvested seeds, kept %d; %d blocks covered before and after", verb, removed, len(prunable), len(kept), len(all)), nil
}
