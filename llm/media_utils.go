package llm

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/gif"
	"image/png"

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

// ImageBlockMediaType returns the canonical media type an Anthropic or OpenAI
// image content block accepts on the wire for v, or "" when v is not one of
// them. v may be an image.Decode format name ("png") or a media type
// ("image/png"); case and surrounding whitespace are ignored. The two builders
// use it to decide which bytes they may carry verbatim and which they must
// re-encode.
func ImageBlockMediaType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "png", "image/png":
		return "image/png"
	case "jpeg", "image/jpeg":
		return "image/jpeg"
	case "gif", "image/gif":
		return "image/gif"
	case "webp", "image/webp":
		return "image/webp"
	default:
		return ""
	}
}

// NormalizeImageBlock returns image bytes and a media type an Anthropic or
// OpenAI image content block accepts on the wire: image/png, image/jpeg,
// image/gif, or image/webp. Bytes already in one of those formats are returned
// unchanged with their canonical media type; any other decodable raster
// (image/bmp, image/tiff) is re-encoded as PNG. Bytes that are not a decodable
// raster return an error.
//
// Both builders enforce this set because the providers do: an image block
// carrying image/bmp or image/tiff is rejected and fails the whole turn
// (#4464). Converting rather than refusing keeps a BMP or TIFF readable on
// every route, matching the Responses builder's existing contract.
func NormalizeImageBlock(data []byte) ([]byte, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if mt := ImageBlockMediaType(format); mt != "" {
		return data, mt, nil
	}
	var normalized bytes.Buffer
	if err := png.Encode(&normalized, img); err != nil {
		return nil, "", fmt.Errorf("re-encode %s image as PNG: %w", format, err)
	}
	return normalized.Bytes(), "image/png", nil
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

// DocumentFileData returns the payload a document part rides the wire as:
// the bytes as a data URI — a blank media type defaults to application/pdf,
// as every document-capable builder does — or the source URL when no bytes
// are set. Bytes win when both are set, matching the image helper and every
// document-capable builder, so a caller that supplies both never silently
// switches sources. A document with neither payload returns "".
func DocumentFileData(doc *DocumentData) string {
	if doc == nil {
		return ""
	}
	if len(doc.Data) > 0 {
		mimeType := strings.TrimSpace(doc.MediaType)
		if mimeType == "" {
			mimeType = "application/pdf"
		}
		return DataURI(mimeType, doc.Data)
	}
	return doc.URL
}
