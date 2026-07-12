package editorurl

import (
	"strings"
	"testing"
)

func FuzzEditorURL(f *testing.F) {
	f.Setenv("SERF_HUB_EDITOR_URL_TEMPLATE", "")
	for _, path := range []string{"", "relative/file.go", "/tmp/file.go", "/tmp/a b/%file.go"} {
		f.Add(path)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if len(path) > 4096 {
			t.Skip()
		}
		got := string(EditorURL(path))
		if path == "" || !strings.HasPrefix(path, "/") {
			if got != "" {
				t.Fatalf("EditorURL(%q) = %q, want empty", path, got)
			}
			return
		}
		if !strings.HasPrefix(got, "vscode://file/") || strings.Contains(got, " ") {
			t.Fatalf("EditorURL(%q) = %q", path, got)
		}
	})
}
