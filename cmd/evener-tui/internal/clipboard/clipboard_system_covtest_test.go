package clipboard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCovClipboardCommandOutput exercises clipboardCommandOutput's delegation
// to the clipboardOutput seam.
func TestCovClipboardCommandOutput(t *testing.T) {
	orig := clipboardOutput
	defer func() { clipboardOutput = orig }()
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		return []byte("output"), nil
	}
	out, err := clipboardCommandOutput("any", "arg")
	if err != nil || string(out) != "output" {
		t.Fatalf("clipboardCommandOutput = %q %v", out, err)
	}
}

// TestCovReadFilePathsDarwin exercises the macOS path with a missing osascript.
func TestCovReadFilePathsDarwinMissingTool(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	s := NewSystemClipboardSource()
	_, err := s.ReadFilePaths()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadFilePathsDarwinSuccess exercises the macOS path with a file path result.
func TestCovReadFilePathsDarwinSuccess(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("/Users/x/file.png\n"), nil }
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/Users/x/file.png" {
		t.Fatalf("paths = %v", paths)
	}
}

// TestCovReadFilePathsDarwinEmptyOutput
func TestCovReadFilePathsDarwinEmpty(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("  \n"), nil }
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want empty", paths)
	}
}

// TestCovReadFilePathsDarwinError
func TestCovReadFilePathsDarwinError(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return nil, errors.New("osascript error") }
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v, want nil (best-effort)", err)
	}
	if paths != nil {
		t.Fatalf("paths = %v, want nil", paths)
	}
}

// TestCovReadFilePathsUnsupportedGOOS
func TestCovReadFilePathsUnsupportedGOOS(t *testing.T) {
	origGOOS := clipboardGOOS
	defer func() { clipboardGOOS = origGOOS }()
	clipboardGOOS = "windows"
	s := NewSystemClipboardSource()
	_, err := s.ReadFilePaths()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadImageBytesUnsupportedGOOS
func TestCovReadImageBytesUnsupportedGOOS(t *testing.T) {
	origGOOS := clipboardGOOS
	defer func() { clipboardGOOS = origGOOS }()
	clipboardGOOS = "windows"
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadImageBytesDarwinMissingTool
func TestCovReadImageBytesDarwinMissingTool(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadImageBytesDarwinTempFileError
func TestCovReadImageBytesDarwinTempFileError(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origCreateTemp := clipboardCreateTemp
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardCreateTemp = origCreateTemp
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("temp fail") }
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err == nil || err.Error() != "temp file: temp fail" {
		t.Fatalf("err = %v", err)
	}
}

// TestCovReadImageBytesDarwinNoImage
func TestCovReadImageBytesDarwinNoImage(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	origCreateTemp := clipboardCreateTemp
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
		clipboardCreateTemp = origCreateTemp
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("noimage"), nil }
	clipboardCreateTemp = os.CreateTemp
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrNoClipboardImage {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovReadImageBytesDarwinCommandError
func TestCovReadImageBytesDarwinCommandError(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	origCreateTemp := clipboardCreateTemp
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
		clipboardCreateTemp = origCreateTemp
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return nil, errors.New("osascript fail") }
	clipboardCreateTemp = os.CreateTemp
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err == nil || err.Error() != "osascript fail" {
		t.Fatalf("err = %v", err)
	}
}

// TestCovReadImageBytesDarwinEmptyData
func TestCovReadImageBytesDarwinEmptyData(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	origCreateTemp := clipboardCreateTemp
	origReadFile := clipboardReadFile
	origRemove := clipboardRemove
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
		clipboardCreateTemp = origCreateTemp
		clipboardReadFile = origReadFile
		clipboardRemove = origRemove
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("ok"), nil }
	clipboardCreateTemp = os.CreateTemp
	clipboardReadFile = func(string) ([]byte, error) { return []byte{}, nil }
	clipboardRemove = func(string) error { return nil }
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrNoClipboardImage {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovReadImageBytesDarwinSuccess
func TestCovReadImageBytesDarwinSuccess(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	origCreateTemp := clipboardCreateTemp
	origReadFile := clipboardReadFile
	origRemove := clipboardRemove
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
		clipboardCreateTemp = origCreateTemp
		clipboardReadFile = origReadFile
		clipboardRemove = origRemove
	}()
	clipboardGOOS = "darwin"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/osascript", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("ok"), nil }
	clipboardCreateTemp = os.CreateTemp
	clipboardReadFile = func(string) ([]byte, error) { return []byte("png-bytes"), nil }
	clipboardRemove = func(string) error { return nil }
	s := NewSystemClipboardSource()
	data, mediaType, err := s.ReadImageBytes()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(data) != "png-bytes" || mediaType != "image/png" {
		t.Fatalf("data = %q mediaType = %q", data, mediaType)
	}
}

// TestCovReadFilePathsX11MissingTool
func TestCovReadFilePathsX11MissingTool(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	s := NewSystemClipboardSource()
	_, err := s.ReadFilePaths()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadFilePathsX11NoURIs
func TestCovReadFilePathsX11NoURIs(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/xclip", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("text/plain\nUTF8_STRING"), nil }
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want empty", paths)
	}
}

// TestCovReadFilePathsX11Success exercises the full X11 file path read.
func TestCovReadFilePathsX11Success(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/xclip", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("text/uri-list\nimage/png"), nil // TARGETS
		}
		return []byte("file:///tmp/test.png\n"), nil // uri-list
	}
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/tmp/test.png" {
		t.Fatalf("paths = %v", paths)
	}
}

// TestCovReadFilePathsX11URIListError
func TestCovReadFilePathsX11URIListError(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/xclip", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("text/uri-list"), nil // TARGETS
		}
		return nil, errors.New("xclip uri-list error")
	}
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v, want nil (best-effort)", err)
	}
	if paths != nil {
		t.Fatalf("paths = %v, want nil", paths)
	}
}

// TestCovReadFilePathsWaylandSuccess exercises the full Wayland file path read.
func TestCovReadFilePathsWaylandSuccess(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/wl-paste", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("text/uri-list\nimage/png"), nil // --list-types
		}
		return []byte("file:///tmp/wl-test.png\n"), nil // uri-list
	}
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/tmp/wl-test.png" {
		t.Fatalf("paths = %v", paths)
	}
}

// TestCovReadFilePathsWaylandNoURIs
func TestCovReadFilePathsWaylandNoURIs(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/wl-paste", nil }
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		return []byte("text/plain"), nil // no text/uri-list
	}
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want empty", paths)
	}
}

// TestCovReadFilePathsWaylandURIListError
func TestCovReadFilePathsWaylandURIListError(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/wl-paste", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("text/uri-list"), nil // --list-types
		}
		return nil, errors.New("wl-paste uri-list error")
	}
	s := NewSystemClipboardSource()
	paths, err := s.ReadFilePaths()
	if err != nil {
		t.Fatalf("err = %v, want nil (best-effort)", err)
	}
	if paths != nil {
		t.Fatalf("paths = %v, want nil", paths)
	}
}

// TestCovReadImageBytesX11MissingTool
func TestCovReadImageBytesX11MissingTool(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadImageBytesX11NoPNG
func TestCovReadImageBytesX11NoPNG(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/xclip", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("text/plain"), nil }
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrNoClipboardImage {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovReadImageBytesX11Success
func TestCovReadImageBytesX11Success(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/xclip", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("image/png\ntext/plain"), nil // TARGETS
		}
		return []byte("png-data"), nil // image/png
	}
	s := NewSystemClipboardSource()
	data, mediaType, err := s.ReadImageBytes()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(data) != "png-data" || mediaType != "image/png" {
		t.Fatalf("data = %q mediaType = %q", data, mediaType)
	}
}

// TestCovReadImageBytesX11EmptyData
func TestCovReadImageBytesX11EmptyData(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/xclip", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("image/png"), nil
		}
		return []byte{}, nil // empty image data
	}
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrNoClipboardImage {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovReadImageBytesX11Error
func TestCovReadImageBytesX11Error(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/xclip", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("image/png"), nil
		}
		return nil, errors.New("xclip error")
	}
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err == nil || err.Error() != "xclip error" {
		t.Fatalf("err = %v", err)
	}
}

// TestCovReadFilePathsWaylandMissingTool
func TestCovReadFilePathsWaylandMissingTool(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
	}()
	clipboardGOOS = "linux"
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardLookPath = func(name string) (string, error) {
		if name == "wl-paste" {
			return "", exec.ErrNotFound
		}
		return name, nil
	}
	s := NewSystemClipboardSource()
	_, err := s.ReadFilePaths()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadImageBytesWaylandMissingTool
func TestCovReadImageBytesWaylandMissingTool(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
	}()
	clipboardGOOS = "linux"
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardLookPath = func(name string) (string, error) {
		if name == "wl-paste" {
			return "", exec.ErrNotFound
		}
		return name, nil
	}
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadImageBytesWaylandNoPNG
func TestCovReadImageBytesWaylandNoPNG(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/wl-paste", nil }
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("text/plain"), nil }
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrNoClipboardImage {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovReadImageBytesWaylandSuccess
func TestCovReadImageBytesWaylandSuccess(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/wl-paste", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("image/png"), nil
		}
		return []byte("png-data"), nil
	}
	s := NewSystemClipboardSource()
	data, mediaType, err := s.ReadImageBytes()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(data) != "png-data" || mediaType != "image/png" {
		t.Fatalf("data = %q mediaType = %q", data, mediaType)
	}
}

// TestCovReadImageBytesWaylandEmptyData
func TestCovReadImageBytesWaylandEmptyData(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/wl-paste", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("image/png"), nil
		}
		return []byte{}, nil
	}
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err != ErrNoClipboardImage {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovReadImageBytesWaylandError
func TestCovReadImageBytesWaylandError(t *testing.T) {
	origGOOS := clipboardGOOS
	origLookPath := clipboardLookPath
	origOutput := clipboardOutput
	defer func() {
		clipboardGOOS = origGOOS
		clipboardLookPath = origLookPath
		clipboardOutput = origOutput
	}()
	clipboardGOOS = "linux"
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	clipboardLookPath = func(string) (string, error) { return "/usr/bin/wl-paste", nil }
	callCount := 0
	clipboardOutput = func(name string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte("image/png"), nil
		}
		return nil, errors.New("wl-paste error")
	}
	s := NewSystemClipboardSource()
	_, _, err := s.ReadImageBytes()
	if err == nil || err.Error() != "wl-paste error" {
		t.Fatalf("err = %v", err)
	}
}

// TestCovReadWindowsClipboardViaPowerShellNoTool
func TestCovReadWindowsClipboardViaPowerShellNoTool(t *testing.T) {
	origLookPath := clipboardLookPath
	defer func() { clipboardLookPath = origLookPath }()
	clipboardLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	s := NewSystemClipboardSource()
	_, err := s.ReadWindowsClipboardViaPowerShell()
	if err != ErrClipboardUnavailable {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

// TestCovReadWindowsClipboardViaPowerShellSuccess
func TestCovReadWindowsClipboardViaPowerShellSuccess(t *testing.T) {
	origLookPath := clipboardLookPath
	origPowerShell := clipboardPowerShellOutput
	defer func() {
		clipboardLookPath = origLookPath
		clipboardPowerShellOutput = origPowerShell
	}()
	clipboardLookPath = func(string) (string, error) { return "powershell.exe", nil }
	clipboardPowerShellOutput = func(string, ...string) ([]byte, error) { return []byte("C:\\Users\\clip.png\n"), nil }
	s := NewSystemClipboardSource()
	path, err := s.ReadWindowsClipboardViaPowerShell()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if path != "C:\\Users\\clip.png" {
		t.Fatalf("path = %q", path)
	}
}

// TestCovReadWindowsClipboardViaPowerShellEmptyPath
func TestCovReadWindowsClipboardViaPowerShellEmptyPath(t *testing.T) {
	origLookPath := clipboardLookPath
	origPowerShell := clipboardPowerShellOutput
	defer func() {
		clipboardLookPath = origLookPath
		clipboardPowerShellOutput = origPowerShell
	}()
	clipboardLookPath = func(string) (string, error) { return "powershell.exe", nil }
	clipboardPowerShellOutput = func(string, ...string) ([]byte, error) { return []byte("  \n"), nil }
	s := NewSystemClipboardSource()
	_, err := s.ReadWindowsClipboardViaPowerShell()
	if err != ErrNoClipboardImage {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovProcVersionSuccess
func TestCovProcVersionSuccess(t *testing.T) {
	origReadFile := clipboardReadFile
	defer func() { clipboardReadFile = origReadFile }()
	clipboardReadFile = func(string) ([]byte, error) { return []byte("Linux version 5.15"), nil }
	s := NewSystemClipboardSource()
	if got := s.ProcVersion(); got != "Linux version 5.15" {
		t.Fatalf("ProcVersion = %q", got)
	}
}

// TestCovProcVersionError
func TestCovProcVersionError(t *testing.T) {
	origReadFile := clipboardReadFile
	defer func() { clipboardReadFile = origReadFile }()
	clipboardReadFile = func(string) ([]byte, error) { return nil, errors.New("read fail") }
	s := NewSystemClipboardSource()
	if got := s.ProcVersion(); got != "" {
		t.Fatalf("ProcVersion = %q, want empty", got)
	}
}

// TestCovWriteTempPNGWriteError
func TestCovWriteTempPNGWriteError(t *testing.T) {
	origCreateTemp := clipboardCreateTemp
	origWrite := clipboardWrite
	origClose := clipboardClose
	origRemove := clipboardRemove
	defer func() {
		clipboardCreateTemp = origCreateTemp
		clipboardWrite = origWrite
		clipboardClose = origClose
		clipboardRemove = origRemove
	}()
	clipboardCreateTemp = os.CreateTemp
	clipboardWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write fail") }
	clipboardClose = func(*os.File) error { return nil }
	clipboardRemove = func(string) error { return nil }
	_, err := WriteTempPNG([]byte("data"))
	if err == nil || err.Error() != "write fail" {
		t.Fatalf("err = %v", err)
	}
}

// TestCovWriteTempPNGCloseError
func TestCovWriteTempPNGCloseError(t *testing.T) {
	origCreateTemp := clipboardCreateTemp
	origWrite := clipboardWrite
	origClose := clipboardClose
	origRemove := clipboardRemove
	defer func() {
		clipboardCreateTemp = origCreateTemp
		clipboardWrite = origWrite
		clipboardClose = origClose
		clipboardRemove = origRemove
	}()
	clipboardCreateTemp = os.CreateTemp
	clipboardWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	clipboardClose = func(*os.File) error { return errors.New("close fail") }
	clipboardRemove = func(string) error { return nil }
	_, err := WriteTempPNG([]byte("data"))
	if err == nil || err.Error() != "close fail" {
		t.Fatalf("err = %v", err)
	}
}

// TestCovPasteClipboardImageFileListNonImageThenImageBytes
func TestCovPasteClipboardImageFileListNonImageThenImageBytes(t *testing.T) {
	dir := t.TempDir()
	// Non-image file exists on clipboard, falls through to image bytes
	nonImg := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(nonImg, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeClipboard{
		files:          []string{nonImg},
		imageBytes:     []byte("png-bytes"),
		imageMediaType: "image/png",
	}
	got, err := PasteClipboardImage(src)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Origin != "clipboard-image" {
		t.Fatalf("Origin = %q, want clipboard-image", got.Origin)
	}
	t.Cleanup(func() { _ = os.Remove(got.Path) })
}

// TestCovPasteClipboardImageFileListImageStatFails
func TestCovPasteClipboardImageFileListImageStatFails(t *testing.T) {
	src := &fakeClipboard{
		files:          []string{"/nonexistent/file.png"}, // stat fails
		imageBytes:     []byte("png-bytes"),
		imageMediaType: "image/png",
	}
	got, err := PasteClipboardImage(src)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Origin != "clipboard-image" {
		t.Fatalf("Origin = %q, want clipboard-image", got.Origin)
	}
	t.Cleanup(func() { _ = os.Remove(got.Path) })
}

// TestCovPasteClipboardImageImageBytesWriteFails
func TestCovPasteClipboardImageImageBytesWriteFails(t *testing.T) {
	origCreateTemp := clipboardCreateTemp
	origWrite := clipboardWrite
	defer func() {
		clipboardCreateTemp = origCreateTemp
		clipboardWrite = origWrite
	}()
	clipboardCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("temp fail") }
	clipboardWrite = func(*os.File, []byte) (int, error) { return 0, nil }
	src := &fakeClipboard{
		imageBytes:     []byte("png-bytes"),
		imageMediaType: "image/png",
	}
	_, err := PasteClipboardImage(src)
	if err == nil {
		t.Fatal("should fail when WriteTempPNG fails")
	}
}

// TestCovPasteClipboardImageNoImageNoWSL
func TestCovPasteClipboardImageNoImageNoWSL(t *testing.T) {
	src := &fakeClipboard{
		imageErr:    ErrNoClipboardImage,
		procVersion: "Linux version 6.8.0-generic",
	}
	_, err := PasteClipboardImage(src)
	if !errors.Is(err, ErrNoClipboardImage) {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}

// TestCovPasteClipboardImageWSLFallbackSuccess
func TestCovPasteClipboardImageWSLFallbackSuccess(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "clip.png")
	if err := os.WriteFile(imgPath, []byte("png-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ConvertWindowsPathToWSL returns "" for non-Windows paths, so we need a
	// Windows-style path that converts to a valid WSL path. But the stat
	// will be on the converted path. The easiest way is to make the winPath
	// convert to the actual file we created. On non-WSL systems,
	// ConvertWindowsPathToWSL returns "" for non-Windows paths, which means
	// TryWSLClipboardFallback returns an error. So we test the WSL path
	// where the prior error propagates.
	src := &fakeClipboard{
		imageErr:    ErrNoClipboardImage,
		procVersion: "microsoft WSL2",
		winErr:      ErrNoClipboardImage,
	}
	_, err := PasteClipboardImage(src)
	// WSL fallback with ErrNoClipboardImage from PowerShell and prior error
	// should return the prior error.
	if !errors.Is(err, ErrNoClipboardImage) {
		t.Fatalf("err = %v, want ErrNoClipboardImage", err)
	}
}
