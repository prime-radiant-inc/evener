package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// sandboxedEnv builds a LocalExecutionEnvironment rooted at a fresh worktree
// beneath a fake home, carrying the resolved policy for mode. This is the
// internal-construction path the M2 plan mandates for testing enforcement (the
// user --sandbox flag is feature-gated off until M5, so a policy can only reach
// the environment by direct construction). The worktree is NOT a git repo so the
// whole tree is the read/write root without git-metadata carve-outs.
func sandboxedEnv(t *testing.T, mode sandbox.Mode) (env *LocalExecutionEnvironment, home, worktree string) {
	t.Helper()
	home = t.TempDir()
	worktree = filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	rp := resolvePolicy(t, mode, home, worktree)
	env = NewLocalExecutionEnvironment(worktree)
	env.Sandbox = rp
	t.Cleanup(env.Cleanup)
	return env, home, worktree
}

// sandboxedModes are the three enforced modes M2 must satisfy.
var sandboxedModes = []sandbox.Mode{sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite, sandbox.ModeRestricted}

// sandboxedEnvWithDenylist is sandboxedEnv with an extra (non-hidden) denylisted
// directory folded into the policy, so tests can exercise masked-path skipping
// independently of grepNative's separate hidden-dotfile skip.
func sandboxedEnvWithDenylist(t *testing.T, mode sandbox.Mode, extraDeny ...string) (env *LocalExecutionEnvironment, home, worktree string) {
	t.Helper()
	home = t.TempDir()
	worktree = filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, DenylistAdd: extraDeny}, sbTestHost(home), worktree)
	if err != nil {
		t.Fatalf("Resolve(%v): %v", mode, err)
	}
	env = NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	return env, home, worktree
}

func mustDenied(t *testing.T, err error, whatf string, args ...any) {
	t.Helper()
	what := fmt.Sprintf(whatf, args...)
	if err == nil {
		t.Fatalf("%s: expected a denial, got nil", what)
	}
	var denied *sandbox.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("%s: expected *sandbox.DeniedError, got %T: %v", what, err, err)
	}
}

// TestReadFileRefusesProcEnviron is the named containment hole: reading serf's own
// /proc/<pid>/environ must be denied in every sandboxed mode (it would otherwise
// leak serf's provider API key).
func TestReadFileRefusesProcEnviron(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, _, _ := sandboxedEnv(t, mode)
			for _, p := range []string{
				fmt.Sprintf("/proc/%d/environ", os.Getpid()),
				"/proc/self/environ",
				"/proc/1/environ",
			} {
				_, err := env.ReadFile(p, nil, nil)
				mustDenied(t, err, "read_file(%q) under %v", p, mode)
			}
		})
	}
}

// TestRestrictedConfinesReads: restricted confines file-tool reads to the
// worktree (a sibling-directory read is refused), while read-only/workspace-write
// allow an out-of-worktree, non-denylisted read.
func TestRestrictedConfinesReads(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, home, _ := sandboxedEnv(t, mode)
			sibling := filepath.Join(home, "sibling.txt")
			if err := os.WriteFile(sibling, []byte("neighbor data\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := env.ReadFile(sibling, nil, nil)
			if mode == sandbox.ModeRestricted {
				mustDenied(t, err, "restricted read_file of a sibling dir")
				return
			}
			if err != nil {
				t.Fatalf("%v: out-of-worktree non-denylisted read should be allowed: %v", mode, err)
			}
			if !strings.Contains(out, "neighbor data") {
				t.Errorf("%v: read returned %q", mode, out)
			}
		})
	}
}

// TestReadFileSymlinkOutRefused: a symlink whose target is outside the worktree is
// refused in every sandboxed mode (the sandbox never follows a symlink).
func TestReadFileSymlinkOutRefused(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, home, worktree := sandboxedEnv(t, mode)
			secret := filepath.Join(home, "target.txt")
			if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(worktree, "innocent.txt")
			if err := os.Symlink(secret, link); err != nil {
				t.Fatal(err)
			}
			_, err := env.ReadFile(link, nil, nil)
			mustDenied(t, err, "%v read_file through an out-of-tree symlink", mode)
		})
	}
}

// TestListDirRestrictedAndDenylist: list_dir cannot escape the worktree under
// restricted, and cannot enumerate a denylisted directory in any mode.
func TestListDirRestrictedAndDenylist(t *testing.T) {
	t.Parallel()

	t.Run("restricted-escape", func(t *testing.T) {
		t.Parallel()
		env, home, _ := sandboxedEnv(t, sandbox.ModeRestricted)
		if err := os.MkdirAll(filepath.Join(home, "elsewhere"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := env.ListDirectory(filepath.Join(home, "elsewhere"), 1)
		mustDenied(t, err, "restricted list_dir outside the worktree")
	})

	t.Run("denylisted-proc", func(t *testing.T) {
		t.Parallel()
		for _, mode := range sandboxedModes {
			env, _, _ := sandboxedEnv(t, mode)
			_, err := env.ListDirectory("/proc", 1)
			mustDenied(t, err, "%v list_dir of /proc", mode)
		}
	})
}

// TestListDirSkipsMaskedEntries: listing a directory that contains a masked
// subtree (e.g. ~/.ssh under $HOME) never enumerates the masked entry.
func TestListDirSkipsMaskedEntries(t *testing.T) {
	t.Parallel()
	// read-only reads anywhere, so $HOME is listable; ~/.ssh under it must be
	// elided from the listing.
	env, home, _ := sandboxedEnv(t, sandbox.ModeReadOnly)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "visible.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ents, err := env.ListDirectory(home, 1)
	if err != nil {
		t.Fatalf("list_dir(home): %v", err)
	}
	sawVisible := false
	for _, e := range ents {
		if e.Name == ".ssh" {
			t.Errorf("list_dir enumerated the masked .ssh directory: %+v", e)
		}
		if e.Name == "visible.txt" {
			sawVisible = true
		}
	}
	if !sawVisible {
		t.Error("list_dir should still enumerate non-masked entries")
	}
}

// TestFileExistsDenylistedFalse: file_exists on a denylisted/secret path returns
// false without leaking existence, even though the file really exists on disk.
func TestFileExistsDenylistedFalse(t *testing.T) {
	t.Parallel()
	env, home, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)

	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	realKey := filepath.Join(ssh, "id_rsa")
	if err := os.WriteFile(realKey, []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env.FileExists(realKey) {
		t.Error("file_exists must return false for a denylisted path even when it exists")
	}
	if env.FileExists("/proc/1/environ") {
		t.Error("file_exists must return false for a pseudo-fs path")
	}

	// A real in-worktree file still reports true.
	realFile := filepath.Join(worktree, "here.txt")
	if err := os.WriteFile(realFile, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !env.FileExists(realFile) {
		t.Error("file_exists must return true for a real in-worktree file")
	}
}

// TestReadFileOffModeIdentical: a nil-policy env reproduces today's afero read
// (byte-identical, including the line-numbered formatting and image handling),
// and a sandboxed in-worktree read returns the SAME formatted output — the
// sandbox read path preserves the output contract.
func TestReadFileOffModeIdentical(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	target := filepath.Join(worktree, "hello.txt")
	content := "line one\nline two\nline three\n"
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	off := NewLocalExecutionEnvironment(worktree)
	offOut, err := off.ReadFile(target, nil, nil)
	if err != nil {
		t.Fatalf("off read: %v", err)
	}
	if !strings.Contains(offOut, "   1\tline one") || !strings.Contains(offOut, "   3\tline three") {
		t.Fatalf("off read did not produce today's line-numbered output: %q", offOut)
	}

	// The same file read under an enforced policy (rooted at the worktree) yields
	// identical formatted output.
	home := filepath.Dir(worktree)
	rp := resolvePolicy(t, sandbox.ModeWorkspaceWrite, home, worktree)
	sb := NewLocalExecutionEnvironment(worktree)
	sb.Sandbox = rp
	t.Cleanup(sb.Cleanup)
	sbOut, err := sb.ReadFile(target, nil, nil)
	if err != nil {
		t.Fatalf("sandboxed read: %v", err)
	}
	if sbOut != offOut {
		t.Errorf("sandboxed read output differs from off:\n off=%q\n  sb=%q", offOut, sbOut)
	}
}

// ---- Task 3: write surface (write_file, edit_file) ----

// TestReadOnlyDeniesWrites: read-only mode denies write_file and edit_file with a
// typed error (writes are tmp-only per the mode matrix).
func TestReadOnlyDeniesWrites(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	target := filepath.Join(worktree, "file.txt")

	_, werr := env.WriteFile(target, "content")
	mustDenied(t, werr, "read-only write_file")

	// Seed a file directly on disk so edit_file has something to open, then confirm
	// the edit itself is denied.
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, eerr := env.EditFile(target, "original", "changed", false)
	mustDenied(t, eerr, "read-only edit_file")
	// The on-disk bytes must be untouched.
	b, _ := os.ReadFile(target)
	if string(b) != "original\n" {
		t.Errorf("read-only edit_file mutated the file: %q", b)
	}
}

// TestWriteConfinedToWritableRoots: workspace-write and restricted confine writes
// to the worktree; a write outside it is refused, and an in-worktree write lands.
func TestWriteConfinedToWritableRoots(t *testing.T) {
	t.Parallel()
	for _, mode := range []sandbox.Mode{sandbox.ModeWorkspaceWrite, sandbox.ModeRestricted} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, home, worktree := sandboxedEnv(t, mode)

			// Outside the worktree → denied.
			outside := filepath.Join(home, "outside.txt")
			_, werr := env.WriteFile(outside, "nope")
			mustDenied(t, werr, "%v write outside worktree", mode)
			if _, err := os.Stat(outside); err == nil {
				t.Errorf("%v: denied write must not create the file", mode)
			}

			// Inside the worktree (with a fresh subdir) → allowed and readable back.
			target := filepath.Join(worktree, "sub", "made.txt")
			if _, err := env.WriteFile(target, "hello sandbox\n"); err != nil {
				t.Fatalf("%v: in-worktree write should succeed: %v", mode, err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "hello sandbox\n" {
				t.Fatalf("%v: wrote bytes not found: got %q err %v", mode, got, err)
			}
		})
	}
}

// TestEditFileSandboxedPreservesContract: edit_file under sandbox reads via the fd,
// applies the same fuzzy/uniqueness logic, and writes back atomically.
func TestEditFileSandboxedPreservesContract(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	target := filepath.Join(worktree, "code.go")
	if err := os.WriteFile(target, []byte("func A() {}\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := env.EditFile(target, "func B() {}", "func C() {}", false); err != nil {
		t.Fatalf("sandboxed edit_file: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "func A() {}\nfunc C() {}\n" {
		t.Errorf("edit_file result = %q", got)
	}

	// Non-unique old_string is still rejected under sandbox.
	if err := os.WriteFile(target, []byte("dup\ndup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := env.EditFile(target, "dup", "x", false); err == nil {
		t.Error("sandboxed edit_file must still reject a non-unique old_string")
	}
}

// TestWriteOffModeIdentical: a nil-policy write reproduces today's afero write.
func TestWriteOffModeIdentical(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	target := filepath.Join(worktree, "out.txt")
	off := NewLocalExecutionEnvironment(worktree)
	msg, err := off.WriteFile(target, "abc")
	if err != nil {
		t.Fatalf("off write: %v", err)
	}
	if !strings.Contains(msg, "wrote 3 bytes") {
		t.Errorf("off write summary = %q", msg)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "abc" {
		t.Errorf("off write content = %q", got)
	}
}

// ---- Task 4: browse surface (glob, grep) ----

// TestGlobRestrictedOutsideRefused: under restricted, a glob base outside the
// worktree is refused.
func TestGlobRestrictedOutsideRefused(t *testing.T) {
	t.Parallel()
	env, home, _ := sandboxedEnv(t, sandbox.ModeRestricted)
	if err := os.WriteFile(filepath.Join(home, "x.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := env.Glob("*.txt", home)
	mustDenied(t, err, "restricted glob outside worktree")
}

// TestGlobSymlinkNoEscape: a glob that would traverse a symlink out of the root
// yields no out-of-root match.
func TestGlobSymlinkNoEscape(t *testing.T) {
	t.Parallel()
	env, home, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "loot.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(worktree, "link")); err != nil {
		t.Fatal(err)
	}
	// An in-worktree file so the glob has a legitimate match too.
	if err := os.WriteFile(filepath.Join(worktree, "real.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, err := env.Glob("**/*.txt", worktree)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, m := range matches {
		if strings.Contains(m, "loot.txt") || strings.Contains(m, "outside") {
			t.Errorf("glob escaped through a symlink: %q", m)
		}
	}
}

// TestGlobSkipsMaskedMatches: a glob over an allowed base that contains a masked
// subtree never returns a match from the masked subtree (the load-bearing
// post-filter), while still returning non-masked matches.
func TestGlobSkipsMaskedMatches(t *testing.T) {
	t.Parallel()
	env, home, _ := sandboxedEnvWithDenylist(t, sandbox.ModeReadOnly, filepath.Join("~", "vault"))
	vault := filepath.Join(home, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "visible.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, err := env.Glob("**/*.txt", home)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sawVisible := false
	for _, m := range matches {
		if strings.Contains(m, "vault") {
			t.Errorf("glob returned a masked match: %q", m)
		}
		if strings.HasSuffix(m, "visible.txt") {
			sawVisible = true
		}
	}
	if !sawVisible {
		t.Error("glob should still return non-masked matches")
	}
}

// TestSandboxWritePreservesMode: a sandboxed write over an existing file keeps its
// mode (the atomic temp+rename must not silently strip an executable bit), while a
// fresh file gets the default mode.
func TestSandboxWritePreservesMode(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)

	script := filepath.Join(worktree, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := env.EditFile(script, "echo hi", "echo bye", false); err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if info, err := os.Stat(script); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Errorf("edit_file stripped the mode: got %o, want 0755", info.Mode().Perm())
	}

	// write_file over the same file also preserves the mode.
	if _, err := env.WriteFile(script, "#!/bin/sh\ntrue\n"); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if info, err := os.Stat(script); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Errorf("write_file over existing file changed the mode: got %o, want 0755", info.Mode().Perm())
	}

	// A fresh file gets the default 0644.
	fresh := filepath.Join(worktree, "new.txt")
	if _, err := env.WriteFile(fresh, "hello"); err != nil {
		t.Fatalf("write_file new: %v", err)
	}
	if info, err := os.Stat(fresh); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o644 {
		t.Errorf("fresh write mode: got %o, want 0644", info.Mode().Perm())
	}
}

// TestFileExistsGrantedRoot: file_exists on the worktree root itself is true even
// under restricted (its parent is outside the read roots).
func TestFileExistsGrantedRoot(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	if !env.FileExists(worktree) {
		t.Error("file_exists on the worktree root must be true under restricted")
	}
}

// TestGrepDenylistedBaseRefused: a grep whose base is a denylisted dir (/proc) is
// refused, in every mode.
func TestGrepDenylistedBaseRefused(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		env, _, _ := sandboxedEnv(t, mode)
		_, err := env.Grep("root", "/proc", "", false, 10, "content")
		mustDenied(t, err, "%v grep base /proc", mode)
	}
}

// TestGrepNativeSkipsDenylist: the native grep fallback never returns a line from
// a masked (denylisted) path, while still matching non-masked files. Uses a
// non-hidden denylisted dir so this is distinct from grepNative's dotfile skip,
// and forces the native path by making ripgrep "absent".
func TestGrepNativeSkipsDenylist(t *testing.T) {
	// Mutates the package execLookPath seam; not parallel.
	vault := "vault" // non-hidden, under home
	env, home, worktree := sandboxedEnvWithDenylist(t, sandbox.ModeReadOnly, filepath.Join("~", vault))

	vaultDir := filepath.Join(home, vault)
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const pat = "NEEDLE_TOKEN"
	if err := os.WriteFile(filepath.Join(vaultDir, "secret.txt"), []byte(pat+" in vault\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "visible.txt"), []byte(pat+" in worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := execLookPath
	execLookPath = func(string) (string, error) { return "", errors.New("no rg") }
	t.Cleanup(func() { execLookPath = orig })

	out, err := env.Grep(pat, home, "", false, 100, "content")
	if err != nil {
		t.Fatalf("native grep: %v", err)
	}
	if strings.Contains(out, "vault") {
		t.Errorf("native grep leaked a denylisted match:\n%s", out)
	}
	if !strings.Contains(out, "visible.txt") {
		t.Errorf("native grep should still match non-denylisted files:\n%s", out)
	}
}

// TestGlobGrepOffIdentical: a nil-policy env reproduces today's os.DirFS glob and
// native grep behavior.
func TestGlobGrepOffIdentical(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "b.md"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	off := NewLocalExecutionEnvironment(worktree)

	matches, err := off.Glob("*.txt", worktree)
	if err != nil || len(matches) != 1 || !strings.HasSuffix(matches[0], "a.txt") {
		t.Fatalf("off glob = %v err %v", matches, err)
	}

	orig := execLookPath
	execLookPath = func(string) (string, error) { return "", errors.New("no rg") }
	t.Cleanup(func() { execLookPath = orig })
	out, err := off.Grep("hit", worktree, "", false, 100, "content")
	if err != nil {
		t.Fatalf("off native grep: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.md") {
		t.Errorf("off native grep = %q", out)
	}
}
