package agenttest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
