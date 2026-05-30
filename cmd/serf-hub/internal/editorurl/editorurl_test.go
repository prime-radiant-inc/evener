package editorurl

import (
	"strings"
	"testing"
)

func TestEditorURL_DefaultIsVSCode(t *testing.T) {
	t.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "")
	got := string(EditorURL("/Users/jesse/code/foo.go"))
	if !strings.HasPrefix(got, "vscode://file/") {
		t.Errorf("got %q, want vscode://file/ prefix", got)
	}
	if !strings.HasSuffix(got, "/foo.go") {
		t.Errorf("got %q, want /foo.go suffix", got)
	}
}

func TestEditorURL_PreservesPathSeparators(t *testing.T) {
	t.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "")
	got := string(EditorURL("/Users/jesse/code/foo.go"))
	want := "vscode://file/Users/jesse/code/foo.go"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditorURL_EnvOverride(t *testing.T) {
	t.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "cursor://file/{path}")
	got := string(EditorURL("/Users/jesse/code/foo.go"))
	want := "cursor://file/Users/jesse/code/foo.go"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditorURL_RejectsRelative(t *testing.T) {
	t.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "")
	got := string(EditorURL("agent/agents/default.md"))
	if got != "" {
		t.Errorf("expected empty string for relative path, got %q", got)
	}
}

func TestEditorURL_EmptyPath(t *testing.T) {
	t.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "")
	got := string(EditorURL(""))
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestEditorURL_EncodesSpecialChars(t *testing.T) {
	t.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "vscode://file/{path}")
	got := string(EditorURL("/Users/jesse/My Code/foo bar.go"))
	if !strings.Contains(got, "My%20Code") {
		t.Errorf("got %q, want spaces percent-encoded", got)
	}
}

func TestEditorURL_QueryStyleTemplate(t *testing.T) {
	t.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "idea://open?file={path}")
	got := string(EditorURL("/Users/jesse/code/foo.go"))
	want := "idea://open?file=Users/jesse/code/foo.go"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
