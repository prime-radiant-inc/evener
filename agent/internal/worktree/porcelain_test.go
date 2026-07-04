package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs git with dir as its working directory, failing the test on a
// non-zero exit so fixture setup errors surface immediately rather than as a
// confusing downstream assertion failure.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Env,
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"HOME="+dir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// buildPorcelainFixture spins up a real git repo (git 2.43, the version this
// package is developed against) with one worktree of each kind the parser
// must handle: a plain branch worktree, a detached-HEAD worktree, a
// worktree locked with a reason containing spaces, one locked with a reason
// containing a newline (which git C-quotes — spec §5), and one made
// prunable by moving its directory away without telling git (mirrors a
// user/OS deleting a worktree's directory out from under git, which is what
// makes `prunable` show up in real porcelain output — never `git worktree
// remove`, which would just deregister it cleanly). It returns the raw
// `git worktree list --porcelain` output plus the repo's canonical root so
// callers can build expectations.
func buildPorcelainFixture(t *testing.T) (porcelain, repoRoot string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	// git canonicalizes worktree paths when it registers them (macOS
	// t.TempDir() lands under /var, a symlink to /private/var), so repo must
	// already be resolved here for the porcelain entries parsed below to
	// compare equal against it.
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")

	runGit(t, repo, "worktree", "add", "-q", "wt-branch", "-b", "branch1")
	runGit(t, repo, "worktree", "add", "-q", "--detach", "wt-detached")
	runGit(t, repo, "worktree", "add", "-q", "wt-locked-space", "-b", "branch2")
	runGit(t, repo, "worktree", "lock", "wt-locked-space", "--reason", "reason with spaces")
	runGit(t, repo, "worktree", "add", "-q", "wt-locked-newline", "-b", "branch3")
	runGit(t, repo, "worktree", "lock", "wt-locked-newline", "--reason", "line one\nline two")
	runGit(t, repo, "worktree", "add", "-q", "wt-prunable", "-b", "branch4")
	if err := os.Rename(filepath.Join(repo, "wt-prunable"), filepath.Join(base, "wt-prunable-moved-away")); err != nil {
		t.Fatalf("move wt-prunable away: %v", err)
	}

	out := runGit(t, repo, "worktree", "list", "--porcelain")
	return out, repo
}

func TestParsePorcelain_RealGitFixture(t *testing.T) {
	out, repo := buildPorcelainFixture(t)
	entries := ParsePorcelain(out)

	byPath := make(map[string]PorcelainEntry, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if len(entries) != 6 {
		t.Fatalf("got %d entries, want 6 (raw output:\n%s)", len(entries), out)
	}

	main, ok := byPath[repo]
	if !ok {
		t.Fatalf("no entry for main worktree %q; entries: %+v", repo, entries)
	}
	if main.Bare {
		t.Errorf("main worktree: Bare = true, want false")
	}
	if main.Branch != "refs/heads/main" {
		t.Errorf("main worktree: Branch = %q, want refs/heads/main", main.Branch)
	}
	if main.Head == "" {
		t.Errorf("main worktree: Head is empty")
	}

	branchEntry, ok := byPath[filepath.Join(repo, "wt-branch")]
	if !ok {
		t.Fatalf("no entry for wt-branch")
	}
	if branchEntry.Branch != "refs/heads/branch1" {
		t.Errorf("wt-branch: Branch = %q, want refs/heads/branch1", branchEntry.Branch)
	}
	if branchEntry.Detached || branchEntry.Locked || branchEntry.Prunable {
		t.Errorf("wt-branch: unexpected flags set: %+v", branchEntry)
	}

	detached, ok := byPath[filepath.Join(repo, "wt-detached")]
	if !ok {
		t.Fatalf("no entry for wt-detached")
	}
	if !detached.Detached {
		t.Errorf("wt-detached: Detached = false, want true")
	}
	if detached.Branch != "" {
		t.Errorf("wt-detached: Branch = %q, want empty", detached.Branch)
	}

	lockedSpace, ok := byPath[filepath.Join(repo, "wt-locked-space")]
	if !ok {
		t.Fatalf("no entry for wt-locked-space")
	}
	if !lockedSpace.Locked {
		t.Errorf("wt-locked-space: Locked = false, want true")
	}
	if lockedSpace.LockReason != "reason with spaces" {
		t.Errorf("wt-locked-space: LockReason = %q, want %q", lockedSpace.LockReason, "reason with spaces")
	}

	lockedNewline, ok := byPath[filepath.Join(repo, "wt-locked-newline")]
	if !ok {
		t.Fatalf("no entry for wt-locked-newline")
	}
	if !lockedNewline.Locked {
		t.Errorf("wt-locked-newline: Locked = false, want true")
	}
	if lockedNewline.LockReason != "line one\nline two" {
		t.Errorf("wt-locked-newline: LockReason = %q, want %q", lockedNewline.LockReason, "line one\nline two")
	}

	prunable, ok := byPath[filepath.Join(repo, "wt-prunable")]
	if !ok {
		t.Fatalf("no entry for wt-prunable")
	}
	if !prunable.Prunable {
		t.Errorf("wt-prunable: Prunable = false, want true")
	}
	if prunable.PrunableReason == "" {
		t.Errorf("wt-prunable: PrunableReason is empty")
	}
}

func TestParsePorcelain_BareRepo(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "bare.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, repo, "init", "-q", "--bare", "-b", "main")
	out := runGit(t, repo, "worktree", "list", "--porcelain")

	entries := ParsePorcelain(out)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (raw:\n%s)", len(entries), out)
	}
	if !entries[0].Bare {
		t.Errorf("bare repo entry: Bare = false, want true")
	}
}

func TestParsePorcelain_QuotedReasonWithEscapes(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// See buildPorcelainFixture: repo must be symlink-resolved to compare
	// equal against git's own (canonicalized) porcelain path entries.
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")
	runGit(t, repo, "worktree", "add", "-q", "wt-quote", "-b", "branchq")
	reason := "he said \"hi\" and used a \\backslash\\ and a\ttab"
	runGit(t, repo, "worktree", "lock", "wt-quote", "--reason", reason)

	out := runGit(t, repo, "worktree", "list", "--porcelain")
	if !strings.Contains(out, `locked "he said \"hi\" and used a \\backslash\\ and a\tt`) {
		t.Fatalf("fixture sanity check failed; raw porcelain locked line not C-quoted as expected:\n%s", out)
	}

	entries := ParsePorcelain(out)
	var found *PorcelainEntry
	for i := range entries {
		if entries[i].Path == filepath.Join(repo, "wt-quote") {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("no entry for wt-quote")
	}
	if found.LockReason != reason {
		t.Errorf("LockReason = %q, want %q", found.LockReason, reason)
	}
}

func TestCUnquote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain unquoted passes through", "reason with spaces", "reason with spaces"},
		{"empty string", "", ""},
		{"simple quoted no escapes", `"hello"`, "hello"},
		{"newline escape", `"line one\nline two"`, "line one\nline two"},
		{"tab escape", `"a\tb"`, "a\tb"},
		{"quote escape", `"he said \"hi\""`, `he said "hi"`},
		{"backslash escape", `"a\\b"`, `a\b`},
		{"octal escape", `"bell\001end"`, "bell\x01end"},
		{"multibyte via octal (café)", `"caf\303\251"`, "café"},
		{"unquoted string that merely starts with a quote char is untouched", `"unterminated`, `"unterminated`},
		{"unrecognized escape is preserved verbatim", `"a\zb"`, `a\zb`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CUnquote(c.in)
			if got != c.want {
				t.Errorf("CUnquote(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParsePorcelain_EmptyInput(t *testing.T) {
	if got := ParsePorcelain(""); len(got) != 0 {
		t.Errorf("ParsePorcelain(\"\") = %+v, want empty", got)
	}
}
