package fuzzharvest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
