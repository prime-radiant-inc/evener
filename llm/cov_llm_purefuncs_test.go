package llm

import (
	"mime"
	"path/filepath"
	"testing"
)

func TestErrorClassStringExhaustive(t *testing.T) {
	tests := []struct {
		class ErrorClass
		want  string
	}{
		{ErrorClassRetryable, "retryable"},
		{ErrorClassPermanent, "permanent"},
		{ErrorClass(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.class.String(); got != tt.want {
			t.Errorf("ErrorClass(%d).String() = %q, want %q", int(tt.class), got, tt.want)
		}
	}
}

func TestExpandTilde(t *testing.T) {
	t.Run("no tilde prefix returns trimmed input", func(t *testing.T) {
		if got := ExpandTilde("  relative/path  "); got != "relative/path" {
			t.Errorf("ExpandTilde = %q, want %q", got, "relative/path")
		}
		if got := ExpandTilde("/abs/path"); got != "/abs/path" {
			t.Errorf("ExpandTilde = %q, want %q", got, "/abs/path")
		}
	})

	t.Run("tilde expands to home", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		want := filepath.Join(home, "sub", "dir")
		if got := ExpandTilde("~/sub/dir"); got != want {
			t.Errorf("ExpandTilde = %q, want %q", got, want)
		}
	})

	t.Run("empty home leaves tilde path unchanged", func(t *testing.T) {
		t.Setenv("HOME", "")
		if got := ExpandTilde("~/sub"); got != "~/sub" {
			t.Errorf("ExpandTilde = %q, want %q (unchanged)", got, "~/sub")
		}
	})
}

func TestInferMimeTypeFromPath(t *testing.T) {
	// Register a deterministic charset-bearing type so the charset-strip branch
	// is exercised regardless of the host's /etc/mime.types.
	if err := mime.AddExtensionType(".covtxt", "text/plain; charset=utf-8"); err != nil {
		t.Fatalf("AddExtensionType: %v", err)
	}

	tests := []struct {
		path string
		want string
	}{
		{"noextension", ""},
		{"", ""},
		{"image.png", "image/png"},
		{"doc.covtxt", "text/plain"}, // charset stripped
		{"archive.zzzznope", ""},     // unknown extension
		{"UPPER.PNG", "image/png"},   // case-insensitive
	}
	for _, tt := range tests {
		if got := InferMimeTypeFromPath(tt.path); got != tt.want {
			t.Errorf("InferMimeTypeFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestDataURI(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		data     []byte
		want     string
	}{
		{"empty mime uses octet-stream", "", []byte("hi"), "data:application/octet-stream;base64,aGk="},
		{"whitespace mime uses octet-stream", "   ", []byte("hi"), "data:application/octet-stream;base64,aGk="},
		{"explicit mime", "image/png", []byte("hi"), "data:image/png;base64,aGk="},
		{"empty data", "text/plain", nil, "data:text/plain;base64,"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DataURI(tt.mimeType, tt.data); got != tt.want {
				t.Errorf("DataURI = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripMarkdownCodeFence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no fence trims whitespace", "  {\"a\":1}  ", `{"a":1}`},
		{"fenced json", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"bare fence", "```\n[1,2]\n```", `[1,2]`},
		{"opening fence with no newline returned as-is", "```json", "```json"},
		{"fence without closing", "```\n{\"a\":1}", `{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripMarkdownCodeFence(tt.in); got != tt.want {
				t.Errorf("stripMarkdownCodeFence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStripMarkdownCodeFenceNested(t *testing.T) {
	// The opening line is dropped and content is trimmed at the LAST closing
	// fence, so an interior fence survives.
	in := "```\nline1\n```\nline2\n```"
	want := "line1\n```\nline2"
	if got := stripMarkdownCodeFence(in); got != want {
		t.Errorf("stripMarkdownCodeFence(%q) = %q, want %q", in, got, want)
	}
}
