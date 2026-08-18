//go:build serffuzz

package identifier

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzGitPointerParsing drives the pure decode-and-derive half of git.go: the
// contents of a `.git` FILE (as found in a linked worktree or a submodule) and
// the output of `git rev-parse --git-common-dir`.
//
// Both are untrusted in the sense that matters here — they come off disk and out
// of a subprocess, and evener derives a project identity from them, so a wrong
// answer silently attributes a session to the wrong project. The filesystem
// validators next to these functions are deliberately NOT driven: they Stat a
// path the fuzzer controls, and the toolkit's safety bound keeps a fuzzer on the
// decode boundary rather than letting it touch attacker-chosen paths.
//
// Oracles:
//
//   - ParseGitdirPointer accepts only a "gitdir:" line with a non-empty target,
//     and never returns an empty or untrimmed target when it says ok.
//   - Derivation is absolute and clean. Given an absolute ancestor, every path
//     these functions return is absolute and already filepath.Clean'd, because
//     callers compare them against other cleaned paths — an uncleaned result
//     compares unequal to its own equivalent and the match silently fails.
//   - MainRootFromGitdirPointer answers only for the layout it documents: the
//     pointer's parent directory must be named "worktrees". Anything else must
//     be refused rather than guessed at.
//   - isSubmoduleGitDirShape terminates on every input. It walks parents until
//     the path stops changing, which is the kind of loop that hangs on an input
//     nobody tried by hand.
func FuzzGitPointerParsing(f *testing.F) {
	f.Add("gitdir: /repo/.git/worktrees/wt", "/repo/wt")
	f.Add("gitdir: ../.git/worktrees/wt\n", "/repo/wt")
	f.Add("gitdir:/repo/.git/modules/sub", "/repo/sub")
	f.Add("", "/repo")
	f.Add("gitdir:", "/repo")
	f.Add("gitdir:    ", "/repo")
	f.Add("ref: refs/heads/main\ngitdir: /repo/.git/worktrees/wt", "/repo")
	f.Add("gitdir: relative/path", "relative-ancestor")
	f.Add(strings.Repeat("../", 64)+"gitdir: x", "/repo")

	f.Fuzz(func(t *testing.T, content, ancestor string) {
		target, ok := ParseGitdirPointer(content)
		if ok {
			if target == "" {
				t.Fatalf("ParseGitdirPointer(%q) reported ok with an empty target", content)
			}
			if strings.TrimSpace(target) != target {
				t.Fatalf("ParseGitdirPointer(%q) returned untrimmed target %q", content, target)
			}
		} else if target != "" {
			t.Fatalf("ParseGitdirPointer(%q) returned target %q alongside ok=false", content, target)
		}

		absAncestor := filepath.IsAbs(ancestor)

		if resolved, ok := pointerTarget(content, ancestor); ok {
			if resolved != filepath.Clean(resolved) {
				t.Fatalf("pointerTarget(%q, %q) = %q, which is not cleaned", content, ancestor, resolved)
			}
			if absAncestor && !filepath.IsAbs(resolved) {
				t.Fatalf("pointerTarget(%q, %q) = %q, not absolute despite an absolute ancestor", content, ancestor, resolved)
			}
		}

		if root, ok := MainRootFromGitdirPointer(content, ancestor); ok {
			if root != filepath.Clean(root) {
				t.Fatalf("MainRootFromGitdirPointer(%q, %q) = %q, which is not cleaned", content, ancestor, root)
			}
			if absAncestor && !filepath.IsAbs(root) {
				t.Fatalf("MainRootFromGitdirPointer(%q, %q) = %q, not absolute", content, ancestor, root)
			}
			if root == "" || root == "." {
				t.Fatalf("MainRootFromGitdirPointer(%q, %q) returned degenerate root %q", content, ancestor, root)
			}
			// It answers only for the documented layout: <root>/.git/worktrees/<name>.
			gitdir, _ := pointerTarget(content, ancestor)
			if filepath.Base(filepath.Dir(gitdir)) != "worktrees" {
				t.Fatalf("MainRootFromGitdirPointer accepted %q whose parent is not \"worktrees\"", gitdir)
			}
		}

		if candidate := MainRootCandidateFromCommonDir(ancestor, content); candidate != "" {
			if candidate != filepath.Clean(candidate) {
				t.Fatalf("MainRootCandidateFromCommonDir(%q, %q) = %q, which is not cleaned", ancestor, content, candidate)
			}
			if candidate == "." {
				t.Fatalf("MainRootCandidateFromCommonDir(%q, %q) returned %q, which it promises to reject", ancestor, content, candidate)
			}
		}

		// Termination oracle: the walk must stop for any path, including ones
		// that are already roots or that Dir() maps to themselves.
		_ = isSubmoduleGitDirShape(content)
		_ = isSubmoduleGitDirShape(ancestor)
	})
}
