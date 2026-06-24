package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

func TestLoadPluginSettings_Valid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\napi_key_env: MY_KEY\nmax_retries: 3\n---\nCustom instructions for this project.\n"
	if err := os.WriteFile(filepath.Join(claudeDir, "my-plugin.local.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ps, err := plugin.LoadSettings(dir, "my-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil plugin.Settings")
	}
	if ps.Frontmatter["api_key_env"] != "MY_KEY" {
		t.Errorf("api_key_env = %v, want %q", ps.Frontmatter["api_key_env"], "MY_KEY")
	}
	if ps.Frontmatter["max_retries"] != 3 {
		t.Errorf("max_retries = %v, want 3", ps.Frontmatter["max_retries"])
	}
	if ps.Body != "Custom instructions for this project.\n" {
		t.Errorf("Body = %q, want %q", ps.Body, "Custom instructions for this project.\n")
	}
}

func TestLoadPluginSettings_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ps, err := plugin.LoadSettings(dir, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ps != nil {
		t.Errorf("expected nil, got %+v", ps)
	}
}

func TestLoadPluginSettings_FrontmatterOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nenabled: true\n---\n"
	if err := os.WriteFile(filepath.Join(claudeDir, "my-plugin.local.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ps, err := plugin.LoadSettings(dir, "my-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil plugin.Settings")
	}
	if ps.Frontmatter["enabled"] != true {
		t.Errorf("enabled = %v, want true", ps.Frontmatter["enabled"])
	}
	if ps.Body != "" {
		t.Errorf("Body = %q, want empty", ps.Body)
	}
}

func TestLoadPluginSettings_BodyOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "Just markdown, no frontmatter.\n"
	if err := os.WriteFile(filepath.Join(claudeDir, "my-plugin.local.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ps, err := plugin.LoadSettings(dir, "my-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil plugin.Settings")
	}
	if ps.Frontmatter != nil {
		t.Errorf("Frontmatter = %v, want nil", ps.Frontmatter)
	}
	if ps.Body != content {
		t.Errorf("Body = %q, want %q", ps.Body, content)
	}
}

func TestLoadPluginSettings_InvalidYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\n: bad: yaml: [unclosed\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(claudeDir, "my-plugin.local.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := plugin.LoadSettings(dir, "my-plugin")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
