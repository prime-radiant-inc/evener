// Clipboard paste primitives for the serf TUI composer.
//
// This file provides pure-function primitives plus an injectable
// [ClipboardSource] interface so the system clipboard can be stubbed in
// tests. The composer wires the primitives up in a separate kata; nothing
// in this file mutates TUI state directly.
//
// Cross-platform clipboard image access is intentionally implemented by
// shelling out to the platform's native clipboard tool (xclip / wl-paste /
// pbpaste+osascript / PowerShell). This trades a small amount of latency
// per paste for keeping serf free of CGo dependencies — every clipboard
// library that reads image data on Linux drags in libx11-dev or
// libwayland-client, which would block cross-compilation. The shell-out
// approach also makes the WSL fallback fit naturally: WSL is just one more
// "platform" whose clipboard tool happens to be PowerShell on the Windows
// side of the mount.
package clipboard

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// imageExtensions is the set of file extensions recognised as images by
// the composer. Membership controls both [IsImageFile] and the
// pasted-path attachment heuristic.
var imageExtensions = map[string]struct{}{
	".png":  {},
	".jpg":  {},
	".jpeg": {},
	".gif":  {},
	".webp": {},
}

// IsImageFile reports whether path has an extension recognised as an image.
// The check is case-insensitive and looks only at the extension; it does
// not stat the file.
func IsImageFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	_, ok := imageExtensions[ext]
	return ok
}

// PastedImage describes a successfully captured clipboard image. The
// caller owns the temp file at Path and is responsible for cleanup once
// the bytes have been shipped to the agent.
type PastedImage struct {
	Path      string // local filesystem path (usually under os.TempDir())
	MediaType string // canonical MIME, e.g. "image/png"
	Width     int    // pixel width if known, 0 otherwise
	Height    int    // pixel height if known, 0 otherwise
	Size      int    // byte length of the file at Path
	Origin    string // "clipboard-file" | "clipboard-image" | "wsl"
	// MarkerN is the number embedded in the "[image N]" literal inserted
	// at the composer cursor when this attachment was added. Assigned at
	// attach time and never reused; 0 means unassigned (e.g. a legacy or
	// directly-constructed attachment that did not flow through
	// addPendingAttachment).
	MarkerN int
}

// ClipboardSource abstracts the three clipboard reads the paste flow
// performs. Production code wires this to the real OS clipboard tools;
// tests inject fakes to drive each branch.
type ClipboardSource interface {
	// ReadFilePaths returns the local paths of files copied to the
	// clipboard (Finder/Explorer "Copy" of a file). Returns an empty slice
	// when the clipboard does not hold a file list.
	ReadFilePaths() ([]string, error)
	// ReadImageBytes returns raw image bytes when the clipboard holds an
	// image (e.g. a screenshot). Returns ErrNoClipboardImage when no
	// image is present.
	ReadImageBytes() ([]byte, string, error)
	// ReadWindowsClipboardViaPowerShell saves the Windows-side clipboard
	// image to a temp PNG and returns the Windows-style path. Only
	// meaningful under WSL. Returns ErrNoClipboardImage when no image is
	// available.
	ReadWindowsClipboardViaPowerShell() (string, error)
	// ProcVersion returns the contents of /proc/version (or equivalent)
	// so [isProbablyWSLFromSource] can be exercised without touching the real fs.
	ProcVersion() string
}

// ErrNoClipboardImage signals that the clipboard exists but does not
// currently hold image data.
var ErrNoClipboardImage = errors.New("no image on clipboard")

// ErrClipboardUnavailable signals that the OS clipboard tool is missing
// or failed to execute. Triggers the WSL fallback on Linux.
var ErrClipboardUnavailable = errors.New("clipboard unavailable")

// PasteClipboardImage walks the standard three-step paste pipeline:
//  1. Prefer a file-list copy (no re-encode needed).
//  2. Fall back to raw image bytes.
//  3. On Linux/WSL, fall back to PowerShell reading the Windows clipboard.
//
// The first successful step wins. The returned PastedImage owns a file on
// disk that the caller must eventually clean up.
func PasteClipboardImage(src ClipboardSource) (*PastedImage, error) {
	if src == nil {
		return nil, errors.New("clipboard source is nil")
	}

	// Step 1: file list (Finder/Explorer copied a file).
	if paths, err := src.ReadFilePaths(); err == nil {
		for _, p := range paths {
			if !IsImageFile(p) {
				continue
			}
			info, err := os.Stat(p)
			if err != nil {
				continue
			}
			return &PastedImage{
				Path:      p,
				MediaType: MediaTypeForPath(p),
				Size:      int(info.Size()),
				Origin:    "clipboard-file",
			}, nil
		}
	}

	// Step 2: raw image bytes.
	data, mediaType, err := src.ReadImageBytes()
	if err == nil && len(data) > 0 {
		path, werr := WriteTempPNG(data)
		if werr != nil {
			return nil, fmt.Errorf("write temp png: %w", werr)
		}
		if mediaType == "" {
			mediaType = "image/png"
		}
		return &PastedImage{
			Path:      path,
			MediaType: mediaType,
			Size:      len(data),
			Origin:    "clipboard-image",
		}, nil
	}

	// Step 3: WSL fallback.
	if IsProbablyWSLFromSource(src) {
		path, werr := TryWSLClipboardFallback(src, err)
		if werr == nil {
			info, statErr := os.Stat(path)
			if statErr != nil {
				return nil, fmt.Errorf("wsl clipboard image stat %q: %w", path, statErr)
			}
			return &PastedImage{
				Path:      path,
				MediaType: "image/png",
				Size:      int(info.Size()),
				Origin:    "wsl",
			}, nil
		}
	}

	if err == nil {
		err = ErrNoClipboardImage
	}
	return nil, err
}

// TryWSLClipboardFallback invokes the Windows-side clipboard via the
// configured ClipboardSource, then converts the returned Windows path to
// its WSL mount equivalent. The error argument propagates the original
// failure so callers can decide whether the fallback is worth attempting.
func TryWSLClipboardFallback(src ClipboardSource, prior error) (string, error) {
	if src == nil {
		return "", errors.New("clipboard source is nil")
	}
	winPath, err := src.ReadWindowsClipboardViaPowerShell()
	if err != nil {
		if prior != nil {
			return "", prior
		}
		return "", err
	}
	winPath = strings.TrimSpace(winPath)
	if winPath == "" {
		if prior != nil {
			return "", prior
		}
		return "", ErrNoClipboardImage
	}
	wslPath := ConvertWindowsPathToWSL(winPath)
	if wslPath == "" {
		if prior != nil {
			return "", prior
		}
		return "", fmt.Errorf("could not convert windows path %q to WSL mount", winPath)
	}
	return wslPath, nil
}

// IsProbablyWSL inspects the contents of /proc/version-style text and
// reports whether they look like a Microsoft/WSL kernel. The path is
// taken as an arg so the heuristic can be exercised without touching the
// real filesystem.
func IsProbablyWSL(versionPath string) bool {
	data, err := os.ReadFile(versionPath)
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// IsProbablyWSLFromSource is the in-memory variant: it inspects whatever
// the source already cached from /proc/version. Used by
// [PasteClipboardImage] so a single ClipboardSource fake fully drives the
// WSL branch in tests.
func IsProbablyWSLFromSource(src ClipboardSource) bool {
	if src == nil {
		return false
	}
	lower := strings.ToLower(src.ProcVersion())
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// NormalizePastedPath recognises the four canonical ways a path arrives
// over a paste:
//   - file:// URLs (RFC 8089)
//   - double- or single-quoted paths
//   - bare Windows / UNC paths
//   - WSL-style /mnt/... paths
//
// Returns "" when the text does not look like a single path. The function
// is intentionally pure: it does not stat or read the path.
func NormalizePastedPath(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}

	// Strip a single surrounding pair of double or single quotes.
	quoted := false
	if len(t) >= 2 {
		if (t[0] == '"' && t[len(t)-1] == '"') || (t[0] == '\'' && t[len(t)-1] == '\'') {
			// Trim boundary whitespace inside the quotes too: a quoted path may keep
			// interior spaces ("/My Pictures/a.png"), but a leading/trailing control
			// char (e.g. '/\f') is stripped by TrimSpace on a second normalization
			// pass, so returning it verbatim would break idempotence.
			t = strings.TrimSpace(t[1 : len(t)-1])
			quoted = true
		}
	}

	// file:// URL.
	if strings.HasPrefix(strings.ToLower(t), "file://") {
		if u, err := url.Parse(t); err == nil && u.Scheme == "file" {
			// A percent-encoded control/whitespace char (e.g. file:///%0A, %0B)
			// decodes to a path with a boundary newline/vtab; returning "/\n" then
			// re-normalizes to "/" (via TrimSpace) on a second pass, breaking
			// idempotence, and an embedded CR/LF is rejected by the whitespace guard
			// below. Return the decoded path only when it is trim-stable and free of
			// CR/LF; a legitimate space-bearing path (file:///a%20b) still passes.
			if p := u.Path; p != "" && p == strings.TrimSpace(p) && !strings.ContainsAny(p, "\r\n") {
				return p
			}
		}
	}

	// Windows drive-letter or UNC. Pass through as-is; the WSL composer
	// can decide whether to convert via ConvertWindowsPathToWSL.
	if IsWindowsPath(t) {
		return t
	}

	if strings.ContainsAny(t, "\r\n") {
		return ""
	}
	if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~") || strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") {
		// Unquoted whitespace almost certainly means we received literal
		// text rather than a single path token. Quoted POSIX paths can
		// legitimately contain spaces, e.g. "/home/me/My Pictures/a.png".
		if !quoted && strings.ContainsAny(t, " \t") {
			return ""
		}
		return t
	}

	return ""
}

// IsWindowsPath returns true when the input starts with a drive letter
// followed by ":\\" or ":/" (e.g. C:\foo) or is a UNC share (\\server\share).
func IsWindowsPath(s string) bool {
	if strings.HasPrefix(s, `\\`) {
		return true
	}
	if len(s) < 3 {
		return false
	}
	if !isASCIIAlpha(s[0]) {
		return false
	}
	if s[1] != ':' {
		return false
	}
	if s[2] != '\\' && s[2] != '/' {
		return false
	}
	return true
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ConvertWindowsPathToWSL maps "C:\\Users\\Alice\\foo.png" to
// "/mnt/c/Users/Alice/foo.png". Returns "" if the input is not a
// drive-letter path. UNC paths (\\server\share) are not supported.
func ConvertWindowsPathToWSL(input string) string {
	if strings.HasPrefix(input, `\\`) {
		return ""
	}
	if len(input) < 3 {
		return ""
	}
	if !isASCIIAlpha(input[0]) || input[1] != ':' {
		return ""
	}
	drive := strings.ToLower(string(input[0]))
	rest := input[2:]
	rest = strings.TrimLeft(rest, `\/`)
	parts := strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' })
	// Filter empties for safety.
	out := "/mnt/" + drive
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = out + "/" + p
	}
	return out
}

// MediaTypeForPath returns the canonical MIME type for an image file at
// the given path, defaulting to image/png when the extension is unknown.
func MediaTypeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// WriteTempPNG persists the given bytes under os.TempDir() with the
// "serf-clipboard-" prefix and ".png" suffix, returning the absolute
// path. The caller owns cleanup.
func WriteTempPNG(data []byte) (string, error) {
	f, err := os.CreateTemp("", "serf-clipboard-*.png")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// FileURIToPath strips the file:// prefix, returning the local path
// portion. Hostnames in the URI (e.g. file://localhost/path) are
// tolerated by skipping any leading segment up to the next '/'.
func FileURIToPath(uri string) string {
	const prefix = "file://"
	if !strings.HasPrefix(uri, prefix) {
		return uri
	}
	rest := uri[len(prefix):]
	if rest == "" {
		return ""
	}
	if rest[0] != '/' {
		// Drop the leading host segment from a "file://host/path" form.
		idx := strings.IndexByte(rest, '/')
		if idx < 0 {
			return ""
		}
		rest = rest[idx:]
	}
	if path, err := url.PathUnescape(rest); err == nil {
		return path
	}
	return rest
}
