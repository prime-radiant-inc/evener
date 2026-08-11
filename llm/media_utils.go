package llm

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/gif"

	// Registered for image.Decode side effects so raster validation accepts JPEG.
	_ "image/jpeg"
	"mime"
	"os"
	"path/filepath"
	"strings"

	// Registered for image.Decode side effects so raster validation accepts
	// these common non-standard-library formats.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// IsLocalPath reports whether s, after trimming surrounding whitespace, looks
// like a local filesystem path: absolute ("/"), relative ("./" or "../"), or
// home-relative ("~/").
func IsLocalPath(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~"+string(os.PathSeparator))
}

// ExpandTilde replaces a leading "~/" in path with the current user's home
// directory. The path is trimmed of surrounding whitespace first. If path does
// not begin with "~/", or the home directory cannot be determined, path is
// returned unchanged.
func ExpandTilde(path string) string {
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"+string(os.PathSeparator)))
}

// InferMimeTypeFromPath returns the MIME type inferred from path's file
// extension, with any charset parameter stripped. It returns an empty string if
// path has no extension or no MIME type is known for it.
func InferMimeTypeFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return ""
	}
	mt := mime.TypeByExtension(ext)
	// Drop charset if present (e.g. text/plain; charset=utf-8).
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	return strings.TrimSpace(mt)
}

// IsImageFile returns true if the file path has a raster image extension.
func IsImageFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// RasterMediaType fully decodes supported raster bytes and returns the media
// type established by those bytes. Header-only or otherwise malformed images
// are rejected before they can enter model history.
func RasterMediaType(data []byte) (string, error) {
	_, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	if format == "gif" {
		if _, err := gif.DecodeAll(bytes.NewReader(data)); err != nil {
			return "", err
		}
	}
	switch format {
	case "png", "gif", "webp", "bmp", "tiff":
		return "image/" + format, nil
	case "jpeg":
		return "image/jpeg", nil
	default:
		return "", fmt.Errorf("unsupported raster format %q", format)
	}
}

// DataURI encodes data as a base64 data URI with the given MIME type. If
// mimeType is empty after trimming whitespace, "application/octet-stream" is
// used.
func DataURI(mimeType string, data []byte) string {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))
}
