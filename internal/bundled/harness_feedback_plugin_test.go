package bundled

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Not parallel: Plugins() reads the package-wide subFS seam that
// TestMustSubPanics swaps out.
func TestHarnessFeedbackBundledPluginAssets(t *testing.T) {
	rawManifest, err := fs.ReadFile(Plugins(), "harness-feedback/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatalf("read bundled plugin manifest: %v", err)
	}
	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if manifest.Name != "harness-feedback" {
		t.Fatalf("manifest name = %q, want %q", manifest.Name, "harness-feedback")
	}
	if manifest.Version == "" || manifest.Description == "" {
		t.Fatalf("manifest missing version or description: %+v", manifest)
	}

	meta := bundledMeta(t, Plugins(), "harness-feedback/skills/harness-feedback/SKILL.md")
	if name := bundledMetaString(t, meta, "name"); name != "harness-feedback" {
		t.Fatalf("skill name = %q, want %q", name, "harness-feedback")
	}
	_ = bundledMetaString(t, meta, "description")

	scriptBytes, err := fs.ReadFile(Plugins(), "harness-feedback/skills/harness-feedback/scripts/harness-feedback")
	if err != nil {
		t.Fatalf("read bundled CLI helper: %v", err)
	}
	if len(bytes.TrimSpace(scriptBytes)) == 0 {
		t.Fatal("bundled CLI helper is empty")
	}
}

type recordedGHCall struct {
	Args []string `json:"args"`
}

type cliHarness struct {
	python3    string
	scriptPath string
	binDir     string
	logPath    string
	statePath  string
}

func newHarnessFeedbackCLI(t *testing.T, scriptPath string, state map[string]any, evenerVersion string, includeGH bool) *cliHarness {
	t.Helper()
	python3, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 required for harness-feedback CLI tests: %v", err)
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "gh-calls.jsonl")
	statePath := filepath.Join(root, "gh-state.json")
	if state == nil {
		state = map[string]any{}
	}
	rawState, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, rawState, 0o600); err != nil {
		t.Fatal(err)
	}

	if includeGH {
		ghPy := filepath.Join(root, "fake_gh.py")
		const fakeGHSource = `import json, os, sys

state_path = os.environ["FAKE_GH_STATE"]
log_path = os.environ["FAKE_GH_LOG"]
args = sys.argv[1:]

with open(log_path, "a", encoding="utf-8") as fh:
    fh.write(json.dumps({"args": args}) + "\n")

with open(state_path, "r", encoding="utf-8") as fh:
    state = json.load(fh)

if state.get("fail_stderr"):
    sys.stderr.write(state["fail_stderr"] + "\n")
    sys.exit(int(state.get("fail_code", 1)))

if len(args) >= 2 and args[0] == "issue" and args[1] == "list":
    sys.stdout.write(json.dumps(state.get("issues", [])))
    sys.exit(0)

if len(args) >= 2 and args[0] == "api":
    endpoint = args[1]
    if endpoint.endswith("/reactions"):
        sys.stdout.write(json.dumps({"id": 901, "content": "+1"}))
        sys.exit(0)
    routes = state.get("api", {})
    if endpoint in routes:
        entry = routes[endpoint]
        if isinstance(entry, dict) and entry.get("__error__"):
            sys.stderr.write(str(entry["__error__"]) + "\n")
            sys.exit(int(entry.get("__code__", 1)))
        sys.stdout.write(json.dumps(entry))
        sys.exit(0)
    sys.stderr.write(f"HTTP 404: Not Found ({endpoint})\n")
    sys.exit(1)

if len(args) >= 2 and args[0] == "issue" and args[1] == "comment":
    sys.stdout.write(state.get("comment_url", "https://github.com/prime-radiant-inc/evener/issues/42#issuecomment-1") + "\n")
    sys.exit(0)

if len(args) >= 2 and args[0] == "issue" and args[1] == "create":
    sys.stdout.write(state.get("created_url", "https://github.com/prime-radiant-inc/evener/issues/999") + "\n")
    sys.exit(0)

sys.stderr.write(f"unexpected gh invocation: {args!r}\n")
sys.exit(1)
`
		if err := os.WriteFile(ghPy, []byte(fakeGHSource), 0o600); err != nil {
			t.Fatal(err)
		}
		ghWrapper := "#!/bin/sh\nexec '" + python3 + "' '" + ghPy + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghWrapper), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if evenerVersion != "" {
		evenerScript := "#!/bin/sh\nprintf '%s\\n' '" + evenerVersion + "'\n"
		if err := os.WriteFile(filepath.Join(binDir, "evener"), []byte(evenerScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	return &cliHarness{
		python3:    python3,
		scriptPath: scriptPath,
		binDir:     binDir,
		logPath:    logPath,
		statePath:  statePath,
	}
}

func (h *cliHarness) run(args ...string) (stdout, stderr string, exitCode int) {
	cmdArgs := append([]string{h.scriptPath}, args...)
	cmd := exec.Command(h.python3, cmdArgs...)
	cmd.Env = []string{
		"PATH=" + h.binDir,
		"HOME=" + filepath.Dir(h.binDir),
		"FAKE_GH_STATE=" + h.statePath,
		"FAKE_GH_LOG=" + h.logPath,
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func (h *cliHarness) ghCalls(t *testing.T) []recordedGHCall {
	t.Helper()
	raw, err := os.ReadFile(h.logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read gh call log: %v", err)
	}
	var calls []recordedGHCall
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var call recordedGHCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("parse gh call log line %q: %v", line, err)
		}
		calls = append(calls, call)
	}
	return calls
}

// Not parallel: extracts the embedded plugin through Plugins(), which reads the
// package-wide subFS seam that TestMustSubPanics swaps out.
func TestHarnessFeedbackCLI(t *testing.T) {
	sub, err := fs.Sub(Plugins(), "harness-feedback")
	if err != nil {
		t.Fatalf("sub harness-feedback plugin: %v", err)
	}
	extracted := t.TempDir()
	if err := os.CopyFS(extracted, sub); err != nil {
		t.Fatalf("copy embedded plugin: %v", err)
	}
	scriptPath := filepath.Join(extracted, "skills", "harness-feedback", "scripts", "harness-feedback")
	// Strip execute bits explicitly so the test exercises the exact go:embed
	// extraction permission constraint via `python3 <script>`.
	if err := os.Chmod(scriptPath, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("check partitions open and closed issues and reads evener version", func(t *testing.T) {
		t.Parallel()
		state := map[string]any{
			"issues": []map[string]any{
				{
					"number":    101,
					"title":     "tool schema timeout edge case",
					"state":     "OPEN",
					"url":       "https://github.com/prime-radiant-inc/evener/issues/101",
					"labels":    []map[string]string{{"name": "bug"}},
					"updatedAt": "2026-11-24T10:00:00Z",
					"closedAt":  nil,
					"body":      "Reproduces under high concurrency.",
				},
				{
					"number":    88,
					"title":     "fixed schema timeout in recent release",
					"state":     "CLOSED",
					"url":       "https://github.com/prime-radiant-inc/evener/issues/88",
					"labels":    []map[string]string{{"name": "fixed"}},
					"updatedAt": "2026-11-20T10:00:00Z",
					"closedAt":  "2026-11-20T10:00:00Z",
					"body":      "Fixed in v0.3.30.",
				},
			},
		}
		h := newHarnessFeedbackCLI(t, scriptPath, state, "evener a968aa7 (2026-11-25T02:28:07Z)", true)
		stdout, stderr, code := h.run("check", "--query", "schema timeout", "--limit", "5", "--json")
		if code != 0 {
			t.Fatalf("check exited %d, stderr=%q", code, stderr)
		}
		var out struct {
			Repo          string `json:"repo"`
			Query         string `json:"query"`
			EvenerVersion struct {
				Available bool   `json:"available"`
				Commit    string `json:"commit"`
				Dirty     bool   `json:"dirty"`
				BuildTime string `json:"build_time"`
			} `json:"evener_version"`
			OpenIssues       []struct{ Number int } `json:"open_issues"`
			ClosedIssues     []struct{ Number int } `json:"closed_issues"`
			HasPossibleFixed bool                   `json:"has_possible_fixed"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("decode check JSON: %v\nstdout=%s", err, stdout)
		}
		if out.Repo != "prime-radiant-inc/evener" || out.Query != "schema timeout" {
			t.Fatalf("repo/query = %q/%q", out.Repo, out.Query)
		}
		if !out.EvenerVersion.Available || out.EvenerVersion.Commit != "a968aa7" || out.EvenerVersion.Dirty || out.EvenerVersion.BuildTime != "2026-11-25T02:28:07Z" {
			t.Fatalf("unexpected evener_version: %+v", out.EvenerVersion)
		}
		if len(out.OpenIssues) != 1 || out.OpenIssues[0].Number != 101 {
			t.Fatalf("open_issues = %+v, want [#101]", out.OpenIssues)
		}
		if len(out.ClosedIssues) != 1 || out.ClosedIssues[0].Number != 88 || !out.HasPossibleFixed {
			t.Fatalf("closed_issues = %+v has_possible_fixed=%v", out.ClosedIssues, out.HasPossibleFixed)
		}
	})

	t.Run("check succeeds gracefully when evener is absent from PATH", func(t *testing.T) {
		t.Parallel()
		h := newHarnessFeedbackCLI(t, scriptPath, map[string]any{"issues": []any{}}, "", true)
		stdout, stderr, code := h.run("check", "--query", "sandbox", "--json")
		if code != 0 {
			t.Fatalf("check without evener exited %d, stderr=%q", code, stderr)
		}
		var out struct {
			EvenerVersion struct {
				Available bool `json:"available"`
			} `json:"evener_version"`
			HasPossibleFixed bool `json:"has_possible_fixed"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("decode JSON: %v", err)
		}
		if out.EvenerVersion.Available {
			t.Fatal("evener_version.available = true when evener is absent")
		}
		if out.HasPossibleFixed {
			t.Fatal("has_possible_fixed = true when no closed issues matched")
		}
	})

	t.Run("check-update detects behind main, up-to-date main, release match, and dirty/missing builds", func(t *testing.T) {
		t.Parallel()
		const (
			releaseCommit = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			mainCommit    = "3f3b9ecbaca26e04c9b9af03795d5ba8b26a60a4"
		)
		baseAPI := map[string]any{
			"repos/prime-radiant-inc/evener/releases/latest": map[string]any{
				"tag_name":     "v0.3.30",
				"published_at": "2026-11-08T23:43:42Z",
				"html_url":     "https://github.com/prime-radiant-inc/evener/releases/tag/v0.3.30",
			},
			"repos/prime-radiant-inc/evener/commits/v0.3.30": map[string]any{"sha": releaseCommit},
			"repos/prime-radiant-inc/evener/commits/main": map[string]any{
				"sha":      mainCommit,
				"html_url": "https://github.com/prime-radiant-inc/evener/commit/" + mainCommit,
				"commit":   map[string]any{"committer": map[string]any{"date": "2026-11-25T01:55:00Z"}},
			},
			"repos/prime-radiant-inc/evener/compare/a968aa7...main": map[string]any{
				"status":    "ahead",
				"ahead_by":  3,
				"behind_by": 0,
			},
		}

		cases := []struct {
			name             string
			evenerVersion    string
			wantAvailable    bool
			wantStatus       string
			wantBehind       int
			wantMainAhead    bool
			wantUpgradeCmd   string
			wantSnapshotHint string
		}{
			{
				name:             "behind main",
				evenerVersion:    "evener a968aa7 (2026-11-25T01:00:00Z)",
				wantAvailable:    true,
				wantStatus:       "update_available",
				wantBehind:       3,
				wantMainAhead:    true,
				wantUpgradeCmd:   "evener upgrade",
				wantSnapshotHint: "evener upgrade snapshot",
			},
			{
				name:             "on latest main",
				evenerVersion:    "evener 3f3b9ec (2026-11-25T01:55:00Z)",
				wantAvailable:    false,
				wantStatus:       "up_to_date_main",
				wantMainAhead:    true,
				wantUpgradeCmd:   "",
				wantSnapshotHint: "",
			},
			{
				name:             "on latest release with main ahead",
				evenerVersion:    "evener 1111111 (2026-11-08T23:43:42Z)",
				wantAvailable:    false,
				wantStatus:       "up_to_date_release",
				wantMainAhead:    true,
				wantUpgradeCmd:   "",
				wantSnapshotHint: "evener upgrade snapshot",
			},
			{
				name:             "dirty local build",
				evenerVersion:    "evener 3f3b9ec-dirty (2026-11-25T02:00:00Z)",
				wantAvailable:    true,
				wantStatus:       "dirty_local_build",
				wantMainAhead:    true,
				wantUpgradeCmd:   "evener upgrade",
				wantSnapshotHint: "evener upgrade snapshot",
			},
			{
				name:             "missing evener binary",
				evenerVersion:    "",
				wantAvailable:    true,
				wantStatus:       "unknown_local_build",
				wantMainAhead:    true,
				wantUpgradeCmd:   "evener upgrade",
				wantSnapshotHint: "evener upgrade snapshot",
			},
		}

		for _, tc := range cases {
			h := newHarnessFeedbackCLI(t, scriptPath, map[string]any{"api": baseAPI}, tc.evenerVersion, true)
			stdout, stderr, code := h.run("check-update", "--json")
			if code != 0 {
				t.Fatalf("%s: check-update exited %d, stderr=%q", tc.name, code, stderr)
			}
			var out struct {
				Status                 string `json:"status"`
				UpdateAvailable        bool   `json:"update_available"`
				MainAheadOfRelease     bool   `json:"main_ahead_of_release"`
				CommitsBehindMain      *int   `json:"commits_behind_main"`
				UpgradeCommand         string `json:"upgrade_command"`
				SnapshotUpgradeCommand string `json:"snapshot_upgrade_command"`
			}
			if err := json.Unmarshal([]byte(stdout), &out); err != nil {
				t.Fatalf("%s: decode JSON: %v\nstdout=%s", tc.name, err, stdout)
			}
			if out.UpdateAvailable != tc.wantAvailable || out.Status != tc.wantStatus {
				t.Errorf("%s: got update_available=%v status=%q, want %v %q", tc.name, out.UpdateAvailable, out.Status, tc.wantAvailable, tc.wantStatus)
			}
			if out.MainAheadOfRelease != tc.wantMainAhead {
				t.Errorf("%s: main_ahead_of_release=%v, want %v", tc.name, out.MainAheadOfRelease, tc.wantMainAhead)
			}
			if tc.wantBehind > 0 && (out.CommitsBehindMain == nil || *out.CommitsBehindMain != tc.wantBehind) {
				t.Errorf("%s: commits_behind_main=%v, want %d", tc.name, out.CommitsBehindMain, tc.wantBehind)
			}
			if out.UpgradeCommand != tc.wantUpgradeCmd {
				t.Errorf("%s: upgrade_command=%q, want %q", tc.name, out.UpgradeCommand, tc.wantUpgradeCmd)
			}
			if out.SnapshotUpgradeCommand != tc.wantSnapshotHint {
				t.Errorf("%s: snapshot_upgrade_command=%q, want %q", tc.name, out.SnapshotUpgradeCommand, tc.wantSnapshotHint)
			}
		}
	})

	t.Run("upvote supports dry-run and posts +1 reaction", func(t *testing.T) {
		t.Parallel()
		h := newHarnessFeedbackCLI(t, scriptPath, nil, "", true)
		stdout, stderr, code := h.run("upvote", "42", "--dry-run", "--json")
		if code != 0 {
			t.Fatalf("upvote --dry-run exited %d, stderr=%q", code, stderr)
		}
		if calls := h.ghCalls(t); len(calls) != 0 {
			t.Fatalf("upvote --dry-run invoked gh: %+v", calls)
		}
		var dry struct {
			Action      string `json:"action"`
			DryRun      bool   `json:"dry_run"`
			IssueNumber int    `json:"issue_number"`
			Reaction    string `json:"reaction"`
		}
		if err := json.Unmarshal([]byte(stdout), &dry); err != nil {
			t.Fatalf("decode dry-run JSON: %v", err)
		}
		if dry.Action != "upvote" || !dry.DryRun || dry.IssueNumber != 42 || dry.Reaction != "+1" {
			t.Fatalf("unexpected dry-run payload: %+v", dry)
		}

		stdout, stderr, code = h.run("upvote", "42", "--json")
		if code != 0 {
			t.Fatalf("upvote exited %d, stderr=%q", code, stderr)
		}
		calls := h.ghCalls(t)
		if len(calls) != 1 {
			t.Fatalf("expected 1 gh call, got %+v", calls)
		}
		want := []string{"api", "repos/prime-radiant-inc/evener/issues/42/reactions", "-f", "content=+1"}
		if !slices.Equal(calls[0].Args, want) {
			t.Fatalf("gh args = %v, want %v", calls[0].Args, want)
		}
		var live struct {
			DryRun     bool `json:"dry_run"`
			ReactionID int  `json:"reaction_id"`
		}
		if err := json.Unmarshal([]byte(stdout), &live); err != nil {
			t.Fatalf("decode live JSON: %v", err)
		}
		if live.DryRun || live.ReactionID != 901 {
			t.Fatalf("unexpected live upvote payload: %+v", live)
		}
	})

	t.Run("comment supports dry-run and attaches version and tool metadata", func(t *testing.T) {
		t.Parallel()
		h := newHarnessFeedbackCLI(t, scriptPath, nil, "evener a968aa7 (2026-11-25T02:28:07Z)", true)
		stdout, stderr, code := h.run("comment", "42", "--details", "Fails on read-only bind mount", "--tool", "apply_patch", "--dry-run", "--json")
		if code != 0 {
			t.Fatalf("comment --dry-run exited %d, stderr=%q", code, stderr)
		}
		if calls := h.ghCalls(t); len(calls) != 0 {
			t.Fatalf("comment --dry-run invoked gh: %+v", calls)
		}
		var dry struct {
			Action      string `json:"action"`
			DryRun      bool   `json:"dry_run"`
			IssueNumber int    `json:"issue_number"`
			Body        string `json:"body"`
		}
		if err := json.Unmarshal([]byte(stdout), &dry); err != nil {
			t.Fatalf("decode comment dry-run JSON: %v", err)
		}
		if dry.Action != "comment" || !dry.DryRun || dry.IssueNumber != 42 {
			t.Fatalf("unexpected comment dry-run metadata: %+v", dry)
		}
		for _, sentinel := range []string{"a968aa7", "apply_patch", "Fails on read-only bind mount"} {
			if !strings.Contains(dry.Body, sentinel) {
				t.Errorf("comment body missing %q:\n%s", sentinel, dry.Body)
			}
		}

		stdout, stderr, code = h.run("comment", "42", "--details", "Fails on read-only bind mount", "--tool", "apply_patch", "--json")
		if code != 0 {
			t.Fatalf("comment live exited %d, stderr=%q", code, stderr)
		}
		calls := h.ghCalls(t)
		if len(calls) != 1 || calls[0].Args[0] != "issue" || calls[0].Args[1] != "comment" || calls[0].Args[2] != "42" {
			t.Fatalf("unexpected gh comment call: %+v", calls)
		}
		var live struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(stdout), &live); err != nil {
			t.Fatalf("decode comment live JSON: %v", err)
		}
		if live.URL == "" {
			t.Fatalf("expected comment URL in live payload: %s", stdout)
		}
	})

	t.Run("report supports dry-run and creates structured issue with metadata", func(t *testing.T) {
		t.Parallel()
		h := newHarnessFeedbackCLI(t, scriptPath, nil, "evener a968aa7 (2026-11-25T02:28:07Z)", true)
		stdout, stderr, code := h.run(
			"report",
			"--title", "apply_patch rejects CRLF files",
			"--summary", "Patching a CRLF file fails without line-ending guidance.",
			"--details", "Observed on fixture_crlf.txt.",
			"--category", "tool_error",
			"--tool", "apply_patch",
			"--dry-run", "--json",
		)
		if code != 0 {
			t.Fatalf("report --dry-run exited %d, stderr=%q", code, stderr)
		}
		if calls := h.ghCalls(t); len(calls) != 0 {
			t.Fatalf("report --dry-run invoked gh: %+v", calls)
		}
		var dry struct {
			Action   string `json:"action"`
			DryRun   bool   `json:"dry_run"`
			Title    string `json:"title"`
			Category string `json:"category"`
			Body     string `json:"body"`
		}
		if err := json.Unmarshal([]byte(stdout), &dry); err != nil {
			t.Fatalf("decode report dry-run JSON: %v", err)
		}
		if dry.Action != "report" || !dry.DryRun || dry.Title != "apply_patch rejects CRLF files" || dry.Category != "tool_error" {
			t.Fatalf("unexpected report dry-run payload: %+v", dry)
		}
		for _, sentinel := range []string{"a968aa7", "tool_error", "apply_patch", "fixture_crlf.txt"} {
			if !strings.Contains(dry.Body, sentinel) {
				t.Errorf("report body missing %q:\n%s", sentinel, dry.Body)
			}
		}

		stdout, stderr, code = h.run(
			"report",
			"--title", "apply_patch rejects CRLF files",
			"--summary", "Patching a CRLF file fails without line-ending guidance.",
			"--details", "Observed on fixture_crlf.txt.",
			"--category", "tool_error",
			"--tool", "apply_patch",
			"--json",
		)
		if code != 0 {
			t.Fatalf("report live exited %d, stderr=%q", code, stderr)
		}
		calls := h.ghCalls(t)
		if len(calls) != 1 || calls[0].Args[0] != "issue" || calls[0].Args[1] != "create" {
			t.Fatalf("unexpected gh create call: %+v", calls)
		}
		var live struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(stdout), &live); err != nil {
			t.Fatalf("decode report live JSON: %v", err)
		}
		if live.URL == "" {
			t.Fatalf("expected created issue URL: %s", stdout)
		}
	})

	t.Run("secret hygiene refuses obvious credentials without calling gh or echoing secret", func(t *testing.T) {
		t.Parallel()
		// Build synthetic tokens dynamically so static secret scanners do not see
		// credential-shaped literals in test source.
		secrets := []struct {
			name  string
			value string
		}{
			{"ghp", "ghp_" + strings.Repeat("a", 24)},
			{"gho", "gho_" + strings.Repeat("b", 24)},
			{"sk", "sk-" + strings.Repeat("c", 24)},
			{"xoxb", "xoxb-" + strings.Repeat("1", 12) + "-" + strings.Repeat("d", 12)},
			{"xoxp", "xoxp-" + strings.Repeat("2", 12) + "-" + strings.Repeat("e", 12)},
			{"akia", "AKIA" + strings.Repeat("Z", 16)},
			{"pem", "-----BEGIN " + "RSA PRIVATE KEY-----"},
		}
		for _, tc := range secrets {
			h := newHarnessFeedbackCLI(t, scriptPath, nil, "", true)
			stdout, stderr, code := h.run(
				"report",
				"--title", "Repro note",
				"--summary", "Short summary",
				"--details", "leaked value: "+tc.value,
			)
			if code == 0 {
				t.Errorf("%s (report): expected non-zero exit on secret payload", tc.name)
			}
			if calls := h.ghCalls(t); len(calls) != 0 {
				t.Errorf("%s (report): gh was invoked despite secret: %+v", tc.name, calls)
			}
			if strings.Contains(stdout, tc.value) || strings.Contains(stderr, tc.value) {
				t.Errorf("%s (report): secret value echoed in output", tc.name)
			}

			stdout, stderr, code = h.run(
				"comment", "42",
				"--details", "token "+tc.value,
				"--dry-run",
			)
			if code == 0 {
				t.Errorf("%s (comment --dry-run): expected non-zero exit on secret payload", tc.name)
			}
			if calls := h.ghCalls(t); len(calls) != 0 {
				t.Errorf("%s (comment --dry-run): gh was invoked despite secret: %+v", tc.name, calls)
			}
			if strings.Contains(stdout, tc.value) || strings.Contains(stderr, tc.value) {
				t.Errorf("%s (comment --dry-run): secret value echoed in output", tc.name)
			}
		}
	})

	t.Run("missing gh exits non-zero gracefully", func(t *testing.T) {
		t.Parallel()
		h := newHarnessFeedbackCLI(t, scriptPath, nil, "", false)
		for _, args := range [][]string{
			{"check", "--query", "sandbox"},
			{"check-update"},
			{"upvote", "42"},
			{"comment", "42", "--details", "extra detail"},
			{"report", "--title", "t", "--summary", "s", "--details", "d"},
		} {
			_, stderr, code := h.run(args...)
			if code == 0 {
				t.Errorf("args %v: expected non-zero exit when gh is missing", args)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Errorf("args %v: expected diagnostic stderr when gh is missing", args)
			}
		}
	})

	t.Run("failing gh auth exits non-zero cleanly", func(t *testing.T) {
		t.Parallel()
		h := newHarnessFeedbackCLI(t, scriptPath, map[string]any{
			"fail_stderr": "To get started with GitHub CLI, please run:  gh auth login",
			"fail_code":   4,
		}, "", true)
		_, stderr, code := h.run("check", "--query", "sandbox")
		if code == 0 {
			t.Fatal("expected non-zero exit on unauthenticated gh")
		}
		if strings.TrimSpace(stderr) == "" {
			t.Fatal("expected diagnostic stderr on unauthenticated gh")
		}
	})

	t.Run("repo override works before or after subcommand and rejects malformed slugs", func(t *testing.T) {
		t.Parallel()
		h := newHarnessFeedbackCLI(t, scriptPath, map[string]any{"issues": []any{}}, "", true)
		stdout, stderr, code := h.run("--repo", "acme/evener-fork", "check", "--query", "foo", "--json")
		if code != 0 {
			t.Fatalf("global --repo exited %d, stderr=%q", code, stderr)
		}
		var out struct {
			Repo string `json:"repo"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.Repo != "acme/evener-fork" {
			t.Fatalf("global --repo decoded %q (err=%v)", out.Repo, err)
		}

		stdout, stderr, code = h.run("check", "--repo", "acme/other-fork", "--query", "foo", "--json")
		if code != 0 {
			t.Fatalf("subcommand --repo exited %d, stderr=%q", code, stderr)
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.Repo != "acme/other-fork" {
			t.Fatalf("subcommand --repo decoded %q (err=%v)", out.Repo, err)
		}

		before := len(h.ghCalls(t))
		for _, bad := range []string{"../evil", "owner/..", "-flag/repo", "noslash", "a/b/c"} {
			_, _, code = h.run("--repo", bad, "upvote", "1")
			if code == 0 {
				t.Errorf("malformed --repo %q should fail", bad)
			}
		}
		if len(h.ghCalls(t)) != before {
			t.Fatal("malformed --repo should not invoke gh")
		}
	})

	t.Run("human-readable output succeeds across all subcommands", func(t *testing.T) {
		t.Parallel()
		state := map[string]any{
			"issues": []map[string]any{
				{"number": 7, "title": "sample", "state": "OPEN", "url": "https://github.com/prime-radiant-inc/evener/issues/7", "labels": []any{}},
				{"number": 6, "title": "solved", "state": "CLOSED", "url": "https://github.com/prime-radiant-inc/evener/issues/6", "closedAt": "2026-11-20T00:00:00Z", "labels": []any{}},
			},
			"api": map[string]any{
				"repos/prime-radiant-inc/evener/releases/latest": map[string]any{"tag_name": "v0.3.30"},
				"repos/prime-radiant-inc/evener/commits/v0.3.30": map[string]any{"sha": "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				"repos/prime-radiant-inc/evener/commits/main":    map[string]any{"sha": "3f3b9ecbaca26e04c9b9af03795d5ba8b26a60a4", "commit": map[string]any{"committer": map[string]any{"date": "2026-11-25T01:55:00Z"}}},
			},
		}
		h := newHarnessFeedbackCLI(t, scriptPath, state, "evener a968aa7 (2026-11-25T02:28:07Z)", true)
		for _, args := range [][]string{
			{"--help"},
			{"check", "--query", "sample"},
			{"check-update"},
			{"upvote", "7", "--dry-run"},
			{"comment", "7", "--details", "extra detail", "--dry-run"},
			{"report", "--title", "t", "--summary", "s", "--details", "d", "--dry-run"},
		} {
			stdout, stderr, code := h.run(args...)
			if code != 0 {
				t.Errorf("args %v exited %d, stderr=%q", args, code, stderr)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Errorf("args %v produced empty stdout", args)
			}
		}
	})
}
