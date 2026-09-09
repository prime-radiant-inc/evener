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

// CheckOptions configures Check. Channel is required; the rest default.
type CheckOptions struct {
	Channel    string
	CurrentSHA string
	RepoURL    string
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

// Check resolves the channel's tag to a commit through the GitHub REST API
// (public repo, unauthenticated) and compares it against CurrentSHA. The
// snapshot channel is the floating "snapshot" tag; release is the tag behind
// releases/latest. An empty CurrentSHA is treated as stale: we cannot prove
// the running build matches, so offer the update.
func Check(ctx context.Context, opts CheckOptions) (CheckResult, error) {
	owner, repo, err := repoOwnerName(envvars.FirstNonEmpty(opts.RepoURL, defaultRepoURL))
	if err != nil {
		return CheckResult{}, err
	}
	apiURL := strings.TrimRight(envvars.FirstNonEmpty(opts.APIURL, defaultAPIURL), "/")
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	base := fmt.Sprintf("%s/repos/%s/%s", apiURL, owner, repo)

	var tag string
	switch opts.Channel {
	case "snapshot":
		tag = "snapshot"
	case "release":
		var latest struct {
			TagName string `json:"tag_name"`
		}
		if err := getJSON(ctx, client, base+"/releases/latest", &latest); err != nil {
			return CheckResult{}, err
		}
		if latest.TagName == "" {
			return CheckResult{}, errors.New("latest release has no tag_name")
		}
		tag = latest.TagName
	default:
		return CheckResult{}, fmt.Errorf("unknown update channel %q; use release or snapshot", opts.Channel)
	}

	var commit struct {
		SHA string `json:"sha"`
	}
	if err := getJSON(ctx, client, base+"/commits/"+url.PathEscape(tag), &commit); err != nil {
		return CheckResult{}, err
	}
	if commit.SHA == "" {
		return CheckResult{}, fmt.Errorf("tag %s resolved to no commit", tag)
	}
	// A prefix shorter than what release builds stamp (7-char ShortCommit)
	// cannot prove currency: it matches 16^(7-len) full SHAs, so a
	// collision would report up to date and hide an available update.
	// Fail open — such an input is always treated as stale.
	return CheckResult{
		Channel:         opts.Channel,
		LatestTag:       tag,
		LatestCommit:    commit.SHA,
		UpdateAvailable: opts.CurrentSHA == "" || len(opts.CurrentSHA) < minCurrentSHALen || !strings.HasPrefix(commit.SHA, opts.CurrentSHA),
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

// getJSON performs one GET and decodes a 2xx JSON body. Non-2xx responses
// become an error carrying the status and GitHub's own "message" field, so
// a rate-limit response reads as such instead of a bare 403.
func getJSON(ctx context.Context, client *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub requires a User-Agent (403 without one). Go's default
	// Go-http-client satisfies that today, but a product string
	// identifies our traffic for rate-limit attribution and survives
	// transport changes.
	req.Header.Set("User-Agent", "evener/"+buildinfo.Version())
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("GET %s: read body: %w", u, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var gh struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &gh)
		if gh.Message != "" {
			return fmt.Errorf("GET %s: %s: %s", u, resp.Status, gh.Message)
		}
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", u, err)
	}
	return nil
}
