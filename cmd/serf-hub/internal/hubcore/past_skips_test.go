package hubcore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubtest"
)

// captureSkipReport runs fn with os.Stderr redirected to a pipe and returns the
// past-index lines written to it. Other hubcore machinery logs to the same
// stream from background goroutines, so the result is filtered to this
// reporter's prefix rather than returned raw. Output is small enough to fit the
// pipe buffer, so fn never blocks on a reader.
func captureSkipReport(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	os.Stderr = w

	fn()

	os.Stderr = original
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	var report strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "[hub] past index: ") {
			report.WriteString(line)
			report.WriteString("\n")
		}
	}
	return report.String()
}

// A hand-seeded project directory or session meta whose id fails validation is
// correctly left out of the index, but the absence on its own points nowhere
// near the cause. Rebuild must name the path and the validation that rejected
// it, while still indexing everything valid around it.
func fuzzScenarioPastIndex_RebuildNamesSkippedEntries(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")

	strayProject := filepath.Join(projects, "no-suffix")
	if err := os.MkdirAll(strayProject, 0o755); err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(projects, hubtest.ProjectID(t, "good"))
	validID := hubtest.SessionID(t)
	writeMeta(t, good, schema.SessionMeta{ID: validID, UpdatedAt: time.Now()})
	// The exact shape that cost three agents a day: a readable placeholder that
	// looks like a session id and is not one.
	writeMeta(t, good, schema.SessionMeta{ID: "02wMz5Txv6USAGE000001", UpdatedAt: time.Now()})

	idx := NewPastIndex(filepath.Join(projects, "*"))
	out := captureSkipReport(t, func() {
		if _, err := idx.Rebuild(); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
	})

	if !strings.Contains(out, strayProject) || !strings.Contains(out, "invalid project id") {
		t.Fatalf("skip report must name the stray project dir and its validation:\n%s", out)
	}
	badMeta := filepath.Join(good, "sessions", "02wMz5Txv6USAGE000001.meta.json")
	if !strings.Contains(out, badMeta) || !strings.Contains(out, "invalid session id") {
		t.Fatalf("skip report must name the rejected session meta and its validation:\n%s", out)
	}
	// The identifier errors name the broken rule, not the shape to write
	// instead; without the shape the report still leaves the reader guessing.
	if !strings.Contains(out, projectIDShape) || !strings.Contains(out, sessionIDShape) {
		t.Fatalf("skip report must spell out the id shapes %q and %q:\n%s", projectIDShape, sessionIDShape, out)
	}
	if _, ok := idx.Find(validID); !ok {
		t.Fatalf("the valid session must still be indexed; report was:\n%s", out)
	}
	if all := idx.All(); len(all) != 1 {
		t.Fatalf("index holds %d entries, want only the valid session", len(all))
	}
}

// A session directory under a valid project whose name is not a session id is
// skipped by the observer-grant fold. That skip is reported too — it is the
// same silent-absence class as the meta and project skips.
func fuzzScenarioPastIndex_RebuildNamesSkippedObserverGrantDir(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	proj := filepath.Join(projects, hubtest.ProjectID(t, "grants"))
	writeMeta(t, proj, schema.SessionMeta{ID: hubtest.SessionID(t), UpdatedAt: time.Now()})

	strayJobstore := filepath.Join(proj, "sessions", "not-a-session-id")
	if err := os.MkdirAll(strayJobstore, 0o755); err != nil {
		t.Fatal(err)
	}

	idx := NewPastIndex(filepath.Join(projects, "*"))
	out := captureSkipReport(t, func() {
		if _, err := idx.Rebuild(); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
	})
	if !strings.Contains(out, strayJobstore) || !strings.Contains(out, "invalid session id") {
		t.Fatalf("skip report must name the stray jobstore dir and its validation:\n%s", out)
	}
}

// A projects root full of pre-identifier directories must announce itself once,
// not on every rebuild tick — and when a new unindexable entry appears, only
// that one is reported, so it is not buried under the standing set.
func fuzzScenarioPastIndex_RebuildReportsOnlyNewlySkippedEntries(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	standing := filepath.Join(projects, "legacy16hexname")
	if err := os.MkdirAll(standing, 0o755); err != nil {
		t.Fatal(err)
	}

	idx := NewPastIndex(filepath.Join(projects, "*"))
	first := captureSkipReport(t, func() { _, _ = idx.Rebuild() })
	if !strings.Contains(first, standing) {
		t.Fatalf("first rebuild must report the standing skip:\n%s", first)
	}

	second := captureSkipReport(t, func() { _, _ = idx.Rebuild() })
	if second != "" {
		t.Fatalf("an unchanged skip set must be silent on re-rebuild, got:\n%s", second)
	}

	fresh := filepath.Join(projects, "another-stray")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	third := captureSkipReport(t, func() { _, _ = idx.Rebuild() })
	if !strings.Contains(third, fresh) {
		t.Fatalf("a newly unindexable entry must be reported:\n%s", third)
	}
	if strings.Contains(third, standing) {
		t.Fatalf("an already-reported skip must not repeat:\n%s", third)
	}
}

// Real projects roots hold hundreds of pre-identifier directories, so the
// report names a bounded sample and counts the rest.
func fuzzScenarioPastIndex_RebuildCapsSkipReport(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	const strays = maxReportedSkips + 3
	for n := range strays {
		if err := os.MkdirAll(filepath.Join(projects, fmt.Sprintf("stray%02d", n)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	idx := NewPastIndex(filepath.Join(projects, "*"))
	out := captureSkipReport(t, func() { _, _ = idx.Rebuild() })

	named := strings.Count(out, ": invalid project id")
	if named != maxReportedSkips {
		t.Fatalf("report named %d skips, want the %d cap:\n%s", named, maxReportedSkips, out)
	}
	if want := fmt.Sprintf("skipped %d more unindexable entries", strays-maxReportedSkips); !strings.Contains(out, want) {
		t.Fatalf("report must count the remainder as %q:\n%s", want, out)
	}
}

// A rebuild over a clean projects root says nothing.
func fuzzScenarioPastIndex_RebuildSilentWhenNothingSkipped(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	proj := filepath.Join(projects, hubtest.ProjectID(t, "clean"))
	writeMeta(t, proj, schema.SessionMeta{ID: hubtest.SessionID(t), UpdatedAt: time.Now()})

	idx := NewPastIndex(filepath.Join(projects, "*"))
	out := captureSkipReport(t, func() {
		if _, err := idx.Rebuild(); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
	})
	if out != "" {
		t.Fatalf("a rebuild with nothing to skip must be silent, got:\n%s", out)
	}
}
