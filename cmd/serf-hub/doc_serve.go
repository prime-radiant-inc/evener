package main

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
)

var docStat = os.Stat
var docOpen = os.Open

// docFileMaxBytes caps how much of a file we read into a document pane. A pane
// is a quick read-only reference, not a pager; large files are truncated with
// a notice rather than streamed in full.
const docFileMaxBytes = 512 * 1024

// handleDocFile serves a LOCAL session file's literal bytes for the React
// doc-viewer pane, which renders the content itself. The route has a single
// mode, ?format=raw; a request that omits format or sends any other value is a
// client error (400 with a hint naming the parameter).
//
// The guard chain below (session/path presence, cwd containment) runs before
// the format check, so a raw and a non-raw request reject the same out-of-cwd
// or unknown-session input identically — only a fully valid request reaches the
// format gate, where a raw request is served (writeDocFileRaw) and anything
// else is refused.
//
// Security: the only file paths we serve are ones that resolve to a location
// inside the session's cwd. We clean the request path, reject any residual
// traversal, and confirm the symlink-resolved absolute path is contained by
// the symlink-resolved cwd. Anything that escapes the cwd is refused.
func (s *WebServer) handleDocFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	session := canonicalRouteID(r.URL.Query().Get("session"))
	rel := r.URL.Query().Get("path")
	if session == "" || rel == "" {
		http.NotFound(w, r)
		return
	}

	cwd, ok := s.localSessionCWD(session)
	if !ok {
		http.NotFound(w, r)
		return
	}

	abs, err := fspaths.ResolveInRoot(cwd, rel)
	if err != nil {
		// A path that escapes the cwd, or that doesn't resolve, is refused.
		// 403 for an escape attempt; 404 for a missing file.
		if errors.Is(err, fspaths.ErrPathEscapesRoot) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
		return
	}

	data, err := readDocFile(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if r.URL.Query().Get("format") != "raw" {
		http.Error(w, "format=raw required", http.StatusBadRequest)
		return
	}
	writeDocFileRaw(w, data, docRawTotalSize(abs, len(data)))
}

// handleDocImage serves a validated image file inside a LOCAL session's working
// directory. It mirrors /doc/file's containment boundary, but only streams v1
// supported image media types for inline output-image previews.
func (s *WebServer) handleDocImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	session := canonicalRouteID(r.URL.Query().Get("session"))
	rel := r.URL.Query().Get("path")
	if session == "" || rel == "" {
		http.NotFound(w, r)
		return
	}

	cwd, ok := s.localSessionCWD(session)
	if !ok {
		http.NotFound(w, r)
		return
	}

	abs, err := fspaths.ResolveInRoot(cwd, rel)
	if err != nil {
		if errors.Is(err, fspaths.ErrPathEscapesRoot) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
		return
	}

	data, _, ok := readOutputImageFile(abs)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mediaType, ok := supportedOutputImageMedia(data, filepath.Base(abs))
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("ETag", `"`+outputImageSHA(data)+`"`)
	_, _ = w.Write(data)
}

// localSessionCWD resolves a local session's working directory from the past
// index or live roster. Non-local (remote/codex) refs are out of scope and
// return false.
func (s *WebServer) localSessionCWD(session string) (string, bool) {
	if !isLocalRouteID(session) {
		return "", false
	}
	if s.cfg.Past != nil {
		pe, ok := s.cfg.Past.Find(session)
		if ok {
			cwd := strings.TrimSpace(pe.Meta.EnvInfo.WorkingDir)
			if cwd != "" {
				return cwd, true
			}
		}
	}
	if s.cfg.Roster != nil {
		live, ok := s.cfg.Roster.Find(session)
		if ok {
			cwd := strings.TrimSpace(live.WorkingDir)
			if cwd != "" {
				return cwd, true
			}
		}
	}
	return "", false
}

// readDocFile reads up to docFileMaxBytes from a regular file. Directories and
// other non-regular files are refused.
func readDocFile(abs string) ([]byte, error) {
	info, err := docStat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := docOpen(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
	buf := make([]byte, docFileMaxBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// looksBinaryBytes reports whether a byte slice looks like binary content. A
// NUL byte in the head is the standard heuristic; we only sniff the first few
// KiB.
func looksBinaryBytes(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// writeDocFileRaw serves a document pane's literal file bytes for the native
// React doc-viewer pane (?format=raw), which renders the content itself
// instead of consuming the server-rendered HTML page. Content-Type reflects
// the same binary/text classification the HTML variant already computes via
// looksBinaryBytes, rather than sniffing the bytes: sniffing (e.g.
// http.DetectContentType) would classify an HTML-like text file as
// text/html, and a browser that ever loads this URL directly (not just via
// fetch) would then execute it same-origin. text/plain and
// application/octet-stream are both honest about the content and never
// browser-executable.
//
// data is capped at docFileMaxBytes; totalSize is the file's true byte size.
// When the file is larger than the cap the body is only its head, so an
// explicit X-Doc-Truncated / X-Doc-Total-Size pair lets the pane render an
// exact notice instead of inferring truncation from the body length (which is
// ambiguous at exactly the cap). A file of exactly the cap size is complete,
// hence not truncated.
func writeDocFileRaw(w http.ResponseWriter, data []byte, totalSize int64) {
	if looksBinaryBytes(data) {
		w.Header().Set("Content-Type", "application/octet-stream")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	if totalSize > docFileMaxBytes {
		w.Header().Set("X-Doc-Truncated", "true")
		w.Header().Set("X-Doc-Total-Size", strconv.FormatInt(totalSize, 10))
	}
	_, _ = w.Write(data)
}

// docRawTotalSize returns the file's true byte size for the raw pane's
// truncation signal. It re-stats the file rather than threading a size out of
// readDocFile, which the HTML variant shares and this raw-only change must not
// disturb. On a stat error — unlikely, the file was readable a moment ago — it
// falls back to the bytes actually read, which reads as "not truncated": an
// honest degrade to the earlier no-signal behavior.
func docRawTotalSize(abs string, read int) int64 {
	if info, err := docStat(abs); err == nil {
		return info.Size()
	}
	return int64(read)
}
