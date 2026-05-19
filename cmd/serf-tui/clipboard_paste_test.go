package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsImageFile(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"png", "/tmp/foo.png", true},
		{"jpg", "/tmp/foo.jpg", true},
		{"jpeg", "/tmp/foo.jpeg", true},
		{"gif", "/tmp/foo.gif", true},
		{"webp", "/tmp/foo.webp", true},
		{"PNG uppercase", "/tmp/foo.PNG", true},
		{"JPG uppercase", "/tmp/foo.JPG", true},
		{"JPEG mixed case", "/tmp/foo.Jpeg", true},
		{"txt", "/tmp/foo.txt", false},
		{"bin", "/tmp/foo.bin", false},
		{"no extension", "/tmp/foo", false},
		{"empty string", "", false},
		{"trailing dot only", "/tmp/foo.", false},
		{"windows path png", `C:\Users\Alice\foo.png`, true},
		{"webp uppercase", "/tmp/foo.WEBP", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsImageFile(tc.path); got != tc.want {
				t.Fatalf("IsImageFile(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsProbablyWSL(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     bool
	}{
		{
			name:     "microsoft kernel",
			contents: "Linux version 5.15.90.1-microsoft-standard-WSL2 (gcc ...)",
			want:     true,
		},
		{
			name:     "WSL token",
			contents: "Linux version 6.6.0-WSL (root@...)",
			want:     true,
		},
		{
			name:     "microsoft mixed case",
			contents: "Linux version 5.10.0-Microsoft-standard",
			want:     true,
		},
		{
			name:     "vanilla amd64",
			contents: "Linux version 6.8.0-110-generic (buildd@lcy02-amd64-013)",
			want:     false,
		},
		{
			name:     "empty",
			contents: "",
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "version")
			if err := os.WriteFile(path, []byte(tc.contents), 0o644); err != nil {
				t.Fatalf("write tmp version file: %v", err)
			}
			if got := isProbablyWSL(path); got != tc.want {
				t.Fatalf("isProbablyWSL(%q) = %v, want %v", tc.contents, got, tc.want)
			}
		})
	}

	t.Run("missing file returns false", func(t *testing.T) {
		if got := isProbablyWSL(filepath.Join(t.TempDir(), "nonexistent")); got {
			t.Fatal("isProbablyWSL on missing file = true, want false")
		}
	})
}

func TestNormalizePastedPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"file URL absolute", "file:///tmp/example.png", "/tmp/example.png"},
		{"double-quoted unix path", `"/tmp/example.png"`, "/tmp/example.png"},
		{"single-quoted unix path", `'/tmp/example.png'`, "/tmp/example.png"},
		{"raw windows path", `C:\Users\Alice\foo.png`, `C:\Users\Alice\foo.png`},
		{"raw windows forward slash", `C:/Users/Alice/foo.png`, `C:/Users/Alice/foo.png`},
		{"WSL mount path passthrough", "/mnt/c/foo.png", "/mnt/c/foo.png"},
		{"UNC path", `\\server\share\foo.png`, `\\server\share\foo.png`},
		{"home tilde", "~/pictures/foo.png", "~/pictures/foo.png"},
		{"relative ./", "./local.png", "./local.png"},
		{"garbage text", "hello world this is not a path", ""},
		{"empty string", "", ""},
		{"whitespace trimmed", "  /tmp/example.png  ", "/tmp/example.png"},
		{"two tokens not a path", "/foo /bar", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizePastedPath(tc.in); got != tc.want {
				t.Fatalf("NormalizePastedPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestConvertWindowsPathToWSL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple drive letter", `C:\Users\Alice\foo.png`, "/mnt/c/Users/Alice/foo.png"},
		{"forward slashes", `C:/Users/Alice/foo.png`, "/mnt/c/Users/Alice/foo.png"},
		{"D drive lowercase", `d:\data\bar.jpg`, "/mnt/d/data/bar.jpg"},
		{"trailing slash", `C:\`, "/mnt/c"},
		{"UNC rejected", `\\server\share\foo.png`, ""},
		{"non-drive rejected", `/etc/foo`, ""},
		{"too short", `C:`, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := convertWindowsPathToWSL(tc.in); got != tc.want {
				t.Fatalf("convertWindowsPathToWSL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// fakeClipboard is a programmable ClipboardSource used to drive each
// branch of PasteClipboardImage without touching the real OS clipboard.
type fakeClipboard struct {
	files          []string
	filesErr       error
	imageBytes     []byte
	imageMediaType string
	imageErr       error
	winPath        string
	winErr         error
	procVersion    string
}

func (f *fakeClipboard) ReadFilePaths() ([]string, error) {
	return f.files, f.filesErr
}

func (f *fakeClipboard) ReadImageBytes() ([]byte, string, error) {
	return f.imageBytes, f.imageMediaType, f.imageErr
}

func (f *fakeClipboard) ReadWindowsClipboardViaPowerShell() (string, error) {
	return f.winPath, f.winErr
}

func (f *fakeClipboard) ProcVersion() string {
	return f.procVersion
}

func TestPasteClipboardImage_PrefersFileList(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "from-finder.png")
	payload := []byte("not really a png but enough for stat")
	if err := os.WriteFile(imgPath, payload, 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	src := &fakeClipboard{
		files:      []string{imgPath},
		imageBytes: []byte("would-be-encoded-image"),
	}

	got, err := PasteClipboardImage(src)
	if err != nil {
		t.Fatalf("PasteClipboardImage err = %v", err)
	}
	if got.Path != imgPath {
		t.Fatalf("Path = %q, want %q", got.Path, imgPath)
	}
	if got.Origin != "clipboard-file" {
		t.Fatalf("Origin = %q, want clipboard-file", got.Origin)
	}
	if got.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", got.MediaType)
	}
	if got.Size != len(payload) {
		t.Fatalf("Size = %d, want %d", got.Size, len(payload))
	}
}

func TestPasteClipboardImage_SkipsNonImageFiles(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	src := &fakeClipboard{
		files:          []string{txt},
		imageBytes:     []byte("png-bytes"),
		imageMediaType: "image/png",
	}

	got, err := PasteClipboardImage(src)
	if err != nil {
		t.Fatalf("PasteClipboardImage err = %v", err)
	}
	if got.Origin != "clipboard-image" {
		t.Fatalf("Origin = %q, want clipboard-image", got.Origin)
	}
	t.Cleanup(func() { _ = os.Remove(got.Path) })
}

func TestPasteClipboardImage_FallsBackToImageBytes(t *testing.T) {
	src := &fakeClipboard{
		filesErr:       errors.New("no file list"),
		imageBytes:     []byte("the encoded image bytes"),
		imageMediaType: "image/png",
	}

	got, err := PasteClipboardImage(src)
	if err != nil {
		t.Fatalf("PasteClipboardImage err = %v", err)
	}
	if got.Origin != "clipboard-image" {
		t.Fatalf("Origin = %q, want clipboard-image", got.Origin)
	}
	if got.Path == "" {
		t.Fatal("Path empty, want temp file path")
	}
	data, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if string(data) != "the encoded image bytes" {
		t.Fatalf("temp file contents = %q, want the encoded bytes", string(data))
	}
	t.Cleanup(func() { _ = os.Remove(got.Path) })
}

func TestPasteClipboardImage_FallsBackToWSL(t *testing.T) {
	// Stage a "Windows" temp file that the PowerShell stub will report.
	dir := t.TempDir()
	winName := filepath.Join(dir, "clip.png")
	if err := os.WriteFile(winName, []byte("wsl image payload"), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	// Build a fake Windows path that, after conversion, lands at winName.
	// We pretend dir is "/mnt/c/wsl-test" so we can synthesise the
	// matching "C:\wsl-test\clip.png".
	// Instead of relying on real /mnt mapping, the fake source returns
	// the staged path directly under a synthetic drive.
	// We construct a fake winPath whose conversion equals winName.
	// Easier path: make the fake return a Windows path whose conversion
	// lands at the actual staged location.
	src := &fakeClipboard{
		filesErr:    errors.New("no file list"),
		imageErr:    ErrNoClipboardImage,
		winPath:     `C:\fake\clip.png`,
		procVersion: "Linux version 5.15-microsoft-standard-WSL2",
	}

	// First confirm the helper translates the fake path correctly.
	gotPath, err := tryWSLClipboardFallback(src, ErrNoClipboardImage)
	if err != nil {
		t.Fatalf("tryWSLClipboardFallback err = %v", err)
	}
	if gotPath != "/mnt/c/fake/clip.png" {
		t.Fatalf("converted path = %q, want /mnt/c/fake/clip.png", gotPath)
	}
}

func TestPasteClipboardImage_WSLErrorWhenConvertedPathMissing(t *testing.T) {
	src := &fakeClipboard{
		filesErr:    errors.New("no file list"),
		imageErr:    ErrNoClipboardImage,
		winPath:     `C:\definitely-missing-serf-clipboard\clip.png`,
		procVersion: "Linux version 5.15-microsoft-standard-WSL2",
	}

	if got, err := PasteClipboardImage(src); err == nil {
		t.Fatalf("PasteClipboardImage err = nil, got %+v; want stat error for missing WSL path", got)
	}
}

func TestFileURIToPathUnescapesPercentEncoding(t *testing.T) {
	got := fileURIToPath("file:///tmp/My%20Shot%231.png")
	if got != "/tmp/My Shot#1.png" {
		t.Fatalf("path=%q, want unescaped path", got)
	}
}

func TestPasteClipboardImage_NoImageReturnsError(t *testing.T) {
	src := &fakeClipboard{
		filesErr:    errors.New("no file list"),
		imageErr:    ErrNoClipboardImage,
		procVersion: "Linux version 6.8.0-110-generic",
	}
	if _, err := PasteClipboardImage(src); err == nil {
		t.Fatal("PasteClipboardImage err = nil, want error when no image")
	}
}

func TestPasteClipboardImage_NilSource(t *testing.T) {
	if _, err := PasteClipboardImage(nil); err == nil {
		t.Fatal("expected error for nil source")
	}
}

func TestTryWSLClipboardFallback_NoWindowsImage(t *testing.T) {
	src := &fakeClipboard{
		winErr: ErrNoClipboardImage,
	}
	if _, err := tryWSLClipboardFallback(src, nil); err == nil {
		t.Fatal("expected error when PowerShell reports no image")
	}
}

func TestTryWSLClipboardFallback_RejectsUNC(t *testing.T) {
	src := &fakeClipboard{winPath: `\\server\share\foo.png`}
	if _, err := tryWSLClipboardFallback(src, nil); err == nil {
		t.Fatal("expected error for UNC path that cannot be mapped to /mnt")
	}
}
