// Production wiring for the [ClipboardSource] interface defined in
// clipboard_paste.go. Each method shells out to the host's native
// clipboard tool — xclip / wl-paste / osascript / PowerShell — so the
// TUI stays free of CGo dependencies on Linux. Platform selection is
// driven by GOOS plus environment variables (WAYLAND_DISPLAY, DISPLAY)
// so a single binary handles Wayland, X11, macOS, and WSL.
//
// All methods are best-effort: if the relevant tool is missing or
// returns no data, they return [ErrNoClipboardImage] or
// [ErrClipboardUnavailable] so [PasteClipboardImage] can attempt the
// next branch (file list → image bytes → WSL fallback).

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// SystemClipboardSource is the production implementation of
// [ClipboardSource]. The zero value is usable; the wrapper exposes
// nothing but the interface methods.
type SystemClipboardSource struct{}

// NewSystemClipboardSource constructs the production clipboard source.
// Returning a constructor (rather than a typed nil) lets future
// versions cache os.Executable() lookups or inject overrides without
// breaking callers.
func NewSystemClipboardSource() *SystemClipboardSource {
	return &SystemClipboardSource{}
}

// ReadFilePaths attempts to enumerate file paths on the clipboard
// (Finder/Explorer "Copy" of a file). It returns an empty slice when
// no file list is present.
func (s *SystemClipboardSource) ReadFilePaths() ([]string, error) {
	switch runtime.GOOS {
	case "darwin":
		return readFilePathsMacOS()
	case "linux":
		if isWaylandSession() {
			return readFilePathsWayland()
		}
		return readFilePathsX11()
	default:
		return nil, ErrClipboardUnavailable
	}
}

// ReadImageBytes reads raw image bytes from the clipboard (the
// screenshot / web-image case). Returns [ErrNoClipboardImage] when no
// image is present.
func (s *SystemClipboardSource) ReadImageBytes() ([]byte, string, error) {
	switch runtime.GOOS {
	case "darwin":
		return readImageBytesMacOS()
	case "linux":
		if isWaylandSession() {
			return readImageBytesWayland()
		}
		return readImageBytesX11()
	default:
		return nil, "", ErrClipboardUnavailable
	}
}

// ReadWindowsClipboardViaPowerShell invokes PowerShell on the Windows
// side of a WSL mount to save the clipboard image to a temp PNG and
// returns the printed Windows-style path.
func (s *SystemClipboardSource) ReadWindowsClipboardViaPowerShell() (string, error) {
	const script = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$img = [System.Windows.Forms.Clipboard]::GetImage()
if ($img -ne $null) {
  $p = [System.IO.Path]::GetTempFileName()
  $p = [System.IO.Path]::ChangeExtension($p, 'png')
  $img.Save($p, [System.Drawing.Imaging.ImageFormat]::Png)
  Write-Output $p
} else { exit 1 }`

	for _, name := range []string{"powershell.exe", "pwsh.exe", "pwsh", "powershell"} {
		if _, err := exec.LookPath(name); err != nil {
			continue
		}
		cmd := exec.Command(name, "-NoProfile", "-NonInteractive", "-Command", script)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			// Exit code 1 means "no image"; anything else is a real failure.
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return "", ErrNoClipboardImage
			}
			continue
		}
		path := strings.TrimSpace(out.String())
		if path == "" {
			return "", ErrNoClipboardImage
		}
		return path, nil
	}
	return "", ErrClipboardUnavailable
}

// ProcVersion returns /proc/version so [isProbablyWSLFromSource] can
// detect WSL kernels without re-reading the file on every paste.
func (s *SystemClipboardSource) ProcVersion() string {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return ""
	}
	return string(data)
}

// isWaylandSession reports whether the user appears to be running a
// Wayland compositor. WAYLAND_DISPLAY is set for native Wayland; we
// fall back to X11 otherwise.
func isWaylandSession() bool {
	return strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
}

// readFilePathsMacOS asks osascript for any file references on the
// clipboard. The "as «class furl»" coercion returns POSIX paths.
func readFilePathsMacOS() ([]string, error) {
	if _, err := exec.LookPath("osascript"); err != nil {
		return nil, ErrClipboardUnavailable
	}
	const script = `try
  set theFiles to the clipboard as «class furl»
  return POSIX path of theFiles
end try`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return nil, nil
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, nil
	}
	return []string{line}, nil
}

// readImageBytesMacOS pipes the system clipboard through osascript to
// extract PNG bytes for any image content. Returns ErrNoClipboardImage
// when the clipboard does not hold an image.
func readImageBytesMacOS() ([]byte, string, error) {
	if _, err := exec.LookPath("osascript"); err != nil {
		return nil, "", ErrClipboardUnavailable
	}
	tmp, err := os.CreateTemp("", "serf-clip-mac-*.png")
	if err != nil {
		return nil, "", fmt.Errorf("temp file: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	script := fmt.Sprintf(`try
  set png to (the clipboard as «class PNGf»)
  set f to open for access POSIX file %q with write permission
  set eof of f to 0
  write png to f
  close access f
  return "ok"
on error
  return "noimage"
end try`, tmp.Name())
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return nil, "", ErrNoClipboardImage
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", ErrNoClipboardImage
	}
	return data, "image/png", nil
}

// readFilePathsX11 inspects the X11 clipboard for text/uri-list-style
// content. xclip exposes available targets via `-t TARGETS -o`; if
// "text/uri-list" appears, we request it.
func readFilePathsX11() ([]string, error) {
	if _, err := exec.LookPath("xclip"); err != nil {
		return nil, ErrClipboardUnavailable
	}
	targets, _ := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if !bytes.Contains(targets, []byte("text/uri-list")) {
		return nil, nil
	}
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "text/uri-list", "-o").Output()
	if err != nil {
		return nil, nil
	}
	return parseURIList(string(out)), nil
}

// readImageBytesX11 asks xclip for image/png bytes. If image/png is not
// advertised in the TARGETS list we skip the call entirely so xclip
// doesn't hang waiting for selection-conversion errors.
func readImageBytesX11() ([]byte, string, error) {
	if _, err := exec.LookPath("xclip"); err != nil {
		return nil, "", ErrClipboardUnavailable
	}
	targets, _ := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if !bytes.Contains(targets, []byte("image/png")) {
		return nil, "", ErrNoClipboardImage
	}
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
	if err != nil {
		return nil, "", err
	}
	if len(out) == 0 {
		return nil, "", ErrNoClipboardImage
	}
	return out, "image/png", nil
}

// readFilePathsWayland asks wl-paste for any file URI list on the
// clipboard. `--list-types` enumerates MIME types; we only call the
// expensive `wl-paste --type ...` once we know the type is present.
func readFilePathsWayland() ([]string, error) {
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return nil, ErrClipboardUnavailable
	}
	types, _ := exec.Command("wl-paste", "--list-types").Output()
	if !bytes.Contains(types, []byte("text/uri-list")) {
		return nil, nil
	}
	out, err := exec.Command("wl-paste", "--no-newline", "--type", "text/uri-list").Output()
	if err != nil {
		return nil, nil
	}
	return parseURIList(string(out)), nil
}

// readImageBytesWayland asks wl-paste for image/png bytes.
func readImageBytesWayland() ([]byte, string, error) {
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return nil, "", ErrClipboardUnavailable
	}
	types, _ := exec.Command("wl-paste", "--list-types").Output()
	if !bytes.Contains(types, []byte("image/png")) {
		return nil, "", ErrNoClipboardImage
	}
	out, err := exec.Command("wl-paste", "--no-newline", "--type", "image/png").Output()
	if err != nil {
		return nil, "", err
	}
	if len(out) == 0 {
		return nil, "", ErrNoClipboardImage
	}
	return out, "image/png", nil
}

// parseURIList splits a text/uri-list payload into local filesystem
// paths. Lines that start with "#" are comments per RFC 2483; lines
// without a `file://` prefix are skipped.
func parseURIList(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "file://") {
			continue
		}
		out = append(out, fileURIToPath(line))
	}
	return out
}

// fileURIToPath strips the file:// prefix, returning the local path
// portion. Hostnames in the URI (e.g. file://localhost/path) are
// tolerated by skipping any leading segment up to the next '/'.
func fileURIToPath(uri string) string {
	const prefix = "file://"
	if !strings.HasPrefix(uri, prefix) {
		return uri
	}
	rest := uri[len(prefix):]
	if rest == "" {
		return ""
	}
	if rest[0] != '/' {
		// file://host/path → drop host segment.
		idx := strings.IndexByte(rest, '/')
		if idx < 0 {
			return ""
		}
		rest = rest[idx:]
	}
	return rest
}

// Compile-time assertion that the production source satisfies the
// interface. Provides a single source-of-truth diagnostic if the
// interface drifts.
var _ ClipboardSource = (*SystemClipboardSource)(nil)
var _ = errors.New
