package agent

import (
	"strings"
	"testing"
)

func TestValidatePluginName(t *testing.T) {
	valid := []string{
		"my-plugin",
		"a",
		"test-123",
		"a-b-c",
		"plugin42",
	}
	for _, name := range valid {
		if err := validatePluginName(name); err != nil {
			t.Errorf("validatePluginName(%q) returned error: %v", name, err)
		}
	}

	invalid := []struct {
		name string
		desc string
	}{
		{"", "empty"},
		{"has spaces", "spaces"},
		{"UPPER", "uppercase"},
		{"under_score", "underscore"},
		{"-leading", "leading hyphen"},
		{"trailing-", "trailing hyphen"},
	}
	for _, tt := range invalid {
		if err := validatePluginName(tt.name); err == nil {
			t.Errorf("validatePluginName(%q) [%s]: expected error, got nil", tt.name, tt.desc)
		}
	}
}

func TestParsePluginManifest_Minimal(t *testing.T) {
	data := []byte(`{"name": "my-plugin"}`)
	m, err := ParsePluginManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "my-plugin" {
		t.Errorf("Name = %q, want %q", m.Name, "my-plugin")
	}
}

func TestParsePluginManifest_FullMetadata(t *testing.T) {
	data := []byte(`{
		"name": "full-plugin",
		"version": "1.2.3",
		"description": "A full plugin",
		"author": {"name": "Jesse", "email": "j@example.com", "url": "https://example.com"},
		"homepage": "https://example.com/full-plugin",
		"repository": "https://github.com/example/full-plugin",
		"license": "MIT",
		"keywords": ["test", "full"],
		"commands": ["/greet"],
		"agents": {"helper": {"description": "helps"}},
		"hooks": {"on-start": "echo hi"},
		"mcpServers": {"srv": {"command": "run-it"}}
	}`)
	m, err := ParsePluginManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "full-plugin" {
		t.Errorf("Name = %q, want %q", m.Name, "full-plugin")
	}
	if m.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", m.Version, "1.2.3")
	}
	if m.Description != "A full plugin" {
		t.Errorf("Description = %q, want %q", m.Description, "A full plugin")
	}
	if m.Homepage != "https://example.com/full-plugin" {
		t.Errorf("Homepage = %q", m.Homepage)
	}
	if m.Repository != "https://github.com/example/full-plugin" {
		t.Errorf("Repository = %q", m.Repository)
	}
	if m.License != "MIT" {
		t.Errorf("License = %q, want %q", m.License, "MIT")
	}
	if len(m.Keywords) != 2 || m.Keywords[0] != "test" || m.Keywords[1] != "full" {
		t.Errorf("Keywords = %v, want [test full]", m.Keywords)
	}
	if m.Author == nil {
		t.Error("Author is nil, expected object")
	}
	if m.Commands == nil {
		t.Error("Commands is nil, expected data")
	}
	if m.Agents == nil {
		t.Error("Agents is nil, expected data")
	}
	if m.Hooks == nil {
		t.Error("Hooks is nil, expected data")
	}
	if m.MCPServers == nil {
		t.Error("MCPServers is nil, expected data")
	}
}

func TestParsePluginManifest_AuthorString(t *testing.T) {
	data := []byte(`{"name": "a", "author": "Jesse"}`)
	m, err := ParsePluginManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Author == nil {
		t.Fatal("Author is nil, expected string value")
	}
}

func TestParsePluginManifest_MissingName(t *testing.T) {
	data := []byte(`{"version": "1.0.0"}`)
	_, err := ParsePluginManifest(data)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q should mention 'name'", err.Error())
	}
}

func TestParsePluginManifest_InvalidNames(t *testing.T) {
	names := []string{
		"has spaces",
		"UPPERCASE",
		"-leading",
		"trailing-",
		"under_score",
	}
	for _, name := range names {
		data := []byte(`{"name": "` + name + `"}`)
		_, err := ParsePluginManifest(data)
		if err == nil {
			t.Errorf("ParsePluginManifest with name %q: expected error, got nil", name)
		}
	}
}

func TestParsePluginManifest_InvalidJSON(t *testing.T) {
	_, err := ParsePluginManifest([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExpandPluginRoot(t *testing.T) {
	tests := []struct {
		input     string
		pluginDir string
		want      string
	}{
		{
			"${CLAUDE_PLUGIN_ROOT}/bin/tool",
			"/home/user/.plugins/my-plugin",
			"/home/user/.plugins/my-plugin/bin/tool",
		},
		{
			"no variable here",
			"/some/dir",
			"no variable here",
		},
		{
			"${CLAUDE_PLUGIN_ROOT}",
			"/plugins/x",
			"/plugins/x",
		},
		{
			"prefix-${CLAUDE_PLUGIN_ROOT}-suffix",
			"/p",
			"prefix-/p-suffix",
		},
	}
	for _, tt := range tests {
		got := expandPluginRoot(tt.input, tt.pluginDir)
		if got != tt.want {
			t.Errorf("expandPluginRoot(%q, %q) = %q, want %q", tt.input, tt.pluginDir, got, tt.want)
		}
	}
}
