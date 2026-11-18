package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/testfs"
)

// TestEnvInfoFromEnvRecordsAbsoluteWorkingDir pins the recorded form of the
// working directory. `evener --dir .` runs in the directory it was launched in;
// storing that as "." loses which checkout it was.
func TestEnvInfoFromEnvRecordsAbsoluteWorkingDir(t *testing.T) {
	info := envInfoFromEnv(execenv.NewLocalExecutionEnvironment("."), clock.Real())
	if !filepath.IsAbs(info.WorkingDir) {
		t.Fatalf("envInfoFromEnv(--dir .).WorkingDir = %q, want an absolute path", info.WorkingDir)
	}
	launchedIn, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	want := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(launchedIn)).ID
	if got := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(info.WorkingDir)).ID; got != want {
		t.Fatalf("project ID of the recorded working directory = %q, want the launch directory's %q", got, want)
	}

	// An absolute record is stored as it was given: making a relative path
	// absolute must not rewrite a path through an alias on the way.
	//
	// The expected value is the canonical spelling because that is what the
	// recorder produces. t.TempDir() returns the lexical one, and on a host whose
	// temp prefix is a symlink (macOS /var → /private/var) the two differ, so
	// comparing against the raw spelling fails for a reason unrelated to the test.
	absolute := testfs.CanonicalTempDir(t)
	if got := envInfoFromEnv(execenv.NewLocalExecutionEnvironment(absolute), clock.Real()).WorkingDir; got != absolute {
		t.Fatalf("envInfoFromEnv(absolute).WorkingDir = %q, want %q unchanged", got, absolute)
	}
}

// TestEnvInfoFromEnvDoesNotFoldDotDotAcrossSymlink covers an absolute --dir whose
// ".." sits behind a symlink. The kernel resolves `link/..` against the physical
// parent of link's target, so cleaning it lexically records a different directory
// than the one the session's commands run in. The record would then describe a
// checkout the session is not in: the startup Git snapshot would save that
// repository's branch and origin.
func TestEnvInfoFromEnvDoesNotFoldDotDotAcrossSymlink(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	sibling := filepath.Join(root, "sibling")
	for _, dir := range []string{physical, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	link := filepath.Join(sibling, "link")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// `link/..` denotes sibling — the physical parent of what link points at — and
	// lexical cleaning calls it sibling too, so the case only differs when the
	// target is nested one level deeper than the link.
	nested := filepath.Join(physical, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.Symlink(nested, link); err != nil {
		t.Fatalf("relink: %v", err)
	}

	// The kernel resolves link/.. to physical; a lexical clean gives sibling.
	env := execenv.NewLocalExecutionEnvironment(link + string(filepath.Separator) + "..")
	got := envInfoFromEnv(env, clock.Real()).WorkingDir
	if want := canonicalStateDir(physical); got != want {
		t.Fatalf("envInfoFromEnv(--dir %s/..).WorkingDir = %q, want the physical parent %q", link, got, want)
	}

	// The snapshot is not the only thing that has to agree: a relative write is
	// joined to the environment's root and cleaned lexically, so a root that kept
	// the launch spelling would put the write in sibling while the snapshot named
	// physical. The assertion is where the bytes land, not what the record says.
	if _, err := env.WriteFile("landed.txt", "x"); err != nil {
		t.Fatalf("write relative to the resolved root: %v", err)
	}
	landed := filepath.Join(physical, "landed.txt")
	if _, err := os.Stat(landed); err != nil {
		t.Fatalf("relative write did not land in the reported checkout %q: %v", landed, err)
	}
	if stray := filepath.Join(sibling, "landed.txt"); fileExists(stray) {
		t.Fatalf("relative write landed in the lexical parent %q instead of the reported checkout %q", stray, landed)
	}
}

// fileExists reports whether path exists, for assertions about where a write did
// NOT land.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestFind_ProjectName_RelativeWorkDirStillNamesItsLane covers a parent launched
// with a relative working directory. A delegate lane's path records the project
// ID of the checkout it was created from, and that ID is a hash of the checkout's
// absolute path. A parent that recorded "." names no project at all, so once the
// lane is deleted the parent is the only evidence left and it cannot be matched
// to the lane it created. The recording has to be absolute where it is written,
// because that is the only point at which the directory it is relative to is
// still known.
//
// Not parallel: it pins the process working directory through t.Chdir.
func TestFind_ProjectName_RelativeWorkDirStillNamesItsLane(t *testing.T) {
	stateDir := t.TempDir()
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	// The parent is a session launched from the originless checkout with a
	// relative --dir, recorded through the launcher's own env snapshot.
	parent := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	prior, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Chdir(checkout)
	parent.EnvInfo = envInfoFromEnv(execenv.NewLocalExecutionEnvironment("."), clock.Real())
	if err := os.Chdir(prior); err != nil {
		t.Fatalf("restore cwd: %v", err)
	}
	if err := schema.SaveSessionMeta(stateDir, parent); err != nil {
		t.Fatal(err)
	}

	// The delegate's managed lane is gone; only the path it occupied, which
	// records the checkout's project ID, survives.
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	row := schema.SessionMeta{ParentSessionID: parent.ID}
	row.EnvInfo.WorkingDir = filepath.Join(stateDir, "worktrees", project.ID, "dlg_gone")

	if got, want := projectName(row, stateDir), filepath.Base(checkout); got != want {
		t.Fatalf("projectName(deleted managed lane whose parent was launched with --dir .) = %q, want the parent's %q", got, want)
	}

}

// TestFind_ProjectName_LegacyRelativeRecordIsNoEvidence covers a record written
// before the working directory was stored absolute. A relative path names no
// checkout, so it must be read as silence rather than as a contradictory project:
// hashing it invents an identity that disagrees with the lane's and vetoes the
// sibling whose saved origin could still have named the repository.
func TestFind_ProjectName_LegacyRelativeRecordIsNoEvidence(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	checkout := newNamedCheckout(t, "legacy-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling is a legacy record: it says "." and carries the origin, which is
	// the only thing that can name this project.
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.WorkingDir = "."
	sibling.EnvInfo.GitOriginURL = "git@github.com:owner/legacy-repo.git"
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	lane := schema.SessionMeta{}
	lane.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", project.ID, "dlg_legacy")
	if got, want := projectName(lane, bucket), "legacy-repo"; got != want {
		t.Fatalf("projectName(lane whose only sibling is a legacy relative record) = %q, want that sibling's saved origin %q", got, want)
	}
}

// TestEnvInfoKeepsLaunchAliasForClientBinding covers a session launched through an
// absolute symlink. Project identity must come from the canonical path, but the
// transcript client binds an absolute file reference by LEXICAL prefix against the
// session's CWD, so a document written with the launch alias no longer opens once
// the alias is erased — even though the symlink and the file both still exist.
// Both facts have to be recorded: identity canonical, binding from the spelling
// the user typed.
func TestEnvInfoKeepsLaunchAliasForClientBinding(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical-checkout")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatalf("mkdir physical: %v", err)
	}
	alias := filepath.Join(root, "checkout-alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}

	info := envInfoFromEnv(execenv.NewLocalExecutionEnvironment(alias), clock.Real())

	// Identity is canonical: it is a hash of this path, and it must match the
	// project a lane created from the checkout would record.
	wantWorking := canonicalStateDir(physical)
	if info.WorkingDir != wantWorking {
		t.Fatalf("WorkingDir for a session launched through %q = %q, want the canonical %q", alias, info.WorkingDir, wantWorking)
	}

	// Binding is the alias the user typed, so an absolute reference under it still
	// lexically prefixes the served CWD.
	if got, want := info.BindingDir(), alias; got != want {
		t.Fatalf("BindingDir for a session launched through %q = %q, want the launch alias %q", alias, got, want)
	}
	reference := filepath.Join(alias, "notes.md")
	if !strings.HasPrefix(reference, info.BindingDir()+string(filepath.Separator)) {
		t.Fatalf("reference %q does not lexically prefix the bound CWD %q", reference, info.BindingDir())
	}

	// A launch that needed no alias records none, so BindingDir falls back to the
	// canonical path and no second spelling is invented.
	plain := envInfoFromEnv(execenv.NewLocalExecutionEnvironment(physical), clock.Real())
	if plain.LaunchDir != "" {
		t.Fatalf("LaunchDir for a direct launch = %q, want empty", plain.LaunchDir)
	}
	if plain.BindingDir() != plain.WorkingDir {
		t.Fatalf("BindingDir for a direct launch = %q, want the working directory %q", plain.BindingDir(), plain.WorkingDir)
	}
}
