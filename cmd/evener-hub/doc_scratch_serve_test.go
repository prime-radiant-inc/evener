package hub

// Tests for the scratch root the doc routes serve. A session's named scratch
// directory — the tree its meta records, where tools put screenshots and other
// session-local output — is a third served boundary beside the working
// directory and the retained artifacts directory, for absolute paths only. The
// load-bearing requirements are the same ones the artifacts root carries: an
// absolute path inside the session's own scratch is served through the same
// containment and open discipline, a path anywhere else is refused, a relative
// path never retargets to a second root, and a session fenced for deletion
// serves nothing from it.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// docScratchTestServer seeds a past session whose meta records a scratch temp
// base, creates that session's scratch directory in the base, and returns the
// WebServer, the session's cwd, its id and the scratch directory.
func docScratchTestServer(t *testing.T) (*WebServer, string, string, string) {
	t.Helper()
	root := t.TempDir()
	cwd := t.TempDir()
	base := t.TempDir()
	proj := filepath.Join(root, "projects", "project-docs-0000000000")
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: sessionID, UpdatedAt: time.Now(), OriginalPrompt: "scratch session",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: cwd},
		ScratchTempDir: base,
	}); err != nil {
		t.Fatal(err)
	}
	scratch := scratchTreePath(base, sessionID, sessionID)
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	idx := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: idx})
	return web, cwd, sessionID, scratch
}

// scratchTreePath names a session's scratch directory through the allocator's
// own composer, so the fixture cannot drift from the layout it pins.
func scratchTreePath(base, rootID, sessionID string) string {
	return agentsandbox.SessionScratchDirPath(base, rootID, sessionID)
}

func TestDocFile_Raw_ServesSessionScratch(t *testing.T) {
	t.Parallel()
	web, _, session, scratch := docScratchTestServer(t)
	content := []byte("# Scratch notes\n\nsession-local output")
	target := filepath.Join(scratch, "notes.md")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("scratch file should be served, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want %q", rec.Body.Bytes(), content)
	}
}

func TestDocImage_ServesSessionScratchImage(t *testing.T) {
	t.Parallel()
	web, _, session, scratch := docScratchTestServer(t)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	target := filepath.Join(scratch, "shot.png")
	if err := os.WriteFile(target, png, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docImageRequest(t, web, session, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("scratch image should be served, got %d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type=%q, want image/png", ct)
	}
}

func TestDocFile_DownloadServesSessionScratch(t *testing.T) {
	t.Parallel()
	web, _, session, scratch := docScratchTestServer(t)
	content := []byte("whole scratch file")
	target := filepath.Join(scratch, "evidence.log")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	u := "/doc/file?format=download&session=" + session + "&path=" + target
	req := httptest.NewRequest(http.MethodGet, u, nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scratch download should be served, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want the whole file %q", rec.Body.Bytes(), content)
	}
}

// The scratch root is one more boundary, never a widening of the others: a
// file anywhere else in the temp base — another session's scratch tree, a
// sibling of the session's own directory, or an unrelated temp file — stays
// refused, and no bytes outside the session's scratch can leave the route.
func TestDocFile_ScratchRootDoesNotWidenContainment(t *testing.T) {
	t.Parallel()
	web, _, session, scratch := docScratchTestServer(t)
	base := filepath.Dir(filepath.Dir(scratch))
	other := hubtest.SessionID(t)
	if err := os.MkdirAll(scratchTreePath(base, other, other), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(scratchTreePath(base, other, other), "secret.md")
	if err := os.WriteFile(secret, []byte("OTHER SESSION SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	stranger := filepath.Join(t.TempDir(), "stranger.txt")
	if err := os.WriteFile(stranger, []byte("STRANGER SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{secret, stranger, filepath.Join(base, "evener-scratch-"+session, "sibling.txt")} {
		rec := docRawRequest(t, web, session, path)
		if rec.Code == http.StatusOK {
			t.Fatalf("path %q outside the session's scratch must be refused, got 200", path)
		}
		for _, want := range []string{"OTHER SESSION SECRET", "STRANGER SECRET"} {
			if bytes.Contains(rec.Body.Bytes(), []byte(want)) {
				t.Fatalf("SECURITY: %q served bytes from outside the session's scratch", path)
			}
		}
	}
}

// A relative path keeps resolving against cwd only: a file that exists only in
// scratch is not reachable by its bare name.
func TestDocFile_RelativePathNeverResolvesAgainstScratch(t *testing.T) {
	t.Parallel()
	web, cwd, session, scratch := docScratchTestServer(t)
	if err := os.WriteFile(filepath.Join(scratch, "only-in-scratch.md"), []byte("scratch only"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, "only-in-scratch.md")
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

// A path inside the session's scratch that names no file is a miss answered as
// not-found, the same classification a missing cwd file gets — not the
// escape refusal, which a client would read as "forbidden document" rather
// than "gone".
func TestDocFile_MissingScratchFileIsNotFound(t *testing.T) {
	t.Parallel()
	web, _, session, scratch := docScratchTestServer(t)
	rec := docRawRequest(t, web, session, filepath.Join(scratch, "never-written.md"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a missing scratch file should 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// A session whose scratch directory never existed (an old session, a cleaned
// tree) advertises no scratch root and serves none: its scratch-shaped paths
// fall back to the cwd refusal rather than a second root.
func TestDocFile_AbsentScratchDirIsNotServed(t *testing.T) {
	t.Parallel()
	web, cwd, session := docServeTestServer(t)
	missing := filepath.Join(t.TempDir(), "evener-scratch-"+session, session, "shot.png")
	if err := os.MkdirAll(filepath.Dir(missing), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := docRawRequest(t, web, session, missing)
	if rec.Code == http.StatusOK {
		t.Fatalf("a scratch path no session records must not be served, got 200")
	}
	_ = cwd
}

// A delegate's scratch lives in the tree of the root session that spawned it,
// recorded in its meta; the doc route resolves the delegate's own directory
// through that record without serving anything from the root's scratch.
func TestDocFile_DelegateScratchResolvesThroughTheTreeRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := t.TempDir()
	proj := filepath.Join(root, "projects", "project-docs-0000000000")
	delegate := hubtest.SessionID(t)
	treeRoot := hubtest.SessionID(t)
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: delegate, UpdatedAt: time.Now(), OriginalPrompt: "delegate",
		EnvInfo:           schema.EnvironmentInfo{WorkingDir: t.TempDir()},
		ScratchTempDir:    base,
		ScratchTreeRootID: treeRoot,
		IsSubagent:        true,
	}); err != nil {
		t.Fatal(err)
	}
	delegateScratch := scratchTreePath(base, treeRoot, delegate)
	if err := os.MkdirAll(delegateScratch, 0o700); err != nil {
		t.Fatal(err)
	}
	rootScratch := scratchTreePath(base, treeRoot, treeRoot)
	if err := os.MkdirAll(rootScratch, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("delegate evidence")
	target := filepath.Join(delegateScratch, "report.md")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(rootScratch, "root-secret.md")
	if err := os.WriteFile(secret, []byte("ROOT SESSION SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: idx})
	rec := docRawRequest(t, web, delegate, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("delegate scratch file should be served, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body=%q, want %q", rec.Body.Bytes(), content)
	}
	rec = docRawRequest(t, web, delegate, secret)
	if rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte("ROOT SESSION SECRET")) {
		t.Fatalf("the root session's scratch must stay unreachable from the delegate, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// A session fenced for deletion serves nothing from its scratch: the fence
// covers the scratch root just as it covers the artifacts root.
func TestDocFile_DeletionFenceRefusesSessionScratch(t *testing.T) {
	t.Parallel()
	web, _, session, scratch := docScratchTestServer(t)
	secret := filepath.Join(scratch, "fenced.md")
	if err := os.WriteFile(secret, []byte("FENCED SESSION SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin("project-docs-0000000000", []hubcore.DeletionTarget{{
		Ref:      "local:" + session,
		ThreadID: session,
	}}); err != nil {
		t.Fatal(err)
	}
	web.cfg.DeletionStore = store
	rec := docRawRequest(t, web, session, secret)
	if rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte("FENCED SESSION SECRET")) {
		t.Fatalf("a fenced session's scratch must not be served, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// The hub announces the scratch directory its doc route serves a session from,
// exactly as it announces the artifacts directory, so a client can bind a
// session-local path into a link the route answers. The test proves the
// announced directory is the one a real scratch file resolves through.
func TestThreadReadAnnouncesTheScratchDirTheDocRouteServes(t *testing.T) {
	t.Parallel()
	web, _, session, scratch := docScratchTestServer(t)
	target := filepath.Join(scratch, "shot.png")
	if err := os.WriteFile(target, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, abs, _, ok := scratchDocTarget(t.Context(), web.cfg, session, target)
	if !ok || root != scratch || abs != target {
		t.Fatalf("doc route resolved (%q, %q, %v), want (%q, %q, true)", root, abs, ok, scratch, target)
	}
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatalf("fixture session %q is missing from the past index", session)
	}
	thread, err := pastEntryThreadForList(t.Context(), web.cfg, entry)
	if err != nil {
		t.Fatal(err)
	}
	got := applyHubCapabilities(web.cfg, thread).Evener.ScratchDir
	if got != root {
		t.Fatalf("ScratchDir = %q, want the root the doc route served from, %q", got, root)
	}
}

// A session with no scratch directory on disk advertises none: the announced
// root and the served one stay the same answer.
func TestThreadReadLeavesScratchDirUnsetWhenNoScratchExists(t *testing.T) {
	t.Parallel()
	web, _, session := docServeTestServer(t)
	entry, ok := web.cfg.Past.Find(session)
	if !ok {
		t.Fatalf("fixture session %q is missing from the past index", session)
	}
	thread, err := pastEntryThreadForList(t.Context(), web.cfg, entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := applyHubCapabilities(web.cfg, thread).Evener.ScratchDir; got != "" {
		t.Fatalf("ScratchDir = %q, want empty for a session with no scratch directory", got)
	}
}
