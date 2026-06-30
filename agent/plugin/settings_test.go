package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadSettings exercises every branch of LoadSettings — the previously
// untested .claude/<plugin>.local.md parse seam (gremlins flagged settings.go
// lines 21/26/31 as not covered). Cases: missing file (nil,nil), a valid
// frontmatter+body file, a non-IsNotExist read error, and frontmatter that fails
// to parse.
func TestLoadSettings(t *testing.T) {
	t.Run("missing file returns nil,nil", func(t *testing.T) {
		s, err := LoadSettings(t.TempDir(), "myplugin")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if s != nil {
			t.Fatalf("settings = %#v, want nil", s)
		}
	})

	t.Run("valid frontmatter and body", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteLocalMD(t, dir, "myplugin", "---\nmodel: gpt-5.2\nenabled: true\n---\nproject notes\n")
		s, err := LoadSettings(dir, "myplugin")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if s == nil {
			t.Fatal("settings = nil, want non-nil")
		}
		if s.Frontmatter["model"] != "gpt-5.2" || s.Frontmatter["enabled"] != true {
			t.Fatalf("frontmatter = %#v", s.Frontmatter)
		}
		if s.Body != "project notes\n" {
			t.Fatalf("body = %q, want %q", s.Body, "project notes\n")
		}
	})

	t.Run("non-IsNotExist read error propagates", func(t *testing.T) {
		dir := t.TempDir()
		// Make the settings PATH a directory: os.ReadFile then fails with a
		// non-IsNotExist error, exercising the err!=nil branch.
		if err := os.MkdirAll(filepath.Join(dir, ".claude", "myplugin.local.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(dir, "myplugin"); err == nil {
			t.Fatal("err = nil, want a read error for a directory path")
		}
	})

	t.Run("malformed frontmatter propagates a parse error", func(t *testing.T) {
		dir := t.TempDir()
		// A framed block whose YAML is invalid (unterminated quote) makes
		// frontmatter.Parse return an error.
		mustWriteLocalMD(t, dir, "myplugin", "---\nkey: \"unterminated\n---\nbody\n")
		if _, err := LoadSettings(dir, "myplugin"); err == nil {
			t.Fatal("err = nil, want a frontmatter parse error")
		}
	})
}

func mustWriteLocalMD(t *testing.T, workDir, pluginName, content string) {
	t.Helper()
	dir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginName+".local.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
