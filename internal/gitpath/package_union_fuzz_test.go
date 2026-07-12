//go:build serffuzz

package gitpath

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// FuzzPackageUnion replays every deterministic package scenario under fuzz coverage.

func FuzzPackageUnion(f *testing.F) {

	f.Add(uint8(0))

	f.Fuzz(func(t *testing.T, _ uint8) {

		t.Run("TestStructuralWorktreeRoot_MainRepoAndSubdir", TestStructuralWorktreeRoot_MainRepoAndSubdir)
		t.Run("TestStructuralWorktreeRoot_LinkedWorktree", TestStructuralWorktreeRoot_LinkedWorktree)
		t.Run("TestResolveMainRepoRootLocal_MainRepo", TestResolveMainRepoRootLocal_MainRepo)
		t.Run("TestResolveMainRepoRootLocal_LinkedWorktree", TestResolveMainRepoRootLocal_LinkedWorktree)
		t.Run("TestResolveMainRepoRootLocal_RepoSubdir", TestResolveMainRepoRootLocal_RepoSubdir)
		t.Run("TestMainRootCandidateFromCommonDir", TestMainRootCandidateFromCommonDir)
		t.Run("TestResolveMainRepoRootLocal_Submodule", TestResolveMainRepoRootLocal_Submodule)
		t.Run("TestResolveMainRepoRootLocal_NotARepo", TestResolveMainRepoRootLocal_NotARepo)
		t.Run("TestResolveMainRepoRootLocal_BareRepo", TestResolveMainRepoRootLocal_BareRepo)
		t.Run("TestParseGitdirPointer_NoGitdirLine", TestParseGitdirPointer_NoGitdirLine)
		t.Run("TestParseGitdirPointer_SkipsNonMatchingLinesBeforeMatch", TestParseGitdirPointer_SkipsNonMatchingLinesBeforeMatch)
		t.Run("TestParseGitdirPointer_EmptyGitdirValueSkipped", TestParseGitdirPointer_EmptyGitdirValueSkipped)
		t.Run("TestMainRootFromGitdirPointer_UnparseableContent", TestMainRootFromGitdirPointer_UnparseableContent)
		t.Run("TestMainRootFromGitdirPointer_MainRootCollapsesToDot", TestMainRootFromGitdirPointer_MainRootCollapsesToDot)
		t.Run("TestMainRootCandidateFromCommonDir_RootCollapsesToDot", TestMainRootCandidateFromCommonDir_RootCollapsesToDot)
		t.Run("TestGitEntryResolvesToCommon_NoGitEntry", TestGitEntryResolvesToCommon_NoGitEntry)
		t.Run("TestGitEntryResolvesToCommon_DirEntryMatches", TestGitEntryResolvesToCommon_DirEntryMatches)
		t.Run("TestGitEntryResolvesToCommon_DirEntryMismatches", TestGitEntryResolvesToCommon_DirEntryMismatches)
		t.Run("TestGitEntryResolvesToCommon_PointerFileUnparseable", TestGitEntryResolvesToCommon_PointerFileUnparseable)
		t.Run("TestGitEntryResolvesToCommon_PointerFileAbsoluteMatches", TestGitEntryResolvesToCommon_PointerFileAbsoluteMatches)
		t.Run("TestGitEntryResolvesToCommon_PointerFileRelativeMatches", TestGitEntryResolvesToCommon_PointerFileRelativeMatches)
		t.Run("TestGitEntryResolvesToCommon_PointerFileUnreadable", TestGitEntryResolvesToCommon_PointerFileUnreadable)
		t.Run("TestGitEntryResolvesToCommon_PointerFileMismatches", TestGitEntryResolvesToCommon_PointerFileMismatches)
		t.Run("TestHasGitAncestor", TestHasGitAncestor)
		t.Run("coverage edges", fuzzGitpathCoverageEdges)
	})
}

func fuzzGitpathCoverageEdges(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if HasGitEntryAncestor(nested) {
		t.Fatal("unexpected git ancestor")
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasGitEntryAncestor(nested) {
		t.Fatal("missing git ancestor")
	}

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, ".git"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := StructuralWorktreeRoot(bad); ok {
		t.Fatal("invalid pointer resolved")
	}
	if _, ok := StructuralWorktreeRoot(t.TempDir()); ok {
		t.Fatal("non-repository resolved")
	}

	socketRoot := t.TempDir()
	ln, err := net.Listen("unix", filepath.Join(socketRoot, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if _, ok := StructuralWorktreeRoot(socketRoot); ok {
		t.Fatal("socket resolved")
	}
	if GitEntryResolvesToCommon(socketRoot, filepath.Join(socketRoot, "common")) {
		t.Fatal("socket matched common")
	}

	main, wt := newLinkedWorktree(t)
	gitFile := filepath.Join(wt, ".git")
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	gitdir, ok := ParseGitdirPointer(string(raw))
	if !ok {
		t.Fatal("missing generated gitdir")
	}
	worktrees := filepath.Dir(gitdir)
	alias := filepath.Join(filepath.Dir(worktrees), "alias")
	if err := os.Symlink(worktrees, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("gitdir: "+filepath.Join(alias, filepath.Base(gitdir))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveMainRepoRootLocal(wt); got != ResolveClean(main) {
		t.Fatalf("aliased root = %q, want %q", got, ResolveClean(main))
	}
}
