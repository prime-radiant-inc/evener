package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/envvars"
)

const defaultAPIURL = "https://api.github.com"

// minCurrentSHALen is the shortest CurrentSHA that may claim to be current:
// release builds stamp a 7-char ShortCommit (see .goreleaser.yml), and
// anything shorter matches exponentially more full SHAs. See Check.
const minCurrentSHALen = 7

// snapshotVersionAsset is the file the release workflow stamps with the
// commit the snapshot channel's archives were built from. CI refreshes the
// mutable "snapshot" prerelease on every green main build and rewrites this
// asset together with the archives, so the channel resolves without CI
// moving a git tag.
const snapshotVersionAsset = "version.txt"

// CheckOptions configures Check. Channel is required; the rest default.
type CheckOptions struct {
	Channel    string
	CurrentSHA string
	// RepoURL is the repository both channels resolve through. The
	// snapshot channel reads its version.txt from the release download URL
	// under RepoURL — the same space install.sh and Upgrade download from.
	RepoURL string
	// APIURL serves only the release channel's API lookups
	// (releases/latest, then the tag's commit). The snapshot channel never
	// consults the API.
	APIURL     string
	HTTPClient *http.Client
}

// CheckResult reports what the channel currently points at and whether the
// running build (CurrentSHA) differs from it.
type CheckResult struct {
	Channel         string `json:"channel"`
	LatestTag       string `json:"latest_tag"`
	LatestCommit    string `json:"latest_commit"`
	UpdateAvailable bool   `json:"update_available"`
}

// Check resolves the channel's latest commit through GitHub (public repo,
// unauthenticated) and compares it against CurrentSHA. The snapshot channel
// reads the mutable "snapshot" prerelease's version.txt asset — the commit
// CI last built from main — over the same release download URL install.sh
// uses; the release channel resolves the tag behind releases/latest. An
// empty CurrentSHA is treated as stale: we cannot prove the running build
// matches, so offer the update.
func Check(ctx context.Context, opts CheckOptions) (CheckResult, error) {
	repoURL := resolveRepoURL(opts.RepoURL)
	owner, repo, err := repoOwnerName(repoURL)
	if err != nil {
		return CheckResult{}, err
	}
	apiURL := strings.TrimRight(envvars.FirstNonEmpty(opts.APIURL, defaultAPIURL), "/")
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	apiBase := fmt.Sprintf("%s/repos/%s/%s", apiURL, owner, repo)

	var tag, latestCommit string
	switch opts.Channel {
	case "snapshot":
		body, err := getText(ctx, client, releaseURL(repoURL, "snapshot", snapshotVersionAsset))
		if err != nil {
			return CheckResult{}, err
		}
		tag = "snapshot"
		latestCommit = strings.TrimSpace(body)
		if latestCommit == "" {
			return CheckResult{}, fmt.Errorf("%s names no commit", snapshotVersionAsset)
		}
	case "release":
		var latest struct {
			TagName string `json:"tag_name"`
		}
		if err := getJSON(ctx, client, apiBase+"/releases/latest", &latest); err != nil {
			return CheckResult{}, err
		}
		if latest.TagName == "" {
			return CheckResult{}, errors.New("latest release has no tag_name")
		}
		tag = latest.TagName
		var commit struct {
			SHA string `json:"sha"`
		}
		if err := getJSON(ctx, client, apiBase+"/commits/"+url.PathEscape(tag), &commit); err != nil {
			return CheckResult{}, err
		}
		if commit.SHA == "" {
			return CheckResult{}, fmt.Errorf("tag %s resolved to no commit", tag)
		}
		latestCommit = commit.SHA
	default:
		return CheckResult{}, fmt.Errorf("unknown update channel %q; use release or snapshot", opts.Channel)
	}
	// A prefix shorter than what release builds stamp (7-char ShortCommit)
	// cannot prove currency: it matches 16^(7-len) full SHAs, so a
	// collision would report up to date and hide an available update.
	// Fail open — such an input is always treated as stale.
	return CheckResult{
		Channel:         opts.Channel,
		LatestTag:       tag,
		LatestCommit:    latestCommit,
		UpdateAvailable: opts.CurrentSHA == "" || len(opts.CurrentSHA) < minCurrentSHALen || !strings.HasPrefix(latestCommit, opts.CurrentSHA),
	}, nil
}

// repoOwnerName extracts "owner", "repo" from a GitHub repository URL.
func repoOwnerName(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("parse repo URL %q: %w", repoURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo URL %q is not owner/repo", repoURL)
	}
	return parts[0], parts[1], nil
}

// httpGet performs one GET and returns the bounded body. Non-2xx responses
// become an error carrying the status and GitHub's own "message" field, so
// a rate-limit response reads as such instead of a bare 403.
func httpGet(ctx context.Context, client *http.Client, u, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	// GitHub requires a User-Agent (403 without one). Go's default
	// Go-http-client satisfies that today, but a product string
	// identifies our traffic for rate-limit attribution and survives
	// transport changes.
	req.Header.Set("User-Agent", "evener/"+buildinfo.Version())
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("GET %s: read body: %w", u, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var gh struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &gh)
		if gh.Message != "" {
			return nil, fmt.Errorf("GET %s: %s: %s", u, resp.Status, gh.Message)
		}
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return body, nil
}

// getJSON performs one GET and decodes a 2xx JSON body.
func getJSON(ctx context.Context, client *http.Client, u string, out any) error {
	body, err := httpGet(ctx, client, u, "application/vnd.github+json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", u, err)
	}
	return nil
}

// getText performs one GET and returns the body as a string, through the
// same transport, bounds, and error surface as getJSON.
func getText(ctx context.Context, client *http.Client, u string) (string, error) {
	body, err := httpGet(ctx, client, u, "application/octet-stream")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
