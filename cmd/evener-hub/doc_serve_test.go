package hub

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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
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
	return docRawRequestIfNoneMatch(t, web, session, path, "")
}

// docRawRequestIfNoneMatch issues a raw /doc/file GET carrying If-None-Match,
// or no such header when etag is empty.
func docRawRequestIfNoneMatch(t *testing.T, web *WebServer, session, path, etag string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/file?format=raw&session=" + session + "&path=" + path
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDocFile_ArchivedRootBypassesWarmPastMetadata(t *testing.T) {
	web, rootA, sessionID := docServeTestServer(t)
	rootB := t.TempDir()
	for root, contents := range map[string]string{rootA: "launch A", rootB: "current B"} {
		if err := os.WriteFile(filepath.Join(root, "plan.md"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry, ok := web.cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("fixture session is missing from the warm past index")
	}
	meta, err := schema.LoadSessionMeta(entry.StateDir, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta.EnvInfo.WorkingDir = rootB
	if err := schema.SaveSessionMeta(entry.StateDir, meta); err != nil {
		t.Fatal(err)
	}
	if cached, ok := web.cfg.Past.Find(sessionID); !ok || cached.Meta.EnvInfo.WorkingDir != rootA {
		t.Fatalf("fixture did not preserve stale A metadata: %+v", cached)
	}
	response := docRawRequest(t, web, sessionID, "plan.md")
	if response.Code != http.StatusOK || response.Body.String() != "current B" {
		t.Fatalf("document = %d %q, want current B", response.Code, response.Body.String())
	}
}

func TestDocFile_ArchivedMetadataFailureRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		broken    []byte
		directory bool
		status    int
	}{
		{name: "missing metadata", status: http.StatusNotFound},
		{name: "invalid JSON", broken: []byte("{"), status: http.StatusServiceUnavailable},
		{name: "metadata read failure", directory: true, status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			web, rootA, sessionID := docServeTestServer(t)
			rootB := t.TempDir()
			for root, contents := range map[string]string{rootA: "stale A", rootB: "recovered B"} {
				if err := os.WriteFile(filepath.Join(root, "plan.md"), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(rootA, "plot.png"), worktreeImageA, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(rootB, "plot.png"), worktreeImageB, 0o600); err != nil {
				t.Fatal(err)
			}
			entry, ok := web.cfg.Past.Find(sessionID)
			if !ok {
				t.Fatal("fixture session is missing from the warm past index")
			}
			meta, err := schema.LoadSessionMeta(entry.StateDir, entry.ID)
			if err != nil {
				t.Fatal(err)
			}
			meta.EnvInfo.WorkingDir = rootB
			metaPath := filepath.Join(entry.StateDir, "sessions", entry.ID+".meta.json")
			if err := os.Remove(metaPath); err != nil {
				t.Fatal(err)
			}
			if tc.directory {
				if err := os.Mkdir(metaPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if tc.broken != nil {
				if err := os.WriteFile(metaPath, tc.broken, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for name, response := range map[string]*httptest.ResponseRecorder{
				"text":  docRawRequest(t, web, sessionID, "plan.md"),
				"image": docImageRequest(t, web, sessionID, "plot.png"),
			} {
				if response.Code != tc.status {
					t.Errorf("%s status = %d, want %d; body = %q", name, response.Code, tc.status, response.Body.String())
				}
			}
			params := appwire.SessionDocumentParams{SessionID: sessionID, Path: "plan.md"}
			if _, err := sessionDocumentFromHub(t.Context(), web.cfg, web.sources, params); err == nil || sessionDocumentProxyStatus(err) != tc.status {
				t.Errorf("AppWire metadata failure = %v, want status %d", err, tc.status)
			}
			if tc.directory || tc.broken != nil {
				if err := os.Remove(metaPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := schema.SaveSessionMeta(entry.StateDir, meta); err != nil {
				t.Fatal(err)
			}
			if cached, ok := web.cfg.Past.Find(sessionID); !ok || cached.Meta.EnvInfo.WorkingDir != rootA {
				t.Fatalf("fixture did not preserve stale A metadata: %+v", cached)
			}
			if response := docRawRequest(t, web, sessionID, "plan.md"); response.Code != http.StatusOK || response.Body.String() != "recovered B" {
				t.Fatalf("recovered document = %d %q, want recovered B", response.Code, response.Body.String())
			}
			if response := docImageRequest(t, web, sessionID, "plot.png"); response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), worktreeImageB) {
				t.Fatalf("recovered image = %d %x, want current B image", response.Code, response.Body.Bytes())
			}
			if response, err := sessionDocumentFromHub(t.Context(), web.cfg, web.sources, params); err != nil || string(response.Data) != "recovered B" {
				t.Fatalf("recovered AppWire document = %q, %v, want recovered B", response.Data, err)
			}
		})
	}
}

func TestDocFile_Raw_TrustedCWDAliasServesIdenticalBytes(t *testing.T) {
	web, root, sessionID := docServeTestServer(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	authored := []byte("# Aliased plan\n\nidentical authored bytes\n")
	if err := os.WriteFile(filepath.Join(realRoot, "plan.md"), authored, 0o600); err != nil {
		t.Fatal(err)
	}

	entry, ok := web.cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("fixture session is missing from the past index")
	}
	meta, err := schema.LoadSessionMeta(entry.StateDir, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta.EnvInfo.WorkingDir = alias
	if err := schema.SaveSessionMeta(entry.StateDir, meta); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"relative":           "plan.md",
		"alias absolute":     filepath.Join(alias, "plan.md"),
		"canonical absolute": filepath.Join(realRoot, "plan.md"),
	} {
		t.Run(name, func(t *testing.T) {
			response := docRawRequest(t, web, sessionID, url.QueryEscape(path))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %q status = %d, want 200; body = %q", path, response.Code, response.Body.String())
			}
			if !bytes.Equal(response.Body.Bytes(), authored) {
				t.Fatalf("GET %q body = %q, want exact authored bytes %q", path, response.Body.Bytes(), authored)
			}
		})
	}
}

// docRequestWithFormat issues a /doc/file GET with an explicit format value, so
// a test can assert how the raw-only route treats a non-raw format token.
func docRequestWithFormat(t *testing.T, web *WebServer, session, path, format string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/file?format=" + format + "&session=" + session + "&path=" + path
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

// /doc/file serves exactly one mode: the file's raw bytes (?format=raw). A
// request that omits format, or sends any other value, is a client error — 400
// with a plain-text hint naming the required parameter, not a served document.
func TestDocFile_MissingFormat_400(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRequest(t, web, session, "notes.txt")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing format: status=%d, want 400; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "format=raw") {
		t.Errorf("400 hint must name the required parameter; got %q", rec.Body.String())
	}
}

func TestDocFile_NonRawFormat_400(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRequestWithFormat(t, web, session, "notes.txt", "html")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("format=html: status=%d, want 400; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "format=raw") {
		t.Errorf("400 hint must name the required parameter; got %q", rec.Body.String())
	}
}

func TestDocFile_EmptyFormat_400(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRequestWithFormat(t, web, session, "notes.txt", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty format: status=%d, want 400; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "format=raw") {
		t.Errorf("400 hint must name the required parameter; got %q", rec.Body.String())
	}
}

// A non-raw request must be refused before the file is read and hashed. The
// format gate sits ahead of the read, so an existing file is never opened for
// a request that will 400 — the guard is observable because the read itself
// fails here.
func TestDocFile_NonRawFormatSkipsFileRead(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	origOpen := docOpen
	t.Cleanup(func() { docOpen = origOpen })
	opened := false
	docOpen = func(string, string) (*os.File, error) {
		opened = true
		return nil, os.ErrInvalid
	}
	rec := docRequest(t, web, session, "notes.txt")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-raw: status=%d, want 400; body=%q", rec.Code, rec.Body.String())
	}
	if opened {
		t.Fatalf("non-raw request must be refused before the file is opened")
	}
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
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	daemon.SetAppIdentity("local", sessionID)
	daemon.SetStatus(daemonserver.StatusInfo{SessionID: sessionID, State: appwire.ThreadStatusIdle, WorkingDir: cwd})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry := rendezvous.Entry{
		PID: 91, Protocol: appwire.ProtocolVersion, Endpoint: daemonHTTP.URL,
		SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
		WorkspaceRef: "local:" + sessionID, WorkingDir: cwd,
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     entry,
		SessionID: sessionID,
		Status:    appwire.ThreadStatusIdle,
	})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: roster})
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: sessionID}}
	}, daemonHTTP.Client()))
	web.sources = sources

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

func TestDocFile_UnknownSession404(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	rec := docRequest(t, web, "01NOPE", "anything.txt")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown session should 404, got %d", rec.Code)
	}
}

func TestDocFile_NonLocalSessionNeedsRawFormat400(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	// A host-qualified ref is never read locally, and a request the route
	// would refuse is refused before any host is asked.
	rec := docRequest(t, web, "remote:th_remote", "README.md")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-raw request for a non-local session should 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// The tests below cover the served mode, ?format=raw: the doc-viewer pane needs
// the file's actual bytes. The guard chain (session/path validation, 403-vs-404
// containment) runs before the format check, so a raw request and a non-raw
// request reject the same guarded input identically — the parity tests below
// assert that. A fully valid non-raw request is a 400 (see the format tests
// above).

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

func TestDocFile_Raw_UnknownHostSession503(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	// No source named "remote" is registered: the read is refused as the
	// host being unavailable, never resolved against this hub's own disk.
	rec := docRawRequest(t, web, "remote:th_remote", "README.md")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a session on an unknown host should 503, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// A file larger than the cap is served truncated to the first docFileMaxBytes,
// with an explicit truncation signal AND the file's true total size so the
// pane can render an exact "showing the first 512 KiB of <total>" notice
// instead of guessing from the body length.
func TestDocFile_Raw_OverCapEmitsTruncationHeaders(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	const total = docFileMaxBytes + 100
	if err := os.WriteFile(filepath.Join(cwd, "huge.log"), bytes.Repeat([]byte("a"), total), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "huge.log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Doc-Truncated"); got != "true" {
		t.Fatalf("X-Doc-Truncated=%q, want true", got)
	}
	if got := rec.Header().Get("X-Doc-Total-Size"); got != strconv.Itoa(total) {
		t.Fatalf("X-Doc-Total-Size=%q, want %d (the true total)", got, total)
	}
	if rec.Body.Len() != docFileMaxBytes {
		t.Fatalf("served %d bytes, want the first %d (cap)", rec.Body.Len(), docFileMaxBytes)
	}
}

// A file of exactly the cap size is served in full and is NOT truncated: the
// whole file fits, so no truncation signal is emitted (the false-positive the
// old body>=cap derivation produced at exactly this boundary).
func TestDocFile_Raw_ExactlyCapIsNotTruncated(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "exact.log"), bytes.Repeat([]byte("a"), docFileMaxBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "exact.log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Doc-Truncated"); got != "" {
		t.Fatalf("X-Doc-Truncated=%q, want absent (a cap-sized file is complete)", got)
	}
	if got := rec.Header().Get("X-Doc-Total-Size"); got != "" {
		t.Fatalf("X-Doc-Total-Size=%q, want absent for an untruncated file", got)
	}
	if rec.Body.Len() != docFileMaxBytes {
		t.Fatalf("served %d bytes, want the whole %d-byte file", rec.Body.Len(), docFileMaxBytes)
	}
}

// A small file carries no truncation headers at all.
func TestDocFile_Raw_UnderCapEmitsNoTruncationHeaders(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	content := []byte("a short file")
	if err := os.WriteFile(filepath.Join(cwd, "small.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "small.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Doc-Truncated"); got != "" {
		t.Fatalf("X-Doc-Truncated=%q, want absent", got)
	}
	if got := rec.Header().Get("X-Doc-Total-Size"); got != "" {
		t.Fatalf("X-Doc-Total-Size=%q, want absent", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want %q", rec.Body.Bytes(), content)
	}
}

// A session's retained artifacts directory is the second root /doc/file and
// /doc/image serve from. Agent reports filed there are named by file:// URLs in
// assistant prose, and the web transcript turns those into document-pane links,
// so the route has to serve them for the link to resolve.
func artifactsDirOf(t *testing.T, web *WebServer, session string) string {
	t.Helper()
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatalf("fixture session %q is missing from the past index", session)
	}
	dir, err := schema.SessionArtifactsDir(entry.StateDir, session)
	if err != nil {
		t.Fatalf("fixture artifacts dir: %v", err)
	}
	return dir
}

func TestDocFile_Raw_ServesSessionArtifacts(t *testing.T) {
	web, _, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("# Report\n\nretained evidence")
	target := filepath.Join(dir, "report.md")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("artifacts file should be served, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want %q", rec.Body.Bytes(), content)
	}
}

func TestDocImage_ServesSessionArtifactsImage(t *testing.T) {
	web, _, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	target := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(target, png, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docImageRequest(t, web, session, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("artifacts image should be served, got %d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type=%q, want image/png", ct)
	}
}

// The artifacts root is one more boundary, not a replacement for cwd: a file
// outside both is still refused, and the artifacts root of one session is not
// reachable from another.
func TestDocFile_ArtifactsRootDoesNotWidenCwdContainment(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The session's own state directory holds its transcript and job store;
	// a retained document must not reach them just because artifacts is a
	// sibling.
	secret := filepath.Join(filepath.Dir(dir), "delegates.jsonl")
	if err := os.WriteFile(secret, []byte("SESSION SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{secret, filepath.Join(cwd, "..", "secret.txt")} {
		rec := docRawRequest(t, web, session, path)
		if rec.Code == http.StatusOK {
			t.Fatalf("path %q outside both roots must be refused, got 200", path)
		}
		if strings.Contains(rec.Body.String(), "SESSION SECRET") {
			t.Fatalf("SECURITY: %q served a file outside the served roots", path)
		}
	}
}

// A relative path keeps resolving against cwd only. It names no artifacts file
// even when a same-named file exists there, so the second root cannot be used
// to reach a neighboring session's artifacts either.
func TestDocFile_RelativePathNeverResolvesAgainstArtifacts(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "only-in-artifacts.md"), []byte("artifacts only"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "only-in-artifacts.md")
	if rec.Code == http.StatusOK {
		t.Fatalf("a relative path must resolve against cwd only, got 200 body=%q", rec.Body.String())
	}
	if err := os.WriteFile(filepath.Join(cwd, "cwd-file.md"), []byte("cwd file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := docRawRequest(t, web, session, "cwd-file.md"); rec.Code != http.StatusOK {
		t.Fatalf("cwd-relative file must still be served, got %d", rec.Code)
	}
}

func TestDocFile_ArtifactsUnknownSession404(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	// A path under a session that does not exist never resolves, even though
	// its shape is the same one a real session's artifacts would hold.
	target := filepath.Join(t.TempDir(), "01NOPE", "artifacts", "report.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, "01NOPE", target)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session should 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// A worktree cleanup removes the session's working directory. The retained
// artifacts live under the session's state directory, which survives, so a
// link an agent filed there must still open: the artifacts root is resolved on
// its own absolute path, not as a fallback that needs a healthy cwd first.
func TestDocFile_Raw_ServesSessionArtifactsAfterWorkingDirectoryRemoved(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("# Report\n\nretained evidence")
	target := filepath.Join(dir, "report.md")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("artifacts file should be served after cwd removal, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want %q", rec.Body.Bytes(), content)
	}
}

// The AppWire read carries the same rule as the HTTP routes: a relative path
// resolves against the working directory only. Without that guard a relative
// name could land on a same-named file in artifacts, which is a different
// document than the one the request named.
func TestHubSessionDocumentRelativePathNeverResolvesAgainstArtifacts(t *testing.T) {
	cwd := docTestRoot(t)
	// This fixture owns its state dir, so the test can put a same-named file
	// in artifacts and leave cwd without one.
	const session = "02wMz5Txv1C3Hut0M8GCeE"
	stateDir := t.TempDir()
	proj := filepath.Join(stateDir, "projects", "project-doc-0000000000")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: session, UpdatedAt: time.Now(), OriginalPrompt: "doc session",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := schema.SessionArtifactsDir(proj, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("artifacts report"), 0o644); err != nil {
		t.Fatal(err)
	}
	// cwd holds a report.md that is a symlink leading out of the session. A
	// request for the relative name "report.md" names that refusable target,
	// not the unrelated file of the same name in artifacts.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "elsewhere.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "elsewhere.md"), filepath.Join(cwd, "report.md")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(stateDir, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: idx})
	t.Cleanup(srv.Close)
	if _, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: session, Path: "report.md"}); err == nil {
		t.Fatal("a relative path resolved against the artifacts root; want a refusal")
	}
	// The absolute path is the one that may reach it.
	resp, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: session, Path: filepath.Join(dir, "report.md")})
	if err != nil || string(resp.Data) != "artifacts report" {
		t.Fatalf("absolute artifacts path = %q, %v; want the retained report", resp.Data, err)
	}
}

// Removing the working directory must not turn an ordinary relative read into
// an error: the file is simply not there any more, which is the same 404 a
// missing file always gets.
func TestDocFile_RelativeReadAfterWorkingDirectoryRemovedIsNotFound(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "notes.txt")
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("relative read after cwd removal = %d, want a not-found or unavailable refusal", rec.Code)
	}
}

// The AppWire read answers a retained artifact after the working directory is
// removed, exactly as the local /doc/file route does. Resolving the artifacts
// root does not need the cwd, so a removed cwd must not make it a miss.
func TestHubSessionDocumentServesArtifactsAfterWorkingDirectoryRemoved(t *testing.T) {
	cwd := t.TempDir()
	const session = "02wMz5Txv1C3Hut0M8GCeF"
	stateDir := t.TempDir()
	proj := filepath.Join(stateDir, "projects", "project-doc-1111111111")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: session, UpdatedAt: time.Now(), OriginalPrompt: "doc session",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := schema.SessionArtifactsDir(proj, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "report.md")
	if err := os.WriteFile(target, []byte("retained report"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(stateDir, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: idx})
	t.Cleanup(srv.Close)
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatal(err)
	}
	resp, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: session, Path: target})
	if err != nil || string(resp.Data) != "retained report" {
		t.Fatalf("after cwd removal: %q, %v; want the retained report", resp.Data, err)
	}
}

// A live session keeps the stable workspace ref it launched under across
// Clear. A document request naming that ref must reach the current session's
// retained artifacts, on both routes: the roster keys live entries by the
// current id, and no saved entry exists under the stable one.
func TestDocFile_Raw_ServesArtifactsForAStableRef(t *testing.T) {
	const stableRef = "local:02wMz5Txv1C3Hut0M8GCeI"
	const session = "02wMz5Txv1C3Hut0M8GCeJ"
	stateDir := t.TempDir()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{SessionID: session, WorkspaceRef: stableRef, StateDir: stateDir, StartedAt: time.Now().UTC()},
		SessionID: session,
	})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: roster, StateDir: stateDir})
	dir, err := schema.SessionArtifactsDir(stateDir, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("retained report")
	target := filepath.Join(dir, "report.md")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, stableRef, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("stable-ref read = %d body=%q, want the retained report", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want %q", rec.Body.Bytes(), content)
	}
}

// A session fenced for deletion must not serve retained files through the
// artifacts root. That root resolves from the roster and the past index, which
// know nothing about deletion, so it applies the same fence the cwd path gets.
func TestDocFile_ArtifactsRefuseADeletionFencedSession(t *testing.T) {
	web, _, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("retained report")
	target := filepath.Join(dir, "report.md")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	projectKey := "project-docs-0000000000"
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	web.cfg.DeletionStore = store
	if _, err := store.Begin(projectKey, []hubcore.DeletionTarget{{Ref: "local:" + session, ThreadID: session}}); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, target)
	if rec.Code == http.StatusOK {
		t.Fatalf("a deletion-fenced session served %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "retained report") {
		t.Fatalf("SECURITY: a deletion-fenced session served its retained file")
	}
}

// A path that belongs to the artifacts root but names no file there is a miss,
// not a forbidden escape. Calling the cwd's refusal would tell a user their own
// report is off limits when it is simply gone.
func TestDocFile_MissingArtifactIsNotFoundNotEmptyForbidden(t *testing.T) {
	web, _, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, filepath.Join(dir, "never-written.md"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("contained missing artifact = %d, want 404", rec.Code)
	}
	// A path outside both roots keeps its escape refusal.
	rec = docRawRequest(t, web, session, "/etc/passwd")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("outside-both-roots path = %d, want 403", rec.Code)
	}
}

// A symlink planted where a session's artifacts directory belongs must not
// redirect that session's reads into another session's files. The layout
// beneath the state directory is fixed, so every component of it is checked as
// a real directory rather than followed.
func TestDocFile_ArtifactsRefuseASymlinkedRootIntoAnotherSession(t *testing.T) {
	web, _, session := docServeTestServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatal("fixture session is missing from the past index")
	}
	const other = "02wMz5Txv1C3Hut0M8GCeK"
	otherDir, err := schema.SessionArtifactsDir(entry.StateDir, other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(otherDir, "secret.md")
	if err := os.WriteFile(secret, []byte("OTHER SESSION"), 0o644); err != nil {
		t.Fatal(err)
	}
	// This session's own artifacts directory is a symlink to the other one.
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherDir, dir); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	rec := docRawRequest(t, web, session, filepath.Join(dir, "secret.md"))
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "OTHER SESSION") {
		t.Fatalf("a symlinked artifacts root served another session's file (status %d)", rec.Code)
	}
}

// The AppWire read draws the same containment/existence line as the local
// route: a missing file inside an existing artifacts root is not found, not
// outside the session.
func TestHubSessionDocumentMissingArtifactIsNotFound(t *testing.T) {
	cwd := docTestRoot(t)
	const session = "02wMz5Txv1C3Hut0M8GCeL"
	stateDir := t.TempDir()
	proj := filepath.Join(stateDir, "projects", "project-doc-2222222222")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: session, UpdatedAt: time.Now(), OriginalPrompt: "doc session",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := schema.SessionArtifactsDir(proj, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(stateDir, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: idx})
	t.Cleanup(srv.Close)
	_, err = requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: session, Path: filepath.Join(dir, "never.md")})
	if err == nil {
		t.Fatal("a missing artifact was served")
	}
	if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorResourceNotFound) {
		t.Fatalf("refusal = %q, want %q", got, appwire.ErrorResourceNotFound)
	}
}

// The `sessions` component is shared by every project's sessions, so a link
// planted there would redirect every read. It is checked as a real directory
// like the components beneath it.
func TestDocFile_ArtifactsRefuseASymlinkedSessionsDirectory(t *testing.T) {
	web, _, session := docServeTestServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatal("fixture session is missing from the past index")
	}
	sessionsDir := filepath.Join(entry.StateDir, "sessions")
	realSessions, err := os.MkdirTemp(filepath.Dir(sessionsDir), "elsewhere-*")
	if err != nil {
		t.Fatal(err)
	}
	// Move the real sessions directory aside and put a link in its place.
	if err := os.Rename(sessionsDir, filepath.Join(realSessions, "sessions")); err != nil {
		t.Skipf("cannot move the sessions directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(realSessions, "sessions"), sessionsDir); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	dir := filepath.Join(sessionsDir, session, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("VIA LINKED SESSIONS"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, filepath.Join(dir, "report.md"))
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "VIA LINKED SESSIONS") {
		t.Fatalf("a symlinked sessions directory served a file (status %d)", rec.Code)
	}
}

// Names that merely begin with two dots are ordinary contained files, not
// parent references, and must be served.
func TestDocFile_ServesArtifactNamesBeginningWithDotDot(t *testing.T) {
	web, _, session := docServeTestServer(t)
	dir := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"..report.md", "...notes.md"} {
		content := []byte("dotdot " + name)
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
		rec := docRawRequest(t, web, session, filepath.Join(dir, name))
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
			t.Fatalf("%q = %d %q, want the file", name, rec.Code, rec.Body.String())
		}
	}
}

// The state directory may sit under a symlinked ancestor (/var on macOS, for
// one). The request path and the root are resolved the same way, so a real
// artifact is not mistaken for an escape.
func TestDocFile_ServesArtifactUnderASymlinkedAncestor(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linked := filepath.Join(linkParent, "root")
	if err := os.Symlink(realRoot, linked); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	cwd := t.TempDir()
	const session = "02wMz5Txv1C3Hut0M8GCeM"
	proj := filepath.Join(linked, "projects", "project-doc-3333333333")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: session, UpdatedAt: time.Now(), OriginalPrompt: "doc session",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := schema.SessionArtifactsDir(proj, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("through a linked ancestor")
	target := filepath.Join(dir, "report.md")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(linked, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: idx})
	rec := docRawRequest(t, web, session, target)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("symlinked-ancestor read = %d %q, want the file", rec.Code, rec.Body.String())
	}
}

// A deletion can be recorded against the session a stable ref now names. Asking
// through the ref must not sidestep that fence, so both identities are checked.
func TestDocFile_ArtifactsRefuseADeletionFencedStableRef(t *testing.T) {
	const stableRef = "local:02wMz5Txv1C3Hut0M8GCeN"
	const session = "02wMz5Txv1C3Hut0M8GCeP"
	stateDir := t.TempDir()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{SessionID: session, WorkspaceRef: stableRef, StateDir: stateDir, StartedAt: time.Now().UTC()},
		SessionID: session,
	})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: roster, StateDir: stateDir})
	dir, err := schema.SessionArtifactsDir(stateDir, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "report.md")
	if err := os.WriteFile(target, []byte("fenced report"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	web.cfg.DeletionStore = store
	if _, err := store.Begin("project-fence-0000000000", []hubcore.DeletionTarget{{Ref: "local:" + session, ThreadID: session}}); err != nil {
		t.Fatal(err)
	}
	// The request names the stable ref, not the fenced session id.
	rec := docRawRequest(t, web, stableRef, target)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "fenced report") {
		t.Fatalf("a stable ref bypassed its current session's deletion fence (status %d)", rec.Code)
	}
}

// A working directory can be shaped exactly like a session's artifacts layout,
// and it can have a symlinked component. It is still an ordinary cwd, so cwd
// containment decides: artifact traversal applies only to a root the resolver
// declared, never to a folder that merely looks like one.
func TestDocFile_ServesALookalikeWorkingDirectoryThroughASymlink(t *testing.T) {
	web, _, session := docServeTestServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatal("fixture session is missing from the past index")
	}
	// The real cwd lives elsewhere, and the session's cwd is shaped exactly
	// like <stateDir>/sessions/<id>/artifacts with its `sessions` component a
	// link. Cwd containment accepts it; artifact traversal must not apply.
	realRoot := t.TempDir()
	realArtifacts := filepath.Join(realRoot, "work", "artifacts")
	if err := os.MkdirAll(realArtifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("ordinary file behind a linked lookalike")
	if err := os.WriteFile(filepath.Join(realArtifacts, "notes.md"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	lookalikeParent := t.TempDir()
	if err := os.Symlink(realRoot, filepath.Join(lookalikeParent, "sessions")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	lookalike := filepath.Join(lookalikeParent, "sessions", "work", "artifacts")
	target := filepath.Join(lookalike, "notes.md")

	meta, err := schema.LoadSessionMeta(entry.StateDir, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta.EnvInfo.WorkingDir = lookalike
	if err := schema.SaveSessionMeta(entry.StateDir, meta); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(filepath.Dir(entry.StateDir), "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web.cfg.Past = idx
	rec := docRawRequest(t, web, session, target)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("symlinked lookalike cwd read = %d %q, want the file", rec.Code, rec.Body.String())
	}
}
