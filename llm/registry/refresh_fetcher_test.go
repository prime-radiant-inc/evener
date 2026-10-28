package registry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func httpResponse(status int, body io.Reader, etag string) *http.Response {
	h := http.Header{}
	if etag != "" {
		h.Set("ETag", etag)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(body), Header: h}
}

// HTTPFetcher is the one network-coupled seam in the refresh; a fake
// RoundTripper drives every arm without touching the wire: the 200 body and
// ETag, the If-None-Match round trip, a non-200 refusal, a transport
// failure, and a body that dies mid-read.
func TestHTTPFetcherArms(t *testing.T) {
	t.Parallel()
	fetch := func(rt roundTripFunc) Fetcher {
		return HTTPFetcher(&http.Client{Transport: rt})
	}

	t.Run("200 carries body and etag", func(t *testing.T) {
		t.Parallel()
		f := fetch(func(*http.Request) (*http.Response, error) {
			return httpResponse(http.StatusOK, strings.NewReader("body"), `W/"v1"`), nil
		})
		body, etag, notModified, err := f(context.Background(), "")
		if err != nil || notModified || string(body) != "body" || etag != `W/"v1"` {
			t.Fatalf("200: body=%q etag=%q notModified=%v err=%v", body, etag, notModified, err)
		}
	})
	t.Run("If-None-Match is sent and 304 round-trips the etag", func(t *testing.T) {
		t.Parallel()
		var gotEtag string
		f := fetch(func(req *http.Request) (*http.Response, error) {
			gotEtag = req.Header.Get("If-None-Match")
			return httpResponse(http.StatusNotModified, strings.NewReader(""), ""), nil
		})
		body, etag, notModified, err := f(context.Background(), `W/"v1"`)
		if err != nil || !notModified || body != nil || etag != `W/"v1"` {
			t.Fatalf("304: body=%q etag=%q notModified=%v err=%v", body, etag, notModified, err)
		}
		if gotEtag != `W/"v1"` {
			t.Fatalf("If-None-Match = %q, want the stored etag", gotEtag)
		}
	})
	t.Run("non-200 is an error", func(t *testing.T) {
		t.Parallel()
		f := fetch(func(*http.Request) (*http.Response, error) {
			return httpResponse(http.StatusForbidden, strings.NewReader(""), ""), nil
		})
		if _, _, _, err := f(context.Background(), ""); err == nil {
			t.Fatal("a 403 must be an error, not a body")
		}
	})
	t.Run("transport failure propagates", func(t *testing.T) {
		t.Parallel()
		f := fetch(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})
		if _, _, _, err := f(context.Background(), ""); err == nil {
			t.Fatal("a transport error must propagate")
		}
	})
	t.Run("body read failure propagates", func(t *testing.T) {
		t.Parallel()
		f := fetch(func(*http.Request) (*http.Response, error) {
			return httpResponse(http.StatusOK, errReader{}, ""), nil
		})
		if _, _, _, err := f(context.Background(), ""); err == nil {
			t.Fatal("a body that fails mid-read must propagate the error")
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("short read") }

func TestRefreshRequiresFetcherAndStateRoot(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state}); err == nil {
		t.Fatal("a refresh without a fetcher must be refused")
	}
	if _, err := Refresh(context.Background(), RefreshOptions{Fetcher: func(context.Context, string) ([]byte, string, bool, error) {
		return nil, "", false, nil
	}}); err == nil {
		t.Fatal("a refresh without a StateRoot must be refused")
	}
}

// With no Baseline the embedded snapshot is the sanity floor: a body equal
// to it refreshes cleanly.
func TestRefreshEmbeddedBaseline(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	embedded, _, err := EmbeddedSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFetch{body: embedded, etag: `W/"emb"`}
	res, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Force: true})
	if err != nil || !res.Updated {
		t.Fatalf("embedded-baseline refresh: %+v %v", res, err)
	}
	if res.ProvidersBefore == 0 || res.ProvidersAfter != res.ProvidersBefore {
		t.Fatalf("baseline counts: %+v", res)
	}
}

func TestRefreshBaselineMustConvert(t *testing.T) {
	t.Parallel()
	f := &fakeFetch{body: fixtureBytes(t), etag: "a"}
	_, err := Refresh(context.Background(), RefreshOptions{
		StateRoot: t.TempDir(), Fetcher: f.fetcher(), Force: true, Baseline: []byte("{"),
	})
	if err == nil {
		t.Fatal("a baseline that does not convert must fail the refresh")
	}
}

// Note on the "overlay does not load on the new snapshot" refusal in
// Refresh (refresh.go:184): the only error arms of the internal Load that
// the fetched body can reach are (1) a curated overlay base whose provider
// id is in neither the body nor the overlay, and (2) a row transport preset
// the overlay does not define. The embedded overlay's only base references
// are "openai" and "google", both of which carry their own overlay stanzas,
// and the converter only names the vertex-anthropic, vertex-gemini and
// bedrock-mantle-openai presets, all defined in the embedded overlay — so
// a body that passes countCatalog always loads, and that refusal arm is
// unreachable from a models.dev-shaped document.

// readCache needs both halves of the cache and a parseable meta document.
func TestReadCacheHalvesMustExistAndParse(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	jsonPath, metaPath := cachePaths(state)
	if _, _, ok := readCache(state); ok {
		t.Fatal("an empty state root must not read as cached")
	}
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readCache(state); ok {
		t.Fatal("a cache without its meta must not read as cached")
	}
	if err := os.WriteFile(metaPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readCache(state); ok {
		t.Fatal("a cache with an unparseable meta must not read as cached")
	}
	valid := `{"fetched_at":"` + time.Now().UTC().Format(time.RFC3339) + `","etag":"x","source":"y"}`
	if err := os.WriteFile(metaPath, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, meta, ok := readCache(state); !ok || meta.Etag != "x" {
		t.Fatalf("a good cache must read: ok=%v meta=%+v", ok, meta)
	}
}

func TestRefreshWriteFailuresKeepCache(t *testing.T) {
	t.Parallel()

	t.Run("unwritable state root", func(t *testing.T) {
		t.Parallel()
		// Root and Windows write through a 0o500 directory, so the
		// unwritable state root cannot actually refuse the write there.
		if os.Geteuid() == 0 || runtime.GOOS == "windows" {
			t.Skip("needs a directory the process cannot write")
		}
		ro := t.TempDir()
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Skipf("cannot drop write permission: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		f := &fakeFetch{body: fixtureBytes(t), etag: "a"}
		if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: ro, Fetcher: f.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err == nil {
			t.Fatal("a refresh into an unwritable state root must fail")
		}
	})

	t.Run("304 with an unwritable catalog dir", func(t *testing.T) {
		t.Parallel()
		// Root and Windows write through a 0o500 directory, so the
		// unwritable catalog dir cannot actually refuse the write there.
		if os.Geteuid() == 0 || runtime.GOOS == "windows" {
			t.Skip("needs a directory the process cannot write")
		}
		state := t.TempDir()
		f := &fakeFetch{body: fixtureBytes(t), etag: "a"}
		if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err != nil {
			t.Fatal(err)
		}
		_, metaPath := cachePaths(state)
		if err := os.Chmod(filepath.Dir(metaPath), 0o500); err != nil {
			t.Skipf("cannot drop write permission: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(metaPath), 0o700) })
		notModified := &fakeFetch{notModified: true, etag: "a"}
		if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: notModified.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err == nil {
			t.Fatal("a 304 whose meta bump cannot be written must fail")
		}
	})

	t.Run("meta path occupied by a directory", func(t *testing.T) {
		t.Parallel()
		state := t.TempDir()
		jsonPath, metaPath := cachePaths(state)
		if err := os.MkdirAll(metaPath, 0o700); err != nil {
			t.Fatal(err)
		}
		f := &fakeFetch{body: fixtureBytes(t), etag: "a"}
		if _, err := Refresh(context.Background(), RefreshOptions{StateRoot: state, Fetcher: f.fetcher(), Force: true, Baseline: fixtureBytes(t)}); err == nil {
			t.Fatal("a refresh whose meta write fails must report it")
		}
		if _, err := os.Stat(jsonPath); err != nil {
			t.Fatalf("the body write must have succeeded first: %v", err)
		}
	})
}
