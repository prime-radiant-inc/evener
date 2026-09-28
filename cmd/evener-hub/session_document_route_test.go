package hub

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// controllerOverHost serves a controller hub with config cfg and one attached
// host "h1" whose channel is client.
func controllerOverHost(t *testing.T, cfg hubcore.WebConfig, client *appwire.Client) *httptest.Server {
	t.Helper()
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostClientIfAttached(func(host string) (*appwire.Client, bool) {
		return client, host == "h1"
	})
	return controllerOverSource(t, cfg, source)
}

// controllerOverDetachedHost serves a controller hub whose host "h1" is not
// attached. The returned count is how many times its connector dialed, which
// the attached-only proxy routes must leave at zero.
func controllerOverDetachedHost(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	var dials int
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		dials++
		return nil, appwire.SessionUnavailable("the attached-only path must never dial")
	})
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return nil, false })
	return controllerOverSource(t, hubcore.WebConfig{}, source), &dials
}

func controllerOverSource(t *testing.T, cfg hubcore.WebConfig, source *appsource.RemoteHubSource) *httptest.Server {
	t.Helper()
	srv, web := newHubRPCTestServerWithWeb(t, cfg)
	t.Cleanup(srv.Close)
	web.sources.Add(source)
	return srv
}

// controllerOverScriptedHost serves a controller whose host answers every
// evener/session/document with reply: a response, or an appwire.WireError.
func controllerOverScriptedHost(t *testing.T, reply any) (*httptest.Server, func() []appwire.SessionDocumentParams) {
	t.Helper()
	client, calls := newScriptedRemoteHub(t, scriptedRemoteHubReplying(appwire.MethodEvenerSessionDocument, reply))
	seen := func() []appwire.SessionDocumentParams {
		return scriptedRemoteHubParams[appwire.SessionDocumentParams](t, calls(), appwire.MethodEvenerSessionDocument)
	}
	return controllerOverHost(t, hubcore.WebConfig{}, client), seen
}

func getRemoteDoc(t *testing.T, srv *httptest.Server, path string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/doc/file?format=raw&session=h1%3A"+sessionImageTestSession+"&path="+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

// A session on another host reads through the controller's /doc/file exactly as
// a local one does: the real host hub reads its own session folder, and the
// controller answers with the local route's body and headers, revision and
// modification time included, and revalidates with 304.
func TestDocFileRouteReadsARemoteSessionThroughItsHost(t *testing.T) {
	cwd := docTestRoot(t)
	content := []byte("# Plan\n\nStep one.\n")
	modified := time.UnixMilli(1_790_000_000_123)
	writeDocAt(t, filepath.Join(cwd, "plans", "plan.md"), content, modified)
	outside := t.TempDir()
	writeDocAt(t, filepath.Join(outside, "secret.txt"), []byte("secret"), modified)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	host := sessionDocumentServer(t, cwd)
	client := dialHubRPC(t, host)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	controller := controllerOverHost(t, hubcore.WebConfig{}, client)

	resp, body := getRemoteDoc(t, controller, "plans%2Fplan.md", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("status=%d body=%q, want 200 and the host's file", resp.StatusCode, body)
	}
	etag := `"` + docRevisionOf(content) + `"`
	if resp.Header.Get("ETag") != etag || resp.Header.Get("X-Doc-Modified-At") != strconv.FormatInt(modified.UnixMilli(), 10) {
		t.Fatalf("ETag=%q X-Doc-Modified-At=%q, want %s and %d", resp.Header.Get("ETag"), resp.Header.Get("X-Doc-Modified-At"), etag, modified.UnixMilli())
	}
	if resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" || resp.Header.Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("Content-Type=%q Cache-Control=%q, want the local route's", resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}

	revalidated, body := getRemoteDoc(t, controller, "plans%2Fplan.md", http.Header{"If-None-Match": {etag}})
	if revalidated.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("revalidation status=%d body=%q, want 304 and empty", revalidated.StatusCode, body)
	}

	// The host confines the read to the session's folder: a symlink leading
	// out and a path climbing out are 403, as on the local route.
	for _, path := range []string{"link.txt", "..%2F" + filepath.Base(outside) + "%2Fsecret.txt"} {
		if refused, body := getRemoteDoc(t, controller, path, nil); refused.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status=%d body=%q, want 403", path, refused.StatusCode, body)
		}
	}
}

// The request names the session in the host's own namespace, and the path is
// forwarded as it came, for the host to resolve.
func TestDocFileRouteForwardsTheHostsOwnSessionID(t *testing.T) {
	srv, seen := controllerOverScriptedHost(t, appwire.SessionDocumentResponse{Data: []byte("hi"), TotalSize: 2, Revision: docRevisionOf([]byte("hi"))})
	if resp, _ := getRemoteDoc(t, srv, "dir%2Fnotes.txt", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	calls := seen()
	if len(calls) != 1 || calls[0].SessionID != sessionImageTestSession || calls[0].Path != "dir/notes.txt" {
		t.Fatalf("host calls = %+v, want one read of dir/notes.txt in session %s", calls, sessionImageTestSession)
	}
}

// A truncated read keeps the local route's truncation headers, and binary
// content is classified by the controller from the bytes themselves.
func TestDocFileRouteKeepsTruncationAndClassification(t *testing.T) {
	head := bytes.Repeat([]byte("a"), docFileMaxBytes)
	srv, _ := controllerOverScriptedHost(t, appwire.SessionDocumentResponse{Data: head, TotalSize: docFileMaxBytes + 100, Revision: docRevisionOf([]byte("the whole file"))})
	resp, body := getRemoteDoc(t, srv, "big.log", nil)
	if resp.StatusCode != http.StatusOK || len(body) != docFileMaxBytes {
		t.Fatalf("status=%d len=%d, want 200 and the head", resp.StatusCode, len(body))
	}
	if resp.Header.Get("X-Doc-Truncated") != "true" || resp.Header.Get("X-Doc-Total-Size") != strconv.Itoa(docFileMaxBytes+100) {
		t.Fatalf("truncation headers %q/%q", resp.Header.Get("X-Doc-Truncated"), resp.Header.Get("X-Doc-Total-Size"))
	}

	blob := []byte{0x00, 0x01, 0x02}
	srv, _ = controllerOverScriptedHost(t, appwire.SessionDocumentResponse{Data: blob, TotalSize: 3, Revision: docRevisionOf(blob)})
	if resp, _ := getRemoteDoc(t, srv, "blob.bin", nil); resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("Content-Type=%q, want application/octet-stream", resp.Header.Get("Content-Type"))
	}
}

// The host's refusals keep their meaning at the browser, and nothing falls
// back to a local read. A host built before S7 has no such method: 501, the
// phone's cue to keep its "Open it on the host" notice.
func TestDocFileRouteMapsHostRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply appwire.WireError
		want  int
	}{
		{"outside the session", appwire.PathOutsideSession("path must resolve inside the session's working directory"), http.StatusForbidden},
		{"not found", appwire.ResourceNotFound("document not found"), http.StatusNotFound},
		{"invalid params", appwire.InvalidParams("sessionId and path are required"), http.StatusBadRequest},
		{"host predates S7", appwire.WireError{Code: appwire.CodeMethodNotFound, Message: "method not found"}, http.StatusNotImplemented},
		{"host unavailable", appwire.SessionUnavailable("host detached"), http.StatusServiceUnavailable},
		{"host internal error", appwire.WireError{Code: appwire.CodeInternalError, Message: "boom"}, http.StatusServiceUnavailable},
		{"host conflict", appwire.WireError{Code: appwire.CodeConflict, Message: "busy"}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := controllerOverScriptedHost(t, tc.reply)
			if resp, body := getRemoteDoc(t, srv, "notes.txt", nil); resp.StatusCode != tc.want {
				t.Fatalf("status=%d body=%q, want %d", resp.StatusCode, body, tc.want)
			}
		})
	}
}

// A host is bound to answer with at most the cap, a size that covers the bytes,
// a whole head when it truncates, a revision the bytes carry when they are the
// whole file, and a revision exactly when the file is small enough to hash. Anything else is refused as a bad gateway, not served.
func TestDocFileRouteRefusesAMalformedHostAnswer(t *testing.T) {
	hi := []byte("hi")
	for _, tc := range []struct {
		name  string
		reply appwire.SessionDocumentResponse
	}{
		{"over the cap", appwire.SessionDocumentResponse{Data: bytes.Repeat([]byte("a"), docFileMaxBytes+1), TotalSize: docFileMaxBytes + 1}},
		{"size smaller than the bytes", appwire.SessionDocumentResponse{Data: hi, TotalSize: 1}},
		{"a short head on a truncated read", appwire.SessionDocumentResponse{Data: hi, TotalSize: 10}},
		{"a revision that is not a sha256", appwire.SessionDocumentResponse{Data: hi, TotalSize: 2, Revision: "abc"}},
		{"a revision the whole file does not carry", appwire.SessionDocumentResponse{Data: hi, TotalSize: 2, Revision: docRevisionOf([]byte("ho"))}},
		{"no revision on a file small enough to hash", appwire.SessionDocumentResponse{Data: hi, TotalSize: 2}},
		{"a revision on a file too large to hash", appwire.SessionDocumentResponse{Data: bytes.Repeat([]byte("a"), docFileMaxBytes), TotalSize: docRevisionMaxBytes + 1, Revision: docRevisionOf(hi)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := controllerOverScriptedHost(t, tc.reply)
			if resp, body := getRemoteDoc(t, srv, "notes.txt", nil); resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status=%d (%d-byte body), want 502", resp.StatusCode, len(body))
			}
		})
	}
}

// A host-qualified request still needs format=raw, checked before the host is
// asked, and a host that is not attached is refused without a dial.
func TestDocFileRouteRefusesBeforeAskingTheHost(t *testing.T) {
	srv, seen := controllerOverScriptedHost(t, appwire.SessionDocumentResponse{})
	resp, err := srv.Client().Get(srv.URL + "/doc/file?session=h1%3At1&path=notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || len(seen()) != 0 {
		t.Fatalf("no format: status=%d host calls=%d, want 400 and none", resp.StatusCode, len(seen()))
	}

	detached, dials := controllerOverDetachedHost(t)
	if resp, _ := getRemoteDoc(t, detached, "notes.txt", nil); resp.StatusCode != http.StatusServiceUnavailable || *dials != 0 {
		t.Fatalf("detached host: status=%d dials=%d, want 503 and no dial", resp.StatusCode, *dials)
	}
}
