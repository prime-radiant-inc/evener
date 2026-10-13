package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// requireRealBwrap gates a test on a bwrap-capable host and skips under -short so
// the fast unit gate never shells out to bubblewrap. It returns real host facts
// (with the resolved bwrap path) for building a live wrapper.
func requireRealBwrap(t *testing.T) HostFacts {
	t.Helper()
	if testing.Short() {
		t.Skip("real-bwrap integration test skipped under -short")
	}
	facts := RealProber{}.Probe()
	if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
		t.Skip("bwrap not capable on this host")
	}
	// The tests anchor their caches at a fixture home; the runner's own Go
	// settings would put real directories (on whatever filesystem the runner
	// keeps them) under the overlay instead.
	facts.GoPath, facts.GoModCache, facts.GoCache, facts.XDGCacheHome = "", "", "", ""
	return facts
}

// runWrapped resolves mode against real bwrap facts anchored at home, builds a
// live wrapper, wraps a `bash -c script` and runs it, returning combined output.
func runWrapped(t *testing.T, facts HostFacts, mode Mode, netOn bool, cwd, sessionTmp, script string) (string, error) {
	t.Helper()
	net := netOn
	rp, err := Resolve(SandboxPolicy{Mode: mode, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve(%v): %v", mode, err)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	argv := w.Wrap([]string{"/bin/bash", "-c", script}, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Mirror the real spawn sites: raise the sandbox env floor (TMPDIR + cache
	// redirect + secret drops) and inherit no extra fds.
	cmd.Env = ApplyEnvFloor(os.Environ(), rp, sessionTmp)
	cmd.ExtraFiles = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestBwrapLinkedWorktreeStarts covers verifier finding F2 end-to-end: a linked
// worktree's common .git is outside the worktree and read-only, so the sandbox
// must not try to pin a missing protected surface there (which would EROFS-abort).
// The fixture lives OUTSIDE /tmp because the sandbox mounts a fresh tmpfs over
// /tmp — a /tmp fixture's common dir would be shadowed and hide the bug.
func TestBwrapLinkedWorktreeStarts(t *testing.T) {
	facts := requireRealBwrap(t)

	// Keep the fixture outside /tmp, which the sandbox replaces with tmpfs.
	// /var/tmp also remains traversable when the test runner has elevated host
	// privileges that bubblewrap intentionally drops.
	base, err := os.MkdirTemp("/var/tmp", "sbxlwt-")
	if err != nil {
		t.Skipf("create non-/tmp sandbox fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitHarness(t, main, "init", "-q")
	gitHarness(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(base, "wt")
	gitHarness(t, main, "worktree", "add", "-q", wt)
	cwd := resolveCleanPath(wt)

	facts.Home = t.TempDir()
	// If the sandbox aborts (EROFS on a dead pin), the command fails to start.
	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, t.TempDir(),
		`echo LWT-OK; git status --porcelain >/dev/null 2>&1; echo "git=$?"`)
	if err != nil {
		t.Fatalf("linked-worktree sandbox failed to start/run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "LWT-OK") {
		t.Errorf("linked-worktree sandbox did not run the command:\n%s", out)
	}
}

// TestBwrapReadOnlyTmpWorktreeStarts covers MED finding #5 end-to-end: a
// read-only session whose worktree lives under /tmp must still start. Read-only
// mode grants no write root, so without re-binding the cwd read-only after the
// /tmp tmpfs, --chdir into the shadowed worktree aborts the sandbox.
func TestBwrapReadOnlyTmpWorktreeStarts(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	cwd := tmpMainCheckout(t)
	out, err := runWrapped(t, facts, ModeReadOnly, true, cwd, t.TempDir(),
		`echo RO-OK; pwd`)
	if err != nil {
		t.Fatalf("read-only /tmp worktree sandbox failed to start: %v\n%s", err, out)
	}
	if !strings.Contains(out, "RO-OK") {
		t.Errorf("sandbox did not run the command:\n%s", out)
	}
	if !strings.Contains(out, cwd) {
		t.Errorf("pwd inside the sandbox must be the re-bound /tmp worktree %q:\n%s", cwd, out)
	}
}

// The /tmp remount must not happen before bwrap has created the mountpoints for
// the cwd and explicit read grants. Exercise real mount enforcement, including
// write-blocked restricted mode, without changing TMPDIR or widening grants.
func TestBwrapReadOnlyTmpRootsPreserveAccess(t *testing.T) {
	facts := requireRealBwrap(t)
	for _, tc := range []struct {
		name         string
		mode         Mode
		writeBlocked bool
	}{
		{"read-only", ModeReadOnly, false},
		{"write-blocked-restricted", ModeRestricted, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Explicit /tmp fixture: this regression is about mounts shadowed by
			// the private /tmp tmpfs, regardless of the test runner's TMPDIR.
			base, err := os.MkdirTemp("/tmp", "evener-bwrap-read-roots-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(base) })
			cwd := filepath.Join(base, "cwd")
			readRoot := filepath.Join(base, "read-grant")
			sessionTmp := filepath.Join(base, "session")
			for _, dir := range []string{cwd, readRoot, sessionTmp} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// Keep git real: Resolve derives the grants from this checkout.
			gitHarness(t, cwd, "init", "-q")
			for _, dir := range []string{cwd, readRoot} {
				if err := os.WriteFile(filepath.Join(dir, "readable"), []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f := facts
			f.Home = t.TempDir()
			policy := SandboxPolicy{Mode: tc.mode, WriteBlocked: tc.writeBlocked}
			grantArg := ""
			if tc.mode == ModeRestricted {
				policy.ExtraReadRoots = []string{readRoot}
				grantArg = readRoot
			}
			rp, err := Resolve(policy, f, cwd)
			if err != nil {
				t.Fatal(err)
			}
			w, err := NewWrapper(rp, facts.BwrapPath, sessionTmp)
			if err != nil {
				t.Fatal(err)
			}
			const script = `set -eu
test "$(cat "$1/readable")" = fixture
printf writable > "$3/written"
for dir in "$1" "$4"; do
    if (printf forbidden > "$dir/forbidden") 2>/dev/null; then exit 11; fi
done
if test -n "$2"; then
    test "$(cat "$2/readable")" = fixture
    if (printf forbidden > "$2/forbidden") 2>/dev/null; then exit 12; fi
fi
echo ACCESS-OK`
			argv := w.Wrap([]string{"/bin/bash", "-c", script, "read-roots-test", cwd, grantArg, sessionTmp, base}, cwd)
			cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
			cmd.Env = ApplyEnvFloor(os.Environ(), rp, sessionTmp)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("temporary read roots failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "ACCESS-OK") {
				t.Fatalf("access assertions did not finish: %s", out)
			}
			if data, err := os.ReadFile(filepath.Join(sessionTmp, "written")); err != nil || string(data) != "writable" {
				t.Fatalf("session scratch write not preserved: %q %v", data, err)
			}
			for _, dir := range []string{cwd, readRoot, base} {
				if _, err := os.Stat(filepath.Join(dir, "forbidden")); !os.IsNotExist(err) {
					t.Fatalf("unexpected write under %s: %v", dir, err)
				}
			}
		})
	}
}

func TestBwrapConfinesAndMasks(t *testing.T) {
	facts := requireRealBwrap(t)

	// A fake home with a credential file that MUST be invisible inside the sandbox.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "TOPSECRET-KEY-MATERIAL"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	facts.Home = home

	cwd := MaterializeWorkspace(t, MainCheckout)
	sessionTmp := t.TempDir()

	script := strings.Join([]string{
		`echo "comm=$(cat /proc/1/comm)"`,
		`echo "secret=$(cat '` + filepath.Join(home, ".ssh", "id_ed25519") + `' 2>/dev/null)"`,
		`echo "sshdir=$(ls -A '` + filepath.Join(home, ".ssh") + `' 2>&1)"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("wrapped command failed: %v\noutput:\n%s", err, out)
	}

	if strings.Contains(out, secret) {
		t.Errorf("credential content leaked into the sandbox:\n%s", out)
	}
	// PID 1 inside is the sandbox init, not a host process — host PID state is gone.
	if !strings.Contains(out, "comm=bwrap") {
		t.Errorf("expected /proc/1/comm to be the sandbox init (bwrap); host process state visible:\n%s", out)
	}
	// The masked ~/.ssh reads as empty (the tmpfs overlay), not the real dir.
	if !strings.Contains(out, "sshdir=") || strings.Contains(out, "id_ed25519") {
		t.Errorf("masked ~/.ssh must be an empty tmpfs, not the real credential dir:\n%s", out)
	}
}

func TestBwrapDeniesGitConfigWrite(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	cwd := MaterializeWorkspace(t, MainCheckout)
	sessionTmp := t.TempDir()

	script := strings.Join([]string{
		`git config --local core.hooksPath /tmp/evil 2>&1 | head -1`,
		`echo "config-exit=${PIPESTATUS[0]}"`,
		`echo "hook-write=$(echo pwned > '` + filepath.Join(cwd, ".git", "hooks", "post-commit") + `' 2>&1; echo exit=$?)"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("wrapped command failed unexpectedly: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "config-exit=0") {
		t.Errorf("git config --local write into a sandboxed .git/config must fail:\n%s", out)
	}
	if strings.Contains(out, "hook-write=exit=0") {
		t.Errorf("writing a git hook must be denied inside the sandbox:\n%s", out)
	}
	// The real hook file must not exist on the host afterward.
	if _, err := os.Stat(filepath.Join(cwd, ".git", "hooks", "post-commit")); err == nil {
		t.Errorf("a git hook was persisted to the host despite the sandbox")
	}
}

// A read-only sandbox whose workspace lives under /dev/shm must see the real
// workspace and refuse writes to it, as it does anywhere else. Without the
// re-bind, bwrap's fresh --dev tmpfs hid the workspace: the host file was
// missing and a write "succeeded" into a private directory that vanished.
func TestBwrapReadOnlyDevShmWorkspaceIsRealAndReadOnly(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	cwd := devShmMainCheckout(t)
	if err := os.WriteFile(filepath.Join(cwd, "host-marker"), []byte("HOST-MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runWrapped(t, facts, ModeReadOnly, true, cwd, t.TempDir(),
		`cat host-marker; echo; if echo x > sandbox-write 2>/dev/null; then echo WRITE-ALLOWED; else echo WRITE-DENIED; fi`)
	if err != nil {
		t.Fatalf("read-only /dev/shm sandbox failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "HOST-MARKER") {
		t.Errorf("the sandbox did not see the host workspace under /dev/shm:\n%s", out)
	}
	if !strings.Contains(out, "WRITE-DENIED") {
		t.Errorf("a read-only sandbox accepted a write to its /dev/shm workspace:\n%s", out)
	}
}

// A secret under /dev/shm stays hidden when its directory is re-bound into the
// sandbox: here the fake home is itself the read-only workspace under
// /dev/shm, so the cwd re-bind after --dev exposes it, and ~/.ssh must still be
// masked on top of it.
func TestBwrapMasksSecretsInsideADevShmWorkspace(t *testing.T) {
	facts := requireRealBwrap(t)
	home := devShmMainCheckout(t)
	facts.Home = home
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("SHM-SECRET-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "visible"), []byte("SHM-VISIBLE"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runWrapped(t, facts, ModeReadOnly, true, home, t.TempDir(),
		`cat visible; echo; cat .ssh/id_ed25519 2>/dev/null; echo END`)
	if err != nil {
		t.Fatalf("read-only /dev/shm sandbox failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SHM-VISIBLE") {
		t.Fatalf("the re-bound /dev/shm workspace is not visible, so the mask is untested:\n%s", out)
	}
	if strings.Contains(out, "SHM-SECRET-KEY") {
		t.Errorf("a secret under a re-bound /dev/shm workspace was readable in the sandbox:\n%s", out)
	}
}

// With the session scratch inside a read-only /dev/shm workspace, the
// workspace is read-only and the session scratch is still writable.
func TestBwrapSessionTmpInsideADevShmWorkspaceStaysWritable(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	cwd := devShmMainCheckout(t)
	sessionTmp := filepath.Join(cwd, "session-tmp")
	if err := os.Mkdir(sessionTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runWrapped(t, facts, ModeReadOnly, true, cwd, sessionTmp,
		`if echo x > session-tmp/probe 2>/dev/null; then echo SCRATCH-WRITABLE; else echo SCRATCH-DENIED; fi; if echo x > workspace-probe 2>/dev/null; then echo WORKSPACE-WRITABLE; else echo WORKSPACE-DENIED; fi`)
	if err != nil {
		t.Fatalf("read-only /dev/shm sandbox failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SCRATCH-WRITABLE") {
		t.Errorf("the session scratch inside a read-only /dev/shm workspace was not writable:\n%s", out)
	}
	if !strings.Contains(out, "WORKSPACE-DENIED") {
		t.Errorf("the read-only /dev/shm workspace accepted a write:\n%s", out)
	}
}

// A delegate is routinely handed files from its parent's session scratch, which
// lives beside its own in the root's scratch tree (#4170). The shell must see the
// tree read-only exactly where the file tools may read it, keep its own scratch
// writable, and still hide the rest of the host /tmp.
func TestBwrapShellSeesTheScratchTreeTheFileToolsRead(t *testing.T) {
	facts := requireRealBwrap(t)
	base, err := os.MkdirTemp("/tmp", "evener-bwrap-scratch-tree-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base = resolveCleanPath(base)
	if !pathUnder(base, "/tmp") {
		t.Skipf("/tmp resolves to %q on this host; the private /tmp shadows nothing", base)
	}
	cwd := MaterializeWorkspace(t, MainCheckout)
	var scratch [2]string
	for i, session := range []string{"parent", "child"} {
		s, err := OpenSessionScratch(base, cwd, "root", session)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Cleanup() })
		scratch[i] = s.Dir
	}
	parentScratch, ownScratch := scratch[0], scratch[1]
	preserved := filepath.Join(parentScratch, "preserved.txt")
	elsewhere := filepath.Join(base, "elsewhere.txt")
	for _, f := range []string{preserved, elsewhere} {
		if err := os.WriteFile(f, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	const script = `set -u
if test "$(cat "$1" 2>/dev/null)" = fixture; then echo PARENT-VISIBLE; fi
if (printf forbidden > "$2/forbidden") 2>/dev/null; then echo PARENT-WRITABLE; fi
if printf ok > "$3/written"; then echo OWN-WRITABLE; fi
if test -e "$4"; then echo ELSEWHERE-VISIBLE; fi`
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		t.Run(mode.String(), func(t *testing.T) {
			f := facts
			f.Home = t.TempDir()
			rp, err := Resolve(SandboxPolicy{Mode: mode}, f, cwd)
			if err != nil {
				t.Fatal(err)
			}
			w, err := NewWrapper(rp, facts.BwrapPath, ownScratch)
			if err != nil {
				t.Fatal(err)
			}
			argv := w.Wrap([]string{"/bin/bash", "-c", script, "scratch-tree-test", preserved, parentScratch, ownScratch, elsewhere}, cwd)
			cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
			cmd.Env = ApplyEnvFloor(os.Environ(), rp, ownScratch)
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			if err != nil {
				t.Fatalf("sandboxed command failed: %v\n%s", err, out)
			}
			if got, want := strings.Contains(out, "PARENT-VISIBLE"), rp.FileToolCanRead(preserved); got != want {
				t.Errorf("shell sees the parent's scratch file = %v, file tools may read it = %v; they must agree:\n%s", got, want, out)
			}
			if strings.Contains(out, "PARENT-WRITABLE") {
				t.Errorf("the scratch tree must be read-only outside the session's own scratch:\n%s", out)
			}
			if !strings.Contains(out, "OWN-WRITABLE") {
				t.Errorf("the session's own scratch must stay writable:\n%s", out)
			}
			if strings.Contains(out, "ELSEWHERE-VISIBLE") {
				t.Errorf("host /tmp outside the scratch tree must stay hidden:\n%s", out)
			}
		})
	}
}

// Under real bwrap, a cache root holding an extra writable root is not
// overlaid: the write to the granted root persists, and a write elsewhere in
// that cache fails where it happens instead of vanishing with an overlay.
// Another cache that holds no write root stays overlaid (its write succeeds
// and is discarded).
func TestBwrapCacheHoldingAWritableRootIsNotOverlaid(t *testing.T) {
	facts := requireRealBwrap(t)
	if !facts.OverlaySupported {
		t.Skip("this bwrap has no overlay support")
	}
	home, err := os.MkdirTemp("/var/tmp", "evener-overlay-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	cache := filepath.Join(home, ".cache")
	library := filepath.Join(cache, "library")
	npm := filepath.Join(home, ".npm")
	for _, dir := range []string{library, npm} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	facts.Home = home
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true), ExtraWritableRoots: []string{library}}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if rp.CacheStrategy != CacheOverlay {
		t.Skipf("cache strategy is %v, not overlay", rp.CacheStrategy)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script := `echo kept > "$1/kept"; echo private > "$3/private" && echo NPM-WRITABLE; if echo gone > "$2/stray" 2>/dev/null; then echo STRAY-WRITTEN; fi`
	argv := w.Wrap([]string{"/bin/sh", "-c", script, "overlay-test", library, cache, npm}, cwd)
	out, _ := exec.CommandContext(t.Context(), argv[0], argv[1:]...).CombinedOutput()
	if _, err := os.Stat(filepath.Join(library, "kept")); err != nil {
		t.Errorf("a write to the granted root must persist: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "STRAY-WRITTEN") {
		t.Errorf("a write elsewhere in a cache holding a writable root must fail, not land in an overlay:\n%s", out)
	}
	if !strings.Contains(string(out), "NPM-WRITABLE") {
		t.Errorf("a cache holding no writable root must stay overlaid and writable:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(npm, "private")); !os.IsNotExist(err) {
		t.Errorf("a write into an overlaid cache must be discarded: %v", err)
	}
}

// A cache root sharing storage with a writable root through another symlink
// is not overlaid either, and the write to the writable root persists.
func TestBwrapCacheSharingAWritableRootThroughASymlinkIsNotOverlaid(t *testing.T) {
	facts := requireRealBwrap(t)
	if !facts.OverlaySupported {
		t.Skip("this bwrap has no overlay support")
	}
	base, err := os.MkdirTemp("/var/tmp", "evener-overlay-links-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	storage := filepath.Join(base, "storage")
	library := filepath.Join(storage, "cache", "library")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "links")
	if err := os.Symlink(storage, link); err != nil {
		t.Fatal(err)
	}
	facts.Home = filepath.Join(base, "home")
	facts.GoCache = filepath.Join(link, "cache")
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true), ExtraWritableRoots: []string{library}}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(rp.CacheRoots, facts.GoCache) {
		t.Errorf("a cache holding a writable root through a symlink must not be overlaid: %v", rp.CacheRoots)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	argv := w.Wrap([]string{"/bin/sh", "-c", `echo kept > "$1/kept"`, "rebind-test", library}, cwd)
	if out, err := exec.CommandContext(t.Context(), argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("sandboxed command failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(library, "kept")); err != nil {
		t.Errorf("a write to the writable root must persist: %v", err)
	}
}

// The plugin store and the user skills directory are carved out of the masked
// ~/.config/evener read-only in every mode (#4171): the shell reads a plugin
// skill's files and runs its hook script, cannot write either, and still sees
// nothing else of ~/.config/evener.
func TestBwrapShellReadsEvenerContentThroughTheCredentialMask(t *testing.T) {
	facts := requireRealBwrap(t)
	// Outside /tmp, so the private /tmp tmpfs is not what hides the secret.
	home, err := os.MkdirTemp("/var/tmp", "evener-content-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	cwd := MaterializeWorkspace(t, MainCheckout)
	for layout, write := range evenerContentLayouts {
		fx := write(t, filepath.Join(home, layout))
		for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			t.Run(layout+"/"+mode.String(), func(t *testing.T) {
				f := facts
				f.Home = filepath.Join(home, layout)
				f.EvenerContentRoots = fx.contentRoots()
				rp, err := Resolve(SandboxPolicy{Mode: mode, InfraReadRoots: []string{fx.plugin}}, f, cwd)
				if err != nil {
					t.Fatal(err)
				}
				w, err := NewWrapper(rp, facts.BwrapPath, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				argv := w.Wrap(fx.scriptCommand("/bin/bash"), cwd)
				cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
				cmd.Env = ApplyEnvFloor(os.Environ(), rp, w.SessionTmp())
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("sandboxed command failed: %v\n%s", err, out)
				}
				assertEvenerContentOutput(t, string(out))
			})
		}
	}
}

// fixtureGitEnv is os.Environ() prepared for a sandboxed git whose global config
// is the fixture's: the runner's own GIT_CONFIG_* overrides are dropped (they
// would point git at some other global config, which is exactly the confound this
// test must not have), the system config is off so the host's /etc/gitconfig
// cannot supply core.excludesFile, and HOME names the fixture home the sandbox's
// global-config facts were probed from.
func fixtureGitEnv(home string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "GIT_CONFIG_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
}

// TestBwrapRestrictedGitReadsTheFileTheGlobalConfigNames is issue #4234's
// acceptance bar on a host whose sandbox can run: a restricted session's git reads
// the files its global config names — here core.excludesFile and
// core.attributesFile, both spelled with `~/` — so the developer's configuration
// takes effect inside the sandbox and git stays silent.
//
// On macOS/Seatbelt the failure this fixes is a WARNING on every command
// (`unable to access '<home>/global-ignore': Operation not permitted`), because
// Seatbelt judges a denied path the process NAMES and the path still exists. bwrap
// instead hides an ungranted path with its mount namespace, so there the same
// missing grant produces no warning at all: git silently ignores an excludes file
// it cannot open, and the developer's global configuration quietly stops working.
// The observable asserted here is therefore git's BEHAVIOUR, not stderr: the
// fixture's excludes file keeps ignored.txt out of `git status`, and its attributes
// file sets an attribute `git check-attr` reports. Both were absent before the
// grant — the reproduction of the issue on this host.
//
// The same fixture pins the two properties that keep the grant narrow: it is
// file-exact (a sibling of the named file stays unreadable), and it loses to the
// credential denylist even when the named path is a symlink into a masked secret.
func TestBwrapRestrictedGitReadsTheFileTheGlobalConfigNames(t *testing.T) {
	if testing.Short() {
		t.Skip("real-bwrap integration test skipped under -short")
	}
	// Outside /tmp: the private /tmp tmpfs would otherwise shadow the fixture, and
	// this test is about the global-config grant, not the /tmp re-bind.
	base, err := os.MkdirTemp("/var/tmp", "evener-4234-")
	if err != nil {
		t.Skipf("create non-/tmp sandbox fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	secretDir := filepath.Join(home, ".ssh")
	for _, dir := range []string{home, repo, secretDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ignore := filepath.Join(home, "global-ignore")
	attrs := filepath.Join(home, "global-attributes")
	secret := filepath.Join(secretDir, "id_rsa")
	secretLink := filepath.Join(home, "ignore-link")
	sibling := filepath.Join(home, "sibling-not-granted")
	const secretText = "SECRET-SHOULD-NOT-ESCAPE"
	for path, content := range map[string]string{
		ignore:  "ignored.txt\n",
		attrs:   "tracked.txt custom-evener-4234\n",
		secret:  secretText + "\n",
		sibling: "sibling\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A named path that is a SYMLINK into a masked secret must not become a way to
	// read it: the denylist is authoritative over the grant.
	if err := os.Symlink(secret, secretLink); err != nil {
		t.Fatal(err)
	}

	gitHarness(t, repo, "init", "-q")
	for _, file := range []string{"tracked.txt", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(repo, file), []byte(file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitHarness(t, repo, "add", "tracked.txt")

	// resolveFixtureHome writes config as the fixture's global git config, probes the
	// fixture home and resolves a restricted policy from it — the session-start step
	// these subtests need to be able to do ONCE and then spawn from repeatedly. Each
	// helper takes the SUBTEST's t: a Skip or FailNow through the parent's test object
	// would land on the parent while a subtest runs.
	resolveFixtureHome := func(t *testing.T, config string) (ResolvedPolicy, HostFacts) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		facts := probeHost(fixtureHomeProbeSystem{home: home})
		if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
			t.Skip("bwrap not capable on this host")
		}
		rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, facts, repo)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		return rp, facts
	}

	// runWithPolicy spawns script inside the real sandbox under an already-resolved
	// policy, exactly as a later tool call in a live session would.
	runWithPolicy := func(t *testing.T, rp ResolvedPolicy, facts HostFacts, script string, args ...string) (string, string) {
		t.Helper()
		sessionTmp := t.TempDir()
		w, err := NewWrapper(rp, facts.BwrapPath, sessionTmp)
		if err != nil {
			t.Fatalf("NewWrapper: %v", err)
		}
		argv := w.Wrap(append([]string{"/bin/sh", "-c", script, "global-config-referenced"}, args...), repo)
		cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
		cmd.Dir = repo
		cmd.Env = ApplyEnvFloor(fixtureGitEnv(home), rp, sessionTmp)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("restricted sandbox command failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	// runUnderFixtureHome resolves a fresh policy from config and runs script under it.
	runUnderFixtureHome := func(t *testing.T, config, script string, args ...string) (string, string, ResolvedPolicy, HostFacts) {
		t.Helper()
		rp, facts := resolveFixtureHome(t, config)
		stdout, stderr := runWithPolicy(t, rp, facts, script, args...)
		return stdout, stderr, rp, facts
	}

	t.Run("the config's files take effect and the grant stays file-exact", func(t *testing.T) {
		config := "[core]\n\texcludesFile = ~/global-ignore\n\tattributesFile = ~/global-attributes\n"
		script := `set -eu
git status --porcelain
git check-attr --all -- tracked.txt
printf 'SIBLING=%s\n' "$(cat "$1" 2>/dev/null || echo denied)"`
		stdout, stderr, _, _ := runUnderFixtureHome(t, config, script, sibling)
		if got := strings.TrimSpace(stderr); got != "" {
			t.Errorf("git under restricted mode must stay silent; stderr:\n%s\nstdout:\n%s", got, stdout)
		}
		if !strings.Contains(stdout, "A  tracked.txt") {
			t.Errorf("git status did not report the staged file; stdout:\n%s", stdout)
		}
		if strings.Contains(stdout, "?? ignored.txt") {
			t.Errorf("the global config's core.excludesFile must take effect under restricted mode, but git status listed ignored.txt (before the grant it silently did not); stdout:\n%s", stdout)
		}
		if !strings.Contains(stdout, "custom-evener-4234: set") {
			t.Errorf("the global config's core.attributesFile must take effect under restricted mode, but git check-attr reported nothing (before the grant it silently did not); stdout:\n%s", stdout)
		}
		if !strings.Contains(stdout, "SIBLING=denied") {
			t.Errorf("the grant is file-exact: a sibling of the named file must stay unreadable; stdout:\n%s", stdout)
		}
	})

	t.Run("a named path that resolves into a masked secret is never granted", func(t *testing.T) {
		// The value names a SYMLINK whose target exists, so the probe admits it —
		// and the resolver must still refuse the grant, because bwrap's read-only
		// bind of a symlink follows it and materializes the TARGET's content at the
		// literal name (verified on the live kernel), which would expose the masked
		// key at the granted path.
		config := "[core]\n\texcludesFile = ~/ignore-link\n"
		stdout, _, rp, facts := runUnderFixtureHome(t, config, `cat "$1" 2>/dev/null || echo denied`, secretLink)
		if !slices.Contains(facts.GitGlobalReferencedPaths, secretLink) {
			t.Fatalf("the probe must admit a config-named symlink whose target exists, %q; got %v", secretLink, facts.GitGlobalReferencedPaths)
		}
		if slices.Contains(rp.Spawned.ReadRoots, secretLink) {
			t.Errorf("a config-named path whose resolved target is masked must never become a read root; read roots: %v", rp.Spawned.ReadRoots)
		}
		if strings.Contains(stdout, secretText) {
			t.Errorf("a grant for a config-named symlink must not expose its masked target; stdout:\n%s", stdout)
		}
	})

	t.Run("a session cannot retarget a granted name at a masked secret", func(t *testing.T) {
		// The config names a SYMLINK whose target is a file inside the session's own
		// worktree — a dotfile-managed excludes file pointing at a file in the repo.
		// bwrap binds the granted NAME afresh at every spawn, so between two commands
		// the session can replace that target with a symlink to a masked secret and the
		// next bind would materialize the key's content at the granted name. The policy
		// is resolved ONCE here, like a session's, so the two spawns share it.
		target := filepath.Join(repo, "inner-ignore")
		alias := filepath.Join(home, "gitignore-alias")
		writeFixtureFile(t, target, "inner.txt\n")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(target) }()
		config := "[core]\n\texcludesFile = ~/gitignore-alias\n"
		rp, facts := resolveFixtureHome(t, config)
		if !slices.Contains(facts.GitGlobalReferencedPaths, alias) {
			t.Fatalf("the probe must admit a config-named symlink whose target exists, %q; got %v", alias, facts.GitGlobalReferencedPaths)
		}
		if slices.Contains(rp.Spawned.ReadRoots, alias) {
			t.Fatalf("a name the session can retarget must not be granted; read roots: %v", rp.Spawned.ReadRoots)
		}
		before, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, alias)
		if strings.Contains(before, secretText) {
			t.Fatalf("unexpected secret content before the retarget; stdout:\n%s", before)
		}

		// The session writes to its own worktree: replace the target with a link to the
		// masked secret, then run the next command under the SAME policy.
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, target); err != nil {
			t.Fatal(err)
		}
		after, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, alias)
		if strings.Contains(after, secretText) {
			t.Errorf("a session must not be able to retarget a granted name at a masked secret; stdout:\n%s", after)
		}
	})

	t.Run("a named file swapped for a link after resolution is not bound again", func(t *testing.T) {
		// The granted file is a plain regular file outside every writable root, so the
		// session cannot re-point it: nothing is a symlink and nothing it can write is
		// on the path. But the bind follows the literal name afresh at every spawn, so
		// the argv builder re-walks it — a writer that swaps the file for a link to a
		// masked secret between two commands must not turn the second command into a
		// reader of that secret.
		target := filepath.Join(home, "swappable-ignore")
		writeFixtureFile(t, target, "inner.txt\n")
		rp, facts := resolveFixtureHome(t, "[core]\n\texcludesFile = ~/swappable-ignore\n")
		before, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, target)
		if !strings.Contains(before, "inner.txt") {
			t.Fatalf("the granted file must be readable at first; stdout:\n%s", before)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, target); err != nil {
			t.Fatal(err)
		}
		after, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, target)
		if strings.Contains(after, secretText) {
			t.Errorf("a file swapped for a link after resolution must not be bound again; stdout:\n%s", after)
		}
	})

	t.Run("the same holds for a named file under /tmp, which is rebound privately", func(t *testing.T) {
		// A root under /tmp is bound, shadowed by the private /tmp tmpfs, and then
		// re-bound: that second bind is another bind of the literal name and must carry
		// the same rule, or a swapped file would slip back in through it.
		tmpDir, err := os.MkdirTemp("/tmp", "evener-4234-rebind-")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()
		target := filepath.Join(tmpDir, "swappable-ignore")
		writeFixtureFile(t, target, "inner.txt\n")
		rp, facts := resolveFixtureHome(t, "[core]\n\texcludesFile = "+target+"\n")
		before, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, target)
		if !strings.Contains(before, "inner.txt") {
			t.Fatalf("the granted file must be readable at first; stdout:\n%s", before)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, target); err != nil {
			t.Fatal(err)
		}
		after, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, target)
		if strings.Contains(after, secretText) {
			t.Errorf("a /tmp-held file swapped for a link after resolution must not be bound again; stdout:\n%s", after)
		}
	})

	t.Run("a named file that becomes a directory is not bound, and a restored file is", func(t *testing.T) {
		// The grant is file-exact: were the named path to become a directory, bwrap
		// would bind the whole tree and Seatbelt's subpath grant would cover its
		// descendants. The spawn-time check therefore requires a non-directory as well,
		// and because it runs per spawn, restoring a regular file grants it again.
		target := filepath.Join(home, "flip-ignore")
		writeFixtureFile(t, target, "first.txt\n")
		rp, facts := resolveFixtureHome(t, "[core]\n\texcludesFile = ~/flip-ignore\n")
		first, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, target)
		if !strings.Contains(first, "first.txt") {
			t.Fatalf("the granted file must be readable at first; stdout:\n%s", first)
		}

		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		inside := filepath.Join(target, "inside")
		writeFixtureFile(t, inside, "inside-content\n")
		asDir, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, inside)
		if strings.Contains(asDir, "inside-content") {
			t.Errorf("a named file that became a directory must not be bound as a tree; stdout:\n%s", asDir)
		}

		if err := os.RemoveAll(target); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, target, "again.txt\n")
		again, _ := runWithPolicy(t, rp, facts, `cat "$1" 2>/dev/null || echo unreadable`, target)
		if !strings.Contains(again, "again.txt") {
			t.Errorf("a regular file restored at the named path must be granted again; stdout:\n%s", again)
		}
	})
}
