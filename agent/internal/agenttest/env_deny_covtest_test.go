package agenttest

import (
	"context"
	"io"
	"strings"
	"testing"
)

// TestDenyEnvBoundedText covers boundedText for empty and non-empty cases
// (lines 61-68).
func TestDenyEnvBoundedText(t *testing.T) {
	d := &DenyEnv{Seed: 0}
	// h%511 == 0 produces n=0, which returns "".
	if got := d.boundedText(0); got != "" {
		t.Fatalf("boundedText(0) = %q, want empty", got)
	}
	// Non-zero draw produces non-empty text.
	got := d.boundedText(100)
	if len(got) == 0 {
		t.Fatal("expected non-empty text for non-zero draw")
	}
}

// TestDenyEnvDraw covers the draw function with various inputs.
func TestDenyEnvDraw(t *testing.T) {
	d := &DenyEnv{Seed: 42}
	h1 := d.draw("test", "path1")
	h2 := d.draw("test", "path1")
	if h1 != h2 {
		t.Fatal("draw is not deterministic for same inputs")
	}
	h3 := d.draw("test", "path2")
	if h1 == h3 {
		t.Fatal("draw should differ for different inputs")
	}
}

// TestDenyEnvBoundedDenyResult covers the truncation function (lines 71-75).
func TestDenyEnvBoundedDenyResult(t *testing.T) {
	// Short string — returned as-is.
	short := "hello"
	if got := boundedDenyResult(short); got != short {
		t.Fatalf("boundedDenyResult = %q, want %q", got, short)
	}
	// Long string — truncated.
	long := strings.Repeat("x", denyMaxBytes+100)
	if got := boundedDenyResult(long); len(got) != denyMaxBytes {
		t.Fatalf("boundedDenyResult length = %d, want %d", len(got), denyMaxBytes)
	}
}

// TestDenyEnvReadFile covers ReadFile's error and success paths
// (lines 79-83).
func TestDenyEnvReadFile(t *testing.T) {
	// Find a seed that produces h%5==0 (error).
	for seed := uint64(0); seed < 100; seed++ {
		d := &DenyEnv{Seed: seed}
		h := d.draw("read", "/test/path")
		if h%5 == 0 {
			_, err := d.ReadFile("/test/path", nil, nil)
			if err == nil {
				t.Fatalf("Seed %d: expected error for ReadFile, got nil", seed)
			}
			break
		}
	}

	// Find a seed that produces h%5!=0 (success).
	for seed := uint64(0); seed < 100; seed++ {
		d := &DenyEnv{Seed: seed}
		h := d.draw("read", "/test/path")
		if h%5 != 0 {
			content, err := d.ReadFile("/test/path", nil, nil)
			if err != nil {
				t.Fatalf("Seed %d: unexpected error: %v", seed, err)
			}
			if content == "" && h%(denyMaxBytes+1) != 0 {
				t.Fatalf("Seed %d: expected non-empty content", seed)
			}
			break
		}
	}
}

// TestDenyEnvWriteFile covers WriteFile (line 87).
func TestDenyEnvWriteFile(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	result, err := d.WriteFile("/test/path", "hello")
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !strings.Contains(result, "5 bytes") {
		t.Fatalf("WriteFile result = %q, want to contain '5 bytes'", result)
	}
}

// TestDenyEnvEditFile covers EditFile's error and success paths
// (lines 90-95).
func TestDenyEnvEditFile(t *testing.T) {
	// Find a seed that produces h%3==0 (error).
	for seed := uint64(0); seed < 100; seed++ {
		d := &DenyEnv{Seed: seed}
		h := d.draw("edit", "/test/path", "old")
		if h%3 == 0 {
			_, err := d.EditFile("/test/path", "old", "new", false)
			if err == nil {
				t.Fatalf("Seed %d: expected error for EditFile", seed)
			}
			break
		}
	}

	// Find a seed that produces h%3!=0 (success).
	for seed := uint64(0); seed < 100; seed++ {
		d := &DenyEnv{Seed: seed}
		h := d.draw("edit", "/test/path", "old")
		if h%3 != 0 {
			result, err := d.EditFile("/test/path", "old", "new", false)
			if err != nil {
				t.Fatalf("Seed %d: unexpected error: %v", seed, err)
			}
			if !strings.Contains(result, "edited") {
				t.Fatalf("Seed %d: result = %q, want to contain 'edited'", seed, result)
			}
			break
		}
	}
}

// TestDenyEnvFileExists covers FileExists (line 98-99).
func TestDenyEnvFileExists(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	// Should return true or false deterministically.
	result := d.FileExists("/test/path")
	// Same call should return the same result.
	if d.FileExists("/test/path") != result {
		t.Fatal("FileExists is not deterministic")
	}
}

// TestDenyEnvGlob covers Glob (lines 102-109).
func TestDenyEnvGlob(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	results, err := d.Glob("*.go", "/base", true)
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	// Should have between 0 and 3 results.
	if len(results) > 3 {
		t.Fatalf("Glob returned %d results, want <= 3", len(results))
	}
	for _, r := range results {
		if !strings.HasPrefix(r, "/base/") {
			t.Fatalf("Glob result %q should start with /base/", r)
		}
	}
}

// TestDenyEnvGrep covers Grep's three output modes (lines 112-124).
func TestDenyEnvGrep(t *testing.T) {
	d := &DenyEnv{Seed: 1}

	// count mode.
	result, err := d.Grep("pattern", "/path", "", false, 0, "count")
	if err != nil {
		t.Fatalf("Grep count: %v", err)
	}
	if result == "" {
		t.Fatal("Grep count should return a number")
	}

	// files_with_matches mode.
	result, _ = d.Grep("pattern", "/path", "", false, 0, "files_with_matches")
	// Either returns path or empty depending on seed.
	_ = result

	// default mode.
	result, _ = d.Grep("pattern", "/path", "", false, 0, "content")
	_ = result
}

// TestDenyEnvListDirectory covers ListDirectory (lines 127-138).
func TestDenyEnvListDirectory(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	entries, err := d.ListDirectory("/test", 1)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	// Should have 0-3 entries.
	if len(entries) > 3 {
		t.Fatalf("ListDirectory returned %d entries, want <= 3", len(entries))
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, "entry-") {
			t.Fatalf("entry name = %q, want 'entry-*'", e.Name)
		}
	}
}

// TestDenyEnvExecCommand covers ExecCommand's error and success paths
// (lines 141-151).
func TestDenyEnvExecCommand(t *testing.T) {
	// Find a seed that produces h%7==0 (error).
	for seed := uint64(0); seed < 100; seed++ {
		d := &DenyEnv{Seed: seed}
		h := d.draw("exec", "ls", "/wd")
		if h%7 == 0 {
			_, err := d.ExecCommand(context.Background(), "ls", 1000, "/wd", nil)
			if err == nil {
				t.Fatalf("Seed %d: expected error for ExecCommand", seed)
			}
			break
		}
	}

	// Find a seed that produces h%7!=0 (success).
	for seed := uint64(0); seed < 100; seed++ {
		d := &DenyEnv{Seed: seed}
		h := d.draw("exec", "ls", "/wd")
		if h%7 != 0 {
			result, err := d.ExecCommand(context.Background(), "ls", 1000, "/wd", nil)
			if err != nil {
				t.Fatalf("Seed %d: unexpected error: %v", seed, err)
			}
			if result.ExitCode < 0 || result.ExitCode > 2 {
				t.Fatalf("Seed %d: ExitCode = %d, want 0-2", seed, result.ExitCode)
			}
			break
		}
	}
}

// TestDenyEnvStreamCommand covers StreamCommand (lines 153-163).
func TestDenyEnvStreamCommand(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	var buf strings.Builder
	handle, err := d.StreamCommand(context.Background(), "ls", "/wd", nil, &buf)
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	if handle == nil {
		t.Fatal("expected non-nil handle")
	}
	exit, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exit < 0 || exit > 2 {
		t.Fatalf("exit = %d, want 0-2", exit)
	}
	handle.Signal() // should not panic
}

// TestDenyEnvStreamCommand_NilWriter covers StreamCommand with nil writer
// (line 155-156).
func TestDenyEnvStreamCommand_NilWriter(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	handle, err := d.StreamCommand(context.Background(), "ls", "/wd", nil, nil)
	if err != nil {
		t.Fatalf("StreamCommand nil writer: %v", err)
	}
	if handle == nil {
		t.Fatal("expected non-nil handle")
	}
	_, _ = handle.Wait()
}

// TestDenyEnvInitialize covers Initialize (line 36).
func TestDenyEnvInitialize(t *testing.T) {
	d := &DenyEnv{Seed: 0}
	if err := d.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// TestDenyEnvCleanup covers Cleanup (line 37).
func TestDenyEnvCleanup(t *testing.T) {
	d := &DenyEnv{Seed: 0}
	d.Cleanup() // should not panic
}

// TestDenyEnvBasics covers the basic accessor methods.
func TestDenyEnvBasics(t *testing.T) {
	d := &DenyEnv{WorkDir: "/work", Seed: 42}
	if d.WorkingDirectory() != "/work" {
		t.Fatalf("WorkingDirectory = %q, want /work", d.WorkingDirectory())
	}
	if d.Platform() != "linux" {
		t.Fatalf("Platform = %q, want 'linux'", d.Platform())
	}
	if d.OSVersion() != "deny-env" {
		t.Fatalf("OSVersion = %q, want 'deny-env'", d.OSVersion())
	}
}

// Ensure io is used to avoid unused import.
var _ = io.Copy
