package hub

// Tests for /doc/download, the whole-file read a native client saves or hands
// to another app. Unlike /doc/file it has no 512 KiB pane cap, so the body is
// the file byte for byte. It shares /doc/file's containment rules: a path that
// escapes the session cwd is refused, and a host-qualified session id is never
// read locally.

import (
	"bytes"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func docDownloadRequest(t *testing.T, web *WebServer, session, path string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/doc/download?session=" + session + "&path=" + path
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
	if resp.Header().Get("X-Doc-Truncated") != "" {
		t.Fatalf("download reported truncation; the body was whole")
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
	if strings.Contains(resp.Body.String(), "secret") {
		t.Fatalf("the refused body carried the file's contents")
	}
}

func TestDocDownload_RefusesAHostQualifiedSessionWithoutReadingLocally(t *testing.T) {
	web, cwd, _ := docServeTestServer(t)
	if err := os.WriteFile(filepath.Join(cwd, "onboarding.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := docDownloadRequest(t, web, "studio:02wMz5Txv1C3Hut0M8GCeB", "onboarding.mp4")
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.Code)
	}
}

func TestDocDownload_MissingFileIs404(t *testing.T) {
	web, _, sessionID := docServeTestServer(t)
	resp := docDownloadRequest(t, web, sessionID, "nope.mp4")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
}
