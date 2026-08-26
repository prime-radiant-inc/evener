package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newNavigationHTTPTestServer(t *testing.T) *WebServer {
	t.Helper()
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	return &WebServer{navigation: newTestNavigationService(t, source)}
}

func requestNavigation(t *testing.T, web *WebServer, target, encoding, etag string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept-Encoding", encoding)
	req.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()
	web.Handler().ServeHTTP(response, req)
	return response
}

func TestNavigationManifestConditionalGzipHTTP(t *testing.T) {
	web := newNavigationHTTPTestServer(t)
	first := requestNavigation(t, web, "/api/navigation", "gzip", "")
	if first.Code != http.StatusOK || first.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("first response code=%d headers=%v", first.Code, first.Header())
	}
	if got, err := strconv.Atoi(first.Header().Get("Content-Length")); err != nil || got != first.Body.Len() {
		t.Fatalf("gzip response omitted Content-Length: %v", first.Header())
	}
	reader, err := gzip.NewReader(bytes.NewReader(first.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(decoded, []byte(`"generation_id"`)) {
		t.Fatalf("gzip body is not cached navigation JSON: %q", decoded)
	}
	second := requestNavigation(t, web, "/api/navigation", "gzip", first.Header().Get("ETag"))
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional response=%d bytes=%d", second.Code, second.Body.Len())
	}
	for _, header := range []string{"ETag", "Vary", "Cache-Control", "X-Evener-Navigation-Generation", "X-Evener-Navigation-Revision", "Content-Encoding"} {
		if second.Header().Get(header) == "" {
			t.Fatalf("304 omitted %s: %v", header, second.Header())
		}
	}
	if second.Header().Get("Content-Length") != "" || second.Header().Get("Content-Type") != "" {
		t.Fatalf("304 has representation-only headers: %v", second.Header())
	}
}

func TestNavigationRouteParsing(t *testing.T) {
	cases := []struct {
		path  string
		want  navigationResourceKey
		class string
	}{
		{"/api/navigation", navigationResourceKey{Kind: navigationResourceManifest}, "manifest"},
		{"/api/navigation/", navigationResourceKey{Kind: navigationResourceManifest}, "manifest"},
		{"/api/navigation/sections/live", navigationResourceKey{Kind: navigationResourceLive, Limit: 50}, "section"},
		{"/api/navigation/sections/needs-you?offset=3&limit=4", navigationResourceKey{Kind: navigationResourceNeedsYou, Offset: 3, Limit: 4}, "section"},
		{"/api/navigation/pin-sections", navigationResourceKey{Kind: navigationResourcePinCatalog, Limit: 100}, "pin_catalog"},
		{"/api/navigation/pin-sections/section", navigationResourceKey{Kind: navigationResourcePinSection, SectionID: "section", Limit: 50}, "pin_section"},
		{"/api/navigation/catalogs/projects", navigationResourceKey{Kind: navigationResourceProjects, Limit: 100}, "catalog"},
		{"/api/navigation/catalogs/archived-projects", navigationResourceKey{Kind: navigationResourceArchivedProjects, Limit: 100}, "catalog"},
		{"/api/navigation/catalogs/test-runs", navigationResourceKey{Kind: navigationResourceTestRuns, Limit: 100}, "catalog"},
		{"/api/navigation/projects/p1", navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}, "project"},
		{"/api/navigation/projects/p1?tier=recent&offset=0&limit=50", navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "recent", Limit: 50}, "project_page"},
		{"/api/navigation/sessions/local:01ARZ3NDEKTSV4RRFFQ69G5FAV", navigationResourceKey{Kind: navigationResourceLocation, ID: "local:01ARZ3NDEKTSV4RRFFQ69G5FAV"}, "location"},
	}
	for _, test := range cases {
		t.Run(test.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			got, class, err := parseNavigationRequest(req)
			if err != nil || class != test.class || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("key=%+v class=%q err=%v, want %+v %q", got, class, err, test.want, test.class)
			}
		})
	}
}

func TestNavigationCanonicalPathAndValidationHTTP(t *testing.T) {
	web := newNavigationHTTPTestServer(t)
	for _, test := range []struct {
		target string
		status int
	}{
		{"/api/navigation/projects/p1/", http.StatusNotFound},
		{"/api/navigation/projects/p1?limit=0", http.StatusBadRequest},
		{"/api/navigation/projects/p1?offset=1", http.StatusBadRequest},
		{"/api/navigation/projects/p1?tier=future", http.StatusBadRequest},
		{"/api/navigation/projects/p1?wat=1", http.StatusBadRequest},
		{"/api/navigation/projects/p1?limit=01&limit=2", http.StatusBadRequest},
		{"/api/navigation/projects/%70%31", http.StatusBadRequest},
	} {
		response := requestNavigation(t, web, test.target, "", "")
		if response.Code != test.status {
			t.Fatalf("%s status=%d want=%d body=%q", test.target, response.Code, test.status, response.Body.String())
		}
	}
	malformed := httptest.NewRequest(http.MethodGet, "/api/navigation/projects/p1", nil)
	malformed.URL.RawPath = "/api/navigation/projects/%zz"
	response := httptest.NewRecorder()
	web.Handler().ServeHTTP(response, malformed)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed raw path status=%d", response.Code)
	}

	// This request traverses the real Handler raw-path guard. The percent form
	// decodes once to a literal %2F and cannot become a route separator.
	req := httptest.NewRequest(http.MethodGet, "/api/navigation/projects/%252F", nil)
	key, _, err := parseNavigationRequest(req)
	if err != nil || key.ProjectKey != "%2F" {
		t.Fatalf("double encoded key=%+v err=%v", key, err)
	}
	if response := httptest.NewRecorder(); response != nil {
		web.Handler().ServeHTTP(response, req)
		if response.Code != http.StatusNotFound {
			t.Fatalf("double encoded real handler status=%d", response.Code)
		}
	}
}

func TestNavigationPathAndPaginationBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		wantOK bool
	}{
		{"encoded slash", "/api/navigation/projects/" + url.PathEscape("a/b"), true},
		{"invalid utf8", "/api/navigation/projects/%FF", false},
		{"identity bound", "/api/navigation/projects/" + strings.Repeat("a", maxNavigationIdentityBytes+1), false},
		{"uint32 max", "/api/navigation/sections/live?offset=4294967295&limit=50", true},
		{"uint32 overflow", "/api/navigation/sections/live?offset=4294967296", false},
		{"section limit", "/api/navigation/sections/live?limit=51", false},
		{"catalog limit", "/api/navigation/catalogs/projects?limit=101", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			key, _, err := parseNavigationRequest(httptest.NewRequest(http.MethodGet, test.target, nil))
			if (err == nil) != test.wantOK {
				t.Fatalf("target=%q key=%+v err=%v", test.target, key, err)
			}
		})
	}
	web := newNavigationHTTPTestServer(t)
	for _, target := range []string{
		"/api/navigation/pin-sections/absent",
		"/api/navigation/sessions/local:01ARZ3NDEKTSV4RRFFQ69G5FAA",
	} {
		response := requestNavigation(t, web, target, "", "")
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "absent") {
			t.Fatalf("target=%q status=%d body=%q", target, response.Code, response.Body.String())
		}
	}
}

func TestNavigationRepresentationHeaderMatrix(t *testing.T) {
	representation := navigationRepresentation{
		JSON:       []byte(`{"identity":true}`),
		Gzip:       []byte("gzip-bytes"),
		ETag:       `W/"nav-test"`,
		Generation: "00112233445566778899aabbccddeeff",
		Revision:   7,
	}
	for _, test := range []struct {
		name, accept, inm, encoding string
		status                      int
		length                      string
	}{
		{"identity", "", "", "", http.StatusOK, "17"},
		{"gzip", "br, gzip;q=0.5", "", "gzip", http.StatusOK, "10"},
		{"gzip-zero", "gzip;q=0, *;q=1", "", "", http.StatusOK, "17"},
		{"wildcard", "*;q=0.1", "", "gzip", http.StatusOK, "10"},
		{"weak-304", "gzip", `"nav-test"`, "gzip", http.StatusNotModified, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/navigation", nil)
			req.Header.Set("Accept-Encoding", test.accept)
			req.Header.Set("If-None-Match", test.inm)
			response := httptest.NewRecorder()
			status, err := writeNavigationRepresentation(response, req, representation)
			if err != nil || status != test.status || response.Code != test.status {
				t.Fatalf("status=%d response=%d err=%v", status, response.Code, err)
			}
			if got := response.Header().Get("Content-Encoding"); got != test.encoding {
				t.Fatalf("Content-Encoding=%q want %q", got, test.encoding)
			}
			if got := response.Header().Get("Content-Length"); got != test.length {
				t.Fatalf("Content-Length=%q want %q", got, test.length)
			}
			if response.Header().Get("X-Evener-Navigation-Sequence") != "" {
				t.Fatalf("response leaked sequence: %v", response.Header())
			}
			if test.status == http.StatusNotModified && response.Body.Len() != 0 {
				t.Fatalf("304 wrote %d bytes", response.Body.Len())
			}
			if test.status == http.StatusNotModified && response.Header().Get("Content-Type") != "" {
				t.Fatalf("304 Content-Type=%q", response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestNavigationAuthIsolationHTTP(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	web := &WebServer{navigation: newTestNavigationService(t, source)}
	web.cfg.AuthToken = "navigation-test-token"
	request := httptest.NewRequest(http.MethodGet, "/api/navigation", nil)
	response := httptest.NewRecorder()
	web.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	if got := source.captureCount(); got != 0 {
		t.Fatalf("unauthorized request captured navigation source %d times", got)
	}
	request.Header.Set("Authorization", "Bearer navigation-test-token")
	response = httptest.NewRecorder()
	web.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated status=%d", response.Code)
	}
}

func TestNavigationTwoServerAuthIsolationHTTP(t *testing.T) {
	sourceA := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	sourceB := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	webA := &WebServer{navigation: newTestNavigationService(t, sourceA)}
	webB := &WebServer{navigation: newTestNavigationService(t, sourceB)}
	webA.cfg.AuthToken, webB.cfg.AuthToken = "token-a", "token-b"
	request := httptest.NewRequest(http.MethodGet, "/api/navigation", nil)
	request.Header.Set("Authorization", "Bearer token-a")
	response := httptest.NewRecorder()
	webA.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || sourceA.captureCount() != 1 || sourceB.captureCount() != 0 {
		t.Fatalf("server A status=%d captures=(%d,%d)", response.Code, sourceA.captureCount(), sourceB.captureCount())
	}
	response = httptest.NewRecorder()
	webB.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || sourceB.captureCount() != 0 {
		t.Fatalf("server B status=%d captures=%d", response.Code, sourceB.captureCount())
	}
}

func TestNavigationMetricsRedactHTTP(t *testing.T) {
	web := newNavigationHTTPTestServer(t)
	var mu sync.Mutex
	var events []navigationMetricEvent
	web.navigationMetrics = &navigationMetricFunc{fn: func(event navigationMetricEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}}
	response := requestNavigation(t, web, "/api/navigation/projects/secret%252Fopaque", "gzip", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].RouteClass != "project" || events[0].Status != http.StatusNotFound {
		t.Fatalf("events=%+v", events)
	}
	serialized := []byte(strings.Join([]string{events[0].RouteClass, events[0].Encoding, strconv.Itoa(events[0].Status)}, ":"))
	for _, forbidden := range []string{"secret", "opaque", "%252F", "/api/navigation"} {
		if bytes.Contains(serialized, []byte(forbidden)) {
			t.Fatalf("metric leaks %q: %s", forbidden, serialized)
		}
	}
}

func TestNavigationCanonicalEscapeUsesStandardLibrary(t *testing.T) {
	identity := "a+b/%"
	escaped := url.PathEscape(identity)
	req := httptest.NewRequest(http.MethodGet, "/api/navigation/projects/"+escaped, nil)
	key, _, err := parseNavigationRequest(req)
	if err != nil || key.ProjectKey != identity {
		t.Fatalf("escaped=%q key=%+v err=%v", escaped, key, err)
	}
	if strings.Contains(escaped, "+") && strings.Contains(key.ProjectKey, " ") {
		t.Fatalf("path plus was converted to space")
	}
}

func TestNavigationRepeatedHeadersAndMissingEncodings(t *testing.T) {
	representation := navigationRepresentation{
		JSON:       []byte(`{"identity":true}`),
		Gzip:       []byte("gzip-bytes"),
		ETag:       `W/"nav-test"`,
		Generation: "00112233445566778899aabbccddeeff",
		Revision:   7,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/navigation", nil)
	req.Header.Add("Accept-Encoding", " , br")
	req.Header.Add("Accept-Encoding", "gzip;q=1.")
	req.Header.Add("If-None-Match", " , \"other\",")
	req.Header.Add("If-None-Match", `W/"nav-test"`)
	response := httptest.NewRecorder()
	status, err := writeNavigationRepresentation(response, req, representation)
	if err != nil || status != http.StatusNotModified || response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status=%d err=%v headers=%v", status, err, response.Header())
	}

	for _, broken := range []navigationRepresentation{
		{Gzip: representation.Gzip, ETag: representation.ETag, Generation: representation.Generation},
		{JSON: representation.JSON, ETag: representation.ETag, Generation: representation.Generation},
	} {
		response := httptest.NewRecorder()
		if _, err := writeNavigationRepresentation(response, httptest.NewRequest(http.MethodGet, "/api/navigation", nil), broken); err == nil {
			t.Fatal("missing cached encoding was accepted")
		}
	}
}

func TestNavigationMethodAndErrorMappingHTTP(t *testing.T) {
	web := newNavigationHTTPTestServer(t)
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/api/navigation", nil)
		response := httptest.NewRecorder()
		web.Handler().ServeHTTP(response, req)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("%s status=%d allow=%q", method, response.Code, response.Header().Get("Allow"))
		}
	}
	for _, test := range []struct {
		err  error
		want int
	}{
		{navigationUnavailable(context.Canceled), http.StatusServiceUnavailable},
		{navigationUnavailable(context.DeadlineExceeded), http.StatusServiceUnavailable},
		{navigationNotFoundError{}, http.StatusNotFound},
		{errors.New("broken"), http.StatusInternalServerError},
	} {
		response := httptest.NewRecorder()
		if got := navigationHTTPError(response, test.err); got != test.want || response.Code != test.want {
			t.Fatalf("error=%v status=%d response=%d", test.err, got, response.Code)
		}
	}
}

func TestNavigationServiceDeadlineIsTypedUnavailable(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.entered = make(chan struct{})
	source.release = make(chan struct{})
	service := newTestNavigationService(t, source)
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err := service.Representation(ctx, navigationResourceKey{Kind: navigationResourceManifest})
	close(source.release)
	var unavailable navigationAvailabilityError
	if !errors.As(err, &unavailable) || unavailable.StatusCode() != http.StatusServiceUnavailable || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", err)
	}
}

func TestNavigationRepresentationVersionsSemanticKeyAtomically(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	representation, err := service.Representation(t.Context(), navigationResourceKey{
		Kind:       navigationResourceManifest,
		Generation: "caller-must-not-version",
		Revision:   99,
	})
	if err != nil || representation.Generation == "caller-must-not-version" || representation.Revision == 99 {
		t.Fatalf("representation=%+v err=%v", representation, err)
	}
}

func TestNavigationMetricsStatusCoverageHTTP(t *testing.T) {
	web := newNavigationHTTPTestServer(t)
	var mu sync.Mutex
	var events []navigationMetricEvent
	web.navigationMetrics = &navigationMetricFunc{fn: func(event navigationMetricEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}}
	first := requestNavigation(t, web, "/api/navigation", "", "")
	if first.Code != http.StatusOK {
		t.Fatal(first.Code)
	}
	if second := requestNavigation(t, web, "/api/navigation", "", first.Header().Get("ETag")); second.Code != http.StatusNotModified {
		t.Fatal(second.Code)
	}
	if bad := requestNavigation(t, web, "/api/navigation?unexpected=1", "", ""); bad.Code != http.StatusBadRequest {
		t.Fatal(bad.Code)
	}
	broken := &WebServer{}
	broken.navigationMetrics = web.navigationMetrics
	if failure := requestNavigation(t, broken, "/api/navigation", "", ""); failure.Code != http.StatusInternalServerError {
		t.Fatal(failure.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	statuses := map[int]bool{}
	for _, event := range events {
		statuses[event.Status] = true
	}
	for _, status := range []int{http.StatusOK, http.StatusNotModified, http.StatusBadRequest, http.StatusInternalServerError} {
		if !statuses[status] {
			t.Fatalf("events do not include %d: %+v", status, events)
		}
	}
}

func TestNavigationStandardServer304HTTP(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable: %v", err)
	}
	web := newNavigationHTTPTestServer(t)
	server := &http.Server{Handler: web.Handler()}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/api/navigation", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	first, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	etag := first.Header.Get("ETag")
	_ = first.Body.Close()
	request, err = http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/api/navigation", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("If-None-Match", etag)
	second, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusNotModified || second.Header.Get("Content-Type") != "" || second.Header.Get("Content-Length") != "" {
		t.Fatalf("standard 304 status=%d headers=%v", second.StatusCode, second.Header)
	}
	for _, header := range []string{"Cache-Control", "Vary", "ETag", "X-Evener-Navigation-Generation", "X-Evener-Navigation-Revision", "Content-Encoding"} {
		if second.Header.Get(header) == "" {
			t.Fatalf("standard 304 omitted %s: %v", header, second.Header)
		}
	}
}
