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
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// docServeTestServer seeds a past session whose cwd is a real temp directory,
// then returns the WebServer plus that cwd so a test can drop files into it.
func docServeTestServer(t *testing.T) (*WebServer, string, string) {
	t.Helper()
	root := t.TempDir()
	cwd := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "01DOC", UpdatedAt: time.Now(), OriginalPrompt: "doc session",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: idx})
	return web, cwd, "01DOC"
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
