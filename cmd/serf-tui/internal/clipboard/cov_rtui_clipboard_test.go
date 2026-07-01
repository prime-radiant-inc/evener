package clipboard

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestMediaTypeForPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"png", "/tmp/a.png", "image/png"},
		{"png uppercase", "/tmp/a.PNG", "image/png"},
		{"jpg", "/tmp/a.jpg", "image/jpeg"},
		{"jpeg", "/tmp/a.jpeg", "image/jpeg"},
		{"gif", "/tmp/a.gif", "image/gif"},
		{"webp", "/tmp/a.webp", "image/webp"},
		{"unknown extension defaults to png", "/tmp/a.bmp", "image/png"},
		{"no extension defaults to png", "/tmp/a", "image/png"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MediaTypeForPath(tc.path); got != tc.want {
				t.Fatalf("MediaTypeForPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsWindowsPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"UNC", `\\server\share`, true},
		{"drive backslash", `C:\foo`, true},
		{"drive forward slash", `C:/foo`, true},
		{"lowercase drive", `d:/foo`, true},
		{"too short", `C:`, false},
		{"empty", ``, false},
		{"non-alpha first char", `1:\foo`, false},
		{"missing colon", `Cx\foo`, false},
		{"third char not a slash", `C:foo`, false},
		{"posix path", `/etc/hosts`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsWindowsPath(tc.in); got != tc.want {
				t.Fatalf("IsWindowsPath(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsProbablyWSLFromSource(t *testing.T) {
	if IsProbablyWSLFromSource(nil) {
		t.Fatal("IsProbablyWSLFromSource(nil) = true, want false")
	}
	if !IsProbablyWSLFromSource(&fakeClipboard{procVersion: "Linux ... microsoft ... WSL2"}) {
		t.Fatal("IsProbablyWSLFromSource(microsoft) = false, want true")
	}
	if IsProbablyWSLFromSource(&fakeClipboard{procVersion: "Linux version 6.8.0-generic"}) {
		t.Fatal("IsProbablyWSLFromSource(generic) = true, want false")
	}
}

func TestFileURIToPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no file prefix passes through", "/tmp/plain.png", "/tmp/plain.png"},
		{"empty after prefix", "file://", ""},
		{"absolute path", "file:///tmp/a.png", "/tmp/a.png"},
		{"hostname stripped", "file://localhost/tmp/a.png", "/tmp/a.png"},
		{"host with no path segment", "file://hostonly", ""},
		{"percent decoded", "file:///tmp/My%20Shot.png", "/tmp/My Shot.png"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileURIToPath(tc.in); got != tc.want {
				t.Fatalf("FileURIToPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseURIList(t *testing.T) {
	in := "# comment line\n" +
		"\n" +
		"file:///tmp/one.png\n" +
		"   file:///tmp/two%20space.png   \n" +
		"http://example.com/not-a-file\n" +
		"file://localhost/tmp/three.png\n"
	got := ParseURIList(in)
	want := []string{"/tmp/one.png", "/tmp/two space.png", "/tmp/three.png"}
	if len(got) != len(want) {
		t.Fatalf("ParseURIList returned %d entries (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseURIList[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if out := ParseURIList(""); len(out) != 0 {
		t.Fatalf("ParseURIList(\"\") = %v, want empty", out)
	}
}

func TestTryWSLClipboardFallback_PriorErrorPropagates(t *testing.T) {
	prior := errors.New("original failure")

	// PowerShell reports no image; prior error takes precedence.
	src := &fakeClipboard{winErr: ErrNoClipboardImage}
	if _, err := TryWSLClipboardFallback(src, prior); !errors.Is(err, prior) {
		t.Fatalf("err = %v, want prior error to propagate", err)
	}

	// PowerShell returns empty path; prior error takes precedence.
	srcEmpty := &fakeClipboard{winPath: "   "}
	if _, err := TryWSLClipboardFallback(srcEmpty, prior); !errors.Is(err, prior) {
		t.Fatalf("empty-path err = %v, want prior error", err)
	}

	// Empty path with no prior error surfaces ErrNoClipboardImage.
	if _, err := TryWSLClipboardFallback(srcEmpty, nil); !errors.Is(err, ErrNoClipboardImage) {
		t.Fatalf("empty-path nil-prior err = %v, want ErrNoClipboardImage", err)
	}

	// UNC path cannot be converted; prior error takes precedence.
	srcUNC := &fakeClipboard{winPath: `\\server\share\clip.png`}
	if _, err := TryWSLClipboardFallback(srcUNC, prior); !errors.Is(err, prior) {
		t.Fatalf("unc err = %v, want prior error", err)
	}
}

func TestTryWSLClipboardFallback_NilSource(t *testing.T) {
	if _, err := TryWSLClipboardFallback(nil, nil); err == nil {
		t.Fatal("TryWSLClipboardFallback(nil) = nil error, want error")
	}
}

func TestWriteTempPNG_WritesBytes(t *testing.T) {
	data := []byte("some png bytes")
	path, err := WriteTempPNG(data)
	if err != nil {
		t.Fatalf("WriteTempPNG err = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if path == "" {
		t.Fatal("WriteTempPNG returned empty path")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp png: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("temp png contents = %q, want %q", got, data)
	}
}
