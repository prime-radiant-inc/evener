package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
)

// docFileMaxBytes caps how much of a file we read into a document pane. A pane
// is a quick read-only reference, not a pager; large files are truncated with
// a notice rather than streamed in full.
const docFileMaxBytes = 512 * 1024

// handleDocFile serves a read-only document pane for a file inside a LOCAL
// session's working directory. It is HTML-over-the-wire like the /_partials
// family (HX-gated, same auth) but lives under /doc so a pane iframe can frame
// it. Markdown renders via marked; other text renders escaped in <pre>; binary
// gets a notice.
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
	if r.Header.Get("HX-Request") != "true" {
		http.NotFound(w, r)
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
		if err == fspaths.ErrPathEscapesRoot {
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

// localSessionCWD resolves a local session's working directory from the past
// index. Non-local (remote/codex) refs are out of scope and return false.
func (s *WebServer) localSessionCWD(session string) (string, bool) {
	if !isLocalRouteID(session) {
		return "", false
	}
	if s.cfg.Past == nil {
		return "", false
	}
	pe, ok := s.cfg.Past.Find(session)
	if !ok {
		return "", false
	}
	cwd := strings.TrimSpace(pe.Meta.EnvInfo.WorkingDir)
	if cwd == "" {
		return "", false
	}
	return cwd, true
}

// readDocFile reads up to docFileMaxBytes from a regular file. Directories and
// other non-regular files are refused.
func readDocFile(abs string) ([]byte, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := os.Open(abs)
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

func formatDocBytes(n int) string {
	switch {
	case n >= 1<<20:
		return itoaApprox(n>>20) + " MiB"
	case n >= 1<<10:
		return itoaApprox(n>>10) + " KiB"
	default:
		return itoaApprox(n) + " B"
	}
}

func itoaApprox(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// writeDocPage emits a minimal standalone HTML page for a document pane: no
// composer, no sidebar — just the document body styled to the grammar via the
// shared stylesheet.
func writeDocPage(w http.ResponseWriter, title, bodyHTML string) {
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
		`<title>` + htmlEscape(title) + `</title>` +
		`<link rel="stylesheet" href="/assets/style.css">` +
		`<script>(function(){try{var t=localStorage.getItem("serf-hub.theme");if(t)document.documentElement.dataset.theme=t;}catch(e){}})();</script>` +
		`</head><body class="doc-page"><main class="doc-body">` +
		`<header class="doc-head">` + htmlEscape(title) + `</header>` +
		bodyHTML +
		`</main></body></html>`))
}

// writeDocMarkdownPage renders markdown client-side with marked. The raw
// source is carried HTML-escaped inside a non-executed
// <script type="text/plain"> block: the browser decodes the entities back to
// the original markdown when read via textContent, and an escaped "</script>"
// can never break out of the block. The page reads it back and hands it to
// marked.parse for rendering into the target div.
func writeDocMarkdownPage(w http.ResponseWriter, title, md string) {
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
		`<title>` + htmlEscape(title) + `</title>` +
		`<link rel="stylesheet" href="/assets/style.css">` +
		`<script>(function(){try{var t=localStorage.getItem("serf-hub.theme");if(t)document.documentElement.dataset.theme=t;}catch(e){}})();</script>` +
		`<script src="/assets/marked.min.js"></script>` +
		`</head><body class="doc-page"><main class="doc-body">` +
		`<header class="doc-head">` + htmlEscape(title) + `</header>` +
		`<div class="doc-markdown markdown"></div>` +
		`<script type="text/plain" id="doc-src">` + htmlEscape(md) + `</script>` +
		`<script>(function(){var raw=document.getElementById("doc-src").textContent;` +
		`var target=document.querySelector(".doc-markdown");` +
		`if(window.marked&&window.marked.parse){target.innerHTML=window.marked.parse(raw);}` +
		`else{var pre=document.createElement("pre");pre.textContent=raw;target.appendChild(pre);}})();</script>` +
		`</main></body></html>`))
}
