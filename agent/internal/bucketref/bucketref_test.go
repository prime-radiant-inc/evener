package bucketref

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRefFor pins the shared ref-formatting contract directly (the agent and
// doctor refFor helpers are now thin wrappers, so pinning the contract here is
// what guards behavior — a wrapper-vs-original comparison would be circular).
func TestRefFor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		projectID string
		sid       string
		want      string
	}{
		{"empty project → local", "", "01JABC", "local:01JABC"},
		{"valid project → proj", "myproj-a1b2c3d4e5", "01JABC", "proj:myproj-a1b2c3d4e5:01JABC"},
		{"invalid project → no ref", "has space", "01JABC", ""},
		{"invalid project colon → no ref", "a:b", "01JABC", ""},
		{"invalid project dot → no ref", "has.dot", "01JABC", ""},
		{"invalid project empty-alike → no ref", "x", "01JABC", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := RefFor(tt.projectID, tt.sid); got != tt.want {
				t.Fatalf("RefFor(%q, %q) = %q, want %q", tt.projectID, tt.sid, got, tt.want)
			}
		})
	}
}

// setupProjects writes a projects dir with a regular bucket dir, a symlinked
// bucket dir (pointing at a real dir elsewhere), and a regular file, returning
// the projects dir path. The symlinked bucket is a directory symlink so the two
// policies can be distinguished.
func setupProjects(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	projects := filepath.Join(root, "evener", "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	// Regular bucket.
	if err := os.MkdirAll(filepath.Join(projects, "realbucket"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Regular file (must be skipped by both policies).
	if err := os.WriteFile(filepath.Join(projects, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlinked bucket: a symlink whose target is a real dir outside projects/.
	target := filepath.Join(root, "linked-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(projects, "linkbucket")); err != nil {
		t.Fatal(err)
	}
	return projects
}

// TestEnumerateBuckets_FollowSymlinks pins the doctor's policy: a symlinked
// bucket dir is enumerated like any other directory (the shared core re-Stats
// it, matching the old isDir/os.Stat follow behavior).
func TestEnumerateBuckets_FollowSymlinks(t *testing.T) {
	t.Parallel()
	projects := setupProjects(t)
	got, err := EnumerateBuckets(projects)
	if err != nil {
		t.Fatalf("EnumerateBuckets Follow: %v", err)
	}
	var names []string
	for _, b := range got {
		names = append(names, b.ProjectID)
	}
	// afile is a regular file → skipped; linkbucket is a symlink-to-dir →
	// included under Follow; realbucket is a dir → included.
	wantIncluded := map[string]bool{"linkbucket": true, "realbucket": true}
	gotSet := map[string]bool{}
	for _, n := range names {
		gotSet[n] = true
	}
	if gotSet["afile"] {
		t.Errorf("FollowSymlinks included a regular file %q (ProjectIDs=%v)", "afile", names)
	}
	for w := range wantIncluded {
		if !gotSet[w] {
			t.Errorf("FollowSymlinks missing %q (ProjectIDs=%v)", w, names)
		}
	}
	if len(got) != len(wantIncluded) {
		t.Errorf("FollowSymlinks count = %d (ProjectIDs=%v), want %d", len(got), names, len(wantIncluded))
	}
}

// TestEnumerateBuckets_RefuseSymlinks pins the agent's policy: a symlinked
// bucket dir is skipped entirely (it never enters the result), and a regular
// dir is still included.
func TestEnumerateBuckets_RefuseSymlinks(t *testing.T) {
	t.Parallel()
	projects := setupProjects(t)
	got, err := EnumerateBuckets(projects, WithSymlinkPolicy(RefuseSymlinks))
	if err != nil {
		t.Fatalf("EnumerateBuckets Refuse: %v", err)
	}
	var names []string
	for _, b := range got {
		names = append(names, b.ProjectID)
	}
	if len(got) != 1 || got[0].ProjectID != "realbucket" {
		t.Errorf("RefuseSymlinks ProjectIDs=%v, want [realbucket] only (symlink + file skipped)", names)
	}
}

// TestEnumerateBuckets_WithGlob pins the injection seam: a caller-supplied glob
// is honored (the agent and doctor read their package-level glob variable at
// call time and pass it here; this is the override hook their fuzz/covtest
// tests rely on).
func TestEnumerateBuckets_WithGlob(t *testing.T) {
	t.Parallel()
	projects := setupProjects(t)
	called := false
	injected := func(pattern string) ([]string, error) {
		called = true
		return filepath.Glob(pattern)
	}
	if _, err := EnumerateBuckets(projects, WithGlob(injected)); err != nil {
		t.Fatalf("EnumerateBuckets WithGlob: %v", err)
	}
	if !called {
		t.Errorf("WithGlob: injected glob was not called")
	}
	// Injected glob returning an error propagates wrapped as
	// "glob project buckets: …".
	wantErr := errors.New("scripted glob failure")
	_, err := EnumerateBuckets(projects, WithGlob(func(string) ([]string, error) {
		return nil, wantErr
	}))
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithGlob error = %v, want errors.Is wrapping %v", err, wantErr)
	}
}

// TestEnumerateBuckets_SkipsStatErrors pins the per-entry skip-on-error
// behavior under BOTH policies: a transiently-unreadable entry never aborts the
// sweep — matching today's behavior on both sides (agent skips on Lstat error;
// doctor's isDir skips on Stat error).
func TestEnumerateBuckets_SkipsStatErrors(t *testing.T) {
	t.Parallel()
	for _, policy := range []SymlinkPolicy{FollowSymlinks, RefuseSymlinks} {
		projects := setupProjects(t)
		// Make one entry whose Lstat errors: chmod 000 on a parent is
		// platform-dependent, so instead inject a glob that returns a path
		// that does not exist (Lstat on a nonexistent path errors → skip).
		ghost := filepath.Join(projects, "does-not-exist")
		injected := func(string) ([]string, error) {
			return []string{ghost, filepath.Join(projects, "realbucket")}, nil
		}
		got, err := EnumerateBuckets(projects, WithGlob(injected), WithSymlinkPolicy(policy))
		if err != nil {
			t.Fatalf("policy %v: EnumerateBuckets stat-error: %v", policy, err)
		}
		if len(got) != 1 || got[0].ProjectID != "realbucket" {
			t.Errorf("policy %v: stat error not skipped (got %d entries); want only realbucket", policy, len(got))
		}
	}
}
