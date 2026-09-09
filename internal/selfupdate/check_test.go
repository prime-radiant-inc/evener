package selfupdate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub serves the two GitHub REST endpoints Check uses under
// /repos/prime-radiant-inc/evener/... and records the paths it saw.
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
		case strings.HasPrefix(r.URL.Path, "/repos/prime-radiant-inc/evener/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": tagCommit})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &paths, &userAgents
}

func TestCheckSnapshotUpToDate(t *testing.T) {
	server, paths, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "be70029", APIURL: server.URL})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := CheckResult{Channel: "snapshot", LatestTag: "snapshot", LatestCommit: "be7002918fdc60dbdeab71d9dd17e00d3d006c56", UpdateAvailable: false}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(*paths) != 1 || (*paths)[0] != "/repos/prime-radiant-inc/evener/commits/snapshot" {
		t.Fatalf("paths = %v", *paths)
	}
}

func TestCheckSnapshotStale(t *testing.T) {
	server, _, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "3b1c5f8", APIURL: server.URL})
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
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "", APIURL: server.URL})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !got.UpdateAvailable {
		t.Fatal("empty CurrentSHA must report an update")
	}
}

func TestCheckRateLimitSurfacesGitHubMessage(t *testing.T) {
	server, _, _ := fakeGitHub(t, "", "", http.StatusForbidden)
	_, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "be70029", APIURL: server.URL})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "API rate limit exceeded") {
		t.Fatalf("error = %v", err)
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
	_, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "abc", APIURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "resolved to no commit") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckUsesRepoURLOwnerAndName(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": "abc"})
	}))
	t.Cleanup(server.Close)
	_, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "abc", APIURL: server.URL, RepoURL: "https://github.com/acme/widgets/"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if gotPath != "/repos/acme/widgets/commits/snapshot" {
		t.Fatalf("path = %q", gotPath)
	}
}

// TestCheckSendsProductUserAgent proves every GitHub API request carries a
// stable product User-Agent: GitHub requires a UA (403 otherwise), Go's
// default Go-http-client/2.0 happens to satisfy that today, but a product
// string identifies our traffic for rate-limit attribution and survives
// transport changes. Fails today: getJSON sets only Accept.
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
