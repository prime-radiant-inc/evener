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
func TestEvenerFeedbackBundledPluginAssets(t *testing.T) {
	rawManifest, err := fs.ReadFile(Plugins(), "evener-feedback/.claude-plugin/plugin.json")
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
	if manifest.Name != "evener-feedback" {
		t.Fatalf("manifest name = %q, want %q", manifest.Name, "evener-feedback")
	}
	if manifest.Version == "" || manifest.Description == "" {
		t.Fatalf("manifest missing version or description: %+v", manifest)
	}

	meta := bundledMeta(t, Plugins(), "evener-feedback/skills/reporting-feedback/SKILL.md")
	if name := bundledMetaString(t, meta, "name"); name != "reporting-feedback" {
		t.Fatalf("skill name = %q, want %q", name, "reporting-feedback")
	}
	_ = bundledMetaString(t, meta, "description")

	scriptBytes, err := fs.ReadFile(Plugins(), "evener-feedback/skills/reporting-feedback/scripts/evener-feedback")
	if err != nil {
		t.Fatalf("read bundled CLI helper: %v", err)
	}
	if len(bytes.TrimSpace(scriptBytes)) == 0 {
		t.Fatal("bundled CLI helper is empty")
	}
}

type cliSpec struct {
	ID            string         `json:"id"`
	Args          []string       `json:"args"`
	EvenerVersion string         `json:"evener_version,omitempty"`
	IncludeGH     bool           `json:"include_gh"`
	State         map[string]any `json:"state,omitempty"`
}

type cliResult struct {
	Stdout   string     `json:"stdout"`
	Stderr   string     `json:"stderr"`
	ExitCode int        `json:"exit_code"`
	GHCalls  [][]string `json:"gh_calls"`
}

// runBatchedCLI executes every CLI scenario against the exact extracted script
// inside one Python interpreter so internal/bundled stays well inside its
// package CPU/wall timing budget. The real subprocess entrypoint and stripped
// execute bit are verified separately in TestEvenerFeedbackCLI.
func runBatchedCLI(t *testing.T, python3, scriptPath string, specs []cliSpec) map[string]cliResult {
	t.Helper()
	const driver = `
import contextlib, importlib.machinery, importlib.util, io, json, shutil, subprocess, sys

script_path = sys.argv[1]
specs = json.load(sys.stdin)

loader = importlib.machinery.SourceFileLoader("evener_feedback_cli", script_path)
spec = importlib.util.spec_from_loader(loader.name, loader)
mod = importlib.util.module_from_spec(spec)
loader.exec_module(mod)

def fake_gh(args, state):
    if state.get("fail_stderr"):
        return int(state.get("fail_code", 1)), "", str(state["fail_stderr"]) + "\n"
    if len(args) >= 2 and args[0] == "issue" and args[1] == "list":
        return 0, json.dumps(state.get("issues", [])), ""
    if len(args) >= 2 and args[0] == "pr" and args[1] == "list":
        if state.get("pr_list_error"):
            return 1, "", str(state["pr_list_error"]) + "\n"
        return 0, json.dumps(state.get("merged_prs", [])), ""
    if len(args) >= 2 and args[0] == "api":
        endpoint = args[1]
        if endpoint.endswith("/reactions"):
            return 0, json.dumps({"id": 901, "content": "+1"}), ""
        routes = state.get("api", {})
        if endpoint in routes:
            entry = routes[endpoint]
            if isinstance(entry, dict) and entry.get("__error__"):
                return int(entry.get("__code__", 1)), "", str(entry["__error__"]) + "\n"
            return 0, json.dumps(entry), ""
        return 1, "", f"HTTP 404: Not Found ({endpoint})\n"
    if len(args) >= 2 and args[0] == "issue" and args[1] == "comment":
        return 0, state.get("comment_url", "https://github.com/prime-radiant-inc/evener/issues/42#issuecomment-1") + "\n", ""
    if len(args) >= 2 and args[0] == "issue" and args[1] == "create":
        return 0, state.get("created_url", "https://github.com/prime-radiant-inc/evener/issues/999") + "\n", ""
    return 1, "", f"unexpected gh invocation: {args!r}\n"

results = {}
for item in specs:
    state = item.get("state") or {}
    include_gh = bool(item.get("include_gh"))
    evener_version = item.get("evener_version") or ""
    gh_calls = []

    def which_stub(name, _include_gh=include_gh, _evener_version=evener_version):
        if name == "gh":
            return "/fake/bin/gh" if _include_gh else None
        if name == "evener":
            return "/fake/bin/evener" if _evener_version else None
        return None

    def run_stub(cmd, *_a, _state=state, _evener_version=evener_version, _gh_calls=gh_calls, **_kw):
        if cmd[0] == "evener":
            return subprocess.CompletedProcess(cmd, 0, _evener_version + "\n", "")
        if cmd[0] == "gh":
            _gh_calls.append(list(cmd[1:]))
            code, out, err = fake_gh(list(cmd[1:]), _state)
            return subprocess.CompletedProcess(cmd, code, out, err)
        raise AssertionError(f"unexpected subprocess: {cmd!r}")

    old_which, old_run = mod.shutil.which, mod.subprocess.run
    mod.shutil.which, mod.subprocess.run = which_stub, run_stub
    out_buf, err_buf = io.StringIO(), io.StringIO()
    exit_code = 0
    try:
        with contextlib.redirect_stdout(out_buf), contextlib.redirect_stderr(err_buf):
            try:
                exit_code = int(mod.main(item["args"]))
            except SystemExit as exc:
                exit_code = int(exc.code) if isinstance(exc.code, int) else (1 if exc.code else 0)
    finally:
        mod.shutil.which, mod.subprocess.run = old_which, old_run

    results[item["id"]] = {
        "stdout": out_buf.getvalue(),
        "stderr": err_buf.getvalue(),
        "exit_code": exit_code,
        "gh_calls": gh_calls,
    }

json.dump(results, sys.stdout)
`
	rawSpecs, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python3, "-B", "-c", driver, scriptPath)
	cmd.Stdin = bytes.NewReader(rawSpecs)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("batched CLI test runner failed: %v\nstderr=%s", err, errBuf.String())
	}
	var results map[string]cliResult
	if err := json.Unmarshal(outBuf.Bytes(), &results); err != nil {
		t.Fatalf("decode batched CLI results: %v\nstdout=%s", err, outBuf.String())
	}
	return results
}

// Not parallel: extracts the embedded plugin through Plugins(), which reads the
// package-wide subFS seam that TestMustSubPanics swaps out.
func TestEvenerFeedbackCLI(t *testing.T) {
	python3, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 required for evener-feedback CLI tests: %v", err)
	}
	sub, err := fs.Sub(Plugins(), "evener-feedback")
	if err != nil {
		t.Fatalf("sub evener-feedback plugin: %v", err)
	}
	extracted := t.TempDir()
	if err := os.CopyFS(extracted, sub); err != nil {
		t.Fatalf("copy embedded plugin: %v", err)
	}
	scriptPath := filepath.Join(extracted, "skills", "reporting-feedback", "scripts", "evener-feedback")
	// Strip execute bits explicitly so the test exercises the exact go:embed
	// extraction permission constraint via `python3 <script>`.
	if err := os.Chmod(scriptPath, 0o644); err != nil {
		t.Fatal(err)
	}

	// Real subprocess entrypoint check with stripped +x bit and POSIX shell stubs.
	t.Run("subprocess entrypoint runs with stripped execute bit and reads gh and evener", func(t *testing.T) {
		binDir := t.TempDir()
		evenerStub := "#!/bin/sh\nprintf 'evener a968aa7 (2026-11-25T02:28:07Z)\\n'\n"
		if err := os.WriteFile(filepath.Join(binDir, "evener"), []byte(evenerStub), 0o755); err != nil {
			t.Fatal(err)
		}
		ghStub := `#!/bin/sh
case "$1:$2" in
  issue:list)
    printf '%s\n' '[{"number":101,"title":"tool schema timeout edge case","state":"OPEN","url":"https://github.com/prime-radiant-inc/evener/issues/101","labels":[{"name":"bug"}],"updatedAt":"2026-11-24T10:00:00Z","closedAt":null,"body":"Reproduces under high concurrency."},{"number":88,"title":"fixed schema timeout in recent release","state":"CLOSED","url":"https://github.com/prime-radiant-inc/evener/issues/88","labels":[{"name":"fixed"}],"updatedAt":"2026-11-20T10:00:00Z","closedAt":"2026-11-20T10:00:00Z","body":"Fixed in v0.3.30."}]'
    ;;
  pr:list)
    printf '%s\n' '[{"number":7469,"title":"fix(execenv): read_file schema timeout handling","url":"https://github.com/prime-radiant-inc/evener/pull/7469","mergedAt":"2026-11-23T12:00:00Z","body":"Shipped fix on main."}]'
    ;;
  *)
    printf 'unexpected gh call: %s\n' "$*" >&2
    exit 1
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghStub), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(python3, "-B", scriptPath, "check", "--query", "schema timeout", "--limit", "5", "--json")
		cmd.Env = []string{"PATH=" + binDir, "HOME=" + binDir}
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			code := -1
			if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				code = ee.ExitCode()
			}
			t.Fatalf("subprocess check exited %d, stderr=%q", code, errBuf.String())
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
			MergedPRs        []struct{ Number int } `json:"merged_prs"`
			HasPossibleFixed bool                   `json:"has_possible_fixed"`
		}
		if err := json.Unmarshal(outBuf.Bytes(), &out); err != nil {
			t.Fatalf("decode subprocess check JSON: %v\nstdout=%s", err, outBuf.String())
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
		if len(out.ClosedIssues) != 1 || out.ClosedIssues[0].Number != 88 {
			t.Fatalf("closed_issues = %+v, want [#88]", out.ClosedIssues)
		}
		if len(out.MergedPRs) != 1 || out.MergedPRs[0].Number != 7469 || !out.HasPossibleFixed {
			t.Fatalf("merged_prs = %+v has_possible_fixed=%v", out.MergedPRs, out.HasPossibleFixed)
		}
	})

	const (
		releaseCommit = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		mainCommit    = "3f3b9ecbaca26e04c9b9af03795d5ba8b26a60a4"
		evenerVer     = "evener a968aa7 (2026-11-25T02:28:07Z)"
	)
	baseUpdateAPI := map[string]any{
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

	updateCases := []struct {
		id               string
		evenerVersion    string
		wantAvailable    bool
		wantStatus       string
		wantBehind       int
		wantMainAhead    bool
		wantUpgradeCmd   string
		wantSnapshotHint string
	}{
		{"update_behind", "evener a968aa7 (2026-11-25T01:00:00Z)", true, "update_available", 3, true, "evener upgrade", "evener upgrade snapshot"},
		{"update_main", "evener 3f3b9ec (2026-11-25T01:55:00Z)", false, "up_to_date_main", 0, true, "", ""},
		{"update_release", "evener 1111111 (2026-11-08T23:43:42Z)", false, "up_to_date_release", 0, true, "", "evener upgrade snapshot"},
		{"update_dirty", "evener 3f3b9ec-dirty (2026-11-25T02:00:00Z)", true, "dirty_local_build", 0, true, "evener upgrade", "evener upgrade snapshot"},
		{"update_missing", "", true, "unknown_local_build", 0, true, "evener upgrade", "evener upgrade snapshot"},
	}

	missingGHCases := []struct {
		id   string
		args []string
	}{
		{"missing_gh_check", []string{"check", "--query", "sandbox"}},
		{"missing_gh_update", []string{"check-update"}},
		{"missing_gh_upvote", []string{"upvote", "42"}},
		{"missing_gh_comment", []string{"comment", "42", "--details", "extra detail"}},
		{"missing_gh_report", []string{"report", "--title", "t", "--summary", "s", "--details", "d"}},
	}

	badRepos := []string{"../evil", "owner/..", "-flag/repo", "noslash", "a/b/c"}

	humanState := map[string]any{
		"issues": []map[string]any{
			{"number": 7, "title": "sample", "state": "OPEN", "url": "https://github.com/prime-radiant-inc/evener/issues/7", "labels": []any{}},
			{"number": 6, "title": "solved", "state": "CLOSED", "url": "https://github.com/prime-radiant-inc/evener/issues/6", "closedAt": "2026-11-20T00:00:00Z", "labels": []any{}},
		},
		"merged_prs": []map[string]any{
			{"number": 7469, "title": "fix(execenv): sample", "url": "https://github.com/prime-radiant-inc/evener/pull/7469", "mergedAt": "2026-11-23T00:00:00Z"},
		},
		"api": baseUpdateAPI,
	}
	humanCases := []struct {
		id   string
		args []string
	}{
		{"human_help", []string{"--help"}},
		{"human_check", []string{"check", "--query", "sample"}},
		{"human_update", []string{"check-update"}},
		{"human_upvote", []string{"upvote", "7", "--dry-run"}},
		{"human_comment", []string{"comment", "7", "--details", "extra detail", "--dry-run"}},
		{"human_report", []string{"report", "--title", "t", "--summary", "s", "--details", "d", "--dry-run"}},
	}

	specs := []cliSpec{
		{
			ID:        "check_no_evener",
			Args:      []string{"check", "--query", "sandbox", "--json"},
			IncludeGH: true,
			State:     map[string]any{"issues": []any{}, "merged_prs": []any{}},
		},
		{
			ID:        "check_merged_pr_only",
			Args:      []string{"check", "--query", "read_file pdf", "--json"},
			IncludeGH: true,
			State: map[string]any{
				"issues": []any{},
				"merged_prs": []map[string]any{
					{"number": 7469, "title": "feat(execenv): read_file PDF and EOF", "url": "https://github.com/prime-radiant-inc/evener/pull/7469", "mergedAt": "2026-11-23T12:00:00Z", "body": "Adds PDF and negative offset support."},
				},
			},
		},
		{
			ID:        "check_pr_list_failure_is_optional",
			Args:      []string{"check", "--query", "sandbox", "--json"},
			IncludeGH: true,
			State: map[string]any{
				"issues":        []map[string]any{{"number": 12, "title": "open bug", "state": "OPEN", "url": "https://github.com/prime-radiant-inc/evener/issues/12", "labels": []any{}}},
				"pr_list_error": "HTTP 503: Service Unavailable",
			},
		},
		{ID: "upvote_dry", Args: []string{"upvote", "42", "--dry-run", "--json"}, IncludeGH: true},
		{ID: "upvote_live", Args: []string{"upvote", "42", "--json"}, IncludeGH: true},
		{ID: "comment_dry", Args: []string{"comment", "42", "--details", "Fails on read-only bind mount", "--tool", "apply_patch", "--dry-run", "--json"}, EvenerVersion: evenerVer, IncludeGH: true},
		{ID: "comment_live", Args: []string{"comment", "42", "--details", "Fails on read-only bind mount", "--tool", "apply_patch", "--json"}, EvenerVersion: evenerVer, IncludeGH: true},
		{ID: "report_dry", Args: []string{"report", "--title", "apply_patch rejects CRLF files", "--summary", "Patching a CRLF file fails without line-ending guidance.", "--details", "Observed on fixture_crlf.txt.", "--category", "tool_error", "--tool", "apply_patch", "--dry-run", "--json"}, EvenerVersion: evenerVer, IncludeGH: true},
		{ID: "report_live", Args: []string{"report", "--title", "apply_patch rejects CRLF files", "--summary", "Patching a CRLF file fails without line-ending guidance.", "--details", "Observed on fixture_crlf.txt.", "--category", "tool_error", "--tool", "apply_patch", "--json"}, EvenerVersion: evenerVer, IncludeGH: true},
		{
			ID:        "gh_auth_fail",
			Args:      []string{"check", "--query", "sandbox"},
			IncludeGH: true,
			State:     map[string]any{"fail_stderr": "To get started with GitHub CLI, please run:  gh auth login", "fail_code": 4},
		},
		{ID: "repo_global", Args: []string{"--repo", "acme/evener-fork", "check", "--query", "foo", "--json"}, IncludeGH: true, State: map[string]any{"issues": []any{}}},
		{ID: "repo_sub", Args: []string{"check", "--repo", "acme/other-fork", "--query", "foo", "--json"}, IncludeGH: true, State: map[string]any{"issues": []any{}}},
	}
	for _, tc := range updateCases {
		specs = append(specs, cliSpec{
			ID:            tc.id,
			Args:          []string{"check-update", "--json"},
			EvenerVersion: tc.evenerVersion,
			IncludeGH:     true,
			State:         map[string]any{"api": baseUpdateAPI},
		})
	}
	for _, tc := range secrets {
		specs = append(specs,
			cliSpec{
				ID:        "secret_report_" + tc.name,
				Args:      []string{"report", "--title", "Repro note", "--summary", "Short summary", "--details", "leaked value: " + tc.value},
				IncludeGH: true,
			},
			cliSpec{
				ID:        "secret_comment_" + tc.name,
				Args:      []string{"comment", "42", "--details", "token " + tc.value, "--dry-run"},
				IncludeGH: true,
			},
		)
	}
	for _, tc := range missingGHCases {
		specs = append(specs, cliSpec{ID: tc.id, Args: tc.args, IncludeGH: false})
	}
	for _, bad := range badRepos {
		specs = append(specs, cliSpec{ID: "bad_repo_" + bad, Args: []string{"--repo", bad, "upvote", "1"}, IncludeGH: true})
	}
	for _, tc := range humanCases {
		specs = append(specs, cliSpec{ID: tc.id, Args: tc.args, EvenerVersion: evenerVer, IncludeGH: true, State: humanState})
	}

	results := runBatchedCLI(t, python3, scriptPath, specs)

	t.Run("check succeeds gracefully when evener is absent from PATH", func(t *testing.T) {
		r := results["check_no_evener"]
		if r.ExitCode != 0 {
			t.Fatalf("check without evener exited %d, stderr=%q", r.ExitCode, r.Stderr)
		}
		var out struct {
			EvenerVersion    struct{ Available bool } `json:"evener_version"`
			HasPossibleFixed bool                     `json:"has_possible_fixed"`
		}
		if err := json.Unmarshal([]byte(r.Stdout), &out); err != nil {
			t.Fatalf("decode JSON: %v", err)
		}
		if out.EvenerVersion.Available || out.HasPossibleFixed {
			t.Fatalf("unexpected payload: %+v", out)
		}
	})

	t.Run("check surfaces merged PRs as possible already-fixed candidates and tolerates pr list failure", func(t *testing.T) {
		r := results["check_merged_pr_only"]
		if r.ExitCode != 0 {
			t.Fatalf("check with merged PR exited %d, stderr=%q", r.ExitCode, r.Stderr)
		}
		var out struct {
			OpenIssues   []any `json:"open_issues"`
			ClosedIssues []any `json:"closed_issues"`
			MergedPRs    []struct {
				Number   int    `json:"number"`
				MergedAt string `json:"merged_at"`
			} `json:"merged_prs"`
			HasPossibleFixed bool `json:"has_possible_fixed"`
		}
		if err := json.Unmarshal([]byte(r.Stdout), &out); err != nil {
			t.Fatalf("decode JSON: %v", err)
		}
		if len(out.MergedPRs) != 1 || out.MergedPRs[0].Number != 7469 || out.MergedPRs[0].MergedAt == "" || !out.HasPossibleFixed {
			t.Fatalf("unexpected merged PR payload: %+v", out)
		}

		fallback := results["check_pr_list_failure_is_optional"]
		if fallback.ExitCode != 0 {
			t.Fatalf("check with failing pr list exited %d, stderr=%q", fallback.ExitCode, fallback.Stderr)
		}
		var fb struct {
			OpenIssues       []struct{ Number int } `json:"open_issues"`
			MergedPRs        []any                  `json:"merged_prs"`
			HasPossibleFixed bool                   `json:"has_possible_fixed"`
		}
		if err := json.Unmarshal([]byte(fallback.Stdout), &fb); err != nil {
			t.Fatalf("decode fallback JSON: %v", err)
		}
		if len(fb.OpenIssues) != 1 || fb.OpenIssues[0].Number != 12 || len(fb.MergedPRs) != 0 || fb.HasPossibleFixed {
			t.Fatalf("unexpected fallback payload: %+v", fb)
		}
	})

	t.Run("check-update detects behind main, up-to-date main, release match, and dirty/missing builds", func(t *testing.T) {
		for _, tc := range updateCases {
			r := results[tc.id]
			if r.ExitCode != 0 {
				t.Fatalf("%s: check-update exited %d, stderr=%q", tc.id, r.ExitCode, r.Stderr)
			}
			var out struct {
				Status                 string `json:"status"`
				UpdateAvailable        bool   `json:"update_available"`
				MainAheadOfRelease     bool   `json:"main_ahead_of_release"`
				CommitsBehindMain      *int   `json:"commits_behind_main"`
				UpgradeCommand         string `json:"upgrade_command"`
				SnapshotUpgradeCommand string `json:"snapshot_upgrade_command"`
			}
			if err := json.Unmarshal([]byte(r.Stdout), &out); err != nil {
				t.Fatalf("%s: decode JSON: %v\nstdout=%s", tc.id, err, r.Stdout)
			}
			if out.UpdateAvailable != tc.wantAvailable || out.Status != tc.wantStatus {
				t.Errorf("%s: got update_available=%v status=%q, want %v %q", tc.id, out.UpdateAvailable, out.Status, tc.wantAvailable, tc.wantStatus)
			}
			if out.MainAheadOfRelease != tc.wantMainAhead {
				t.Errorf("%s: main_ahead_of_release=%v, want %v", tc.id, out.MainAheadOfRelease, tc.wantMainAhead)
			}
			if tc.wantBehind > 0 && (out.CommitsBehindMain == nil || *out.CommitsBehindMain != tc.wantBehind) {
				t.Errorf("%s: commits_behind_main=%v, want %d", tc.id, out.CommitsBehindMain, tc.wantBehind)
			}
			if out.UpgradeCommand != tc.wantUpgradeCmd {
				t.Errorf("%s: upgrade_command=%q, want %q", tc.id, out.UpgradeCommand, tc.wantUpgradeCmd)
			}
			if out.SnapshotUpgradeCommand != tc.wantSnapshotHint {
				t.Errorf("%s: snapshot_upgrade_command=%q, want %q", tc.id, out.SnapshotUpgradeCommand, tc.wantSnapshotHint)
			}
		}
	})

	t.Run("upvote supports dry-run and posts +1 reaction", func(t *testing.T) {
		dryRes := results["upvote_dry"]
		if dryRes.ExitCode != 0 || len(dryRes.GHCalls) != 0 {
			t.Fatalf("upvote --dry-run exited %d calls=%v stderr=%q", dryRes.ExitCode, dryRes.GHCalls, dryRes.Stderr)
		}
		var dry struct {
			Action      string `json:"action"`
			DryRun      bool   `json:"dry_run"`
			IssueNumber int    `json:"issue_number"`
			Reaction    string `json:"reaction"`
		}
		if err := json.Unmarshal([]byte(dryRes.Stdout), &dry); err != nil || dry.Action != "upvote" || !dry.DryRun || dry.IssueNumber != 42 || dry.Reaction != "+1" {
			t.Fatalf("unexpected dry-run payload: %+v err=%v", dry, err)
		}

		liveRes := results["upvote_live"]
		if liveRes.ExitCode != 0 {
			t.Fatalf("upvote exited %d, stderr=%q", liveRes.ExitCode, liveRes.Stderr)
		}
		want := []string{"api", "repos/prime-radiant-inc/evener/issues/42/reactions", "-f", "content=+1"}
		if len(liveRes.GHCalls) != 1 || !slices.Equal(liveRes.GHCalls[0], want) {
			t.Fatalf("gh calls = %v, want [%v]", liveRes.GHCalls, want)
		}
		var live struct {
			DryRun     bool `json:"dry_run"`
			ReactionID int  `json:"reaction_id"`
		}
		if err := json.Unmarshal([]byte(liveRes.Stdout), &live); err != nil || live.DryRun || live.ReactionID != 901 {
			t.Fatalf("unexpected live upvote payload: %+v err=%v", live, err)
		}
	})

	t.Run("comment supports dry-run and attaches version and tool metadata", func(t *testing.T) {
		dryRes := results["comment_dry"]
		if dryRes.ExitCode != 0 || len(dryRes.GHCalls) != 0 {
			t.Fatalf("comment --dry-run exited %d calls=%v stderr=%q", dryRes.ExitCode, dryRes.GHCalls, dryRes.Stderr)
		}
		var dry struct {
			Action      string `json:"action"`
			DryRun      bool   `json:"dry_run"`
			IssueNumber int    `json:"issue_number"`
			Body        string `json:"body"`
		}
		if err := json.Unmarshal([]byte(dryRes.Stdout), &dry); err != nil || dry.Action != "comment" || !dry.DryRun || dry.IssueNumber != 42 {
			t.Fatalf("unexpected comment dry-run payload: %+v err=%v", dry, err)
		}
		for _, sentinel := range []string{"a968aa7", "apply_patch", "Fails on read-only bind mount"} {
			if !strings.Contains(dry.Body, sentinel) {
				t.Errorf("comment body missing %q:\n%s", sentinel, dry.Body)
			}
		}

		liveRes := results["comment_live"]
		if liveRes.ExitCode != 0 {
			t.Fatalf("comment live exited %d, stderr=%q", liveRes.ExitCode, liveRes.Stderr)
		}
		if len(liveRes.GHCalls) != 1 || liveRes.GHCalls[0][0] != "issue" || liveRes.GHCalls[0][1] != "comment" || liveRes.GHCalls[0][2] != "42" {
			t.Fatalf("unexpected gh comment call: %v", liveRes.GHCalls)
		}
		var live struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(liveRes.Stdout), &live); err != nil || live.URL == "" {
			t.Fatalf("expected comment URL: stdout=%s err=%v", liveRes.Stdout, err)
		}
	})

	t.Run("report supports dry-run and creates structured issue with metadata", func(t *testing.T) {
		dryRes := results["report_dry"]
		if dryRes.ExitCode != 0 || len(dryRes.GHCalls) != 0 {
			t.Fatalf("report --dry-run exited %d calls=%v stderr=%q", dryRes.ExitCode, dryRes.GHCalls, dryRes.Stderr)
		}
		var dry struct {
			Action   string `json:"action"`
			DryRun   bool   `json:"dry_run"`
			Title    string `json:"title"`
			Category string `json:"category"`
			Body     string `json:"body"`
		}
		if err := json.Unmarshal([]byte(dryRes.Stdout), &dry); err != nil || dry.Action != "report" || !dry.DryRun || dry.Title != "apply_patch rejects CRLF files" || dry.Category != "tool_error" {
			t.Fatalf("unexpected report dry-run payload: %+v err=%v", dry, err)
		}
		for _, sentinel := range []string{"a968aa7", "tool_error", "apply_patch", "fixture_crlf.txt"} {
			if !strings.Contains(dry.Body, sentinel) {
				t.Errorf("report body missing %q:\n%s", sentinel, dry.Body)
			}
		}

		liveRes := results["report_live"]
		if liveRes.ExitCode != 0 || len(liveRes.GHCalls) != 1 || liveRes.GHCalls[0][0] != "issue" || liveRes.GHCalls[0][1] != "create" {
			t.Fatalf("unexpected report live result: exit=%d calls=%v stderr=%q", liveRes.ExitCode, liveRes.GHCalls, liveRes.Stderr)
		}
		var live struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(liveRes.Stdout), &live); err != nil || live.URL == "" {
			t.Fatalf("expected created issue URL: stdout=%s err=%v", liveRes.Stdout, err)
		}
	})

	t.Run("secret hygiene refuses obvious credentials without calling gh or echoing secret", func(t *testing.T) {
		for _, tc := range secrets {
			for _, prefix := range []string{"secret_report_", "secret_comment_"} {
				r := results[prefix+tc.name]
				if r.ExitCode == 0 {
					t.Errorf("%s%s: expected non-zero exit on secret payload", prefix, tc.name)
				}
				if len(r.GHCalls) != 0 {
					t.Errorf("%s%s: gh was invoked despite secret: %v", prefix, tc.name, r.GHCalls)
				}
				if strings.Contains(r.Stdout, tc.value) || strings.Contains(r.Stderr, tc.value) {
					t.Errorf("%s%s: secret value echoed in output", prefix, tc.name)
				}
			}
		}
	})

	t.Run("missing gh exits non-zero gracefully", func(t *testing.T) {
		for _, tc := range missingGHCases {
			r := results[tc.id]
			if r.ExitCode == 0 || strings.TrimSpace(r.Stderr) == "" {
				t.Errorf("%s: exit=%d stderr=%q, want non-zero with diagnostic", tc.id, r.ExitCode, r.Stderr)
			}
		}
	})

	t.Run("failing gh auth exits non-zero cleanly", func(t *testing.T) {
		r := results["gh_auth_fail"]
		if r.ExitCode == 0 || strings.TrimSpace(r.Stderr) == "" {
			t.Fatalf("gh auth fail: exit=%d stderr=%q", r.ExitCode, r.Stderr)
		}
	})

	t.Run("repo override works before or after subcommand and rejects malformed slugs", func(t *testing.T) {
		for _, tc := range []struct{ id, want string }{
			{"repo_global", "acme/evener-fork"},
			{"repo_sub", "acme/other-fork"},
		} {
			r := results[tc.id]
			if r.ExitCode != 0 {
				t.Fatalf("%s exited %d, stderr=%q", tc.id, r.ExitCode, r.Stderr)
			}
			var out struct {
				Repo string `json:"repo"`
			}
			if err := json.Unmarshal([]byte(r.Stdout), &out); err != nil || out.Repo != tc.want {
				t.Fatalf("%s decoded %q, want %q (err=%v)", tc.id, out.Repo, tc.want, err)
			}
		}
		for _, bad := range badRepos {
			r := results["bad_repo_"+bad]
			if r.ExitCode == 0 || len(r.GHCalls) != 0 {
				t.Errorf("malformed --repo %q: exit=%d calls=%v", bad, r.ExitCode, r.GHCalls)
			}
		}
	})

	t.Run("human-readable output succeeds across all subcommands", func(t *testing.T) {
		for _, tc := range humanCases {
			r := results[tc.id]
			if r.ExitCode != 0 || strings.TrimSpace(r.Stdout) == "" {
				t.Errorf("%s: exit=%d stdout=%q stderr=%q", tc.id, r.ExitCode, r.Stdout, r.Stderr)
			}
		}
	})

	t.Run("SKILL.md command reference runs cleanly under bash -o pipefail -c", func(t *testing.T) {
		bashPath, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("bash not available on PATH")
		}
		rawSkill, err := fs.ReadFile(Plugins(), "evener-feedback/skills/reporting-feedback/SKILL.md")
		if err != nil {
			t.Fatalf("read SKILL.md: %v", err)
		}
		_, afterFence, ok := strings.Cut(string(rawSkill), "```sh\n")
		if !ok {
			t.Fatal("SKILL.md missing sh fenced command reference")
		}
		block, _, ok := strings.Cut(afterFence, "\n```")
		if !ok {
			t.Fatal("SKILL.md missing closing fence on sh command reference")
		}
		skillDir := filepath.Join(t.TempDir(), "evener feedback skill")
		scriptsDir := filepath.Join(skillDir, "scripts")
		if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
			t.Fatalf("mkdir scripts dir: %v", err)
		}
		rawScript, err := fs.ReadFile(Plugins(), "evener-feedback/skills/reporting-feedback/scripts/evener-feedback")
		if err != nil {
			t.Fatalf("read bundled script: %v", err)
		}
		if err := os.WriteFile(filepath.Join(scriptsDir, "evener-feedback"), rawScript, 0o644); err != nil {
			t.Fatalf("write script: %v", err)
		}
		var snippetLines []string
		foundUpvote := false
		for line := range strings.SplitSeq(block, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			expanded := strings.ReplaceAll(trimmed, "<base_directory>", skillDir)
			if strings.Contains(expanded, " upvote ") {
				expanded = strings.ReplaceAll(expanded, "<issue_number>", "42")
				expanded = strings.ReplaceAll(expanded, "[--dry-run]", "--dry-run")
				expanded = strings.ReplaceAll(expanded, "[--json]", "--json")
				snippetLines = append(snippetLines, expanded)
				foundUpvote = true
				break
			}
			if !strings.Contains(expanded, " check ") && !strings.Contains(expanded, " check-update ") && !strings.Contains(expanded, " comment ") && !strings.Contains(expanded, " report ") {
				snippetLines = append(snippetLines, expanded)
			}
		}
		if !foundUpvote {
			t.Fatalf("SKILL.md sh block missing upvote command:\n%s", block)
		}
		snippet := strings.Join(snippetLines, "\n")
		cmd := exec.Command(bashPath, "-o", "pipefail", "-c", snippet)
		cmd.Env = []string{"PATH=" + filepath.Dir(python3) + ":/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("SKILL.md command reference failed under bash -o pipefail -c:\nsnippet:\n%s\nerr: %v\noutput:\n%s", snippet, err, out)
		}
		var parsed struct {
			Action      string `json:"action"`
			IssueNumber int    `json:"issue_number"`
			DryRun      bool   `json:"dry_run"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil || parsed.Action != "upvote" || parsed.IssueNumber != 42 || !parsed.DryRun {
			t.Fatalf("unexpected upvote dry-run output: %q (err=%v)", out, err)
		}
	})
}
