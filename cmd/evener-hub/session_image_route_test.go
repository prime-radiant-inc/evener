package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const sessionImageRouteSha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// newScriptedImageHost wires an initialized AppWire client to an in-memory
// server that answers evener/session/image with either bytes or a typed wire
// error and records every request's params. No SSH, no network, no host.
func newScriptedImageHost(
	t *testing.T,
	handle func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError),
) (*appwire.Client, func() []appwire.SessionImageParams) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	var mu sync.Mutex
	var seen []appwire.SessionImageParams
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			if msg.Request.Method == appwire.MethodInitialize {
				data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
				if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
					return
				}
				continue
			}
			if msg.Request.Method != appwire.MethodEvenerSessionImage {
				return
			}
			var params appwire.SessionImageParams
			if err := json.Unmarshal(msg.Request.Params, &params); err != nil {
				return
			}
			mu.Lock()
			seen = append(seen, params)
			mu.Unlock()

			resp, wireErr := handle(params)
			if wireErr != nil {
				if err := server.Send(ctx, appwire.ErrorMessage(msg.Request.ID, *wireErr)); err != nil {
					return
				}
				continue
			}
			data, err := json.Marshal(resp)
			if err != nil {
				return
			}
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		cancel()
		t.Fatalf("initialize scripted host: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})
	recorded := func() []appwire.SessionImageParams {
		mu.Lock()
		defer mu.Unlock()
		return append([]appwire.SessionImageParams(nil), seen...)
	}
	return client, recorded
}

// newRemoteSessionImageServer serves a hub with one attached host source whose
// client answers evener/session/image through handle.
func newRemoteSessionImageServer(
	t *testing.T,
	cfg hubcore.WebConfig,
	handle func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError),
) (*httptest.Server, *WebServer, func() []appwire.SessionImageParams) {
	t.Helper()
	client, seen := newScriptedImageHost(t, handle)
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostClientIfAttached(func(host string) (*appwire.Client, bool) {
		return client, host == "h1"
	})
	srv, web := newHubRPCTestServerWithWeb(t, cfg)
	t.Cleanup(srv.Close)
	web.sources.Add(source)
	return srv, web, seen
}

func getSessionImageRoute(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// A session that lives on another host renders through the same image routes as
// a local one, but the controller cannot read another machine's filesystem. The
// host-qualified route id must therefore serve the bytes from the owning host's
// client — one evener/session/image call — with the same cache headers the
// local routes answer with, and the local path must stay untouched.
func TestSessionImageRouteProxiesRemoteSessionImageThroughHostClient(t *testing.T) {
	png := sessionImageTestPNG
	srv, _, seen := newRemoteSessionImageServer(t, hubcore.WebConfig{},
		func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
			return appwire.SessionImageResponse{
				MediaType: "image/png",
				Size:      int64(len(png)),
				SHA:       sessionImageRouteSha,
				Data:      png,
			}, nil
		})

	resp := getSessionImageRoute(t, srv, "/s/h1:t1/images/"+sessionImageRouteSha)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remote sha route = status %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, png) {
		t.Fatalf("remote sha route body = %q, want the host's bytes", body)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=86400, immutable" {
		t.Fatalf("Cache-Control = %q, want the sha-addressed immutable policy", got)
	}
	if got := resp.Header.Get("ETag"); got != `"`+sessionImageRouteSha+`"` {
		t.Fatalf("ETag = %q, want the requested sha", got)
	}
	calls := seen()
	if len(calls) != 1 {
		t.Fatalf("host calls = %+v, want exactly one evener/session/image", calls)
	}
	if calls[0].SessionID != "t1" || calls[0].SHA != sessionImageRouteSha || calls[0].Path != "" {
		t.Fatalf("host params = %+v, want the remote session id with the sha selector", calls[0])
	}
}

// The file-backed /doc/image form travels the same way: the session-relative
// path is forwarded verbatim, and the response's own sha becomes the ETag.
func TestSessionImageRouteProxiesRemoteDocImageThroughHostClient(t *testing.T) {
	png := sessionImageTestPNG
	srv, _, seen := newRemoteSessionImageServer(t, hubcore.WebConfig{},
		func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
			return appwire.SessionImageResponse{
				MediaType: "image/png",
				Size:      int64(len(png)),
				SHA:       sessionImageRouteSha,
				Data:      png,
			}, nil
		})

	resp := getSessionImageRoute(t, srv, "/doc/image?session=h1%3At1&path=shot.png")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remote doc route = status %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, png) {
		t.Fatalf("remote doc route body = %q, want the host's bytes", body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "private, max-age=60" {
		t.Fatalf("Cache-Control = %q, want the file-backed private policy", got)
	}
	if got := resp.Header.Get("ETag"); got != `"`+sessionImageRouteSha+`"` {
		t.Fatalf("ETag = %q, want the response's own sha", got)
	}
	calls := seen()
	if len(calls) != 1 {
		t.Fatalf("host calls = %+v, want exactly one evener/session/image", calls)
	}
	if calls[0].SessionID != "t1" || calls[0].Path != "shot.png" || calls[0].SHA != "" {
		t.Fatalf("host params = %+v, want the remote session id with the path selector", calls[0])
	}
}

// The local route is byte-for-byte unchanged: a local session resolves against
// this hub's own past index and never consults a host client.
func TestSessionImageRouteKeepsLocalSessionsLocal(t *testing.T) {
	past := seedSessionImageSession(t, "", sessionImageTestPNG, "image/png")
	srv, _, seen := newRemoteSessionImageServer(t, hubcore.WebConfig{Past: past},
		func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
			return appwire.SessionImageResponse{}, &appwire.WireError{Code: appwire.CodeInternalError, Message: "the host must not be called for a local session"}
		})

	resp := getSessionImageRoute(t, srv, "/s/"+sessionImageTestSession+"/images/"+imageSha(sessionImageTestPNG))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local sha route = status %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, sessionImageTestPNG) {
		t.Fatalf("local sha route body = %q, want the local transcript's bytes", body)
	}
	if calls := seen(); len(calls) != 0 {
		t.Fatalf("host calls = %+v, want none for a local session", calls)
	}
}

// A remote route is refused typed when the host is not attached, and it never
// falls back to a local read — the session id names a different machine.
func TestSessionImageRouteRefusesUnattachedHost(t *testing.T) {
	var dials int
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		dials++
		return nil, appwire.SessionUnavailable("the attached-only path must never dial")
	})
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return nil, false })
	srv, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
	t.Cleanup(srv.Close)
	web.sources.Add(source)

	resp := getSessionImageRoute(t, srv, "/s/h1:t1/images/"+sessionImageRouteSha)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unattached host = status %d, want 503", resp.StatusCode)
	}
	if dials != 0 {
		t.Fatalf("the dialing connector ran %d times, want 0", dials)
	}
}

// The host's own refusals keep their meaning at the browser: a malformed or
// escaping request is a 400 and an unresolvable one is a 404; neither ever
// falls back to a local read.
func TestSessionImageRouteMapsHostRefusals(t *testing.T) {
	t.Run("invalid params", func(t *testing.T) {
		srv, _, _ := newRemoteSessionImageServer(t, hubcore.WebConfig{},
			func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
				wire := appwire.InvalidParams("path escapes the session root")
				return appwire.SessionImageResponse{}, &wire
			})
		resp := getSessionImageRoute(t, srv, "/doc/image?session=h1%3At1&path=../etc/passwd")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("host invalid params = status %d, want 400", resp.StatusCode)
		}
	})

	t.Run("resource not found", func(t *testing.T) {
		srv, _, _ := newRemoteSessionImageServer(t, hubcore.WebConfig{},
			func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
				wire := appwire.ResourceNotFound("no such image")
				return appwire.SessionImageResponse{}, &wire
			})
		resp := getSessionImageRoute(t, srv, "/s/h1:t1/images/"+sessionImageRouteSha)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("host resource not found = status %d, want 404", resp.StatusCode)
		}
	})

	t.Run("unknown source", func(t *testing.T) {
		srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{})
		t.Cleanup(srv.Close)
		resp := getSessionImageRoute(t, srv, "/s/nope:t1/images/"+sessionImageRouteSha)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("unknown source = status %d, want 503", resp.StatusCode)
		}
	})

	t.Run("malformed sha stays 400", func(t *testing.T) {
		srv, _, _ := newRemoteSessionImageServer(t, hubcore.WebConfig{},
			func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
				return appwire.SessionImageResponse{}, nil
			})
		resp := getSessionImageRoute(t, srv, "/s/h1:t1/images/nothex")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("malformed sha = status %d, want 400", resp.StatusCode)
		}
	})

	t.Run("missing doc path stays 404", func(t *testing.T) {
		srv, _, _ := newRemoteSessionImageServer(t, hubcore.WebConfig{},
			func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
				return appwire.SessionImageResponse{}, nil
			})
		resp := getSessionImageRoute(t, srv, "/doc/image?session=h1%3At1")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("missing doc path = status %d, want 404", resp.StatusCode)
		}
	})

	t.Run("non-GET is 405", func(t *testing.T) {
		srv, _, _ := newRemoteSessionImageServer(t, hubcore.WebConfig{},
			func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
				return appwire.SessionImageResponse{}, nil
			})
		for _, path := range []string{"/s/h1:t1/images/" + sessionImageRouteSha, "/doc/image?session=h1%3At1&path=shot.png"} {
			req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(""))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("POST %s: %v", path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s = status %d, want 405", path, resp.StatusCode)
			}
		}
	})

	// A host is contract-bound to serve image bytes under the 8 MiB bound with
	// the media type those bytes carry. A violation must be refused, not
	// streamed: the proxy must not serve a size or a content type the local
	// routes never do.
	for _, tt := range []struct {
		name string
		resp appwire.SessionImageResponse
	}{
		{
			name: "over-bound bytes",
			resp: appwire.SessionImageResponse{
				MediaType: "image/png",
				Size:      outputImageMaxBytes + 1,
				SHA:       sessionImageRouteSha,
				Data:      bytes.Repeat([]byte{'x'}, outputImageMaxBytes+1),
			},
		},
		{
			name: "media type the bytes do not carry",
			resp: appwire.SessionImageResponse{
				MediaType: "image/png",
				Size:      int64(len("not an image")),
				SHA:       sessionImageRouteSha,
				Data:      []byte("not an image"),
			},
		},
		{
			name: "content type the local routes never serve",
			resp: appwire.SessionImageResponse{
				MediaType: "text/html",
				Size:      int64(len("<html></html>")),
				SHA:       sessionImageRouteSha,
				Data:      []byte("<html></html>"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newRemoteSessionImageServer(t, hubcore.WebConfig{},
				func(appwire.SessionImageParams) (appwire.SessionImageResponse, *appwire.WireError) {
					return tt.resp, nil
				})
			resp := getSessionImageRoute(t, srv, "/doc/image?session=h1%3At1&path=shot.png")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("unservable host answer = status %d, want 404", resp.StatusCode)
			}
		})
	}
}
