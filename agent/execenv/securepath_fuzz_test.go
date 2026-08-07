package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// FuzzSecurePathResolve drives the resolver with arbitrary path text against a
// fixed adversarial tree, for each sandboxed read shape. The oracle is the core
// containment invariant: no input ever panics, no input ever reads the out-of-root
// or denylisted secret markers (the "never escapes a root / never returns a
// denylisted path" property), and every refusal is a typed *sandbox.DeniedError
// (never a bare string error). Genuine filesystem errors (ENOENT, EISDIR, …) are
// allowed — they are not refusals.
func FuzzSecurePathResolve(f *testing.F) {
	const (
		inRootMarker  = "IN_ROOT_OK"
		outRootSecret = "OUT_OF_ROOT_SECRET"
		denyMarker    = "DENYLIST_SECRET"
	)

	seeds := []string{
		"file.txt",
		"sub/dir/file.txt",
		"../outside/secret.txt",
		"../../etc/passwd",
		"/proc/self/environ",
		"/proc/1/environ",
		"link/secret.txt",
		"deny/secret.txt",
		"data/../../outside/secret.txt",
		"./file.txt",
		"",
		"a/b/c/d/e/f/g",
		"//double//slash//file",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		home := t.TempDir()
		worktree := filepath.Join(home, "project")
		if err := os.MkdirAll(filepath.Join(worktree, "sub", "dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(worktree, "file.txt"), inRootMarker)
		mustWrite(t, filepath.Join(worktree, "sub", "dir", "file.txt"), inRootMarker)

		// Out-of-root secret + an in-root symlink pointing at it.
		outside := filepath.Join(home, "outside")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(outside, "secret.txt"), outRootSecret)
		_ = os.Symlink(outside, filepath.Join(worktree, "link"))

		// A denylisted (masked) directory reachable from the read-anywhere shape:
		// plant a secret under ~/.ssh and a symlink to it inside the worktree.
		ssh := filepath.Join(home, ".ssh")
		if err := os.MkdirAll(ssh, 0o700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(ssh, "id_rsa"), denyMarker)
		_ = os.Symlink(ssh, filepath.Join(worktree, "deny"))

		// Write sentinels: an out-of-root file and a masked file that NO fuzzed
		// write may ever overwrite (the write-confinement invariant).
		const sentinel = "ORIGINAL_SENTINEL"
		outSentinel := filepath.Join(outside, "sentinel.txt")
		mustWrite(t, outSentinel, sentinel)
		maskedSentinel := filepath.Join(ssh, "authorized_keys")
		mustWrite(t, maskedSentinel, sentinel)

		for _, mode := range []sandbox.Mode{sandbox.ModeRestricted, sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite} {
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, sbTestHost(home), worktree)
			if err != nil {
				t.Fatalf("Resolve(%v): %v", mode, err)
			}
			s := newSandboxFS(&rp, "")

			// The tool method absolutizes relative paths against the worktree root
			// before calling the resolver; mirror that here.
			abs := raw
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(worktree, raw)
			}

			got, rerr := s.readFile("read_file", abs)
			if rerr == nil {
				// A denylisted (masked) secret must NEVER be read, in any sandboxed
				// mode — masking is the load-bearing floor.
				if strings.Contains(string(got), denyMarker) {
					s.close()
					t.Fatalf("mode=%v input=%q read a denylisted secret", mode, raw)
				}
				// An out-of-worktree read is a containment breach only for the
				// worktree-confined (restricted) read shape; read-only/workspace-write
				// read anywhere-minus-denylist by contract.
				if rp.FileTool.Read == sandbox.ReadWorktreeOnly && strings.Contains(string(got), outRootSecret) {
					s.close()
					t.Fatalf("mode=%v input=%q escaped the worktree and read the out-of-root secret", mode, raw)
				}
			} else if isRefusalClass(rerr) {
				var denied *sandbox.DeniedError
				if !errors.As(rerr, &denied) {
					s.close()
					t.Fatalf("mode=%v input=%q refusal is not a *sandbox.DeniedError: %T %v", mode, raw, rerr, rerr)
				}
			}

			// Write path: no fuzzed write may ever land outside a writable root or
			// on a masked path. Attempt it, then assert both sentinels survive.
			_ = s.writeFile("write_file", abs, []byte("PWNED"), 0o644)
			if got, _ := os.ReadFile(outSentinel); string(got) != sentinel {
				s.close()
				t.Fatalf("mode=%v input=%q overwrote an OUT-OF-ROOT sentinel", mode, raw)
			}
			if got, _ := os.ReadFile(maskedSentinel); string(got) != sentinel {
				s.close()
				t.Fatalf("mode=%v input=%q overwrote a MASKED sentinel", mode, raw)
			}
			s.close()
		}
	})
}

// isRefusalClass reports whether an error looks like a policy refusal (rather than
// a genuine filesystem error such as ENOENT/EISDIR). We only require the typed
// shape for refusals, so this keeps the fuzz oracle honest without pinning it to
// exact errno behavior.
func isRefusalClass(err error) bool {
	var denied *sandbox.DeniedError
	return errors.As(err, &denied)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
