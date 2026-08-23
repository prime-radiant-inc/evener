package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

// TestRunPositionalArgs covers the positional-args rejection path.
func TestRunMigratePositionalArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments are not accepted") {
		t.Fatalf("stderr missing positional args rejection: %s", stderr.String())
	}
}

// TestRunXDGDefaults covers the XDG default path computation (lines 127-128,
// 132-133) when XDG env vars are unset.
func TestRunXDGDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv(envvars.XDGConfigHome.Name, "")
	t.Setenv(envvars.XDGStateHome.Name, "")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "migration report:") {
		t.Fatalf("stdout missing migration report: %s", stdout.String())
	}
}

// TestDiscoverProjectMigrationsWithGitRoot covers the git-root discovery path
// (lines 382-384) in discoverProjectMigrations.
func TestDiscoverProjectMigrationsWithGitRoot(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, "project", ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(tmp, "project", "subdir")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	migrations := discoverProjectMigrations(cwd)
	if len(migrations) < 2 {
		t.Fatalf("discoverProjectMigrations = %d migrations, want >= 2 (cwd + git root)", len(migrations))
	}
	// The first migration is always cwd.
	if migrations[0].src != filepath.Join(cwd, ".serf") {
		t.Fatalf("first migration src = %q, want %q", migrations[0].src, filepath.Join(cwd, ".serf"))
	}
	// One of the migrations should be the git root.
	foundGit := false
	for _, m := range migrations {
		if m.src == filepath.Join(tmp, "project", ".serf") {
			foundGit = true
			break
		}
	}
	if !foundGit {
		t.Fatalf("did not find git root migration among %v", migrations)
	}
}

// TestDiscoverProjectMigrationsDuplicateGitRoot covers the duplicate-seen
// path (lines 367-369) where cwd is itself a git root.
func TestDiscoverProjectMigrationsDuplicateGitRoot(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	migrations := discoverProjectMigrations(tmp)
	// cwd is also a git root, so the git-root add() is a duplicate.
	if len(migrations) != 1 {
		t.Fatalf("discoverProjectMigrations with cwd==git root = %d migrations, want 1 (deduped)", len(migrations))
	}
}

// TestRepairHomeRootFileReferences covers the repairHomeRootFileReferences
// function with an actual migrated tree.
func TestRepairHomeRootFileReferences(t *testing.T) {
	tmp := t.TempDir()
	// Create a config root with a file that references an old path.
	configRoot := filepath.Join(tmp, "config", "evener")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(tmp, "home", ".serf", "providers.toml")
	newPath := filepath.Join(configRoot, "providers.toml")
	body := `# config referencing ` + oldPath + `\n`
	if err := os.WriteFile(filepath.Join(configRoot, "hub.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := options{
		home:       filepath.Join(tmp, "home"),
		configBase: filepath.Join(tmp, "config"),
		stateBase:  filepath.Join(tmp, "state"),
	}
	var stdout, stderr bytes.Buffer
	repairHomeRootFileReferences(opts, &stdout, &stderr)
	// The file should be rewritten.
	got, err := os.ReadFile(filepath.Join(configRoot, "hub.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), oldPath) {
		t.Fatalf("old path still present in hub.toml: %s", got)
	}
	if !strings.Contains(string(got), newPath) {
		t.Fatalf("new path not in hub.toml: %s", got)
	}
}

// TestExecuteWithMoveAndRepair covers the full execute path including
// move and repair, exercising lines 313-318 and 327-328.
func TestExecuteWithMoveAndRepair(t *testing.T) {
	tmp := t.TempDir()
	// Create a legacy .serf directory with a providers.toml.
	home := filepath.Join(tmp, "home")
	serfDir := filepath.Join(home, ".serf")
	if err := os.MkdirAll(serfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write providers.toml that references its own old path.
	oldPath := filepath.Join(serfDir, "providers.toml")
	body := `# old path: ` + oldPath + `\n`
	if err := os.WriteFile(oldPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := options{
		home:       home,
		configBase: filepath.Join(tmp, "config"),
		stateBase:  filepath.Join(tmp, "state"),
		cwd:        tmp,
	}
	var stdout, stderr bytes.Buffer
	code := execute(opts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("execute code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	// The file should have been moved.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old path should not exist after migration")
	}
	newFile := filepath.Join(tmp, "config", "evener", "providers.toml")
	got, err := os.ReadFile(newFile)
	if err != nil {
		t.Fatalf("read migrated file: %v", err)
	}
	if strings.Contains(string(got), oldPath) {
		t.Fatalf("old path should have been rewritten in migrated file: %s", got)
	}
}
