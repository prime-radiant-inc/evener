package hubcore

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// TestResolveProjectMapStrictRejectsAnUnresolvableLivePath pins that strict
// resolution fails the whole capture, returning no map, when a live entry's
// working directory cannot be resolved.
func TestResolveProjectMapStrictRejectsAnUnresolvableLivePath(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing", "project")
	live := []LiveEntry{
		{SessionID: "02wMz5Txv1C3Hut0M8GCeB", WorkingDir: missingPath},
	}
	projects, err := ResolveProjectMapStrict(nil, live)
	if err == nil {
		t.Fatal("ResolveProjectMapStrict accepted an unresolvable live working directory")
	}
	if projects != nil {
		t.Fatalf("ResolveProjectMapStrict projects = %v, want nil on error", projects)
	}
}

// TestResolveProjectMapRepeatedMissingPathsBoundAllocations proves that
// repeating the same unresolvable path across metadata and live entries costs
// constant allocation work per capture. It measures allocation work, not an
// exact syscall count.
func TestResolveProjectMapRepeatedMissingPathsBoundAllocations(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-child")
	if _, err := os.Stat(missing); err == nil {
		t.Fatalf("fixture path %q unexpectedly exists", missing)
	}
	singleMetas := []schema.SessionMeta{
		{ID: "single-meta", EnvInfo: schema.EnvironmentInfo{WorkingDir: missing}},
	}
	singleLive := []LiveEntry{
		{SessionID: "single-live", WorkingDir: missing},
	}
	const occurrences = 256
	repeatedMetas := make([]schema.SessionMeta, occurrences)
	repeatedLive := make([]LiveEntry, occurrences)
	for i := range repeatedMetas {
		repeatedMetas[i] = schema.SessionMeta{EnvInfo: schema.EnvironmentInfo{WorkingDir: missing}}
		repeatedLive[i] = LiveEntry{WorkingDir: missing}
	}
	measure := func(metas []schema.SessionMeta, live []LiveEntry) float64 {
		t.Helper()
		nonEmpty := false
		allocs := testing.AllocsPerRun(20, func() {
			if len(ResolveProjectMap(metas, live)) != 0 {
				nonEmpty = true
			}
		})
		if nonEmpty {
			t.Fatalf("ResolveProjectMap returned a project for the missing path %q", missing)
		}
		return allocs
	}
	singleAllocs := measure(singleMetas, singleLive)
	repeatedAllocs := measure(repeatedMetas, repeatedLive)
	if repeatedAllocs >= 2*singleAllocs {
		t.Fatalf("repeated allocations %g, single allocations %g", repeatedAllocs, singleAllocs)
	}
}

// TestResolveProjectMapFailedPathAcceptsCarriedLiveIdentity pins the
// precedence among failed lookups and carried identities inside one capture.
func TestResolveProjectMapFailedPathAcceptsCarriedLiveIdentity(t *testing.T) {
	t.Run("carried identity after failed attempts", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		carried := identifier.ProjectFromCanonicalPath(missing)
		metas := []schema.SessionMeta{
			{ID: "meta", EnvInfo: schema.EnvironmentInfo{WorkingDir: missing}},
		}
		live := []LiveEntry{
			{SessionID: "live-failed", WorkingDir: missing},
			{SessionID: "live-carried", WorkingDir: missing, Project: carried},
		}
		projects := ResolveProjectMap(metas, live)
		want := map[string]identifier.Project{missing: carried}
		if !reflect.DeepEqual(projects, want) {
			t.Fatalf("projects = %#v, want %#v", projects, want)
		}
	})

	t.Run("successful metadata resolution wins", func(t *testing.T) {
		dir := t.TempDir()
		resolved, err := identifier.ResolveProject(dir)
		if err != nil {
			t.Fatal(err)
		}
		carried := identifier.ProjectFromCanonicalPath(filepath.Join(t.TempDir(), "elsewhere"))
		metas := []schema.SessionMeta{
			{ID: "meta", EnvInfo: schema.EnvironmentInfo{WorkingDir: dir}},
		}
		live := []LiveEntry{
			{SessionID: "live", WorkingDir: dir, Project: carried},
		}
		projects := ResolveProjectMap(metas, live)
		want := map[string]identifier.Project{dir: resolved}
		if !reflect.DeepEqual(projects, want) {
			t.Fatalf("projects = %#v, want the metadata resolution %#v", projects, want)
		}
	})

	t.Run("first carried identity wins", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		first := identifier.ProjectFromCanonicalPath(filepath.Join(t.TempDir(), "first"))
		second := identifier.ProjectFromCanonicalPath(filepath.Join(t.TempDir(), "second"))
		live := []LiveEntry{
			{SessionID: "live-first", WorkingDir: missing, Project: first},
			{SessionID: "live-second", WorkingDir: missing, Project: second},
		}
		projects := ResolveProjectMap(nil, live)
		want := map[string]identifier.Project{missing: first}
		if !reflect.DeepEqual(projects, want) {
			t.Fatalf("projects = %#v, want the first carried identity %#v", projects, want)
		}
	})
}

// TestResolveProjectMapRetriesOnNextCapture proves failed-path state stays
// local to one capture: a directory created after a failed capture resolves on
// the next one, while the still-missing path remains absent.
func TestResolveProjectMapRetriesOnNextCapture(t *testing.T) {
	root := t.TempDir()
	createdLater := filepath.Join(root, "created-later")
	stillMissing := filepath.Join(root, "still-missing")
	metas := []schema.SessionMeta{
		{ID: "created", EnvInfo: schema.EnvironmentInfo{WorkingDir: createdLater}},
		{ID: "missing", EnvInfo: schema.EnvironmentInfo{WorkingDir: stillMissing}},
	}
	if projects := ResolveProjectMap(metas, nil); len(projects) != 0 {
		t.Fatalf("first capture projects = %#v, want none", projects)
	}
	if err := os.MkdirAll(createdLater, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := identifier.ResolveProject(createdLater)
	if err != nil {
		t.Fatal(err)
	}
	projects := ResolveProjectMap(metas, nil)
	want := map[string]identifier.Project{createdLater: resolved}
	if !reflect.DeepEqual(projects, want) {
		t.Fatalf("second capture projects = %#v, want %#v", projects, want)
	}
}

// TestResolveProjectMapStrictStillRejectsBeforeCarriedIdentity proves strict
// mode returns its first resolution error before any carried identity is
// consulted.
func TestResolveProjectMapStrictStillRejectsBeforeCarriedIdentity(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	carried := identifier.ProjectFromCanonicalPath(filepath.Join(t.TempDir(), "carried"))
	metas := []schema.SessionMeta{
		{ID: "meta", EnvInfo: schema.EnvironmentInfo{WorkingDir: missing}},
	}
	live := []LiveEntry{
		{SessionID: "live", WorkingDir: missing, Project: carried},
	}
	projects, err := ResolveProjectMapStrict(metas, live)
	if err == nil {
		t.Fatal("ResolveProjectMapStrict accepted a missing path")
	}
	if projects != nil {
		t.Fatalf("ResolveProjectMapStrict projects = %#v, want nil", projects)
	}
}
