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

// handleDocFile serves a read-only document pane for a file inside a LOCAL
// session's working directory. It is a standalone document route reached by
// direct navigation and by the SPA's fetch, so it cannot gate on a special
// request header. Markdown renders via marked; other text renders escaped in
// <pre>; binary gets a notice.
//
// ?format=raw switches the response from the server-rendered HTML page to
// the file's literal bytes (writeDocFileRaw), for the native React doc-viewer
// pane. It reuses every guard below unchanged — only the final response
// differs — so the two variants reject a given session/path identically. The
// default (no format param) behavior is byte-for-byte unchanged.
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

	if r.URL.Query().Get("format") == "raw" {
		writeDocFileRaw(w, data)
		return
	}

	name := filepath.Base(abs)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if looksBinaryBytes(data) {
		writeDocPage(w, name, `<div class="doc-binary">binary file — `+htmlEscape(name)+` ('`+formatDocBytes(len(data))+`') not shown</div>`)
		return
	}
	text := string(data)
	if strings.EqualFold(filepath.Ext(name), ".md") || strings.EqualFold(filepath.Ext(name), ".markdown") {
		writeDocMarkdownPage(w, name, text)
		return
	}
	writeDocPage(w, name, `<pre class="doc-pre">`+htmlEscape(text)+`</pre>`)
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
func writeDocFileRaw(w http.ResponseWriter, data []byte) {
	if looksBinaryBytes(data) {
		w.Header().Set("Content-Type", "application/octet-stream")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(data)
}

func formatDocBytes(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.Itoa(n>>20) + " MiB"
	case n >= 1<<10:
		return strconv.Itoa(n>>10) + " KiB"
	default:
		return strconv.Itoa(n) + " B"
	}
}

// writeDocPage emits a minimal standalone HTML page for a document pane: no
// composer, no sidebar — just the document body styled to the grammar via the
// shared stylesheet.
func writeDocPage(w http.ResponseWriter, title, bodyHTML string) {
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
		`<title>` + htmlEscape(title) + `</title>` +
		`<script>(function(){try{var t=localStorage.getItem("serf-hub.theme");if(t)document.documentElement.dataset.theme=t;}catch(e){}})();</script>` +
		`</head><body class="doc-page"><main class="doc-body">` +
		`<header class="doc-head">` + htmlEscape(title) + `</header>` +
		bodyHTML +
		`</main></body></html>`))
}

// writeDocMarkdownPage renders markdown client-side with marked. The raw
// source is carried HTML-escaped inside a hidden <div>: reading it back via
// textContent decodes the entities to the original markdown, and because the
// content is fully escaped no markup in the file can inject into the page
// before marked runs. (A <script> block would be wrong here — script content
// is raw text the browser never entity-decodes, so an escaped sequence would
// survive literally and corrupt the markdown.)
func writeDocMarkdownPage(w http.ResponseWriter, title, md string) {
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
		`<title>` + htmlEscape(title) + `</title>` +
		`<script>(function(){try{var t=localStorage.getItem("serf-hub.theme");if(t)document.documentElement.dataset.theme=t;}catch(e){}})();</script>` +
		`</head><body class="doc-page"><main class="doc-body">` +
		`<header class="doc-head">` + htmlEscape(title) + `</header>` +
		`<div class="doc-markdown markdown"></div>` +
		`<div id="doc-src" hidden>` + htmlEscape(md) + `</div>` +
		`<script>(function(){var raw=document.getElementById("doc-src").textContent;` +
		`var target=document.querySelector(".doc-markdown");` +
		`if(window.marked&&window.marked.parse){target.innerHTML=window.marked.parse(raw);}` +
		`else{var pre=document.createElement("pre");pre.textContent=raw;target.appendChild(pre);}})();</script>` +
		`</main></body></html>`))
}
