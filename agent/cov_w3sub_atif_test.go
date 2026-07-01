package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// w3sub_writeATIFTranscript writes a minimal one-turn transcript and returns its
// path, so the exportATIF error-arm tests can reach the output-writing steps
// with valid upstream state.
func w3sub_writeATIFTranscript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sessions", "sess.transcript.jsonl")
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	tw, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "sess", Model: "gpt-5.3-codex", ProfileID: "openai", CreatedAt: ts,
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("hi"), Timestamp: ts}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func TestW3Sub_ExportATIF_ErrorArms(t *testing.T) {
	t.Run("invalid provider handle mode", func(t *testing.T) {
		dir := t.TempDir()
		src := w3sub_writeATIFTranscript(t, dir)
		if err := exportATIF(src, filepath.Join(dir, "out.json"), "not-a-real-mode"); err == nil {
			t.Fatalf("expected NormalizeProviderHandleMode error")
		}
	})

	t.Run("unreadable transcript", func(t *testing.T) {
		dir := t.TempDir()
		if err := exportATIF(filepath.Join(dir, "missing.jsonl"), filepath.Join(dir, "out.json"), ""); err == nil {
			t.Fatalf("expected read-transcript error")
		}
	})

	t.Run("output dir creation blocked by a file", func(t *testing.T) {
		dir := t.TempDir()
		src := w3sub_writeATIFTranscript(t, dir)
		// A regular file where a parent directory component is expected makes
		// MkdirAll fail.
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed blocker: %v", err)
		}
		if err := exportATIF(src, filepath.Join(blocker, "child", "out.json"), ""); err == nil {
			t.Fatalf("expected MkdirAll error")
		}
	})

	t.Run("output path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		src := w3sub_writeATIFTranscript(t, dir)
		outDir := filepath.Join(dir, "out.json")
		if err := os.Mkdir(outDir, 0o755); err != nil {
			t.Fatalf("seed out dir: %v", err)
		}
		if err := exportATIF(src, outDir, ""); err == nil {
			t.Fatalf("expected WriteFile error when output path is a directory")
		}
	})
}
