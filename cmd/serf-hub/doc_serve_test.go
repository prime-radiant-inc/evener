package main

// Tests for the /doc/file document-pane route. The route resolves a LOCAL
// session's cwd and serves a read-only view of a file inside that cwd:
// markdown is rendered, other text is escaped into <pre>, binary gets a
// notice. The load-bearing requirement is path-traversal containment — a
// request whose resolved path escapes the session cwd must be rejected,
// never served.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

// docServeTestServer seeds a past session whose cwd is a real temp directory,
// then returns the WebServer plus that cwd so a test can drop files into it.
func docServeTestServer(t *testing.T) (*WebServer, string, string) {
	t.Helper()
	root := t.TempDir()
	cwd := t.TempDir()
	proj := filepath.Join(root, "projects", "project-docs-0000000000")
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: sessionID, UpdatedAt: time.Now(), OriginalPrompt: "doc session",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: idx})
	return web, cwd, sessionID
}

func docRequest(t *testing.T, web *WebServer, session, path string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/file?session=" + session + "&path=" + path
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

func docImageRequest(t *testing.T, web *WebServer, session, path string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/image?session=" + session + "&path=" + path
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

func docRawRequest(t *testing.T, web *WebServer, session, path string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/file?format=raw&session=" + session + "&path=" + path
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDocImageServesPNG(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "out.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docImageRequest(t, web, session, "out.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type=%q, want image/png", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("body=%x, want %x", rec.Body.Bytes(), png)
	}
}

func TestDocImageServesLiveDescriptorURLWithoutPast(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID := "02wMz5Txv2enqVTitaig6F"
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID:        91,
			Protocol:   appwire.ProtocolVersion,
			Endpoint:   "ws://127.0.0.1:1/rpc",
			ThreadID:   sessionID,
			SessionID:  sessionID,
			WorkingDir: cwd,
		},
		SessionID: sessionID,
		Status:    appwire.ThreadStatusIdle,
	})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: roster})

	imgs := outputImagesForToolCall(sessionID, cwd, "shell", `{}`, "created plot.png")
	if len(imgs) != 1 || imgs[0].URL == "" || imgs[0].Path != "plot.png" {
		t.Fatalf("descriptor=%+v, want one live /doc/image plot.png descriptor", imgs)
	}
	rec := docImageRequest(t, web, sessionID, imgs[0].Path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%q", imgs[0].URL, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type=%q, want image/png", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("body=%x, want %x", rec.Body.Bytes(), png)
	}
}

func TestDocImageRejectsTraversalAndSVG(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	secret := filepath.Join(filepath.Dir(cwd), "secret.png")
	if err := os.WriteFile(secret, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := docImageRequest(t, web, session, "../"+filepath.Base(secret)); rec.Code != http.StatusForbidden {
		t.Fatalf("traversal image request status=%d, want 403", rec.Code)
	}
	if err := os.WriteFile(filepath.Join(cwd, "x.svg"), []byte(`<svg></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := docImageRequest(t, web, session, "x.svg"); rec.Code != http.StatusNotFound {
		t.Fatalf("svg image request status=%d, want 404", rec.Code)
	}
}

func TestDocFile_ServesTextFileEscaped(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello <script>alert(1)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRequest(t, web, session, "notes.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<pre") {
		t.Errorf("text file should render inside <pre>; got %q", body)
	}
	// The file contents must be HTML-escaped so a file can't inject markup.
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("file contents must be escaped, found raw <script>: %q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("escaped contents missing: %q", body)
	}
}

func TestDocFile_RendersMarkdown(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "README.md"), []byte("# Title\n\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRequest(t, web, session, "README.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Markdown is rendered client-side via marked in the served page; the page
	// must carry the raw markdown in a script/data block and load marked.
	if !strings.Contains(body, "marked") {
		t.Errorf("markdown page should load marked.js; got %q", body)
	}
	if !strings.Contains(body, "doc-markdown") {
		t.Errorf("markdown page should mark the render target; got %q", body)
	}
	// The actual markdown source must be forwarded into the page (embedded in
	// the hidden #doc-src div). A broken implementation that passes an empty
	// string still emits "marked" and "doc-markdown", so we verify the content.
	if !strings.Contains(body, "# Title") {
		t.Errorf("markdown page must embed the markdown source (heading missing); got %q", body)
	}
}

func TestDocFile_RejectsTraversalDotDot(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	// A secret file one level above the cwd — must never be served.
	secret := filepath.Join(filepath.Dir(cwd), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRequest(t, web, session, "../"+filepath.Base(secret))
	if rec.Code == http.StatusOK {
		t.Fatalf("traversal must be rejected, got 200 body=%q", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("traversal should yield 403/404, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "TOP SECRET") {
		t.Fatalf("SECURITY: traversal served out-of-cwd file contents")
	}
}

func TestDocFile_RejectsAbsolutePathEscape(t *testing.T) {
	web, _, session := docServeTestServer(t)
	// An absolute path that is clearly outside the cwd.
	rec := docRequest(t, web, session, "/etc/passwd")
	if rec.Code == http.StatusOK {
		t.Fatalf("absolute out-of-cwd path must be rejected, got 200 body=%q", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("absolute escape should yield 403/404, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "root:") {
		t.Fatalf("SECURITY: absolute path served /etc/passwd")
	}
}

func TestDocFile_RejectsSymlinkEscape(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	secret := filepath.Join(filepath.Dir(cwd), "outside.txt")
	if err := os.WriteFile(secret, []byte("SYMLINK SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the cwd that points outside it. The resolved target
	// escapes the cwd, so the route must refuse to serve it.
	link := filepath.Join(cwd, "escape")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	rec := docRequest(t, web, session, "escape")
	if strings.Contains(rec.Body.String(), "SYMLINK SECRET") {
		t.Fatalf("SECURITY: symlink escape served out-of-cwd contents (status %d)", rec.Code)
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("symlink escape must be rejected, got 200")
	}
}

func TestDocFile_ServesWorktreeRelativePathForPaneNavigation(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	rel := filepath.Join(".worktrees", "sandbox-mode", "agent", "job_delegate_test.go")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(cwd, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, rel), []byte("package agent\n\nfunc TestDelegate() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Side panes load /doc/file through an iframe src. Browser iframe navigations
	// cannot attach the HX-Request header, so /doc/file must serve valid in-cwd
	// file paths as normal GET document requests.
	req := httptest.NewRequest(http.MethodGet, "/doc/file?session="+session+"&path=.worktrees%2Fsandbox-mode%2Fagent%2Fjob_delegate_test.go", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "job_delegate_test.go") || !strings.Contains(body, "package agent") {
		t.Fatalf("worktree file document missing title/content: %q", body)
	}
}

func TestDocFile_UnknownSession404(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	rec := docRequest(t, web, "01NOPE", "anything.txt")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown session should 404, got %d", rec.Code)
	}
}

func TestDocFile_NonLocalSession404(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	// A non-local (remote/codex) ref must be skipped — local sources only.
	rec := docRequest(t, web, "codex:th_remote", "README.md")
	if rec.Code != http.StatusNotFound {
		t.Errorf("non-local session should 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestDocFile_BinaryNotice(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	// NUL bytes mark the file as binary.
	if err := os.WriteFile(filepath.Join(cwd, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRequest(t, web, session, "blob.bin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "binary") {
		t.Errorf("binary file should show a binary notice; got %q", rec.Body.String())
	}
}

// The tests below cover the ?format=raw variant, added for the native React
// doc-viewer pane: it needs the file's actual bytes, not the server-rendered
// HTML page. The default (no format param) behavior must stay byte-unchanged
// — proven by the tests above still passing unmodified — and the guard chain
// (session/path validation, 403-vs-404) must reject a raw request exactly
// like it rejects the HTML request for the same guarded input.

func TestDocFile_Raw_ServesTextFileVerbatim(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	content := []byte("hello <script>alert(1)</script>")
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "notes.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type=%q, want text/plain; charset=utf-8", ct)
	}
	// Raw means raw: no HTML escaping, no <pre> wrapper, no doc-page chrome.
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want verbatim %q", rec.Body.Bytes(), content)
	}
}

func TestDocFile_Raw_ServesMarkdownSourceVerbatim(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	content := []byte("# Title\n\nbody")
	if err := os.WriteFile(filepath.Join(cwd, "README.md"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "README.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type=%q, want text/plain; charset=utf-8", ct)
	}
	// Raw must bypass the server-side marked.js HTML page entirely — the
	// client renders markdown itself from the literal source bytes.
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want verbatim markdown source %q", rec.Body.Bytes(), content)
	}
	if strings.Contains(rec.Body.String(), "marked") || strings.Contains(rec.Body.String(), "doc-markdown") {
		t.Fatalf("raw markdown must not carry the HTML render wrapper: %q", rec.Body.String())
	}
}

func TestDocFile_Raw_ServesBinaryAsOctetStream(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	blob := []byte{0x00, 0x01, 0x02, 0x00, 0xff}
	if err := os.WriteFile(filepath.Join(cwd, "blob.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "blob.bin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type=%q, want application/octet-stream", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), blob) {
		t.Fatalf("body=%x, want verbatim %x", rec.Body.Bytes(), blob)
	}
}

func TestDocFile_Raw_RejectsTraversalDotDot(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	secret := filepath.Join(filepath.Dir(cwd), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	htmlRec := docRequest(t, web, session, "../"+filepath.Base(secret))
	rawRec := docRawRequest(t, web, session, "../"+filepath.Base(secret))
	if rawRec.Code != htmlRec.Code {
		t.Fatalf("raw status=%d, want parity with HTML variant status=%d", rawRec.Code, htmlRec.Code)
	}
	if rawRec.Code != http.StatusForbidden && rawRec.Code != http.StatusNotFound {
		t.Errorf("traversal should yield 403/404, got %d", rawRec.Code)
	}
	if strings.Contains(rawRec.Body.String(), "TOP SECRET") {
		t.Fatalf("SECURITY: raw traversal served out-of-cwd file contents")
	}
}

func TestDocFile_Raw_RejectsAbsolutePathEscape(t *testing.T) {
	web, _, session := docServeTestServer(t)
	htmlRec := docRequest(t, web, session, "/etc/passwd")
	rawRec := docRawRequest(t, web, session, "/etc/passwd")
	if rawRec.Code != htmlRec.Code {
		t.Fatalf("raw status=%d, want parity with HTML variant status=%d", rawRec.Code, htmlRec.Code)
	}
	if rawRec.Code != http.StatusForbidden && rawRec.Code != http.StatusNotFound {
		t.Errorf("absolute escape should yield 403/404, got %d", rawRec.Code)
	}
	if strings.Contains(rawRec.Body.String(), "root:") {
		t.Fatalf("SECURITY: raw absolute path served /etc/passwd")
	}
}

func TestDocFile_Raw_RejectsSymlinkEscape(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	secret := filepath.Join(filepath.Dir(cwd), "outside.txt")
	if err := os.WriteFile(secret, []byte("SYMLINK SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "escape")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	rawRec := docRawRequest(t, web, session, "escape")
	if strings.Contains(rawRec.Body.String(), "SYMLINK SECRET") {
		t.Fatalf("SECURITY: raw symlink escape served out-of-cwd contents (status %d)", rawRec.Code)
	}
	if rawRec.Code == http.StatusOK {
		t.Fatalf("raw symlink escape must be rejected, got 200")
	}
}

func TestDocFile_Raw_UnknownSession404(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	rec := docRawRequest(t, web, "01NOPE", "anything.txt")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown session should 404, got %d", rec.Code)
	}
}

func TestDocFile_Raw_NonLocalSession404(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	rec := docRawRequest(t, web, "codex:th_remote", "README.md")
	if rec.Code != http.StatusNotFound {
		t.Errorf("non-local session should 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestWriteDocPages_NoDeadAssetReferences pins the legacy non-raw document
// pages to reference no /assets/ URL. M10 deletes assets/style.css and
// assets/marked.min.js, so a page that linked them would serve dead links.
// The pages degrade instead — unstyled, and markdown falls back to a <pre>
// via the inline script's window.marked guard — with no broken references.
func TestWriteDocPages_NoDeadAssetReferences(t *testing.T) {
	page := httptest.NewRecorder()
	writeDocPage(page, "notes.txt", `<pre class="doc-pre">x</pre>`)
	if strings.Contains(page.Body.String(), "/assets/") {
		t.Errorf("writeDocPage emitted a dead /assets/ reference: %q", page.Body.String())
	}

	md := httptest.NewRecorder()
	writeDocMarkdownPage(md, "README.md", "# Title\n\nbody")
	if strings.Contains(md.Body.String(), "/assets/") {
		t.Errorf("writeDocMarkdownPage emitted a dead /assets/ reference: %q", md.Body.String())
	}
}
