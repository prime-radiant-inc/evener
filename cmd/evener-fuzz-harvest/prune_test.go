package fuzzharvest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSelectByCoverageKeepsOnlySeedsThatAddBlocks(t *testing.T) {
	cover := map[string]coverageSet{
		"a": {"b1": true},
		"b": {"b1": true},             // nothing new over a
		"c": {"b1": true, "b2": true}, // adds b2
		"d": {"b0": true},             // already in the baseline
	}
	kept, all, err := selectByCoverage([]string{"a", "b", "c", "d"}, coverageSet{"b0": true},
		func(f string) (coverageSet, error) { return cover[f], nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "c"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept = %v, want %v", kept, want)
	}
	if len(all) != 3 {
		t.Fatalf("union covers %d blocks, want 3", len(all))
	}
}

func TestSelectByCoveragePropagatesReplayFailure(t *testing.T) {
	boom := errors.New("replay failed")
	_, _, err := selectByCoverage([]string{"a"}, nil, func(string) (coverageSet, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestParseCoverProfileReadsOnlyCoveredBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cover.out")
	profile := "mode: set\nx/a.go:1.1,2.2 1 1\nx/a.go:3.1,4.2 2 0\nx/b.go:5.1,6.2 1 7\n"
	if err := os.WriteFile(path, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := parseCoverProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := coverageSet{"x/a.go:1.1,2.2": true, "x/b.go:5.1,6.2": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("covered = %v, want %v", got, want)
	}
}

func TestHarvesterSeedNameSparesToolchainFiles(t *testing.T) {
	harvested := "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	if !harvesterSeedName.MatchString(harvested) {
		t.Fatal("full sha256 name should be prunable")
	}
	if harvesterSeedName.MatchString("170f91c7fb0b92b5") {
		t.Fatal("toolchain-written name must be kept")
	}
}

func TestEmitterCapsFilesPerTargetIncludingExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEmitter(false, 32768)
	e.maxPerTarget = 3
	var statuses []emitStatus
	for _, seed := range []string{"one", "two", "three"} {
		s, err := e.emitBytes(dir, []byte(seed))
		if err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, s)
	}
	if want := []emitStatus{statusWritten, statusWritten, statusCapped}; !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	if e.capped != 1 {
		t.Fatalf("capped = %d, want 1", e.capped)
	}
	// An identical seed already on disk is a dedupe, not a cap hit.
	if s, _ := e.emitBytes(dir, []byte("one")); s != statusDeduped {
		t.Fatalf("existing seed status = %v, want deduped", s)
	}
	if got := countFiles(t, dir); got != 3 {
		t.Fatalf("directory holds %d files, want 3", got)
	}
}

// pruneFixture writes a tiny module (calling into a dependency module) whose FuzzClassify target (tagged
// evenerfuzz, as the real targets are) has one f.Add seed covering the positive
// branch, and a corpus of: a harvested negative (redundant once the
// toolchain-written negative is counted), a harvested zero (new coverage), and
// a harvested positive (redundant with the f.Add seed). It returns the corpus dir.
func pruneFixture(t *testing.T, targetName string) string {
	t.Helper()
	// The fixture is its own module, outside any workspace the caller's
	// environment names.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The branching lives in a separate module that only the fuzz test imports,
	// as the real targets' oracles run inside third-party validators.
	write("go.mod", "module example.com/fixture\n\ngo 1.21\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n")
	write("dep/go.mod", "module example.com/dep\n\ngo 1.21\n")
	write("dep/dep.go", `package dep

func Classify(n int) string {
	if n < 0 {
		return "neg"
	}
	if n == 0 {
		return "zero"
	}
	return "pos"
}
`)
	write("fixture.go", "package fixture\n")
	write("fixture_fuzz_test.go", `//go:build evenerfuzz

package fixture

import (
	"testing"

	"example.com/dep"
)

func FuzzClassify(f *testing.F) {
	f.Add(1)
	f.Fuzz(func(t *testing.T, n int) { dep.Classify(n) })
}
`)
	corpus := filepath.Join("testdata", "fuzz", targetName)
	seed := func(n string) string { return "go test fuzz v1\nint(" + n + ")\n" }
	write(filepath.Join(corpus, strings.Repeat("a", 64)), seed("-1"))
	write(filepath.Join(corpus, strings.Repeat("b", 64)), seed("0"))
	write(filepath.Join(corpus, strings.Repeat("c", 64)), seed("5"))
	write(filepath.Join(corpus, "0123456789abcdef"), seed("-7"))
	return filepath.Join(root, corpus)
}

func corpusNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPruneCorpusKeepsOnlyCoverageAddingAndToolchainFiles(t *testing.T) {
	dir := pruneFixture(t, "FuzzClassify")
	before := corpusNames(t, dir)

	if _, err := pruneCorpus(dir, true); err != nil {
		t.Fatal(err)
	}
	if got := corpusNames(t, dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("dry run changed the corpus: %v, want %v", got, before)
	}

	if _, err := pruneCorpus(dir, false); err != nil {
		t.Fatal(err)
	}
	want := []string{strings.Repeat("b", 64), "0123456789abcdef"}
	sort.Strings(want)
	if got := corpusNames(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("corpus after prune = %v, want %v", got, want)
	}
}

func TestPruneCorpusAbortsWhenTargetNoLongerExists(t *testing.T) {
	dir := pruneFixture(t, "FuzzRenamedAway")
	before := corpusNames(t, dir)

	if _, err := pruneCorpus(dir, false); err == nil {
		t.Fatal("prune of a corpus whose target does not exist must fail")
	}
	if got := corpusNames(t, dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed prune changed the corpus: %v, want %v", got, before)
	}
}
