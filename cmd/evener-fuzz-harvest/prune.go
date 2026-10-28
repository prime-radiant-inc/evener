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

// pruneCorpus drops the harvester-written seeds of one target corpus
// directory (<package>/testdata/fuzz/<FuzzName>) that add no coverage over the
// seeds in the target's source, the toolchain-written files, and the smaller
// seeds kept before them, so the covered blocks are the same before and after.
// Coverage is measured across the target's whole module under -tags
// evenerfuzz. It returns a one-line report.
func pruneCorpus(corpusDir string) (string, error) {
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

	modOut, err := exec.Command("go", "-C", pkgDir, "list", "-f", "{{.Module.Path}}", ".").Output()
	if err != nil {
		return "", fmt.Errorf("go list: %w", err)
	}
	bin := filepath.Join(tmp, "target.test")
	build := exec.Command("go", "-C", pkgDir, "test", "-tags", "evenerfuzz", "-c", "-cover",
		"-coverpkg", strings.TrimSpace(string(modOut))+"/...", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build %s: %w\n%s", pkgDir, err, out)
	}

	profile := filepath.Join(tmp, "cover.out")
	coverageOf := func(subtest string) (coverageSet, error) {
		run := exec.Command(bin, "-test.run", "^"+target+"$/^"+subtest+"$", "-test.coverprofile", profile)
		run.Dir = pkgDir
		if out, err := run.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("replay %s: %w\n%s", subtest, err, out)
		}
		return parseCoverProfile(profile)
	}

	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return "", err
	}
	baseline, err := coverageOf("seed#[0-9]+")
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
	for _, name := range prunable {
		if !keep[name] {
			if err := os.Remove(filepath.Join(corpusDir, name)); err != nil {
				return "", err
			}
		}
	}
	return fmt.Sprintf("kept %d of %d harvested seeds; %d blocks covered before and after", len(kept), len(prunable), len(all)), nil
}
