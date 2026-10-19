package hub

// Tests for /doc/file's download representation, the whole-file read a native
// client saves or hands to another app. The raw representation stops at the
// pane cap; this one does not, and it streams as content so Range and HEAD
// work.

import (
	"bytes"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func docDownloadRequest(t *testing.T, web *WebServer, session, path string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/file?format=download&session=" + session + "&path=" + path
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

// A file larger than the doc-pane cap comes back whole. The MP4 matters: a
// video the agent wrote is the motivating case, and a truncated body would be
// a corrupt file handed to another app.
func TestDocDownload_ServesWholeFilePastThePaneCap(t *testing.T) {
	web, cwd, sessionID := docServeTestServer(t)
	body := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, (docFileMaxBytes/4)+1024)
	if err := os.WriteFile(filepath.Join(cwd, "onboarding.mp4"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	resp := docDownloadRequest(t, web, sessionID, "onboarding.mp4")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if got := resp.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("body = %d bytes, want the whole %d-byte file", len(got), len(body))
	}
}

func TestDocDownload_NamesTheFileAndItsType(t *testing.T) {
	web, cwd, sessionID := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "onboarding.mp4"), []byte("\x00\x00\x00\x18ftypmp42"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := docDownloadRequest(t, web, sessionID, "onboarding.mp4")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", ct)
	}
	disposition, params, err := mime.ParseMediaType(resp.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || params["filename"] != "onboarding.mp4" {
		t.Fatalf("Content-Disposition = %q, want an attachment named onboarding.mp4", resp.Header().Get("Content-Disposition"))
	}
}

// An unlisted extension is opaque bytes, never a type borrowed from the host's
// own mime tables: a download is an attachment, and a host-dependent type would
// make the same file arrive differently on two hubs.
func TestDocDownload_UnlistedExtensionIsOpaque(t *testing.T) {
	web, cwd, sessionID := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "notes.unknownext"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := docDownloadRequest(t, web, sessionID, "notes.unknownext")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", ct)
	}
}

// Range matters for a large attachment: an interrupted transfer resumes instead
// of starting over, and the body is the requested slice.
func TestDocDownload_ServesARange(t *testing.T) {
	web, cwd, sessionID := docServeTestServer(t)
	body := bytes.Repeat([]byte("0123456789"), 100)
	if err := os.WriteFile(filepath.Join(cwd, "clip.mp4"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	u := "/doc/file?format=download&session=" + sessionID + "&path=clip.mp4"
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Range", "bytes=10-19")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "0123456789" {
		t.Fatalf("body = %q, want the requested ten bytes", got)
	}
}

// HEAD answers the metadata without a body.
func TestDocDownload_HeadHasNoBody(t *testing.T) {
	web, cwd, sessionID := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "clip.mp4"), []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := "/doc/file?format=download&session=" + sessionID + "&path=clip.mp4"
	req := httptest.NewRequest(http.MethodHead, u, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD carried %d bytes", rec.Body.Len())
	}
}

func TestDocDownload_RefusesAPathOutsideTheSession(t *testing.T) {
	web, _, sessionID := docServeTestServer(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := docDownloadRequest(t, web, sessionID, "../../../secret.txt")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if bytes.Contains(resp.Body.Bytes(), []byte("secret")) {
		t.Fatalf("the refused body carried the file's contents")
	}
}

// The host-to-host read carries only the raw head, so a download of a remote
// session is refused rather than served short.
func TestDocDownload_RefusesAHostQualifiedSession(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	resp := docDownloadRequest(t, web, "studio:02wMz5Txv1C3Hut0M8GCeB", "onboarding.mp4")
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.Code)
	}
	if disposition := resp.Header().Get("Content-Disposition"); disposition != "" {
		t.Fatalf("the refusal was shaped as a file download (%q)", disposition)
	}
}

func TestDocDownload_MissingFileIs404(t *testing.T) {
	web, _, sessionID := docServeTestServer(t)
	resp := docDownloadRequest(t, web, sessionID, "nope.mp4")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
}

// A remote read costs a round trip, so the raw representation's GET-only rule
// answers a HEAD before the owning host is asked.
func TestDocFile_RemoteRawHeadIsRefusedWithoutAskingTheHost(t *testing.T) {
	web, _, _ := docServeTestServer(t)
	u := "/doc/file?format=raw&session=studio:02wMz5Txv1C3Hut0M8GCeB&path=notes.md"
	req := httptest.NewRequest(http.MethodHead, u, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// The two representations do not leak into each other: a raw read of the same
// file still stops at the cap and still reports the truncation.
func TestDocFile_RawStillCapsAfterDownloadWasAdded(t *testing.T) {
	web, cwd, sessionID := docServeTestServer(t)
	body := bytes.Repeat([]byte("x"), docFileMaxBytes+1)
	if err := os.WriteFile(filepath.Join(cwd, "big.mp4"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	resp := docRawRequest(t, web, sessionID, "big.mp4")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if resp.Body.Len() != docFileMaxBytes {
		t.Fatalf("raw body = %d bytes, want the %d-byte cap", resp.Body.Len(), docFileMaxBytes)
	}
	if resp.Header().Get("X-Doc-Truncated") != "true" {
		t.Fatal("raw read of an oversized file did not report truncation")
	}
}
