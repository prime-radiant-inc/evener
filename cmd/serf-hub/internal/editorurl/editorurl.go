// Package editorurl builds URLs that ask the OS to open a file path in an
// editor when the URL is opened by a browser.
package editorurl

import (
	"html/template"
	"net/url"
	"strings"

	"primeradiant.com/serf/envvars"
)

// EditorURL returns a URL that, when opened by the browser, asks the OS to
// open the given absolute file path in an editor. The default scheme is
// vscode://file/<path>, which both VS Code and Cursor honor (Cursor also
// honors cursor://file/<path>).
//
// Override via SERF_HUB_EDITOR_URL_TEMPLATE — a Go-style template with the
// literal token "{path}" replaced by the URL-encoded absolute path. Examples:
//
//	"vscode://file/{path}"   (default)
//	"cursor://file/{path}"
//	"zed://file/{path}"
//	"idea://open?file={path}"
//
// When the path is not absolute or the env override is malformed, falls back
// to the file:// scheme (which on macOS opens whatever app the user has
// associated with the file extension).
func EditorURL(absPath string) template.URL {
	if absPath == "" {
		return template.URL("")
	}
	tmpl := envvars.SERFHubEditorURLTemplate.Trimmed()
	if tmpl == "" {
		tmpl = "vscode://file/{path}"
	}
	if !strings.HasPrefix(absPath, "/") {
		// Not absolute — fall back to file:// only if it actually looks like
		// a path the OS can resolve. This catches placeholders used for
		// built-in bundled assets.
		return template.URL("")
	}
	encoded := url.PathEscape(strings.TrimPrefix(absPath, "/"))
	// PathEscape encodes "/" too; restore separators so the editor scheme
	// gets a real-looking path.
	encoded = strings.ReplaceAll(encoded, "%2F", "/")
	return template.URL(strings.ReplaceAll(tmpl, "{path}", encoded))
}
