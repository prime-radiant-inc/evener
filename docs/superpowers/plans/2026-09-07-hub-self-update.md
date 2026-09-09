# Hub Self-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Settings → Hub shows whether a newer evener build exists on the chosen channel and, on one click, downloads it, installs it, and replaces the running hub process with it.

**Architecture:** `internal/selfupdate` gains `Check` (GitHub API, compares the channel's commit to `buildinfo.GitSHA`) and `Restart` (`syscall.Exec` in place). The hub exposes `evener/update/check` and `evener/update/apply` over appwire; apply reuses the existing `selfupdate.Upgrade` then execs the installed binary after the response is flushed. The frontend adds a `hubUpdate` store and an "Updates" block in the Hub settings section that polls `/api/health` until the new hub is up.

**Tech Stack:** Go 1.2x, appwire JSON-RPC catalog (`make generate` → `types.gen.ts`), React + zustand + vitest (`make test-web`).

**Spec:** `docs/superpowers/specs/2026-09-07-hub-self-update-design.md`

## Global Constraints

- Default tests must be deterministic and offline (AGENTS.md). Only the `EVENER_UPDATE_E2E=1` test may touch the network.
- Dev builds (`buildinfo.BuildChannel() == "dev"`) are never updated: check returns `applicable: false`, apply errors.
- No stored channel setting. Default channel = `buildinfo.UpgradeChannel()`.
- Frontend files under `src/` must pass `npx biome check --write` before the gate; no `noNonNullAssertion`, no array-index keys.
- Go tests use the `previous := seam; seam = fake; t.Cleanup(restore)` pattern already in `cmd/evener-hub/app_rpc_test.go`.
- Commit after every task. Never `git add -A`.
- Work in `/home/jesse/git/prime-radiant-inc/evener/.worktrees/hub-self-update` on branch `hub-self-update`.

---

### Task 1: `selfupdate.Check`

**Files:**
- Create: `internal/selfupdate/check.go`
- Test: `internal/selfupdate/check_test.go`

**Interfaces:**
- Produces:
  ```go
  type CheckOptions struct {
      Channel    string       // "release" | "snapshot"
      CurrentSHA string       // buildinfo.GitSHA (short)
      RepoURL    string       // default defaultRepoURL
      APIURL     string       // default "https://api.github.com"
      HTTPClient *http.Client // default &http.Client{Timeout: 10 * time.Second}
  }
  type CheckResult struct {
      Channel         string `json:"channel"`
      LatestTag       string `json:"latestTag"`
      LatestCommit    string `json:"latestCommit"`
      UpdateAvailable bool   `json:"updateAvailable"`
  }
  func Check(ctx context.Context, opts CheckOptions) (CheckResult, error)
  ```

- [ ] **Step 1: Write the failing tests**

```go
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
func fakeGitHub(t *testing.T, latestTag, tagCommit string, status int) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
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
	return server, &paths
}

func TestCheckSnapshotUpToDate(t *testing.T) {
	server, paths := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
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
	server, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "3b1c5f8", APIURL: server.URL})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !got.UpdateAvailable {
		t.Fatalf("UpdateAvailable = false, want true: %+v", got)
	}
}

func TestCheckReleaseResolvesLatestTagThenCommit(t *testing.T) {
	server, paths := fakeGitHub(t, "v0.2.0", "0123456789abcdef0123456789abcdef01234567", http.StatusOK)
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
	server, _ := fakeGitHub(t, "", "be7002918fdc60dbdeab71d9dd17e00d3d006c56", http.StatusOK)
	got, err := Check(t.Context(), CheckOptions{Channel: "snapshot", CurrentSHA: "", APIURL: server.URL})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !got.UpdateAvailable {
		t.Fatal("empty CurrentSHA must report an update")
	}
}

func TestCheckRateLimitSurfacesGitHubMessage(t *testing.T) {
	server, _ := fakeGitHub(t, "", "", http.StatusForbidden)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/ -run 'TestCheck' 2>&1 | head`
Expected: compile error, `undefined: Check`.

- [ ] **Step 3: Implement `check.go`**

```go
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

	"primeradiant.com/evener/envvars"
)

const defaultAPIURL = "https://api.github.com"

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
	LatestTag       string `json:"latestTag"`
	LatestCommit    string `json:"latestCommit"`
	UpdateAvailable bool   `json:"updateAvailable"`
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
	return CheckResult{
		Channel:         opts.Channel,
		LatestTag:       tag,
		LatestCommit:    commit.SHA,
		UpdateAvailable: opts.CurrentSHA == "" || !strings.HasPrefix(commit.SHA, opts.CurrentSHA),
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/selfupdate/ 2>&1 | tail -3`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/selfupdate/check.go internal/selfupdate/check_test.go
git commit -m "selfupdate: Check resolves a channel's commit and reports staleness"
```

---

### Task 2: `selfupdate.Restart`

**Files:**
- Create: `internal/selfupdate/restart_unix.go`, `internal/selfupdate/restart_other.go`
- Test: `internal/selfupdate/restart_unix_test.go`

**Interfaces:**
- Produces: `func Restart(binary string, args []string) error` — replaces the process image; returns only on failure.

- [ ] **Step 1: Write the failing test**

```go
//go:build unix

package selfupdate

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestRestartHelper is the exec target: when invoked with EVENER_RESTART_HELPER
// set, it prints its argv and pid, then exits. Otherwise it is a no-op test.
func TestRestartHelper(t *testing.T) {
	if os.Getenv("EVENER_RESTART_HELPER") == "" {
		return
	}
	mode := os.Getenv("EVENER_RESTART_HELPER")
	if mode == "exec" {
		// First hop: exec ourselves again in "print" mode. The pid must survive.
		os.Setenv("EVENER_RESTART_HELPER", "print")
		if err := Restart(os.Args[0], []string{"-test.run=TestRestartHelper", "hub", "-addr", "127.0.0.1:0"}); err != nil {
			os.Stderr.WriteString("restart failed: " + err.Error())
			os.Exit(3)
		}
	}
	os.Stdout.WriteString("pid=" + itoa(os.Getpid()) + " argv=" + strings.Join(os.Args[1:], " ") + "\n")
	os.Exit(0)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRestartKeepsPIDAndPassesArgs(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestRestartHelper")
	cmd.Env = append(os.Environ(), "EVENER_RESTART_HELPER=exec")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	if cmd.Process == nil {
		t.Fatal("no process")
	}
	want := "pid=" + itoa(cmd.Process.Pid) + " argv=-test.run=TestRestartHelper hub -addr 127.0.0.1:0\n"
	if string(out) != want {
		t.Fatalf("helper output = %q, want %q", out, want)
	}
}

func TestRestartMissingBinaryReturnsError(t *testing.T) {
	err := Restart("/nonexistent/evener", []string{"hub"})
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/selfupdate/ -run 'TestRestart' 2>&1 | head`
Expected: compile error, `undefined: Restart`.

- [ ] **Step 3: Implement**

`restart_unix.go`:

```go
//go:build unix

package selfupdate

import (
	"fmt"
	"os"
	"syscall"
)

// Restart replaces the current process image with binary, keeping the PID,
// the environment, and args as argv[1:]. Go opens files and sockets with
// O_CLOEXEC, so the hub's flock and listener are released by the exec and
// re-acquired by the new image. It only returns when the exec itself fails.
func Restart(binary string, args []string) error {
	argv := append([]string{binary}, args...)
	if err := syscall.Exec(binary, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", binary, err)
	}
	return nil
}
```

`restart_other.go`:

```go
//go:build !unix

package selfupdate

import "errors"

// Restart is unsupported outside unix: there is no exec-in-place there.
func Restart(binary string, args []string) error {
	return errors.New("in-place restart is not supported on this platform")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/selfupdate/ 2>&1 | tail -3`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/selfupdate/restart_unix.go internal/selfupdate/restart_other.go internal/selfupdate/restart_unix_test.go
git commit -m "selfupdate: Restart execs a binary in place"
```

---

### Task 3: appwire catalog entries and `BuildChannel` in the overview

**Files:**
- Modify: `appwire/types.go` (near `UpgradeParams` ~line 2019, `MethodEvenerUpgrade` ~line 74, `SettingsHubOverview` ~line 3134)
- Modify: `appwire/protocol.go` (after the `MethodEvenerUpgrade` catalog row ~line 160)
- Modify: `appwire/client.go` (after `Upgrade` ~line 636)
- Modify: `cmd/evener-hub/app_rpc_settings_overview.go:52`
- Generated: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, appwire goldens
- Test: `cmd/evener-hub/app_rpc_settings_overview_test.go`

**Interfaces:**
- Produces (Go, `appwire`):
  ```go
  const MethodEvenerUpdateCheck = "evener/update/check"
  const MethodEvenerUpdateApply = "evener/update/apply"
  type UpdateCheckParams struct { Channel string `json:"channel,omitempty"` }
  type UpdateCheckResponse struct {
      Channel         string `json:"channel"`
      BuildChannel    string `json:"buildChannel"`
      CurrentVersion  string `json:"currentVersion"`
      CurrentCommit   string `json:"currentCommit"`
      LatestTag       string `json:"latestTag,omitempty"`
      LatestCommit    string `json:"latestCommit,omitempty"`
      UpdateAvailable bool   `json:"updateAvailable"`
      Applicable      bool   `json:"applicable"`
  }
  type UpdateApplyParams struct { Channel string `json:"channel,omitempty"` }
  type UpdateApplyResponse struct {
      Release    string   `json:"release"`
      Channel    string   `json:"channel"`
      Installed  []string `json:"installed"`
      Restarting bool     `json:"restarting"`
  }
  func (c *Client) UpdateCheck(ctx, UpdateCheckParams) (UpdateCheckResponse, error)
  func (c *Client) UpdateApply(ctx, UpdateApplyParams) (UpdateApplyResponse, error)
  ```
  and `SettingsHubOverview.BuildChannel string json:"buildChannel,omitempty"`.
- Produces (TS, generated): `UpdateCheckParams`, `UpdateCheckResponse`, `UpdateApplyParams`, `UpdateApplyResponse`, `MethodTypes["evener/update/check"]`, `MethodTypes["evener/update/apply"]`, `SettingsHubOverview.buildChannel?`.

- [ ] **Step 1: Write the failing overview test**

Append to `cmd/evener-hub/app_rpc_settings_overview_test.go` (match the file's existing imports; it already imports `buildinfo` if a Commit test exists, otherwise add `"primeradiant.com/evener/buildinfo"`):

```go
func TestSettingsHubOverviewReportsBuildChannel(t *testing.T) {
	previous := buildinfo.Channel
	buildinfo.Channel = "snapshot"
	t.Cleanup(func() { buildinfo.Channel = previous })

	got := settingsHubOverview(hubcore.WebConfig{})
	if got.BuildChannel != "snapshot" {
		t.Fatalf("BuildChannel = %q, want snapshot", got.BuildChannel)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/evener-hub/ -run TestSettingsHubOverviewReportsBuildChannel 2>&1 | head -5`
Expected: compile error, `got.BuildChannel undefined`.

- [ ] **Step 3: Add the wire types**

In `appwire/types.go`, after `MethodEvenerUpgrade`:

```go
	MethodEvenerUpdateCheck           = "evener/update/check"
	MethodEvenerUpdateApply           = "evener/update/apply"
```

After `UpgradeResponse`:

```go
// UpdateCheckParams selects the channel to compare the running build against.
// Empty means the running binary's own upgrade channel.
type UpdateCheckParams struct {
	Channel string `json:"channel,omitempty"`
}

// UpdateCheckResponse reports the running build and what the channel points
// at. Applicable is false for dev builds, which are never self-updated; the
// Latest* fields are empty then and no network request was made.
type UpdateCheckResponse struct {
	Channel         string `json:"channel"`
	BuildChannel    string `json:"buildChannel"`
	CurrentVersion  string `json:"currentVersion"`
	CurrentCommit   string `json:"currentCommit"`
	LatestTag       string `json:"latestTag,omitempty"`
	LatestCommit    string `json:"latestCommit,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Applicable      bool   `json:"applicable"`
}

// UpdateApplyParams selects the channel to install. Empty means the running
// binary's own upgrade channel.
type UpdateApplyParams struct {
	Channel string `json:"channel,omitempty"`
}

// UpdateApplyResponse is returned just before the hub execs the installed
// binary in place; Restarting is always true on success.
type UpdateApplyResponse struct {
	Release    string   `json:"release"`
	Channel    string   `json:"channel"`
	Installed  []string `json:"installed"`
	Restarting bool     `json:"restarting"`
}
```

In `SettingsHubOverview`, after `Commit`:

```go
	// BuildChannel is buildinfo.BuildChannel(): release, snapshot, or dev.
	BuildChannel string `json:"buildChannel,omitempty"`
```

In `appwire/protocol.go`, after the `MethodEvenerUpgrade` row:

```go
	{MethodEvenerUpdateCheck, UpdateCheckParams{}, UpdateCheckResponse{}, ScopeHub, "Compares the running hub build against a release channel's current commit; dev builds report applicable=false without a network request."},
	{MethodEvenerUpdateApply, UpdateApplyParams{}, UpdateApplyResponse{}, ScopeHub, "Downloads and installs a channel's build, then execs it in place of the running hub; refused on dev builds."},
```

In `appwire/client.go`, after `Upgrade`:

```go
func (c *Client) UpdateCheck(ctx context.Context, params UpdateCheckParams) (UpdateCheckResponse, error) {
	var out UpdateCheckResponse
	err := c.request(ctx, MethodEvenerUpdateCheck, params, &out)
	return out, err
}

func (c *Client) UpdateApply(ctx context.Context, params UpdateApplyParams) (UpdateApplyResponse, error) {
	var out UpdateApplyResponse
	err := c.request(ctx, MethodEvenerUpdateApply, params, &out)
	return out, err
}
```

In `cmd/evener-hub/app_rpc_settings_overview.go`, after `Commit: buildinfo.GitSHA,`:

```go
		BuildChannel:   buildinfo.BuildChannel(),
```

- [ ] **Step 4: Regenerate and update goldens**

Run: `make generate && go test ./appwire -run '^Test.*Golden$' -update-goldens 2>&1 | tail -2 && go test ./appwire/ ./cmd/evener-hub/ -run 'Golden|Catalog|Protocol|Parity|SettingsHubOverview' 2>&1 | tail -3`
Expected: `ok` for both. Then `git status --short` should show `types.gen.ts`, golden files, and the four Go files. If a parity test complains that a catalog method has no hub handler, that is expected until Task 4; note it and move on (the handler lands in the next task, same branch).

- [ ] **Step 5: Frontend typecheck**

Run: `cd cmd/evener-hub/frontend && npx tsc --noEmit 2>&1 | tail -3`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add appwire/types.go appwire/protocol.go appwire/client.go cmd/evener-hub/app_rpc_settings_overview.go cmd/evener-hub/app_rpc_settings_overview_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts appwire/testdata
git commit -m "appwire: evener/update/check and evener/update/apply; buildChannel in settings overview"
```

---

### Task 4: hub handlers `hubUpdateCheck` / `hubUpdateApply`

**Files:**
- Create: `cmd/evener-hub/app_update.go`
- Modify: `cmd/evener-hub/app_rpc.go:965` (next to the `MethodEvenerUpgrade` registration)
- Test: `cmd/evener-hub/app_update_test.go`

**Interfaces:**
- Consumes: `selfupdate.Check`, `selfupdate.Upgrade`, `selfupdate.Restart` (Tasks 1–2), the appwire types (Task 3), `hubProcessArgs` (main.go:46), `runHubSelfUpgrade` (app_upgrade.go).
- Produces: package vars `runHubUpdateCheck`, `scheduleHubRestart` (test seams).

- [ ] **Step 1: Write the failing tests**

```go
package hub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/internal/selfupdate"
)

func setBuild(t *testing.T, sha, channel string) {
	t.Helper()
	prevSHA, prevChannel := buildinfo.GitSHA, buildinfo.Channel
	buildinfo.GitSHA, buildinfo.Channel = sha, channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.Channel = prevSHA, prevChannel })
}

func stubUpdateCheck(t *testing.T, fn func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error)) *int {
	t.Helper()
	calls := 0
	previous := runHubUpdateCheck
	runHubUpdateCheck = func(ctx context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		calls++
		return fn(ctx, opts)
	}
	t.Cleanup(func() { runHubUpdateCheck = previous })
	return &calls
}

func stubSelfUpgrade(t *testing.T, fn func(context.Context, selfupdate.Options) (selfupdate.Result, error)) *int {
	t.Helper()
	calls := 0
	previous := runHubSelfUpgrade
	runHubSelfUpgrade = func(ctx context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		calls++
		return fn(ctx, opts)
	}
	t.Cleanup(func() { runHubSelfUpgrade = previous })
	return &calls
}

type restartCall struct {
	binary string
	args   []string
}

func stubScheduleRestart(t *testing.T) *[]restartCall {
	t.Helper()
	var calls []restartCall
	previous := scheduleHubRestart
	scheduleHubRestart = func(binary string, args []string) { calls = append(calls, restartCall{binary, args}) }
	t.Cleanup(func() { scheduleHubRestart = previous })
	return &calls
}

func TestHubUpdateCheckDevBuildIsNotApplicable(t *testing.T) {
	setBuild(t, "", "")
	calls := stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		t.Fatal("dev build must not call Check")
		return selfupdate.CheckResult{}, nil
	})
	got, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if got.Applicable || got.UpdateAvailable || got.BuildChannel != "dev" || got.CurrentVersion != "dev" {
		t.Fatalf("got %+v", got)
	}
	if *calls != 0 {
		t.Fatalf("Check called %d times", *calls)
	}
}

func TestHubUpdateCheckDefaultsToBuildChannelAndFillsCurrent(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	var gotOpts selfupdate.CheckOptions
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		gotOpts = opts
		return selfupdate.CheckResult{Channel: opts.Channel, LatestTag: "snapshot", LatestCommit: "be70029abc", UpdateAvailable: true}, nil
	})
	got, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if gotOpts.Channel != "snapshot" || gotOpts.CurrentSHA != "3b1c5f8" {
		t.Fatalf("opts = %+v", gotOpts)
	}
	want := appwire.UpdateCheckResponse{
		Channel: "snapshot", BuildChannel: "snapshot", CurrentVersion: "3b1c5f8", CurrentCommit: "3b1c5f8",
		LatestTag: "snapshot", LatestCommit: "be70029abc", UpdateAvailable: true, Applicable: true,
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHubUpdateCheckHonoursRequestedChannel(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	var gotChannel string
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		gotChannel = opts.Channel
		return selfupdate.CheckResult{Channel: opts.Channel, LatestTag: "v0.1.0", LatestCommit: "3b1c5f8ffff"}, nil
	})
	got, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{Channel: "release"})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if gotChannel != "release" || got.Channel != "release" || got.UpdateAvailable {
		t.Fatalf("channel=%q got=%+v", gotChannel, got)
	}
}

func TestHubUpdateCheckPropagatesError(t *testing.T) {
	setBuild(t, "3b1c5f8", "release")
	stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{}, errors.New("GET x: 403 Forbidden: API rate limit exceeded")
	})
	_, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestHubUpdateApplyDevBuildRefused(t *testing.T) {
	setBuild(t, "", "")
	upgrades := stubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, nil
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil || !strings.Contains(err.Error(), "dev build") {
		t.Fatalf("err = %v", err)
	}
	if *upgrades != 0 || len(*restarts) != 0 {
		t.Fatalf("upgrades=%d restarts=%d", *upgrades, len(*restarts))
	}
}

func TestHubUpdateApplyInstallsThenSchedulesRestartWithHubArgs(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	previousArgs := hubProcessArgs
	hubProcessArgs = func() []string { return []string{"/old/evener", "hub", "-addr", "0.0.0.0:9180"} }
	t.Cleanup(func() { hubProcessArgs = previousArgs })

	var gotOpts selfupdate.Options
	stubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return selfupdate.Result{
			Release: "snapshot", Channel: "snapshot",
			Installed: []string{"/home/u/.local/share/evener/bin/evener", "/home/u/.local/share/evener/bin/evener-dev"},
		}, nil
	})
	restarts := stubScheduleRestart(t)

	got, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"})
	if err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Requested != "snapshot" || gotOpts.CurrentChannel != "snapshot" {
		t.Fatalf("opts = %+v", gotOpts)
	}
	if !got.Restarting || got.Release != "snapshot" || len(got.Installed) != 2 {
		t.Fatalf("got %+v", got)
	}
	if len(*restarts) != 1 {
		t.Fatalf("restarts = %v", *restarts)
	}
	call := (*restarts)[0]
	if call.binary != "/home/u/.local/share/evener/bin/evener" {
		t.Fatalf("binary = %q", call.binary)
	}
	if strings.Join(call.args, " ") != "hub -addr 0.0.0.0:9180" {
		t.Fatalf("args = %v", call.args)
	}
}

func TestHubUpdateApplyDefaultChannelIsBuildChannel(t *testing.T) {
	setBuild(t, "3b1c5f8", "release")
	var gotOpts selfupdate.Options
	stubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return selfupdate.Result{Release: "latest", Channel: "release", Installed: []string{"/x/evener", "/x/evener-dev"}}, nil
	})
	stubScheduleRestart(t)
	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Requested != "release" {
		t.Fatalf("Requested = %q, want release", gotOpts.Requested)
	}
}

func TestHubUpdateApplyUpgradeFailureDoesNotRestart(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, errors.New("download failed")
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("err = %v", err)
	}
	if len(*restarts) != 0 {
		t.Fatalf("restart scheduled after failed upgrade: %v", *restarts)
	}
}

func TestHubUpdateApplyRejectsResultWithoutInstalledBinary(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{Release: "snapshot", Channel: "snapshot"}, nil
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil {
		t.Fatal("expected error for empty Installed")
	}
	if len(*restarts) != 0 {
		t.Fatalf("restart scheduled: %v", *restarts)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/evener-hub/ -run 'TestHubUpdate' 2>&1 | head -5`
Expected: compile errors, `undefined: hubUpdateCheck` etc.

- [ ] **Step 3: Implement `app_update.go`**

```go
package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/selfupdate"
)

// Test seams: the GitHub check and the exec-in-place restart.
var (
	runHubUpdateCheck  = selfupdate.Check
	scheduleHubRestart = scheduleHubRestartAfterResponse
)

// hubRestartDelay gives the appwire response time to reach the browser
// before the process image is replaced; the frontend needs the success
// result to start its health poll.
const hubRestartDelay = 500 * time.Millisecond

// isDevBuild reports whether this hub was built without a release channel
// (a worktree build). Such a hub is never self-updated: replacing it with a
// release binary would silently discard whatever the developer is running.
func isDevBuild() bool { return buildinfo.BuildChannel() == "dev" }

func updateChannel(requested string) string {
	return envvars.FirstNonEmpty(requested, buildinfo.UpgradeChannel())
}

// hubUpdateCheck answers evener/update/check: compare the running build to
// the channel's current commit. Dev builds answer locally.
func hubUpdateCheck(ctx context.Context, params appwire.UpdateCheckParams) (appwire.UpdateCheckResponse, error) {
	resp := appwire.UpdateCheckResponse{
		Channel:        updateChannel(params.Channel),
		BuildChannel:   buildinfo.BuildChannel(),
		CurrentVersion: buildinfo.Version(),
		CurrentCommit:  buildinfo.GitSHA,
	}
	if isDevBuild() {
		return resp, nil
	}
	result, err := runHubUpdateCheck(ctx, selfupdate.CheckOptions{
		Channel:    resp.Channel,
		CurrentSHA: buildinfo.GitSHA,
	})
	if err != nil {
		return appwire.UpdateCheckResponse{}, err
	}
	resp.LatestTag = result.LatestTag
	resp.LatestCommit = result.LatestCommit
	resp.UpdateAvailable = result.UpdateAvailable
	resp.Applicable = true
	return resp, nil
}

// hubUpdateApply answers evener/update/apply: install the channel's build,
// then exec it in place once this response has gone out.
func hubUpdateApply(ctx context.Context, params appwire.UpdateApplyParams) (appwire.UpdateApplyResponse, error) {
	if isDevBuild() {
		return appwire.UpdateApplyResponse{}, errors.New("this hub is a dev build; rebuild with make build-hub instead of self-updating")
	}
	channel := updateChannel(params.Channel)
	result, err := runHubSelfUpgrade(ctx, selfupdate.Options{
		Requested:      channel,
		CurrentChannel: buildinfo.UpgradeChannel(),
	})
	if err != nil {
		return appwire.UpdateApplyResponse{}, err
	}
	if len(result.Installed) == 0 {
		return appwire.UpdateApplyResponse{}, fmt.Errorf("upgrade to %s installed no binaries", channel)
	}
	scheduleHubRestart(result.Installed[0], hubProcessArgs()[1:])
	return appwire.UpdateApplyResponse{
		Release:    result.Release,
		Channel:    result.Channel,
		Installed:  result.Installed,
		Restarting: true,
	}, nil
}

// scheduleHubRestartAfterResponse execs binary with the hub's own arguments
// after hubRestartDelay. On exec failure the old hub keeps running and the
// failure is logged; there is nothing else to roll back.
func scheduleHubRestartAfterResponse(binary string, args []string) {
	go func() {
		time.Sleep(hubRestartDelay)
		_, _ = fmt.Fprintf(os.Stderr, "[hub] self-update: restarting as %s\n", binary)
		if err := selfupdate.Restart(binary, args); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[hub] self-update: restart failed, still running the previous binary: %v\n", err)
		}
	}()
}
```

Register in `app_rpc.go` right after the `MethodEvenerUpgrade` line:

```go
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateCheck, hubUpdateCheck)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateApply, hubUpdateApply)
```

- [ ] **Step 4: Run the hub tests**

Run: `go test ./cmd/evener-hub/ 2>&1 | tail -3`
Expected: `ok` (this package takes ~90s). Any parity test that failed in Task 3 for a missing handler must pass now.

- [ ] **Step 5: Lint**

Run: `make lint 2>&1 | tail -5`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/app_update.go cmd/evener-hub/app_update_test.go cmd/evener-hub/app_rpc.go
git commit -m "hub: evener/update/check and evener/update/apply with exec-in-place restart"
```

---

### Task 5: frontend `hubUpdate` store

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/hubUpdate.ts`
- Test: `cmd/evener-hub/frontend/src/stores/hubUpdate.test.ts`

**Interfaces:**
- Consumes: `MethodTypes["evener/update/check"]`, `["evener/update/apply"]` from `types.gen.ts` (Task 3); `connectionStore` (`stores/connection.ts`); `errorText` (`protocol/errors.ts`); `FakeClient` (`protocol/testing/fakeClient.ts`).
- Produces:
  ```ts
  export type UpdateChannel = "release" | "snapshot";
  export interface HubUpdateStoreState {
    channel: UpdateChannel | null;
    check: UpdateCheckResponse | null;
    checking: boolean;
    checkError: string | null;
    applying: boolean;
    applyError: string | null;
    restarting: boolean;
    restartTimedOut: boolean;
    setChannel(channel: UpdateChannel): void;
    runCheck(): Promise<void>;
    apply(): Promise<void>;
  }
  export const hubUpdateStore; export function useHubUpdateStore(selector?)
  export const RESTART_POLL_MS = 1000; export const RESTART_TIMEOUT_MS = 30_000;
  export function resetHubUpdateStoreForTests(deps?: { fetchImpl?: typeof fetch; reload?: () => void }): void;
  ```

- [ ] **Step 1: Write the failing tests**

```ts
import { act } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { UpdateCheckResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { RESTART_POLL_MS, RESTART_TIMEOUT_MS, hubUpdateStore, resetHubUpdateStoreForTests } from "./hubUpdate";
import { resetThreadsStoreForTests } from "./threads";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const UP_TO_DATE: UpdateCheckResponse = {
  channel: "snapshot",
  buildChannel: "snapshot",
  currentVersion: "be70029",
  currentCommit: "be70029",
  latestTag: "snapshot",
  latestCommit: "be7002918fdc60dbdeab71d9dd17e00d3d006c56",
  updateAvailable: false,
  applicable: true,
};

function healthFetch(versions: string[]): typeof fetch {
  let i = 0;
  return vi.fn(async () => {
    const version = versions[Math.min(i, versions.length - 1)];
    i += 1;
    if (version === "DOWN") throw new TypeError("Failed to fetch");
    return new Response(JSON.stringify({ version }), { status: 200 });
  }) as unknown as typeof fetch;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetHubUpdateStoreForTests();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("runCheck", () => {
  test("requests evener/update/check with the selected channel and stores the result", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);
    hubUpdateStore.getState().setChannel("snapshot");

    await act(() => hubUpdateStore.getState().runCheck());

    expect(fake.calls).toEqual([{ method: "evener/update/check", params: { channel: "snapshot" } }]);
    const state = hubUpdateStore.getState();
    expect(state.check).toEqual(UP_TO_DATE);
    expect(state.checking).toBe(false);
    expect(state.checkError).toBeNull();
  });

  test("sends an empty channel when none is selected yet", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);

    await act(() => hubUpdateStore.getState().runCheck());

    expect(fake.calls).toEqual([{ method: "evener/update/check", params: { channel: "" } }]);
  });

  test("stores the error text and clears the previous result on failure", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => {
      throw new Error("GET x: 403 Forbidden: API rate limit exceeded");
    });

    await act(() => hubUpdateStore.getState().runCheck());

    const state = hubUpdateStore.getState();
    expect(state.check).toBeNull();
    expect(state.checkError).toContain("rate limit");
  });

  test("setChannel clears the stale check result", async () => {
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => UP_TO_DATE);
    await act(() => hubUpdateStore.getState().runCheck());

    act(() => hubUpdateStore.getState().setChannel("release"));

    expect(hubUpdateStore.getState().channel).toBe("release");
    expect(hubUpdateStore.getState().check).toBeNull();
  });
});

describe("apply", () => {
  test("requests evener/update/apply, then polls /api/health until the version changes and reloads", async () => {
    vi.useFakeTimers();
    const reload = vi.fn();
    const fetchImpl = healthFetch(["be70029", "DOWN", "DOWN", "3b1c5f8"]);
    resetHubUpdateStoreForTests({ fetchImpl, reload });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true, latestCommit: "3b1c5f8aaaa" }));
    fake.on("evener/update/apply", () => ({
      release: "snapshot",
      channel: "snapshot",
      installed: ["/x/evener", "/x/evener-dev"],
      restarting: true,
    }));
    hubUpdateStore.getState().setChannel("snapshot");
    await act(() => hubUpdateStore.getState().runCheck());

    await act(() => hubUpdateStore.getState().apply());

    expect(fake.calls[1]).toEqual({ method: "evener/update/apply", params: { channel: "snapshot" } });
    expect(hubUpdateStore.getState().restarting).toBe(true);
    expect(hubUpdateStore.getState().applying).toBe(false);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(RESTART_POLL_MS * 4);
    });

    expect(reload).toHaveBeenCalledTimes(1);
    expect(hubUpdateStore.getState().restartTimedOut).toBe(false);
  });

  test("gives up after RESTART_TIMEOUT_MS when the version never changes", async () => {
    vi.useFakeTimers();
    const reload = vi.fn();
    resetHubUpdateStoreForTests({ fetchImpl: healthFetch(["be70029"]), reload });
    const fake = connectFakeClient();
    fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true }));
    fake.on("evener/update/apply", () => ({ release: "snapshot", channel: "snapshot", installed: ["/x/evener"], restarting: true }));
    await act(() => hubUpdateStore.getState().runCheck());
    await act(() => hubUpdateStore.getState().apply());

    await act(async () => {
      await vi.advanceTimersByTimeAsync(RESTART_TIMEOUT_MS + RESTART_POLL_MS);
    });

    expect(reload).not.toHaveBeenCalled();
    expect(hubUpdateStore.getState().restarting).toBe(false);
    expect(hubUpdateStore.getState().restartTimedOut).toBe(true);
  });

  test("stores applyError and does not poll when the hub refuses", async () => {
    vi.useFakeTimers();
    const fetchImpl = healthFetch(["be70029"]);
    resetHubUpdateStoreForTests({ fetchImpl, reload: vi.fn() });
    const fake = connectFakeClient();
    fake.on("evener/update/apply", () => {
      throw new Error("this hub is a dev build");
    });

    await act(() => hubUpdateStore.getState().apply());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(RESTART_POLL_MS * 2);
    });

    expect(hubUpdateStore.getState().applyError).toContain("dev build");
    expect(hubUpdateStore.getState().restarting).toBe(false);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/hubUpdate.test.ts 2>&1 | tail -5`
Expected: fails to import `./hubUpdate`.

- [ ] **Step 3: Implement the store**

```ts
// hubUpdate.ts drives Settings -> Hub -> Updates: pick a release channel,
// ask the hub whether that channel is ahead of the running build
// (evener/update/check), and apply it (evener/update/apply). Apply is the
// interesting half: the hub answers, then execs the new binary in place, so
// the WebSocket drops and the page has to notice the NEW hub on its own.
// It does that by polling /api/health (auth-exempt, always registered - see
// shell/chrome/webNotBuilt.ts's note) until `version` differs from the
// value the check recorded, then reloads so the SPA bundle matches the
// backend. Fetch failures while polling are the expected mid-restart state.
//
// Like settingsOverview.ts, this store reads connectionStore's current
// client at call time and holds no subscription of its own.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { errorText } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { UpdateCheckResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";

export type UpdateChannel = "release" | "snapshot";

export const RESTART_POLL_MS = 1000;
export const RESTART_TIMEOUT_MS = 30_000;

export interface HubUpdateStoreState {
  // null until the section seeds it from the overview's buildChannel; the
  // hub then falls back to its own upgrade channel for an empty request.
  channel: UpdateChannel | null;
  check: UpdateCheckResponse | null;
  checking: boolean;
  checkError: string | null;
  applying: boolean;
  applyError: string | null;
  restarting: boolean;
  restartTimedOut: boolean;
  setChannel(channel: UpdateChannel): void;
  runCheck(): Promise<void>;
  apply(): Promise<void>;
}

interface Deps {
  fetchImpl: typeof fetch;
  reload: () => void;
}

let deps: Deps = { fetchImpl: (...args) => fetch(...args), reload: () => window.location.reload() };

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("hubUpdate store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

async function healthVersion(): Promise<string | null> {
  try {
    const response = await deps.fetchImpl("/api/health", { credentials: "same-origin" });
    if (!response.ok) return null;
    const body = (await response.json()) as { version?: string };
    return typeof body.version === "string" ? body.version : null;
  } catch {
    return null; // the hub is mid-restart; keep polling
  }
}

// waitForNewHub polls until /api/health reports a version other than
// previous, then reloads. Resolves after reload() or the timeout.
async function waitForNewHub(previous: string | null): Promise<void> {
  const deadline = Date.now() + RESTART_TIMEOUT_MS;
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, RESTART_POLL_MS));
    const version = await healthVersion();
    if (version !== null && version !== previous) {
      hubUpdateStore.setState({ restarting: false });
      deps.reload();
      return;
    }
  }
  hubUpdateStore.setState({ restarting: false, restartTimedOut: true });
}

const INITIAL = {
  channel: null,
  check: null,
  checking: false,
  checkError: null,
  applying: false,
  applyError: null,
  restarting: false,
  restartTimedOut: false,
} as const;

export const hubUpdateStore = createStore<HubUpdateStoreState>((set, get) => ({
  ...INITIAL,

  setChannel(channel) {
    set({ channel, check: null, checkError: null, applyError: null });
  },

  async runCheck() {
    set({ checking: true, checkError: null });
    try {
      const check = await requireClient().request("evener/update/check", { channel: get().channel ?? "" });
      set({ check, checking: false });
    } catch (err) {
      set({ check: null, checking: false, checkError: errorText(err) });
    }
  },

  async apply() {
    set({ applying: true, applyError: null, restartTimedOut: false });
    const previous = get().check?.currentVersion ?? null;
    try {
      await requireClient().request("evener/update/apply", { channel: get().channel ?? "" });
    } catch (err) {
      set({ applying: false, applyError: errorText(err) });
      return;
    }
    set({ applying: false, restarting: true });
    await waitForNewHub(previous);
  },
}));

export function useHubUpdateStore(): HubUpdateStoreState;
export function useHubUpdateStore<T>(selector: (state: HubUpdateStoreState) => T): T;
export function useHubUpdateStore<T>(selector?: (state: HubUpdateStoreState) => T): T | HubUpdateStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(hubUpdateStore, selector) : useStore(hubUpdateStore);
}

// resetHubUpdateStoreForTests resets state and lets tests inject the
// health fetch and reload so the restart poll runs under fake timers with
// no real network and no real navigation. Production never calls this.
export function resetHubUpdateStoreForTests(overrides: Partial<Deps> = {}): void {
  deps = {
    fetchImpl: overrides.fetchImpl ?? ((...args) => fetch(...args)),
    reload: overrides.reload ?? (() => window.location.reload()),
  };
  hubUpdateStore.setState({ ...INITIAL });
}
```

- [ ] **Step 4: Run the tests and Biome**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/stores/hubUpdate.ts src/stores/hubUpdate.test.ts && npx vitest run src/stores/hubUpdate.test.ts 2>&1 | tail -5`
Expected: all tests pass. If `apply()` awaiting `waitForNewHub` makes the `apply` test hang under fake timers, change the tests' `await act(() => apply())` to `act(() => { void apply(); })` followed by `await act(async () => { await vi.advanceTimersByTimeAsync(0); })`; the store code stays as written.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/stores/hubUpdate.ts cmd/evener-hub/frontend/src/stores/hubUpdate.test.ts
git commit -m "hub web: hubUpdate store (check, apply, health poll to reload)"
```

---

### Task 6: Updates block in Settings → Hub

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.tsx`, `hubUpdates.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/hub.tsx` (render `<HubUpdates />` after the `<dl>`)
- Test: `cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.test.tsx`

**Interfaces:**
- Consumes: `hubUpdateStore`, `useHubUpdateStore`, `resetHubUpdateStoreForTests`, `UpdateChannel` (Task 5); `useSettingsOverviewStore` (`hub.buildChannel`, `hub.version`, `hub.commit`); widgets `Button`, `RadioGroup`, `ConfirmDialog`, `Loader` from `../../../widgets`; `FieldDim`, `Code` from `./settingsField`.
- Produces: `export function HubUpdates(): JSX.Element`.

- [ ] **Step 1: Write the failing tests**

```tsx
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse, UpdateCheckResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetHubUpdateStoreForTests } from "../../../stores/hubUpdate";
import { resetSettingsOverviewStoreForTests, settingsOverviewStore } from "../../../stores/settingsOverview";
import { HubSection } from "./hub";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function overview(buildChannel: string): SettingsOverviewResponse {
  return {
    hub: {
      version: "3b1c5f8",
      commit: "3b1c5f8",
      buildChannel,
      listenAddr: "127.0.0.1:9180",
      runDir: "/tmp/run",
      spawnTimeout: "30s",
    },
  };
}

const UP_TO_DATE: UpdateCheckResponse = {
  channel: "snapshot",
  buildChannel: "snapshot",
  currentVersion: "3b1c5f8",
  currentCommit: "3b1c5f8",
  latestTag: "snapshot",
  latestCommit: "3b1c5f8ffffffff",
  updateAvailable: false,
  applicable: true,
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
  resetHubUpdateStoreForTests({ fetchImpl: vi.fn() as unknown as typeof fetch, reload: vi.fn() });
});

afterEach(cleanup);

test("dev build shows the rebuild note and no update controls", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));

  render(<HubSection />);

  expect(await screen.findByText(/Dev build/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Check for updates" })).toBeNull();
  expect(fake.calls.map((c) => c.method)).toEqual(["evener/settings/overview"]);
});

test("release build checks on mount with the build channel and reports up to date", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);

  render(<HubSection />);

  expect(await screen.findByText(/Up to date on snapshot/)).toBeTruthy();
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "snapshot" } });
  expect(screen.getByRole("radio", { name: "Snapshot" })).toHaveProperty("checked", true);
  expect(screen.getByRole("button", { name: "Update and restart" })).toHaveProperty("disabled", true);
});

test("switching channel re-checks with the new channel", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", (params) => ({ ...UP_TO_DATE, channel: params.channel, latestTag: params.channel === "release" ? "v0.1.0" : "snapshot" }));

  render(<HubSection />);
  await screen.findByText(/Up to date on snapshot/);

  await userEvent.click(screen.getByRole("radio", { name: "Release" }));

  expect(await screen.findByText(/Up to date on release/)).toBeTruthy();
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "release" } });
});

test("update available enables the button; confirming applies and shows restarting", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true, latestCommit: "be7002918fdc" }));
  fake.on("evener/update/apply", () => new Promise(() => {})); // stays pending: we only assert the request and the busy state

  render(<HubSection />);
  expect(await screen.findByText(/Update available: snapshot be70029/)).toBeTruthy();

  const button = screen.getByRole("button", { name: "Update and restart" });
  expect(button).toHaveProperty("disabled", false);
  await userEvent.click(button);
  await userEvent.click(await screen.findByRole("button", { name: "Update and restart", hidden: false, exact: true }));

  await waitFor(() => expect(fake.calls).toContainEqual({ method: "evener/update/apply", params: { channel: "snapshot" } }));
});

test("restarting and timed-out states render their messages", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);
  render(<HubSection />);
  await screen.findByText(/Up to date on snapshot/);

  const { hubUpdateStore } = await import("../../../stores/hubUpdate");
  hubUpdateStore.setState({ restarting: true });
  expect(await screen.findByText(/Restarting hub/)).toBeTruthy();

  hubUpdateStore.setState({ restarting: false, restartTimedOut: true });
  expect(await screen.findByText(/didn't come back within 30s/)).toBeTruthy();
});

test("check failure shows the error and a working Check for updates button", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("release"));
  let attempts = 0;
  fake.on("evener/update/check", () => {
    attempts += 1;
    if (attempts === 1) throw new Error("GET x: 403 Forbidden: API rate limit exceeded");
    return { ...UP_TO_DATE, channel: "release", latestTag: "v0.1.0" };
  });

  render(<HubSection />);
  expect(await screen.findByText(/rate limit/)).toBeTruthy();

  await userEvent.click(screen.getByRole("button", { name: "Check for updates" }));

  expect(await screen.findByText(/Up to date on release/)).toBeTruthy();
  expect(settingsOverviewStore.getState().data).not.toBeNull();
});
```

Note for the confirm-dialog click in the fourth test: `ConfirmDialog` renders its own confirm button with `confirmLabel`. Use a distinct label for the dialog's button, `"Yes, update and restart"`, and change that test's second click to `screen.findByRole("button", { name: "Yes, update and restart" })`. (This is the intended copy; the line above is superseded by this note.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/hubUpdates.test.tsx 2>&1 | tail -5`
Expected: fails (no Updates UI; "Dev build" text not found).

- [ ] **Step 3: Implement `hubUpdates.tsx` and CSS**

`hubUpdates.module.css`:

```css
.root {
  display: flex;
  flex-direction: column;
  gap: var(--space-3, 12px);
  margin-top: var(--space-4, 16px);
}

.heading {
  margin: 0;
  font-size: var(--font-size-md, 14px);
  font-weight: 600;
}

.status {
  margin: 0;
}

.actions {
  display: flex;
  gap: var(--space-2, 8px);
  align-items: center;
}
```

Check `hub.module.css`/`general.module.css` and the theme tokens (`grep -rn "\-\-space-" src/theme* | head`) and use the real spacing/font tokens the sibling sections use instead of the fallbacks above if they exist.

`hubUpdates.tsx`:

```tsx
import { useEffect, useState } from "react";
import { friendlyErrorMessage } from "../../../protocol/errors";
import { hubUpdateStore, type UpdateChannel, useHubUpdateStore } from "../../../stores/hubUpdate";
import { useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, ConfirmDialog, Loader, RadioGroup } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hubUpdates.module.css";
import { Code, FieldDim } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "hubUpdates.module.css", "root"),
  heading: requireClass(styles.heading, "hubUpdates.module.css", "heading"),
  status: requireClass(styles.status, "hubUpdates.module.css", "status"),
  actions: requireClass(styles.actions, "hubUpdates.module.css", "actions"),
};

const CHANNEL_OPTIONS = [
  { value: "release", label: "Release" },
  { value: "snapshot", label: "Snapshot" },
];

function isChannel(value: string | undefined): value is UpdateChannel {
  return value === "release" || value === "snapshot";
}

function shortCommit(sha: string): string {
  return sha.slice(0, 7);
}

/**
 * Settings -> Hub -> Updates. Seeds the channel from the overview's
 * buildChannel (release/snapshot builds only), checks on mount and on
 * channel change, and gates "Update and restart" behind a confirm dialog.
 * Dev builds get a note instead of controls: a worktree build must never be
 * silently replaced by a release binary (design spec, Decisions).
 */
export function HubUpdates() {
  const hub = useSettingsOverviewStore((s) => s.data?.hub);
  const channel = useHubUpdateStore((s) => s.channel);
  const check = useHubUpdateStore((s) => s.check);
  const checking = useHubUpdateStore((s) => s.checking);
  const checkError = useHubUpdateStore((s) => s.checkError);
  const applying = useHubUpdateStore((s) => s.applying);
  const applyError = useHubUpdateStore((s) => s.applyError);
  const restarting = useHubUpdateStore((s) => s.restarting);
  const restartTimedOut = useHubUpdateStore((s) => s.restartTimedOut);
  const [confirming, setConfirming] = useState(false);

  const buildChannel = hub?.buildChannel;
  const isDev = buildChannel === "dev";

  // Seed the selector from the running binary once the overview is here.
  useEffect(() => {
    if (channel === null && isChannel(buildChannel)) {
      hubUpdateStore.getState().setChannel(buildChannel);
    }
  }, [channel, buildChannel]);

  // Check whenever a channel is selected (mount and every change).
  useEffect(() => {
    if (channel !== null) void hubUpdateStore.getState().runCheck();
  }, [channel]);

  if (!hub) return null;

  return (
    <section className={CLASS.root} aria-labelledby="hub-updates-heading">
      <h3 id="hub-updates-heading" className={CLASS.heading}>
        Updates
      </h3>
      <p className={CLASS.status}>
        Running {hub.version ?? "unknown"}
        {hub.commit !== undefined && <FieldDim> ({hub.commit})</FieldDim>}
        {buildChannel !== undefined && <FieldDim> · {buildChannel}</FieldDim>}
      </p>

      {isDev ? (
        <p className={CLASS.status}>
          <FieldDim>
            Dev build. Rebuild with <Code>make build-hub</Code> to update.
          </FieldDim>
        </p>
      ) : (
        <>
          <RadioGroup
            label="Channel"
            value={channel ?? ""}
            onChange={(value) => {
              if (isChannel(value)) hubUpdateStore.getState().setChannel(value);
            }}
            options={CHANNEL_OPTIONS}
            disabled={applying || restarting}
          />

          <p className={CLASS.status} role="status">
            {restarting && (
              <>
                <Loader label="Restarting hub" /> Restarting hub…
              </>
            )}
            {!restarting && restartTimedOut && "The hub didn't come back within 30s. Check its logs."}
            {!restarting && !restartTimedOut && checking && "Checking…"}
            {!restarting && !restartTimedOut && !checking && checkError !== null && (
              <>Couldn't check for updates: {checkError}</>
            )}
            {!restarting && !restartTimedOut && !checking && checkError === null && check !== null && (
              check.updateAvailable
                ? `Update available: ${check.channel} ${shortCommit(check.latestCommit ?? "")}, running ${check.currentVersion}`
                : `Up to date on ${check.channel} (${shortCommit(check.latestCommit ?? "")})`
            )}
          </p>
          {applyError !== null && <p className={CLASS.status}>Update failed: {friendlyErrorMessage(applyError)}</p>}

          <div className={CLASS.actions}>
            <Button size="sm" onClick={() => void hubUpdateStore.getState().runCheck()} disabled={checking || applying || restarting}>
              Check for updates
            </Button>
            <Button
              size="sm"
              onClick={() => setConfirming(true)}
              disabled={!(check?.updateAvailable ?? false) || applying || restarting}
            >
              Update and restart
            </Button>
          </div>

          <ConfirmDialog
            open={confirming}
            title="Update and restart the hub?"
            confirmLabel="Yes, update and restart"
            busy={applying}
            onConfirm={() => {
              setConfirming(false);
              void hubUpdateStore.getState().apply();
            }}
            onCancel={() => setConfirming(false)}
          >
            The hub goes offline for a few seconds while the new binary starts. Running sessions keep going.
          </ConfirmDialog>
        </>
      )}
    </section>
  );
}
```

Check `friendlyErrorMessage`'s signature in `protocol/errors.ts` (it may take the raw string; adapt). Check whether `Loader` is exported from `../../../widgets/index.ts`; if not, import it from `../../../widgets/loader`. If `RadioGroup`'s `value` must match an option, render the group only when `channel !== null`.

In `hub.tsx`, add `import { HubUpdates } from "./hubUpdates";` and render `<HubUpdates />` right after the closing `</dl>` inside `CLASS.root`.

- [ ] **Step 4: Run Biome, the test file, and the full web gate**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/panes/settings/sections/hubUpdates.tsx src/panes/settings/sections/hubUpdates.test.tsx src/panes/settings/sections/hub.tsx && npx vitest run src/panes/settings/sections/ 2>&1 | tail -5 && cd ../../.. && make test-web 2>&1 | tail -4`
Expected: all pass; `PASS web-typecheck`, `PASS web-test`, `PASS web-lint`. Existing `hub.test.tsx` must still pass: its `SAMPLE_RESPONSE` has no `buildChannel`, so `HubUpdates` renders only the "Running" line and no check request; if its `fake.calls` assertion breaks because a check fired, that is a bug in the seeding guard, not the test.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.tsx cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.module.css cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections/hub.tsx
git commit -m "hub web: Updates block in Settings -> Hub with channel selector and update-and-restart"
```

---

### Task 7: Opt-in end-to-end test, docs, full gates

**Files:**
- Create: `cmd/evener-hub/update_e2e_test.go`
- Modify: `docs/evener-hub.md` (new subsection after the "deploy-hub.sh" paragraph ~line 310)

**Interfaces:**
- Consumes: everything above; `install.sh` (`PREFIX`, `EVENER_INSTALL_VERSION`), `/api/health`, `appwire.Client` (`appwire.Dial` or whatever constructor `appwire/client.go` exposes; read it) and the hub's bearer token in `<hub_state_root>/` (`hubedge.TokenFileName`).

- [ ] **Step 1: Write the e2e test**

```go
//go:build unix

package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUpdateApplyEndToEnd installs the current snapshot into a temp prefix,
// runs that hub, applies an update through the RPC, and proves the process
// replaced itself in place: same PID, /api/health back. Opt-in only: it
// downloads from GitHub twice (AGENTS.md forbids network in default tests).
//
//	EVENER_UPDATE_E2E=1 go test ./cmd/evener-hub/ -run TestUpdateApplyEndToEnd -v
func TestUpdateApplyEndToEnd(t *testing.T) {
	if os.Getenv("EVENER_UPDATE_E2E") == "" {
		t.Skip("set EVENER_UPDATE_E2E=1 to run (downloads from GitHub)")
	}
	root := t.TempDir()
	prefix := filepath.Join(root, "local")
	install := exec.Command("sh", repoFile(t, "install.sh"))
	install.Env = append(os.Environ(), "PREFIX="+prefix, "EVENER_INSTALL_VERSION=snapshot")
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	binary := filepath.Join(prefix, "bin", "evener")

	stateRoot := filepath.Join(root, "state")
	hub := exec.Command(binary, "hub", "-addr", "127.0.0.1:0")
	hub.Env = append(os.Environ(), "EVENER_HUB_STATE_ROOT="+stateRoot, "HOME="+root)
	hub.Stderr = os.Stderr
	// The hub prints its bound address; read it from the rendezvous/health
	// surface the current hub exposes (see runMain's listener log line) —
	// adapt the discovery below to whatever the hub actually prints.
	stdout, err := hub.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.Start(); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	t.Cleanup(func() { _ = hub.Process.Kill() })
	addr := waitForListenAddr(t, stdout)
	pid := hub.Process.Pid

	before := healthVersion(t, addr)
	client := dialHubForTest(t, addr, stateRoot)
	resp, err := client.UpdateApply(context.Background(), appwireUpdateApplyParams("snapshot"))
	if err != nil {
		t.Fatalf("UpdateApply: %v", err)
	}
	if !resp.Restarting {
		t.Fatalf("resp = %+v", resp)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		r, err := http.Get("http://" + addr + "/api/health")
		if err != nil {
			continue
		}
		var body struct{ Version string `json:"version"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = r.Body.Close()
		if body.Version != "" {
			if !processAlive(pid) {
				t.Fatalf("hub pid %d died", pid)
			}
			t.Logf("before=%s after=%s pid=%d", before, body.Version, pid)
			return
		}
	}
	t.Fatal("hub did not come back within 30s")
}
```

Then fill in the helpers (`repoFile`, `waitForListenAddr`, `healthVersion`, `dialHubForTest`, `appwireUpdateApplyParams`, `processAlive`) by reading what the package already has: search `cmd/evener-hub/*_test.go` for an existing helper that starts a real hub binary and dials it over appwire (grep `exec.Command` and `appwire.Dial`/`NewClient` in test files, and `EVENER_HUB_STATE_ROOT`/`hubStateRoot` for the env var name). Reuse those helpers rather than writing new ones; if none exist, write the smallest versions that work and keep them in this file. Replace the placeholder names above with the real ones.

- [ ] **Step 2: Run it for real, once**

Run: `EVENER_UPDATE_E2E=1 go test ./cmd/evener-hub/ -run TestUpdateApplyEndToEnd -v 2>&1 | tail -15`
Expected: PASS with a `before=... after=... pid=...` log line. Then confirm it skips by default: `go test ./cmd/evener-hub/ -run TestUpdateApplyEndToEnd -v 2>&1 | grep -c SKIP` → `1`.

- [ ] **Step 3: Docs**

In `docs/evener-hub.md`, after the `deploy-hub.sh` paragraph, add:

```markdown
### Updating the hub from Settings

Settings → Hub → Updates shows the running build (version, commit, channel),
a channel selector (release or snapshot), and whether that channel is ahead
of the running build. "Update and restart" downloads and installs the
channel's archive with the same code as `evener upgrade`, then the hub
`exec`s the installed binary in place: same PID, same arguments, same
environment. That is why it works the same under launchd, systemd, or a
plain shell, and why nothing needs `KeepAlive`. The `hub.lock` flock and the
listener are released by the exec and re-acquired by the new process; the
page polls `/api/health` until the new version answers, then reloads.

The channel selector has no stored setting. It defaults to the channel the
running binary was built for, and after an update the installed binary's
channel becomes the new default.

Dev builds (a worktree `make build-hub`, channel `dev`) are excluded:
Settings shows a rebuild note instead of the controls, and
`evener/update/apply` is refused. Use `make build-hub` or
`scripts/ops/deploy-hub.sh` for those.

Running session daemons keep the binary they were spawned from (see the
"Existing daemons keep the `evener` binary" note below); restart a session
to move it to the new build.
```

- [ ] **Step 4: Full gates**

Run: `make lint 2>&1 | tail -5 && make vet 2>&1 | tail -3 && make test 2>&1 | tail -8`
Expected: all clean. Fix anything that fails before committing.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/update_e2e_test.go docs/evener-hub.md
git commit -m "hub: opt-in self-update e2e test and docs"
```

---

## Self-review notes

- Spec coverage: Check (T1), Restart (T2), wire types + BuildChannel (T3), hub handlers + registration + restart scheduling (T4), store (T5), Settings UI (T6), e2e + docs (T7). Snapshot-release asset cleanup is a one-off `gh` command done by the plan owner, not a task.
- Type consistency: `runHubUpdateCheck`/`scheduleHubRestart` names match between T4 code and tests; `UpdateCheckResponse` fields match T3 ↔ T5/T6 (`currentVersion`, `latestCommit`, `updateAvailable`, `applicable`, `buildChannel`); store method is `runCheck` everywhere (not `check`, which is the result field).
