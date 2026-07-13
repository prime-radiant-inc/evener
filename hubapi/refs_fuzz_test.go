package hubapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fuzzRoundTripper func(*http.Request) (*http.Response, error)

func (fn fuzzRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

// FuzzParseRef drives the real hubapi.ParseRef seam over arbitrary strings. The
// oracle is floor "no panic" plus a parse→format→parse fixed point: any ref that
// parses cleanly must re-serialize to a non-empty String() that re-parses to an
// identical Ref. This exercises the ref regexp, the host:session split, and the
// path-traversal guard against their inverse (String).
func FuzzParseRef(f *testing.F) {
	f.Add("local:01ABCDEF")
	f.Add("host-1:session.id_2~3")
	f.Add("local:")
	f.Add(":session")
	f.Add("local:..")
	f.Add("local:a..b")
	f.Add("no-colon")
	f.Add("")
	f.Add("a:b:c")
	f.Add("local:a/b")

	f.Fuzz(func(t *testing.T, raw string) {
		coverClient(t)

		ref, err := ParseRef(raw)
		if err != nil {
			return // rejected input
		}

		formatted := ref.String()
		if formatted == "" {
			t.Fatalf("ParseRef accepted %q but String() returned empty (host=%q session=%q)",
				raw, ref.HostID, ref.SessionID)
		}

		reparsed, err := ParseRef(formatted)
		if err != nil {
			t.Fatalf("re-parsing formatted ref failed: %v\n raw=%q\n formatted=%q", err, raw, formatted)
		}
		if reparsed != ref {
			t.Fatalf("ref not stable through String():\n raw=%q\n once=%#v\n twice=%#v", raw, ref, reparsed)
		}
	})
}

func coverClient(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	ref := LocalRef("seed")
	if ref.String() != "local:seed" || (Ref{}).String() != "" || (Ref{HostID: "h"}).String() != "" {
		t.Fatal("ref formatting contract changed")
	}
	for _, state := range []string{"errored", "awaiting", "active", "warning", "idle", "ended", "closed", "notLoaded", "unknown"} {
		_ = AttentionRank(state)
		_ = RollupRank(state)
		_ = StateWord(state, false)
		_ = StateWord(state, true)
		_ = NeedsYouBand(state, false)
		_ = NeedsYouBand(state, true)
	}

	if _, err := NewClient("http://%", nil); err == nil {
		t.Fatal("malformed URL accepted")
	}
	if _, err := NewClient("/relative", nil); err == nil {
		t.Fatal("relative URL accepted")
	}
	client, err := NewClient("seed.invalid/base/", &http.Client{Transport: fuzzRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch req.URL.Path {
		case "/base/api/models", "/base/api/sessions/local:seed/tasks":
			body = `[]`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_ = client.URL("plain")
	_ = client.URL("query?a=b")
	_, _ = client.Health(ctx)
	_, _ = client.Tree(ctx)
	_, _ = client.Session(ctx, ref)
	_, _ = client.SpawnSchema(ctx)
	_, _ = client.Spawn(ctx, SpawnRequest{})
	_, _ = client.Models(ctx)
	_ = client.Send(ctx, ref, "hello")
	_, _ = client.Tasks(ctx, ref)
	_ = client.Interrupt(ctx, ref)
	_ = client.Compact(ctx, ref)
	_, _ = client.Clear(ctx, ref)
	_, _ = client.Fork(ctx, ref, ForkRequest{})
	_ = client.SetModel(ctx, ref, "model")

	defaultClient, err := NewClient("seed.invalid", nil)
	if err != nil || defaultClient.httpClient != http.DefaultClient {
		t.Fatal("nil HTTP client did not select default")
	}
	transportErr := errors.New("transport")
	failing := &Client{baseURL: client.baseURL, httpClient: &http.Client{Transport: fuzzRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}}
	if err := failing.get(ctx, "/get", &struct{}{}); !errors.Is(err, transportErr) {
		t.Fatalf("get transport error = %v", err)
	}
	if err := failing.post(ctx, "/post", nil, nil); !errors.Is(err, transportErr) {
		t.Fatalf("post transport error = %v", err)
	}

	badStatus := &Client{baseURL: client.baseURL, httpClient: &http.Client{Transport: fuzzRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTeapot, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}}
	if err := badStatus.get(ctx, "/get", &struct{}{}); err == nil {
		t.Fatal("get accepted error status")
	}
	if err := badStatus.post(ctx, "/post", nil, nil); err == nil {
		t.Fatal("post accepted error status")
	}

	badJSON := &Client{baseURL: client.baseURL, httpClient: &http.Client{Transport: fuzzRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{")), Header: make(http.Header)}, nil
	})}}
	_ = badJSON.get(ctx, "/get", &struct{}{})
	_ = badJSON.post(ctx, "/post", nil, &struct{}{})
	if err := client.post(ctx, "/post", make(chan int), nil); err == nil {
		t.Fatal("post marshaled unsupported body")
	}
	if err := client.get(nil, "/get", &struct{}{}); err == nil { //nolint:staticcheck // Verifies nil contexts are rejected before an HTTP request is built.
		t.Fatal("get accepted a nil context")
	}
	if err := client.post(nil, "/post", nil, nil); err == nil { //nolint:staticcheck // Verifies nil contexts are rejected before an HTTP request is built.
		t.Fatal("post accepted a nil context")
	}
}
