package hub

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func requestSessionDocument(t *testing.T, srv *httptest.Server, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error) {
	t.Helper()
	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp appwire.SessionDocumentResponse
	err := rpc.Request(context.Background(), appwire.MethodEvenerSessionDocument, params, &resp)
	return resp, err
}

// sessionDocumentServer serves a hub whose one past session works in cwd.
func sessionDocumentServer(t *testing.T, cwd string) *httptest.Server {
	t.Helper()
	past := seedSessionImageSession(t, cwd, sessionImageTestPNG, "image/png")
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
	t.Cleanup(srv.Close)
	return srv
}

// The host-side evener/session/document is the AppWire counterpart of the
// local raw /doc/file read: the same head, total size, revision and
// modification time, from the host's own session folder.
func TestHubSessionDocumentServesTheLocalRead(t *testing.T) {
	cwd := t.TempDir()
	content := []byte("# Plan\n\nStep one.\n")
	modified := time.UnixMilli(1_790_000_000_123)
	writeDocAt(t, filepath.Join(cwd, "plans", "plan.md"), content, modified)
	srv := sessionDocumentServer(t, cwd)

	resp, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{
		SessionID: sessionImageTestSession,
		Path:      "plans/plan.md",
	})
	if err != nil {
		t.Fatalf("evener/session/document: %v", err)
	}
	if !bytes.Equal(resp.Data, content) || resp.TotalSize != int64(len(content)) {
		t.Fatalf("Data/TotalSize = %q/%d, want the whole file", resp.Data, resp.TotalSize)
	}
	if resp.Revision != docRevisionOf(content) || resp.ModifiedAt != modified.UnixMilli() {
		t.Fatalf("Revision/ModifiedAt = %q/%d, want %q/%d", resp.Revision, resp.ModifiedAt, docRevisionOf(content), modified.UnixMilli())
	}
}

// A file over the cap comes back as its head and its true size, as the local
// route serves it.
func TestHubSessionDocumentSendsTheHeadOfALargeFile(t *testing.T) {
	cwd := t.TempDir()
	content := bytes.Repeat([]byte("a"), docFileMaxBytes+100)
	writeDocAt(t, filepath.Join(cwd, "big.log"), content, time.UnixMilli(1_790_000_000_000))
	srv := sessionDocumentServer(t, cwd)

	resp, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "big.log"})
	if err != nil {
		t.Fatalf("evener/session/document: %v", err)
	}
	if len(resp.Data) != docFileMaxBytes || resp.TotalSize != int64(len(content)) || resp.Revision != docRevisionOf(content) {
		t.Fatalf("len(Data)=%d TotalSize=%d Revision=%q, want the %d-byte head of %d and the whole file's revision",
			len(resp.Data), resp.TotalSize, resp.Revision, docFileMaxBytes, len(content))
	}
}

// The method reads only inside the named session's own folder on this hub,
// by the local route's own rule (fspaths.ResolveInRoot): a path that climbs
// out, an absolute path outside the folder, and a symlink that leads out are
// refused as pathOutsideSession; a session this hub does not have, including
// one named on another host, and a missing file are resourceNotFound.
func TestHubSessionDocumentStaysInsideTheSessionFolder(t *testing.T) {
	// The folder's real path: on macOS t.TempDir sits under /var, a symlink to
	// /private/var, and an absolute path is compared with the resolved folder.
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeDocAt(t, filepath.Join(outside, "secret.txt"), []byte("secret"), time.UnixMilli(1_790_000_000_000))
	writeDocAt(t, filepath.Join(cwd, "notes.txt"), []byte("notes"), time.UnixMilli(1_790_000_000_000))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	srv := sessionDocumentServer(t, cwd)

	// An absolute path inside the folder is the same file, as on /doc/file.
	inside, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: filepath.Join(cwd, "notes.txt")})
	if err != nil || string(inside.Data) != "notes" {
		t.Fatalf("absolute path inside the folder = %q, %v; want notes", inside.Data, err)
	}

	for _, tc := range []struct {
		name   string
		params appwire.SessionDocumentParams
		want   appwire.ErrorInfo
	}{
		{"no session", appwire.SessionDocumentParams{Path: "notes.txt"}, appwire.ErrorInvalidParams},
		{"no path", appwire.SessionDocumentParams{SessionID: sessionImageTestSession}, appwire.ErrorInvalidParams},
		{"dot-dot", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "../" + filepath.Base(outside) + "/secret.txt"}, appwire.ErrorPathOutsideSession},
		{"absolute outside", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: filepath.Join(outside, "secret.txt")}, appwire.ErrorPathOutsideSession},
		{"symlink out", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "link.txt"}, appwire.ErrorPathOutsideSession},
		{"unknown session", appwire.SessionDocumentParams{SessionID: "01NOPE", Path: "notes.txt"}, appwire.ErrorResourceNotFound},
		{"another host's session", appwire.SessionDocumentParams{SessionID: "h2:" + sessionImageTestSession, Path: "notes.txt"}, appwire.ErrorResourceNotFound},
		{"missing file", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "gone.txt"}, appwire.ErrorResourceNotFound},
		{"a directory", appwire.SessionDocumentParams{SessionID: sessionImageTestSession, Path: "."}, appwire.ErrorResourceNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := requestSessionDocument(t, srv, tc.params)
			if err == nil {
				t.Fatalf("served %q, want a %s refusal", resp.Data, tc.want)
			}
			if got := sessionImageErrorInfo(t, err); got != string(tc.want) {
				t.Fatalf("refusal = %q (%v), want %q", got, err, tc.want)
			}
		})
	}
}
