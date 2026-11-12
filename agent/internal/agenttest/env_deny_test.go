package agenttest

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDenyEnvDeterministicAndBounded(t *testing.T) {
	a := &DenyEnv{WorkDir: "/w", Seed: 42}
	b := &DenyEnv{WorkDir: "/w", Seed: 42}
	ctx := context.Background()

	// Same seed + same args ⇒ byte-identical outputs (replay stability),
	// independent of call order.
	r1, _ := a.ExecCommand(ctx, "ls -la", 0, "/w", nil)
	r2, _ := b.ExecCommand(ctx, "ls -la", 0, "/w", nil)
	if r1 != r2 {
		t.Fatalf("ExecCommand not deterministic: %+v vs %+v", r1, r2)
	}
	if len(r1.Stdout) > denyMaxBytes {
		t.Fatalf("stdout exceeded cap: %d", len(r1.Stdout))
	}

	c1, _ := a.ReadFile("/w/x", nil, nil)
	c2, _ := b.ReadFile("/w/x", nil, nil)
	if c1 != c2 {
		t.Fatalf("ReadFile not deterministic")
	}
	if len(c1) > denyMaxBytes {
		t.Fatalf("ReadFile content exceeded cap: %d", len(c1))
	}
	if a.FileExists("/w/x") != b.FileExists("/w/x") {
		t.Fatalf("FileExists not deterministic")
	}
}

func TestDenyEnvSeedVariation(t *testing.T) {
	// Different seeds should generally produce different outputs for the same
	// args (otherwise the env explores no extra branches). Probe several args.
	differ := false
	for _, arg := range []string{"a", "bb", "ccc", "dddd", "eeeee"} {
		x, _ := (&DenyEnv{Seed: 1}).ExecCommand(context.Background(), arg, 0, "/w", nil)
		y, _ := (&DenyEnv{Seed: 2}).ExecCommand(context.Background(), arg, 0, "/w", nil)
		if x != y {
			differ = true
			break
		}
	}
	if !differ {
		t.Fatal("seed had no effect on ExecCommand outputs")
	}
}

// TestDenyEnvTouchesNoFilesystem is the exec-sandbox containment proof: the
// mutating filesystem methods report success but create, modify, and delete
// nothing on the real disk. A B3 tool-execution fuzz target relies on exactly
// this — a tool handler may call WriteFile/EditFile with any fuzzed path and the
// real filesystem stays untouched. The assertions are falsifiable: if DenyEnv
// were ever backed by the real os package, the planted paths would appear.
func TestDenyEnvTouchesNoFilesystem(t *testing.T) {
	dir := t.TempDir()
	d := &DenyEnv{WorkDir: dir, Seed: 7}

	target := filepath.Join(dir, "written.txt")
	if _, err := d.WriteFile(target, "payload that must never hit disk"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("WriteFile escaped the sandbox: %s exists on disk (err=%v)", target, err)
	}

	if _, err := d.EditFile(target, "old", "new", false); err != nil {
		// An edit "no match" error is fine; what matters is no file was created.
		_ = err
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("EditFile escaped the sandbox: %s exists on disk", target)
	}

	// FileExists is fabricated from the seed, never the real disk: it reports a
	// path that does not exist as present (proving it consulted no filesystem).
	missing := filepath.Join(dir, "definitely-absent")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("test precondition: %s should not exist", missing)
	}
	reported := false
	for _, p := range []string{"a", "b", "c", "d", "e", "f"} {
		if d.FileExists(filepath.Join(dir, p)) {
			reported = true
			break
		}
	}
	if !reported {
		t.Fatal("FileExists never reported a (fabricated) hit; cannot prove it ignores the real disk")
	}
}

func TestDenyEnvStreamWritesBounded(t *testing.T) {
	d := &DenyEnv{Seed: 9}
	var sb strings.Builder
	h, err := d.StreamCommand(context.Background(), "tail -f log", "/w", nil, &sb)
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	if sb.Len() > denyMaxBytes {
		t.Fatalf("stream output exceeded cap: %d", sb.Len())
	}
	code, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code < 0 || code > 2 {
		t.Fatalf("exit code out of expected range: %d", code)
	}
	h.Signal() // must not panic
}

// TestDenyEnvBoundedText covers boundedText for empty and non-empty cases.
func TestDenyEnvBoundedText(t *testing.T) {
	d := &DenyEnv{Seed: 0}
	// A draw where h%(denyMaxBytes+1) == 0 produces n=0, which returns "".
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

// TestDenyEnvBoundedDenyResult covers the truncation function.
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

// TestDenyEnvBoundedDenyResultNeverSplitsRune pins that the 512-byte cap lands
// on a rune boundary instead of emitting invalid UTF-8.
func TestDenyEnvBoundedDenyResultNeverSplitsRune(t *testing.T) {
	// 3-byte runes: 512 is not a multiple of 3, so the cap would fall mid-rune.
	long := strings.Repeat("€", denyMaxBytes)
	if len(long) <= denyMaxBytes {
		t.Fatalf("fixture is %d bytes, must exceed denyMaxBytes=%d", len(long), denyMaxBytes)
	}
	got := boundedDenyResult(long)
	if !utf8.ValidString(got) {
		t.Fatalf("boundedDenyResult emitted invalid UTF-8: the cap split a rune")
	}
	if len(got) > denyMaxBytes {
		t.Fatalf("boundedDenyResult returned %d bytes, want <= %d", len(got), denyMaxBytes)
	}
}

// TestDenyEnvReadFile covers ReadFile's error and success paths.
func TestDenyEnvReadFile(t *testing.T) {
	// Find a seed that produces h%5==0 (error).
	for seed := range uint64(100) {
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
	for seed := range uint64(100) {
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

// TestDenyEnvWriteFile covers WriteFile.
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

// TestDenyEnvEditFile covers EditFile's error and success paths.
func TestDenyEnvEditFile(t *testing.T) {
	// Find a seed that produces h%3==0 (error).
	for seed := range uint64(100) {
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
	for seed := range uint64(100) {
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

// TestDenyEnvFileExists covers FileExists.
func TestDenyEnvFileExists(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	// FileExists is fabricated from the seed: several paths must produce both
	// outcomes, and each path must stay deterministic.
	seen := map[bool]bool{}
	for _, path := range []string{"/test/a", "/test/b", "/test/c", "/test/d"} {
		got := d.FileExists(path)
		seen[got] = true
		if again := d.FileExists(path); again != got {
			t.Fatalf("FileExists(%q) = %v then %v; want deterministic", path, got, again)
		}
	}
	if !seen[true] || !seen[false] {
		t.Fatalf("FileExists over four paths saw outcomes %v, want both", seen)
	}
}

// TestDenyEnvGlob covers Glob.
func TestDenyEnvGlob(t *testing.T) {
	d := &DenyEnv{Seed: 1}
	results, err := d.Glob(context.Background(), "*.go", "/base", true)
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

// TestDenyEnvGrep covers Grep's three output modes.
func TestDenyEnvGrep(t *testing.T) {
	d := &DenyEnv{Seed: 1}

	// count mode returns the seed-derived count as a 0-9 integer.
	result, err := d.Grep(context.Background(), "pattern", "/path", "", false, 0, "count")
	if err != nil {
		t.Fatalf("Grep count: %v", err)
	}
	count, err := strconv.Atoi(result)
	if err != nil || count < 0 || count > 9 {
		t.Fatalf("Grep count = %q, want a 0-9 integer", result)
	}

	// files_with_matches mode returns the searched path or no match.
	result, err = d.Grep(context.Background(), "pattern", "/path", "", false, 0, "files_with_matches")
	if err != nil {
		t.Fatalf("Grep files_with_matches: %v", err)
	}
	if result != "" && result != "/path" {
		t.Fatalf("Grep files_with_matches = %q, want /path or empty", result)
	}

	// content mode returns seed-derived bounded text, deterministically.
	result, err = d.Grep(context.Background(), "pattern", "/path", "", false, 0, "content")
	if err != nil {
		t.Fatalf("Grep content: %v", err)
	}
	if len(result) > denyMaxBytes {
		t.Fatalf("Grep content returned %d bytes, want <= %d", len(result), denyMaxBytes)
	}
	again, err := d.Grep(context.Background(), "pattern", "/path", "", false, 0, "content")
	if err != nil || again != result {
		t.Fatalf("Grep content is not deterministic: %q vs %q (err=%v)", result, again, err)
	}
}

// TestDenyEnvListDirectory covers ListDirectory.
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

// TestDenyEnvExecCommand covers ExecCommand's error and success paths.
func TestDenyEnvExecCommand(t *testing.T) {
	// Find a seed that produces h%7==0 (error).
	for seed := range uint64(100) {
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
	for seed := range uint64(100) {
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

// TestDenyEnvStreamCommand covers StreamCommand.
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

// TestDenyEnvStreamCommand_NilWriter covers StreamCommand with nil writer.
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

// TestDenyEnvInitialize covers Initialize.
func TestDenyEnvInitialize(t *testing.T) {
	d := &DenyEnv{Seed: 0}
	if err := d.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
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
