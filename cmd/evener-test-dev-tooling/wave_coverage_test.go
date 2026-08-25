//go:build linux || darwin

package devtoolingtest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestListDirUnreadable covers the os.ReadDir error path in listDir.
func TestListDirUnreadable(t *testing.T) {
	dir := t.TempDir()
	deleted := filepath.Join(dir, "deleted")
	if err := os.MkdirAll(deleted, 0o755); err != nil {
		t.Fatal(err)
	}
	// Remove the directory so ReadDir fails.
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	got := listDir(deleted)
	if len(got) != 1 || !strings.Contains(got[0], "unreadable") {
		t.Fatalf("listDir(deleted) = %v, want one unreadable entry", got)
	}
}

// TestListDirEmpty covers the empty-directory path.
func TestListDirEmpty(t *testing.T) {
	dir := t.TempDir()
	got := listDir(dir)
	if len(got) != 0 {
		t.Fatalf("listDir(empty) = %v, want empty", got)
	}
}

// TestListDirWithEntries covers the positive path.
func TestListDirWithEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := listDir(dir)
	if len(got) != 2 {
		t.Fatalf("listDir = %v, want 2 entries", got)
	}
}

// TestReplayLogMissing covers the os.Open error path in replayLog.
func TestReplayLogMissing(t *testing.T) {
	var out bytes.Buffer
	replayLog(&out, filepath.Join(t.TempDir(), "nonexistent.log"))
	if out.Len() != 0 {
		t.Fatalf("replayLog on missing file should write nothing, got %q", out.String())
	}
}

// TestReplayLogWithContent covers the positive path.
func TestReplayLogWithContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	replayLog(&out, path)
	if out.String() != "line1\nline2\n" {
		t.Fatalf("replayLog wrote %q, want line1\\nline2\\n", out.String())
	}
}

// TestCheckLeaks covers the checkLeaks function.
func TestCheckLeaks(t *testing.T) {
	dir := t.TempDir()
	if got := checkLeaks(dir); len(got) != 0 {
		t.Fatalf("checkLeaks(empty) = %v, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "leftover"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := checkLeaks(dir); len(got) != 1 || got[0] != "leftover" {
		t.Fatalf("checkLeaks = %v, want [leftover]", got)
	}
}

// TestEnvironWithout covers the environWithout function.
func TestEnvironWithout(t *testing.T) {
	t.Setenv("TEST_ENVWITHOUT_A", "1")
	t.Setenv("TEST_ENVWITHOUT_B", "2")
	env := environWithout("TEST_ENVWITHOUT_A")
	foundA := false
	foundB := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "TEST_ENVWITHOUT_A=") {
			foundA = true
		}
		if strings.HasPrefix(kv, "TEST_ENVWITHOUT_B=") {
			foundB = true
		}
	}
	if foundA {
		t.Fatalf("environWithout should have removed TEST_ENVWITHOUT_A")
	}
	if !foundB {
		t.Fatalf("environWithout should have kept TEST_ENVWITHOUT_B")
	}
}

// TestWriteMktempShim covers the writeMktempShim function.
func TestWriteMktempShim(t *testing.T) {
	dir := t.TempDir()
	if err := writeMktempShim(dir); err != nil {
		t.Fatalf("writeMktempShim: %v", err)
	}
	shimPath := filepath.Join(dir, "bin", "mktemp")
	if _, err := os.Stat(shimPath); err != nil {
		t.Fatalf("shim not created: %v", err)
	}
	data, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh") {
		t.Fatalf("shim should be a shell script, got %q", string(data[:20]))
	}
}

// TestWriteMktempShimMkdirFail covers the os.Mkdir error path.
func TestWriteMktempShimMkdirFail(t *testing.T) {
	// Create a file where the bin directory should go, so Mkdir fails.
	tmp := t.TempDir()
	conflict := filepath.Join(tmp, "bin")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeMktempShim(tmp); err == nil {
		t.Fatalf("writeMktempShim should fail when bin is a file")
	}
}

// TestRunSuiteMissingScript covers the cmd.Start error path in runSuite.
func TestRunSuiteMissingScript(t *testing.T) {
	dir := t.TempDir()
	shutdown := make(chan struct{})
	defer close(shutdown)
	r := runSuite(waveConfig{
		ScriptsDir: dir,
		KillGrace:  time.Second,
	}, dir, "nonexistent-suite", shutdown)
	if r.failure == "" {
		t.Fatalf("runSuite with missing script should have a failure, got %+v", r)
	}
	if !strings.Contains(r.failure, "nonexistent-suite") {
		t.Fatalf("failure should name the suite: %q", r.failure)
	}
}

// TestRunWaveEmptySuites covers the zero-suites path.
func TestRunWaveEmptySuites(t *testing.T) {
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: t.TempDir(), Suites: nil, KillGrace: time.Second, Out: &out})
	if code != 0 {
		t.Fatalf("empty suites exit = %d, want 0; output: %s", code, out.String())
	}
}
