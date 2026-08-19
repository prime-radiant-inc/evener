package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseOpts(home, cwd string) options {
	return options{
		home:       home,
		configBase: filepath.Join(home, ".config"),
		stateBase:  filepath.Join(home, ".local", "state"),
		cwd:        cwd,
	}
}

func TestExecuteMovesLegacyStateRoot(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".serf")
	dst := filepath.Join(home, ".evener")
	if err := os.MkdirAll(filepath.Join(src, "run"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "credentials.toml"), []byte("test"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source %s should be gone", src)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dest %s should exist: %v", dst, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "credentials.toml")); err != nil {
		t.Fatalf("credentials.toml should be in dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "run")); err != nil {
		t.Fatalf("run dir should be in dest: %v", err)
	}
	if !strings.Contains(stdout.String(), "moved=1") {
		t.Fatalf("stdout = %q, want moved=1", stdout.String())
	}
}

func TestExecuteIdempotent(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".serf")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	opts := baseOpts(home, t.TempDir())

	var stdout1, stderr1 bytes.Buffer
	if code := execute(opts, &stdout1, &stderr1); code != 0 {
		t.Fatalf("first run code = %d, want 0; stderr = %q", code, stderr1.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source %s should be gone after first run", src)
	}

	var stdout2, stderr2 bytes.Buffer
	code2 := execute(opts, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("second run code = %d, want 0; stderr = %q", code2, stderr2.String())
	}
	if !strings.Contains(stdout2.String(), "moved=0") {
		t.Fatalf("second run stdout = %q, want moved=0", stdout2.String())
	}
}

func TestExecuteRefusesOverwriteDestExists(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".serf")
	dst := filepath.Join(home, ".evener")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "old"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "new"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write new: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(src, "old")); err != nil {
		t.Fatalf("source content should be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "new")); err != nil {
		t.Fatalf("dest content should be preserved: %v", err)
	}
	if !strings.Contains(stdout.String(), "destination already exists") {
		t.Fatalf("stdout = %q, want 'destination already exists'", stdout.String())
	}
}

func TestExecuteSkipsMissingSource(t *testing.T) {
	home := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "moved=0") {
		t.Fatalf("stdout = %q, want moved=0", stdout.String())
	}
	if stderr.Len() > 0 {
		t.Fatalf("stderr = %q, want empty (silent skip)", stderr.String())
	}
}

func TestExecuteDryRunMakesNoChanges(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".serf")
	dst := filepath.Join(home, ".evener")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "test"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write test: %v", err)
	}

	opts := baseOpts(home, t.TempDir())
	opts.dryRun = true

	var stdout, stderr bytes.Buffer
	code := execute(opts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source %s should still exist: %v", src, err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dest %s should not exist", dst)
	}
	if !strings.Contains(stdout.String(), "would move") {
		t.Fatalf("stdout = %q, want 'would move'", stdout.String())
	}
	if !strings.Contains(stdout.String(), "would_move=1") {
		t.Fatalf("stdout = %q, want would_move=1", stdout.String())
	}
}

func TestExecuteMigratesXDGConfigAndState(t *testing.T) {
	home := t.TempDir()
	configBase := filepath.Join(home, ".config")
	stateBase := filepath.Join(home, ".local", "state")

	configSrc := filepath.Join(configBase, "serf")
	if err := os.MkdirAll(filepath.Join(configSrc, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir config source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configSrc, "mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}
	stateSrc := filepath.Join(stateBase, "serf")
	if err := os.MkdirAll(filepath.Join(stateSrc, "projects"), 0o755); err != nil {
		t.Fatalf("mkdir state source: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(configSrc); !os.IsNotExist(err) {
		t.Fatalf("config source %s should be gone", configSrc)
	}
	if _, err := os.Stat(filepath.Join(configBase, "evener", "skills")); err != nil {
		t.Fatalf("config skills not in dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configBase, "evener", "mcp.json")); err != nil {
		t.Fatalf("mcp.json not in config dest: %v", err)
	}

	if _, err := os.Stat(stateSrc); !os.IsNotExist(err) {
		t.Fatalf("state source %s should be gone", stateSrc)
	}
	if _, err := os.Stat(filepath.Join(stateBase, "evener", "projects")); err != nil {
		t.Fatalf("state projects not in dest: %v", err)
	}
}

func TestExecuteMigratesProjectSerfDirectory(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()
	src := filepath.Join(projectDir, ".serf")
	dst := filepath.Join(projectDir, ".evener")
	if err := os.MkdirAll(filepath.Join(src, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir project source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write project mcp.json: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, projectDir), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("project source %s should be gone", src)
	}
	if _, err := os.Stat(filepath.Join(dst, "mcp.json")); err != nil {
		t.Fatalf("project mcp.json not in dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "prompts")); err != nil {
		t.Fatalf("project prompts not in dest: %v", err)
	}
}

func TestExecuteHandlesPartialMigration(t *testing.T) {
	home := t.TempDir()

	// Legacy state root already migrated (evener exists, serf does not).
	if err := os.MkdirAll(filepath.Join(home, ".evener"), 0o755); err != nil {
		t.Fatalf("mkdir evener: %v", err)
	}

	// Config root still needs migration.
	configBase := filepath.Join(home, ".config")
	configSrc := filepath.Join(configBase, "serf")
	if err := os.MkdirAll(configSrc, 0o755); err != nil {
		t.Fatalf("mkdir config source: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := execute(baseOpts(home, t.TempDir()), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(configSrc); !os.IsNotExist(err) {
		t.Fatalf("config source %s should be gone", configSrc)
	}
	if _, err := os.Stat(filepath.Join(configBase, "evener")); err != nil {
		t.Fatalf("config dest should exist: %v", err)
	}
	// The already-migrated legacy root has no source (it was moved), so it is
	// skipped silently; only the config root that still had a source is moved.
	if !strings.Contains(stdout.String(), "moved=1") {
		t.Fatalf("stdout = %q, want moved=1", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".evener")); err != nil {
		t.Fatalf("already-migrated evener root should still exist: %v", err)
	}
}

func TestExecuteVerbosePrintsSkippedSources(t *testing.T) {
	home := t.TempDir()
	opts := baseOpts(home, t.TempDir())
	opts.verbose = true

	var stdout, stderr bytes.Buffer
	code := execute(opts, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "source does not exist") {
		t.Fatalf("stdout = %q, want 'source does not exist' with --verbose", stdout.String())
	}
}

func TestRunRespectsXDGEnvVars(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "custom-config")
	stateHome := filepath.Join(home, "custom-state")

	configSrc := filepath.Join(configHome, "serf")
	if err := os.MkdirAll(filepath.Join(configSrc, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir config source: %v", err)
	}
	stateSrc := filepath.Join(stateHome, "serf")
	if err := os.MkdirAll(filepath.Join(stateSrc, "projects"), 0o755); err != nil {
		t.Fatalf("mkdir state source: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Isolate cwd to a temp dir to prevent the project scan from finding
	// unrelated .evener directories in ancestor git roots.
	cwd := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldwd)

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(configSrc); !os.IsNotExist(err) {
		t.Fatalf("config source %s should be gone", configSrc)
	}
	if _, err := os.Stat(filepath.Join(configHome, "evener", "skills")); err != nil {
		t.Fatalf("config skills not in dest: %v", err)
	}
	if _, err := os.Stat(stateSrc); !os.IsNotExist(err) {
		t.Fatalf("state source %s should be gone", stateSrc)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "evener", "projects")); err != nil {
		t.Fatalf("state projects not in dest: %v", err)
	}
}

func TestRunRejectsPositionalArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"unexpected"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "positional arguments are not accepted") {
		t.Fatalf("stderr = %q, want positional arguments rejection", stderr.String())
	}
}
