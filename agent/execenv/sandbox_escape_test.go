package execenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// This is the M2 adversarial escape suite. It drives the spec's Validation
// escapes through EVERY file tool and takes its allow/deny ORACLE from M1's
// exported contract table (sandbox.ContractCases) — not from M2's own resolver —
// so a resolver that granted the wrong shape would be caught here.

// modeOracle is a mode's expected file-tool shape, read from the M1 contract.
type modeOracle struct {
	read          sandbox.ReadScope
	worktreeWrite bool
}

// oracleFor extracts the contract's expectation for a mode from the golden
// ContractCases table (the bwrap, main-checkout, non-refusal cell). This is the
// independent oracle: the escape assertions below compare against it, not against
// what M2's resolver happens to produce.
func oracleFor(t *testing.T, mode sandbox.Mode) modeOracle {
	t.Helper()
	for _, c := range sandbox.ContractCases() {
		if c.Mode == mode && !c.WantRefusal && c.Host.BwrapCapable && c.Workspace == sandbox.MainCheckout {
			return modeOracle{read: c.WantFileRead, worktreeWrite: c.WantWorktreeWrite}
		}
	}
	t.Fatalf("no non-refusal bwrap contract case for mode %v", mode)
	return modeOracle{}
}

func isDeniedErr(err error) bool {
	var d *sandbox.DeniedError
	return errors.As(err, &d)
}

// TestEscape_SymlinkOutDeniedEveryTool: a symlink whose target is outside the
// worktree is refused by read_file, write_file, and apply_patch in every
// sandboxed mode (the sandbox never follows a symlink).
func TestEscape_SymlinkOutDeniedEveryTool(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, home, worktree := sandboxedEnv(t, mode)
			secret := filepath.Join(home, "secret.txt")
			if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(worktree, "link.txt")
			if err := os.Symlink(secret, link); err != nil {
				t.Fatal(err)
			}
			if _, err := env.ReadFile(link, nil, nil); !isDeniedErr(err) {
				t.Errorf("read_file through symlink-out: want denial, got %v", err)
			}
			if _, err := env.WriteFile(link, "x"); !isDeniedErr(err) {
				t.Errorf("write_file through symlink-out: want denial, got %v", err)
			}
			if _, err := env.EditFile(link, "SECRET", "PWNED", false); !isDeniedErr(err) {
				t.Errorf("edit_file through symlink-out: want denial, got %v", err)
			}
			if got, _ := os.ReadFile(secret); string(got) != "SECRET" {
				t.Errorf("secret was modified through a symlink: %q", got)
			}
			// apply_patch through a symlink is covered in the tool package's
			// apply_patch_sandbox_test.go (execenv cannot import that package).
		})
	}
}

// TestEscape_ProcReadDeniedEveryMode: reading serf's own /proc environment and the
// /proc/<pid>/root aliasing paths is denied in every sandboxed mode.
func TestEscape_ProcReadDeniedEveryMode(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	targets := []string{
		fmt.Sprintf("/proc/%d/environ", pid),
		"/proc/self/environ",
		"/proc/1/environ",
		fmt.Sprintf("/proc/%d/root/etc/passwd", pid),
		"/proc/1/root/etc/passwd",
	}
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, _, _ := sandboxedEnv(t, mode)
			for _, p := range targets {
				if _, err := env.ReadFile(p, nil, nil); !isDeniedErr(err) {
					t.Errorf("read_file(%q): want denial, got %v", p, err)
				}
				if env.FileExists(p) {
					t.Errorf("file_exists(%q): must be false under sandbox", p)
				}
			}
		})
	}
}

// TestEscape_DenylistReadEveryTool: a denylisted (credential) path is refused via
// read_file, list_dir, file_exists, glob, grep, and apply_patch, in every mode.
func TestEscape_DenylistReadEveryTool(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, home, worktree := sandboxedEnv(t, mode)
			ssh := filepath.Join(home, ".ssh")
			if err := os.MkdirAll(ssh, 0o700); err != nil {
				t.Fatal(err)
			}
			key := filepath.Join(ssh, "id_rsa")
			if err := os.WriteFile(key, []byte("PRIVATE KEY\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := env.ReadFile(key, nil, nil); !isDeniedErr(err) {
				t.Errorf("read_file(denylisted): want denial, got %v", err)
			}
			if _, err := env.ListDirectory(ssh, 1); !isDeniedErr(err) {
				t.Errorf("list_dir(denylisted): want denial, got %v", err)
			}
			if env.FileExists(key) {
				t.Error("file_exists(denylisted): must be false")
			}
			if _, err := env.Glob("*", ssh); !isDeniedErr(err) {
				t.Errorf("glob(denylisted base): want denial, got %v", err)
			}
			if _, err := env.Grep("KEY", ssh, "", false, 10, "content"); !isDeniedErr(err) {
				t.Errorf("grep(denylisted base): want denial, got %v", err)
			}
			_ = worktree // apply_patch's denylist case lives in apply_patch_sandbox_test.go
		})
	}
}

// TestEscape_HomeConfigWriteDenied: writing ~/.bashrc / ~/.gitconfig (persistence
// vectors) is a write-confinement denial in every sandboxed mode, via write_file,
// edit_file, and apply_patch.
func TestEscape_HomeConfigWriteDenied(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			env, home, _ := sandboxedEnv(t, mode)
			bashrc := filepath.Join(home, ".bashrc")
			gitconfig := filepath.Join(home, ".gitconfig")
			if err := os.WriteFile(bashrc, []byte("# orig\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(gitconfig, []byte("[user]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := env.WriteFile(bashrc, "evil"); !isDeniedErr(err) {
				t.Errorf("write_file(~/.bashrc): want denial, got %v", err)
			}
			if _, err := env.EditFile(gitconfig, "[user]", "[evil]", false); !isDeniedErr(err) {
				t.Errorf("edit_file(~/.gitconfig): want denial, got %v", err)
			}
			if got, _ := os.ReadFile(bashrc); string(got) != "# orig\n" {
				t.Errorf("~/.bashrc was modified: %q", got)
			}
		})
	}
}

// TestEscape_GitConfigHookWriteDenied: git config + hook surfaces are write-denied
// in the writable modes (workspace-write, restricted), via write_file and
// apply_patch, so a core.hooksPath redirect or a planted hook cannot persist. The
// git-metadata classification comes from M1's gitdir resolution (a real repo).
func TestEscape_GitConfigHookWriteDenied(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	for _, mode := range []sandbox.Mode{sandbox.ModeWorkspaceWrite, sandbox.ModeRestricted} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			worktree := gitRepo(t)
			host := sbTestHost(home)
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, worktree)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			env := NewLocalExecutionEnvironment(worktree)
			env.Sandbox = &rp
			t.Cleanup(env.Cleanup)

			for _, rel := range []string{".git/config", ".git/hooks/pre-commit", ".git/config.worktree"} {
				target := filepath.Join(worktree, rel)
				if _, err := env.WriteFile(target, "evil"); !isDeniedErr(err) {
					t.Errorf("write_file(%s): want protected denial, got %v", rel, err)
				}
			}
			// apply_patch planting a hook is covered in apply_patch_sandbox_test.go.
		})
	}
}

// TestEscape_ReadWriteShapeMatchesContract: out-of-worktree reads and in-worktree
// writes follow the M1 contract's declared shape for each mode — proving the M2
// enforcement matches the contract, not the other way round.
func TestEscape_ReadWriteShapeMatchesContract(t *testing.T) {
	t.Parallel()
	for _, mode := range sandboxedModes {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			oracle := oracleFor(t, mode)
			env, home, worktree := sandboxedEnv(t, mode)

			// Out-of-worktree, non-denylisted read: allowed iff the contract says the
			// file-tool read shape is ReadAnywhere.
			outside := filepath.Join(home, "notes.txt")
			if err := os.WriteFile(outside, []byte("data\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, rerr := env.ReadFile(outside, nil, nil)
			wantReadAllowed := oracle.read == sandbox.ReadAnywhere
			if gotAllowed := rerr == nil; gotAllowed != wantReadAllowed {
				t.Errorf("out-of-worktree read allowed=%v, contract wants %v (err=%v)", gotAllowed, wantReadAllowed, rerr)
			}

			// In-worktree write: allowed iff the contract says the worktree is writable.
			target := filepath.Join(worktree, "made.txt")
			_, werr := env.WriteFile(target, "x")
			if gotAllowed := werr == nil; gotAllowed != oracle.worktreeWrite {
				t.Errorf("in-worktree write allowed=%v, contract wants %v (err=%v)", gotAllowed, oracle.worktreeWrite, werr)
			}
		})
	}
}

// TestEscape_PreExistingHardlinkResidual asserts the CURRENT (documented) behavior
// for a pre-planted hardlink, so a future inode-preflight change is a conscious
// one (spec: this residual is out of the running-amok model):
//   - a hardlink INSIDE the worktree to an out-of-tree secret is READABLE (path-
//     based masking cannot see it shares an inode) — the acknowledged residual;
//   - a WRITE to such a hardlink does NOT write through to the secret: the atomic
//     temp+rename replaces the name with a fresh inode, leaving the original
//     untouched (temp+rename incidentally closes the write-through vector).
func TestEscape_PreExistingHardlinkResidual(t *testing.T) {
	t.Parallel()
	env, home, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)

	secret := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET-DATA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(worktree, "hard.txt")
	if err := os.Link(secret, hard); err != nil {
		t.Skipf("hardlink unsupported here: %v", err)
	}

	// Residual: the hard-linked secret is readable (documented gap).
	out, err := env.ReadFile(hard, nil, nil)
	if err != nil {
		t.Fatalf("pre-existing hardlink read unexpectedly failed (residual expects success): %v", err)
	}
	if wantSub := "SECRET-DATA"; !strings.Contains(out, wantSub) {
		t.Errorf("hardlink read did not return the secret (residual): %q", out)
	}

	// Write does NOT propagate to the secret: temp+rename replaces the name.
	if _, err := env.WriteFile(hard, "OVERWRITTEN\n"); err != nil {
		t.Fatalf("in-worktree hardlink write: %v", err)
	}
	if got, _ := os.ReadFile(secret); string(got) != "SECRET-DATA\n" {
		t.Errorf("write propagated through the hardlink to the secret: %q", got)
	}
}

// TestEscape_OffModeProvesGuard: with no policy (off), the escapes the sandbox
// denies instead behave exactly as today — the read reaches its target rather than
// returning a sandbox denial. This proves the enforcement is gated on the policy.
func TestEscape_OffModeProvesGuard(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree) // nil Sandbox = off

	// A symlink out of the worktree to a normal text file: off follows it.
	secret := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(secret, []byte("visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktree, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	out, err := env.ReadFile(link, nil, nil)
	if err != nil {
		t.Fatalf("off read through symlink-out should succeed: %v", err)
	}
	if !strings.Contains(out, "visible") {
		t.Errorf("off symlink read = %q", out)
	}

	// Off read of /proc is not a sandbox denial (it reaches the file; the NUL-byte
	// binary rejection today is not a *DeniedError).
	if _, perr := env.ReadFile("/proc/self/environ", nil, nil); isDeniedErr(perr) {
		t.Errorf("off /proc read must not be a sandbox denial, got %v", perr)
	}
}

// --- helpers ---

func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGitInit(t, root)
	return root
}

func runGitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}
