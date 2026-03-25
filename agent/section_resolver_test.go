package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiskSource_ReadFile(t *testing.T) {
	dir := t.TempDir()
	content := "I am serf"
	if err := os.WriteFile(filepath.Join(dir, "identity.md"), []byte(content), 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	src := diskSource{dir: dir}

	data, ok := src.ReadFile("identity.md")
	if !ok {
		t.Fatal("expected ok=true for existing file")
	}
	if string(data) != content {
		t.Errorf("got %q, want %q", string(data), content)
	}

	// Missing file returns (nil, false).
	data, ok = src.ReadFile("nonexistent.md")
	if ok {
		t.Error("expected ok=false for missing file")
	}
	if data != nil {
		t.Error("expected nil data for missing file")
	}
}

func TestDiskSource_EmptyDir(t *testing.T) {
	src := diskSource{dir: ""}
	data, ok := src.ReadFile("anything.md")
	if ok {
		t.Error("expected ok=false for empty dir")
	}
	if data != nil {
		t.Error("expected nil data for empty dir")
	}
}

func TestEmbedSource_ReadFile(t *testing.T) {
	src := embedSource{fs: embeddedPrompts, prefix: "prompts/"}

	data, ok := src.ReadFile("core.md")
	if !ok {
		t.Fatal("expected ok=true for embedded core.md")
	}
	if len(data) == 0 {
		t.Error("expected non-empty content for core.md")
	}

	// Missing file returns (nil, false).
	data, ok = src.ReadFile("nonexistent.md")
	if ok {
		t.Error("expected ok=false for missing embedded file")
	}
	if data != nil {
		t.Error("expected nil data for missing embedded file")
	}
}
