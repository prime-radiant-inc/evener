package identifier

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// This file closes coverage gaps in the identifier package. Two uncovered
// statements are reachable and get tests; two are truly unreachable defensive
// code and are documented below.
//
// Unreachable statements (per coverage-floors.txt comment):
//
//   project.go:146 — ValidateProjectID's base62 suffix loop error return:
//     The preceding character loop (project.go:136) rejects any byte that is
//     not an ASCII alphanumeric or '-'. When that loop passes, every byte in
//     value is alphanumeric or '-'. The suffix is the substring after the last
//     '-', so it contains only alphanumerics. isBase62 is identical to
//     isASCIIAlphaNumeric, so every suffix byte is base62. The error at line 146
//     cannot fire for any input that passes the first loop.
//
//   project.go:167 — projectID's empty-readable fallback:
//     readableProjectPath returns "project" (a non-empty string) whenever the
//     path produces no readable parts, and returns a joined non-empty string
//     otherwise. The only way readable becomes "" at line 166 is after
//     trimming leading hyphens from a max-readable substring (line 164), but
//     that substring's final byte is always alphanumeric (readableProjectPath
//     trims trailing hyphens from each part), so TrimLeft('-', ...) cannot
//     empty it. The fallback is unreachable.

// TestCovGitBinaryMainRootLocal_WorktreeCandidateHit covers git.go:201 — the
// `return candidate, true, nil` success path where
// MainRootCandidateFromCommonDir returns a non-empty candidate and
// GitEntryResolvesToCommon confirms it. This path is only reached inside
// gitBinaryMainRootLocal, which mainCheckoutLocal dispatches to when the .git
// entry is neither a directory (main checkout) nor a linked-worktree pointer
// — i.e. the submodule-pointer branch. To reach line 201, we build a
// submodule-shaped .git pointer (target under .git/modules/) whose gitdir is a
// bare repo carrying a commondir file pointing back to the main repo's .git.
// Then git rev-parse --git-common-dir returns the main repo's .git absolute
// path, MainRootCandidateFromCommonDir yields the repo root, and the main
// repo's .git is a directory that resolves to common — so the early return at
// line 201 fires.
//
// The existing TestMainCheckoutLocal_RealGitRepo calls mainCheckoutLocal on
// the main repo itself, where .git is a directory and the function returns
// early at the directory branch, never reaching gitBinaryMainRootLocal. The
// existing submodule tests exercise gitBinaryMainRootLocal but either the
// common-dir resolution fails (non-git dir) or the candidate check fails and
// --show-toplevel fails (bare repo), so they cover the error returns but not
// the success return at line 201.
func TestCovGitBinaryMainRootLocal_WorktreeCandidateHit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := mustMkdir(t, filepath.Join(t.TempDir(), "repo"))
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "t@t")
	runGit(t, root, "config", "user.name", "t")

	// Create a submodule-shaped gitdir: a bare repo at root/.git/modules/sub.
	// isSubmoduleGitDirShape recognizes the .git/modules/<name> path, so
	// validateSubmodulePointer passes and mainCheckoutLocal dispatches to
	// gitBinaryMainRootLocal instead of erroring.
	modulesDir := filepath.Join(root, ".git", "modules", "sub")
	mustDir(t, modulesDir)
	runGit(t, modulesDir, "init", "--bare", ".")
	// commondir points relatively (../..) back to the main repo's .git, so
	// `git rev-parse --git-common-dir` in the work dir returns root/.git.
	if err := os.WriteFile(filepath.Join(modulesDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Work directory whose .git file points at the submodule-shaped gitdir.
	work := mustMkdir(t, filepath.Join(root, "submodule-work"))
	if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: "+modulesDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// mainCheckoutLocal(work): .git is a file (not a dir), the pointer is not a
	// worktrees-shape pointer (MainRootFromGitdirPointer returns false), but it
	// is a submodule shape (validateSubmodulePointer passes), so control reaches
	// gitBinaryMainRootLocal. There git rev-parse --git-common-dir returns
	// root/.git, MainRootCandidateFromCommonDir gives root, and root/.git is a
	// directory resolving to common — so line 201 returns candidate=root.
	gotRoot, isGit, err := mainCheckoutLocal(work)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isGit {
		t.Fatal("expected isGit=true")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != filepath.Clean(resolvedRoot) {
		t.Fatalf("got root %q, want %q", gotRoot, filepath.Clean(resolvedRoot))
	}
}

// TestCovDecodeUUID_Overflow128Bit covers uuid.go:62 — the
// `return value, errInvalidUUIDPayload` branch that fires when a 22-character
// base62 payload decodes to a number exceeding 128 bits.
//
// A payload of 22 'z' characters (the highest base62 digit) decodes to
// 62^22 - 1, which has a BitLen of 131 — greater than 128 — so the overflow
// check at line 61 rejects it. This is the only reachable path to line 62:
// shorter or non-base62 payloads are rejected by the length check (line 42)
// or the character lookup (line 55) before the BitLen check.
func TestCovDecodeUUID_Overflow128Bit(t *testing.T) {
	payload := "zzzzzzzzzzzzzzzzzzzzzz" // 22 'z' chars, decodes to 62^22-1 (131 bits)
	got, err := DecodeUUID(payload)
	if !errors.Is(err, errInvalidUUIDPayload) {
		t.Fatalf("DecodeUUID overflow error = %v, want errInvalidUUIDPayload", err)
	}
	if got != uuid.Nil {
		t.Fatalf("DecodeUUID overflow value = %v, want zero UUID", got)
	}
}
