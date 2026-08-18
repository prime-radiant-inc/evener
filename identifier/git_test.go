package identifier

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	missing := filepath.Join(t.TempDir(), "missing", ".git")
	if got := MainRootCandidateFromCommonDir(filepath.Dir(missing), missing); got != filepath.Dir(missing) {
		t.Fatalf("missing common changed lexically: got %q, want %q", got, filepath.Dir(missing))
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

// TestMainCheckoutLocalRejectsPointersThatDoNotDescribeARealCheckout covers the
// guards between a `.git` FILE and a project identity.
//
// A pointer file is plain text that anything can write, and mainCheckoutLocal
// turns it into the directory whose path becomes the project ID. Each rejection
// below is therefore the difference between refusing an unusable repository and
// silently filing a session under some other project's identity — including one
// outside the checkout entirely.
//
// No git binary is involved: every case is a directory shape on disk, which is
// exactly what the validators inspect.
func TestMainCheckoutLocalRejectsPointersThatDoNotDescribeARealCheckout(t *testing.T) {
	// A worktree-shaped pointer (…/worktrees/<name>) takes the linked-worktree
	// path; anything else falls through to the submodule check.
	for _, tc := range []struct {
		name    string
		build   func(t *testing.T, dir string) string // returns the pointer target
		wantErr string
	}{
		{
			name: "linked worktree gitdir does not exist",
			build: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "main", ".git", "worktrees", "wt")
			},
			wantErr: "linked worktree Git directory",
		},
		{
			name: "linked worktree gitdir is a file",
			build: func(t *testing.T, dir string) string {
				target := filepath.Join(dir, "main", ".git", "worktrees", "wt")
				mustDir(t, filepath.Dir(target))
				mustFile(t, target)
				return target
			},
			wantErr: "is not a directory",
		},
		{
			// The pointer is worktree-shaped, but the checkout it implies has no
			// .git of its own — so nothing anchors it to a real repository.
			name: "implied main checkout has no .git",
			build: func(t *testing.T, dir string) string {
				target := filepath.Join(dir, "main", "notgit", "worktrees", "wt")
				mustDir(t, target)
				return target
			},
			wantErr: "main checkout Git directory",
		},
		{
			// Both exist, but the pointer's common directory is not the main
			// checkout's .git — the shape a pointer copied between repositories
			// produces, and the one that would silently borrow another project's
			// identity.
			name: "gitdir belongs to a different checkout",
			build: func(t *testing.T, dir string) string {
				target := filepath.Join(dir, "main", "notgit", "worktrees", "wt")
				mustDir(t, target)
				mustDir(t, filepath.Join(dir, "main", ".git"))
				return target
			},
			wantErr: "does not match main checkout",
		},
		{
			name: "submodule pointer target does not exist",
			build: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "nowhere", "modules", "sub")
			},
			wantErr: "git pointer target",
		},
		{
			name: "submodule pointer target is a file",
			build: func(t *testing.T, dir string) string {
				target := filepath.Join(dir, "store", "sub")
				mustDir(t, filepath.Dir(target))
				mustFile(t, target)
				return target
			},
			wantErr: "is not a directory",
		},
		{
			name: "submodule pointer target is not shaped like a submodule gitdir",
			build: func(t *testing.T, dir string) string {
				target := filepath.Join(dir, "somewhere", "else")
				mustDir(t, target)
				return target
			},
			wantErr: "not a submodule Git directory",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "work")
			mustDir(t, work)
			target := tc.build(t, root)
			mustFile(t, filepath.Join(work, ".git"))
			if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: "+target+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			gotRoot, isGit, err := mainCheckoutLocal(work)
			if err == nil {
				t.Fatalf("accepted pointer to %q: root=%q isGit=%v", target, gotRoot, isGit)
			}
			if gotRoot != "" {
				t.Fatalf("returned root %q alongside error %v, want empty", gotRoot, err)
			}
			if !isGit {
				t.Fatalf("isGit=false for a directory that carries a .git pointer (err=%v)", err)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func mustDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFilteredGitEnvironmentCaseInsensitive(t *testing.T) {
	input := []string{
		"PATH=/bin", "gIt_DiR=/hostile/dir", "git_work_tree=/hostile/tree",
		"Git_Common_Dir=/hostile/common", "GIT_INDEX_FILE=/hostile/index",
		"gIt_oBjEcT_dIrEcToRy=/hostile/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/hostile/alternates",
		"git_ceiling_directories=/hostile/ceiling",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1", "EVENER_UNRELATED=value",
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
	for _, entry := range []string{"PATH=/bin", "EVENER_UNRELATED=value"} {
		if !strings.Contains(joined, entry) {
			t.Fatalf("filtered environment removed %s: %v", entry, got)
		}
	}
}

func TestSubmoduleGitDirShape(t *testing.T) {
	for _, path := range []string{
		"/repo/.git/modules/sub",
		"/repo/.git/modules/sub/modules/nested",
	} {
		if !isSubmoduleGitDirShape(path) {
			t.Fatalf("isSubmoduleGitDirShape(%q) = false", path)
		}
	}
	for _, path := range []string{
		"/repo/.git",
		"/repo/.git/worktrees/wt",
		"/repo/other/modules/sub",
	} {
		if isSubmoduleGitDirShape(path) {
			t.Fatalf("isSubmoduleGitDirShape(%q) = true", path)
		}
	}
}
