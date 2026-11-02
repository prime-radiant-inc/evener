package hub

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type sessionFileOutcome string

const (
	sessionFileServed   sessionFileOutcome = "served"
	sessionFileEscape   sessionFileOutcome = "escape"
	sessionFileNotFound sessionFileOutcome = "not found"
)

func outcomeOfStatus(t *testing.T, code int) sessionFileOutcome {
	t.Helper()
	switch code {
	case http.StatusOK:
		return sessionFileServed
	case http.StatusForbidden:
		return sessionFileEscape
	case http.StatusNotFound:
		return sessionFileNotFound
	}
	t.Fatalf("unexpected status %d", code)
	return ""
}

func outcomeOfWire(t *testing.T, served bool, err error) sessionFileOutcome {
	t.Helper()
	if err == nil {
		if !served {
			t.Fatal("no data and no error")
		}
		return sessionFileServed
	}
	switch got := sessionImageErrorInfo(t, err); got {
	case string(appwire.ErrorPathOutsideSession), string(appwire.ErrorInvalidParams):
		return sessionFileEscape
	case string(appwire.ErrorResourceNotFound):
		return sessionFileNotFound
	default:
		t.Fatalf("unexpected wire refusal %q", got)
	}
	return ""
}

// The doc and image routes, local and remote, resolve a request path by one
// rule: the session's cwd first, then (for an absolute path only) the retained
// artifacts root, with a miss inside the artifacts root answering not-found
// rather than the cwd's escape refusal.
func TestSessionFileRoutesResolveByOneRule(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	artifacts := artifactsDirOf(t, web, session)
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for _, p := range []string{
		filepath.Join(cwd, "in-cwd.png"),
		filepath.Join(artifacts, "in-artifacts.png"),
		filepath.Join(outside, "outside.png"),
	} {
		if err := os.WriteFile(p, sessionImageTestPNG, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: web.cfg.Past})
	t.Cleanup(srv.Close)

	routes := map[string]func(path string) sessionFileOutcome{
		"local doc file": func(path string) sessionFileOutcome {
			return outcomeOfStatus(t, docRawRequest(t, web, session, path).Code)
		},
		"local doc image": func(path string) sessionFileOutcome {
			return outcomeOfStatus(t, docImageRequest(t, web, session, path).Code)
		},
		"remote document": func(path string) sessionFileOutcome {
			resp, err := requestSessionDocument(t, srv, appwire.SessionDocumentParams{SessionID: session, Path: path})
			return outcomeOfWire(t, len(resp.Data) > 0, err)
		},
		"remote image": func(path string) sessionFileOutcome {
			resp, err := requestSessionImage(t, srv, appwire.SessionImageParams{SessionID: session, Path: path})
			return outcomeOfWire(t, len(resp.Data) > 0, err)
		},
	}
	cases := []struct {
		name string
		path string
		want sessionFileOutcome
	}{
		{"relative cwd hit", "in-cwd.png", sessionFileServed},
		{"absolute cwd hit", filepath.Join(cwd, "in-cwd.png"), sessionFileServed},
		{"absolute artifacts hit", filepath.Join(artifacts, "in-artifacts.png"), sessionFileServed},
		{"relative path never reaches artifacts", "in-artifacts.png", sessionFileNotFound},
		{"absolute artifacts contained miss", filepath.Join(artifacts, "gone.png"), sessionFileNotFound},
		{"absolute cwd miss", filepath.Join(cwd, "gone.png"), sessionFileNotFound},
		{"absolute outside both roots", filepath.Join(outside, "outside.png"), sessionFileEscape},
		{"relative escape", "../" + filepath.Base(outside) + "/outside.png", sessionFileEscape},
	}
	for route, run := range routes {
		for _, tc := range cases {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				if got := run(tc.path); got != tc.want {
					t.Fatalf("outcome = %q, want %q", got, tc.want)
				}
			})
		}
	}
}
