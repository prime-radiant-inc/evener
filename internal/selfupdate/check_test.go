package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRepoURL shapes a RepoURL whose owner/repo path lands on fakeGitHub's
// snapshot download path.
func fakeRepoURL(server *httptest.Server) string {
	return server.URL + "/prime-radiant-inc/evener"
}

// fakeGitHub serves the GitHub REST endpoints Check uses under
// /repos/prime-radiant-inc/evener/..., the snapshot release's version.txt
// under /prime-radiant-inc/evener/releases/download/snapshot/, and records
// the paths it saw.
func fakeGitHub(t *testing.T, latestTag, tagCommit string, status int) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var paths []string
	var userAgents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		userAgents = append(userAgents, r.Header.Get("User-Agent"))
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "API rate limit exceeded"})
			return
		}
		switch {
		case r.URL.Path == "/repos/prime-radiant-inc/evener/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": latestTag})
		case r.URL.Path == "/prime-radiant-inc/evener/releases/download/snapshot/version.txt":
			// The workflow stamps the built commit into version.txt.
			fmt.Fprintf(w, "%s\n", tagCommit)
		case strings.HasPrefix(r.URL.Path, "/repos/prime-radiant-inc/evener/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": tagCommit})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &paths, &userAgents
}

// TestCheckSnapshotUpToDate proves the snapshot channel resolves the commit
// through the snapshot release's version.txt asset, never the git tag.
func TestCheckSnapshotUpToDate(t *testing.T) {
	server, paths, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "be70029", RepoURL: fakeRepoURL(server)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := CheckResult{Channel: "snapshot", LatestTag: "snapshot", LatestCommit: "be7002918fdc60dbdeab71d9dd17e00d3d006c56", UpdateAvailable: false}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(*paths) != 1 || (*paths)[0] != "/prime-radiant-inc/evener/releases/download/snapshot/version.txt" {
		t.Fatalf("paths = %v", *paths)
	}
}

func TestCheckSnapshotStale(t *testing.T) {
	server, _, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "3b1c5f8", RepoURL: fakeRepoURL(server)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !got.UpdateAvailable {
		t.Fatalf("UpdateAvailable = false, want true: %+v", got)
	}
}

func TestCheckReleaseResolvesLatestTagThenCommit(t *testing.T) {
	server, paths, _ := fakeGitHub(t, "v0.2.0", "0123456789abcdef0123456789abcdef01234567", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "release", CurrentSHA: "0123456", APIURL: server.URL})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.LatestTag != "v0.2.0" || got.UpdateAvailable {
		t.Fatalf("got %+v", got)
	}
	wantPaths := []string{
		"/repos/prime-radiant-inc/evener/releases/latest",
		"/repos/prime-radiant-inc/evener/commits/v0.2.0",
	}
	if strings.Join(*paths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("paths = %v, want %v", *paths, wantPaths)
	}
}

func TestCheckEmptyCurrentSHAIsStale(t *testing.T) {
	server, _, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "", RepoURL: fakeRepoURL(server)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !got.UpdateAvailable {
		t.Fatal("empty CurrentSHA must report an update")
	}
}

func TestCheckRateLimitSurfacesGitHubMessage(t *testing.T) {
	server, _, _ := fakeGitHub(t, "v0.2.0", "", http.StatusForbidden)
	_, err := Check(t.Context(), CheckOptions{Channel: "release", CurrentSHA: "0123456", APIURL: server.URL})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "API rate limit exceeded") {
		t.Fatalf("error = %v", err)
	}
}

// TestCheckSnapshotDownloadStatusSurfacesError proves a failing version.txt
// download reports the HTTP status verbatim: the release asset CDN does not
// answer with GitHub's JSON error surface, so the status is all the error
// can carry.
func TestCheckSnapshotDownloadStatusSurfacesError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	_, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "be70029", RepoURL: fakeRepoURL(server)})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want the HTTP status surfaced", err)
	}
}

// TestCheckSnapshotIgnoresAPIURL pins the split resolution contract: the
// snapshot channel resolves through RepoURL's release download URL and
// never consults the API, so a dead APIURL cannot break a snapshot check.
func TestCheckSnapshotIgnoresAPIURL(t *testing.T) {
	server, paths, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "be70029", APIURL: "http://127.0.0.1:1", RepoURL: fakeRepoURL(server)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.LatestCommit != "be7002918fdc60dbdeab71d9dd17e00d3d006c56" || got.UpdateAvailable {
		t.Fatalf("got %+v", got)
	}
	if len(*paths) != 1 || (*paths)[0] != "/prime-radiant-inc/evener/releases/download/snapshot/version.txt" {
		t.Fatalf("paths = %v", *paths)
	}
}

func TestCheckRejectsUnknownChannel(t *testing.T) {
	_, err := Check(t.Context(), CheckOptions{Channel: "nightly", CurrentSHA: "abc"})
	if err == nil || !strings.Contains(err.Error(), "nightly") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckReleaseEmptyTagNameErrors(t *testing.T) {
	server, _, _ := fakeGitHub(t, "", "0123456789abcdef0123456789abcdef01234567", http.StatusOK)
	_, err := Check(t.Context(), CheckOptions{Channel: "release", CurrentSHA: "0123456", APIURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "tag_name") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckEmptyCommitSHAErrors(t *testing.T) {
	server, _, _ := fakeGitHub(t, "", "", http.StatusOK)
	_, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "abc", RepoURL: fakeRepoURL(server)})
	if !errors.Is(err, errSnapshotNamesNoCommit) {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckUsesRepoURLOwnerAndName(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/acme/widgets/releases/download/snapshot/version.txt" {
			fmt.Fprint(w, "abc\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	_, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "abc", RepoURL: server.URL + "/acme/widgets"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if gotPath != "/acme/widgets/releases/download/snapshot/version.txt" {
		t.Fatalf("path = %q", gotPath)
	}
}

// TestCheckSendsProductUserAgent proves every GitHub API request carries a
// stable product User-Agent: GitHub requires a UA (403 otherwise), Go's
// default Go-http-client/2.0 happens to satisfy that today, but a product
// string identifies our traffic for rate-limit attribution and survives
// transport changes.
func TestCheckSendsProductUserAgent(t *testing.T) {
	server, _, agents := fakeGitHub(t, "v0.2.0", "0123456789abcdef0123456789abcdef01234567", http.StatusOK)
	if _, err := Check(t.Context(), CheckOptions{Channel: "release", CurrentSHA: "nope", APIURL: server.URL}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(*agents) == 0 {
		t.Fatal("no requests recorded")
	}
	for _, ua := range *agents {
		if !strings.HasPrefix(ua, "evener/") {
			t.Fatalf("User-Agent = %q, want evener/<version>", ua)
		}
	}
}

// TestCheckShortPrefixIsStale proves a truncated CurrentSHA cannot claim to
// be current: builds stamp a 7-char short SHA, so a 1-6 char prefix match
// against the channel's full commit hides an available update on collision.
// Such a short input must fail open (update available), never up to date.
func TestCheckShortPrefixIsStale(t *testing.T) {
	server, _, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "be70", RepoURL: fakeRepoURL(server)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !got.UpdateAvailable {
		t.Fatal("UpdateAvailable = false for a 4-char prefix of the latest commit, want true (too short to prove currency)")
	}
}

func TestCheckRejectsUnusableRepoURL(t *testing.T) {
	t.Parallel()
	if _, err := Check(t.Context(), CheckOptions{Channel: "snapshot", RepoURL: "https://github.com/onlyowner"}); err == nil {
		t.Fatal("a repo URL without owner/repo was accepted")
	}
	if _, _, err := repoOwnerName("://"); err == nil {
		t.Fatal("an unparseable repo URL was accepted")
	}
	if _, _, err := repoOwnerName("https://github.com/owner/repo/extra"); err == nil {
		t.Fatal("a repo URL with extra path elements was accepted")
	}
}

func TestCheckReleaseRefusesBrokenCommitLookups(t *testing.T) {
	t.Parallel()
	sha, _ := json.Marshal(map[string]string{"sha": ""})
	tag, _ := json.Marshal(map[string]string{"tag_name": "v0.2.0"})
	paths := map[string]string{
		"/repos/prime-radiant-inc/evener/releases/latest": string(tag),
		"/repos/prime-radiant-inc/evener/commits/v0.2.0":  string(sha),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := paths[r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	if _, err := Check(t.Context(), CheckOptions{Channel: "release", CurrentSHA: "0123456", APIURL: server.URL}); err == nil {
		t.Fatal("a tag whose commit lookup 404s resolved to a commit")
	}
	if _, err := Check(t.Context(), CheckOptions{Channel: "release", CurrentSHA: "0123456", APIURL: server.URL}); err == nil {
		t.Fatal("a tag that resolves to no commit succeeded")
	}
}

func TestCheckReleaseRefusesUndecodableBodies(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(server.Close)
	if _, err := Check(t.Context(), CheckOptions{Channel: "release", CurrentSHA: "0123456", APIURL: server.URL}); err == nil {
		t.Fatal("a latest-release body that does not decode succeeded")
	}
}

// errBody fails every read, standing in for a connection that dies mid-body.
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("body went away") }
func (errBody) Close() error             { return nil }

// failingTransport answers one 200 whose body cannot be read.
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: errBody{}, Header: http.Header{}}, nil
}

func TestHTTPGetSurfacesTransportFailures(t *testing.T) {
	t.Parallel()
	if _, err := httpGet(t.Context(), http.DefaultClient, "://missing-scheme", "text/plain"); err == nil {
		t.Fatal("a request that cannot be built succeeded")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if _, err := httpGet(t.Context(), http.DefaultClient, closed.URL, "text/plain"); err == nil {
		t.Fatal("a request to a dead server succeeded")
	}
	client := &http.Client{Transport: failingTransport{}}
	if _, err := httpGet(t.Context(), client, "http://updates.example/version.txt", "text/plain"); err == nil {
		t.Fatal("a body that cannot be read succeeded")
	}
}
