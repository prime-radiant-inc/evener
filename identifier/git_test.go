package identifier

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	full := append([]string{"-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func evalSym(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(resolved)
}

func TestParseGitdirPointer(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"none", "garbage", "", false},
		{"skips empty", "gitdir:   \ngitdir: /repo/.git/worktrees/wt\n", "/repo/.git/worktrees/wt", true},
		{"trims line", " other\ngitdir: relative/path \n", "relative/path", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseGitdirPointer(tt.input)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ParseGitdirPointer = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestMainRootFromGitdirPointer(t *testing.T) {
	if got, ok := MainRootFromGitdirPointer("gitdir: /repo/.git/worktrees/feature\n", "/ignored"); !ok || got != "/repo" {
		t.Fatalf("absolute pointer = %q, %v", got, ok)
	}
	if got, ok := MainRootFromGitdirPointer("gitdir: ../main/.git/worktrees/feature\n", "/tmp/wt"); !ok || got != "/tmp/main" {
		t.Fatalf("relative pointer = %q, %v", got, ok)
	}
	for _, input := range []string{"garbage", "gitdir: /repo/.git/modules/sub\n"} {
		if _, ok := MainRootFromGitdirPointer(input, "/tmp"); ok {
			t.Fatalf("accepted non-worktree pointer %q", input)
		}
	}
	if _, ok := MainRootFromGitdirPointer("gitdir: worktrees/id\n", ""); ok {
		t.Fatal("accepted root-collapsing relative pointer")
	}
}

func TestMainRootCandidateFromCommonDir(t *testing.T) {
	if got := MainRootCandidateFromCommonDir("/wt/abc", "/main/.git"); got != "/main" {
		t.Fatalf("absolute common = %q", got)
	}
	if got := MainRootCandidateFromCommonDir("/repo", ".git"); got != "/repo" {
		t.Fatalf("relative common = %q", got)
	}
	if got := MainRootCandidateFromCommonDir("", ""); got != "" {
		t.Fatalf("empty common = %q", got)
	}
}

func TestGitEntryResolvesToCommon(t *testing.T) {
	candidate := t.TempDir()
	common := filepath.Join(candidate, ".git")
	if err := os.MkdirAll(common, 0o755); err != nil {
		t.Fatal(err)
	}
	if !GitEntryResolvesToCommon(candidate, common) {
		t.Fatal("directory .git did not resolve to common")
	}
	other := filepath.Join(t.TempDir(), ".git")
	if GitEntryResolvesToCommon(candidate, other) {
		t.Fatal("mismatched directory .git resolved to common")
	}

	pointerCandidate := t.TempDir()
	pointerCommon := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(pointerCommon, "worktrees", "id"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pointerCandidate, ".git"), []byte("gitdir: "+filepath.Join(pointerCommon, "worktrees", "id")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !GitEntryResolvesToCommon(pointerCandidate, pointerCommon) {
		t.Fatal("pointer .git did not resolve to common")
	}
}

func TestMainCheckoutLocal_NonRepository(t *testing.T) {
	if root, isGit, err := mainCheckoutLocal(t.TempDir()); err != nil || isGit || root != "" {
		t.Fatalf("non-repository = %q, %v, %v", root, isGit, err)
	}
}

func TestMainCheckoutLocal_MalformedPointer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mainCheckoutLocal(dir); err == nil {
		t.Fatal("malformed pointer did not return an error")
	}
}

func TestFilteredGitEnvironmentCaseInsensitive(t *testing.T) {
	input := []string{
		"PATH=/bin", "gIt_DiR=/hostile/dir", "git_work_tree=/hostile/tree",
		"Git_Common_Dir=/hostile/common", "GIT_INDEX_FILE=/hostile/index",
		"gIt_oBjEcT_dIrEcToRy=/hostile/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/hostile/alternates",
		"git_ceiling_directories=/hostile/ceiling",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1", "SERF_UNRELATED=value",
	}
	got := filteredGitEnvironment(input)
	joined := strings.Join(got, "\n")
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
	} {
		if strings.Contains(strings.ToUpper(joined), key+"=") {
			t.Fatalf("filtered environment retained %s: %v", key, got)
		}
	}
	for _, entry := range []string{"PATH=/bin", "SERF_UNRELATED=value"} {
		if !strings.Contains(joined, entry) {
			t.Fatalf("filtered environment removed %s: %v", entry, got)
		}
	}
}

func TestSubmoduleGitDirShape(t *testing.T) {
	for _, path := range []string{
		filepath.Join("/repo", ".git", "modules", "sub"),
		filepath.Join("/repo", ".git", "modules", "sub", "modules", "nested"),
	} {
		if !isSubmoduleGitDirShape(path) {
			t.Fatalf("isSubmoduleGitDirShape(%q) = false", path)
		}
	}
	for _, path := range []string{
		filepath.Join("/repo", ".git"),
		filepath.Join("/repo", ".git", "worktrees", "wt"),
		filepath.Join("/repo", "other", "modules", "sub"),
	} {
		if isSubmoduleGitDirShape(path) {
			t.Fatalf("isSubmoduleGitDirShape(%q) = true", path)
		}
	}
}
