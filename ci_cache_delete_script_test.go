package evener_test

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ghStub stands in for gh: it logs every call and answers `cache list` with
// the entries of GH_STUB_ENTRIES whose key starts with the requested --key.
const ghStub = `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >> "$GH_STUB_LOG"
case "$1 $2" in
"cache list")
	if [ "${GH_STUB_LIST_FAILS:-}" = "true" ]; then exit 1; fi
	key=""
	shift 2
	while [ $# -gt 0 ]; do
		case "$1" in
		--key) key="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	while read -r id name; do
		[ -z "$id" ] && continue
		case "$name" in "$key"*) printf '%s %s\n' "$id" "$name" ;; esac
	done <<< "${GH_STUB_ENTRIES:-}"
	;;
"cache delete")
	if [ "${GH_STUB_DELETE_FAILS:-}" = "true" ]; then exit 1; fi
	;;
esac
exit 0
`

// runCacheDeleteScript runs a shipped delete step's script with the stub gh
// first on PATH, and returns its output and the gh calls it made.
func runCacheDeleteScript(t *testing.T, script string, vars map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "gh")
	if err := os.WriteFile(stub, []byte(ghStub), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "gh.log")
	env := []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GH_STUB_LOG=" + log,
		"GITHUB_REPOSITORY=owner/repo",
	}
	for name, value := range vars {
		env = append(env, name+"="+value)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("delete script failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		// No log file means the script never called gh, which several cases
		// below require.
		if errors.Is(err, os.ErrNotExist) {
			return string(out), nil
		}
		t.Fatalf("reading the stub gh log: %v\n%s", err, out)
	}
	var calls []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return string(out), calls
}

// deletedIDs lists the ids the script passed to `gh cache delete`.
func deletedIDs(calls []string) []string {
	var ids []string
	for _, call := range calls {
		fields := strings.Fields(call)
		if len(fields) >= 3 && fields[0] == "cache" && fields[1] == "delete" {
			ids = append(ids, fields[2])
		}
	}
	return ids
}

// The shipped delete steps are shell, so shell is where they are checked: the
// predecessor goes, the key this run saves stays, an exact-hit re-run deletes
// nothing, and a run whose restore never happened deletes nothing at all
// (issue #7025). An empty --key would list every cache in the repository,
// Go and iOS alike, so that last case is the one that must never reach gh.
func TestCacheDeleteScriptAgainstStubGH(t *testing.T) {
	sha := strings.Repeat("a", 40)
	newer := strings.Repeat("b", 40)
	buildPrefix := "go-tests-root-ubuntu24-amd64-"
	buildKey := buildPrefix + sha
	modulesPrefix := "gomod-ubuntu24-amd64-"
	modulesKey := modulesPrefix + sha
	entries := strings.Join([]string{
		"11 " + buildPrefix + newer,
		"12 " + buildKey,
		"21 " + modulesPrefix + newer,
		"22 " + modulesKey,
		"31 ios-deriveddata-macos26-xcode-26.6-" + newer,
	}, "\n")

	script := ""
	for _, step := range readActionSteps(t, ".github/actions/go-cache-save/action.yml") {
		if step.Name == "Delete the cache entries these saves supersede" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("go-cache-save has no delete step")
	}
	base := map[string]string{
		"BUILD_PREFIX":         buildPrefix,
		"BUILD_KEY":            buildKey,
		"MODULES_PREFIX":       modulesPrefix,
		"MODULES_KEY":          modulesKey,
		"MODULES":              "true",
		"GO_CACHE_EXACT_HIT":   "false",
		"GO_MODULES_EXACT_HIT": "false",
		"GH_STUB_ENTRIES":      entries,
	}

	t.Run("deletes each predecessor and keeps the key it saves", func(t *testing.T) {
		out, calls := runCacheDeleteScript(t, script, base)
		if got, want := strings.Join(deletedIDs(calls), ","), "11,21"; got != want {
			t.Fatalf("deleted %q, want %q\n%s", got, want, out)
		}
	})
	t.Run("an exact hit deletes nothing", func(t *testing.T) {
		vars := maps.Clone(base)
		vars["GO_CACHE_EXACT_HIT"] = "true"
		vars["GO_MODULES_EXACT_HIT"] = "true"
		out, calls := runCacheDeleteScript(t, script, vars)
		if ids := deletedIDs(calls); len(ids) != 0 {
			t.Fatalf("deleted %v on an exact hit\n%s", ids, out)
		}
	})
	t.Run("no prefix deletes nothing", func(t *testing.T) {
		out, calls := runCacheDeleteScript(t, script, map[string]string{
			"MODULES":         "true",
			"GH_STUB_ENTRIES": entries,
		})
		if len(calls) != 0 {
			t.Fatalf("called gh with no prefix: %v\n%s", calls, out)
		}
		if !strings.Contains(out, "::warning::") {
			t.Errorf("no warning for an unset prefix:\n%s", out)
		}
	})
	t.Run("a failed listing keeps the predecessors", func(t *testing.T) {
		vars := maps.Clone(base)
		vars["GH_STUB_LIST_FAILS"] = "true"
		out, calls := runCacheDeleteScript(t, script, vars)
		if ids := deletedIDs(calls); len(ids) != 0 {
			t.Fatalf("deleted %v after a failed listing\n%s", ids, out)
		}
		if !strings.Contains(out, "::warning::") {
			t.Errorf("no warning for a failed listing:\n%s", out)
		}
	})
	t.Run("a refused delete does not fail the lane", func(t *testing.T) {
		vars := maps.Clone(base)
		vars["GH_STUB_DELETE_FAILS"] = "true"
		out, calls := runCacheDeleteScript(t, script, vars)
		if got, want := strings.Join(deletedIDs(calls), ","), "11,21"; got != want {
			t.Fatalf("attempted deletes %q, want %q\n%s", got, want, out)
		}
		if !strings.Contains(out, "was not deleted") {
			t.Errorf("no notice for a refused delete:\n%s", out)
		}
	})

	iosScript := ""
	for _, step := range readCacheWorkflow(t, ".github/workflows/ios-qualification.yml")["simulator-build"].Steps {
		if step.Name == "Delete the DerivedData cache entry this save supersedes" {
			iosScript = step.Run
		}
	}
	if iosScript == "" {
		t.Fatal("ios-qualification simulator-build has no delete step")
	}
	iosPrefix := "ios-deriveddata-macos26-xcode-26.6-"
	iosBase := map[string]string{
		"PREFIX":   iosPrefix,
		"KEPT_KEY": iosPrefix + sha,
		"GH_STUB_ENTRIES": strings.Join([]string{
			"41 " + iosPrefix + newer,
			"42 " + iosPrefix + sha,
			"11 " + buildPrefix + newer,
		}, "\n"),
	}

	t.Run("ios deletes the predecessor and keeps this commit's key", func(t *testing.T) {
		out, calls := runCacheDeleteScript(t, iosScript, iosBase)
		if got, want := strings.Join(deletedIDs(calls), ","), "41"; got != want {
			t.Fatalf("deleted %q, want %q\n%s", got, want, out)
		}
	})
	t.Run("ios with no prefix deletes nothing", func(t *testing.T) {
		out, calls := runCacheDeleteScript(t, iosScript, map[string]string{
			"GH_STUB_ENTRIES": iosBase["GH_STUB_ENTRIES"],
		})
		if len(calls) != 0 {
			t.Fatalf("called gh with no prefix: %v\n%s", calls, out)
		}
		if !strings.Contains(out, "::warning::") {
			t.Errorf("no warning for an unset prefix:\n%s", out)
		}
	})
}
