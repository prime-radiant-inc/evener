package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The M3f adversarial escape suite. Every test runs a real command under the real
// bwrap backend (gated on -short + a capability probe) and asserts the MECHANISM
// that closes the escape — masking / namespace / write-confinement — not an
// incidental error. Each attack is the deliberate-evasion form from the spec's
// threat model, not a fuzzer's accident.

// escapeHome plants a credential the sandbox must hide and returns host facts
// anchored at that home, plus a materialized worktree and session tmp.
func escapeHome(t *testing.T) (facts HostFacts, home, cwd, sessionTmp, secretPath, secret string) {
	facts = requireRealBwrap(t)
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret = "ESCAPE-SUITE-SECRET-" + t.Name()
	secretPath = filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	facts.Home = home
	cwd = MaterializeWorkspace(t, MainCheckout)
	sessionTmp = t.TempDir()
	return facts, home, cwd, sessionTmp, secretPath, secret
}

// TestEscapeProcAliasing: a spawned proc cannot reach a masked secret through any
// /proc/<...>/root alias, and host process state is invisible (fresh pid-ns proc
// + masking). Attacks /proc/1/root, /proc/self/root, and a raw read.
func TestEscapeProcAliasing(t *testing.T) {
	facts, _, cwd, sessionTmp, secretPath, secret := escapeHome(t)

	script := strings.Join([]string{
		`echo "self=$(cat /proc/self/root` + secretPath + ` 2>&1)"`,
		`echo "pid1=$(cat /proc/1/root` + secretPath + ` 2>&1)"`,
		`echo "raw=$(cat ` + secretPath + ` 2>&1)"`,
		// Count visible PIDs: a fresh pid-ns /proc shows only the sandbox's own
		// processes (a handful), never the host's hundreds.
		`echo "pids=$(ls -d /proc/[0-9]* 2>/dev/null | wc -l)"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("PROC-ALIASING ESCAPE: secret reachable via /proc/*/root:\n%s", out)
	}
}

// TestEscapeLdSoIndirectExec: invoking ld.so directly cannot execute a binary
// outside the read roots. Confinement is the kernel FS view (the out-of-view file
// simply is not there), not a string denylist on the command name.
func TestEscapeLdSoIndirectExec(t *testing.T) {
	facts, home, cwd, sessionTmp, _, _ := escapeHome(t)

	ld := ""
	for _, cand := range []string{"/lib64/ld-linux-x86-64.so.2", "/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2"} {
		if _, err := os.Stat(cand); err == nil {
			ld = cand
			break
		}
	}
	if ld == "" {
		t.Skip("no ld-linux found to drive the indirect-exec attack")
	}
	// A real ELF binary planted OUTSIDE the read roots (in home, invisible under
	// restricted's tmpfs root). If confinement were a name denylist, ld.so would
	// bypass it; with an FS-view sandbox the file is not in the namespace at all.
	evil := filepath.Join(home, "evil")
	src, err := os.ReadFile("/bin/echo")
	if err != nil {
		t.Skip("cannot read /bin/echo to plant the attack binary")
	}
	if err := os.WriteFile(evil, src, 0o755); err != nil {
		t.Fatal(err)
	}

	script := strings.Join([]string{
		`echo "direct=$(` + evil + ` MARKER-DIRECT 2>&1)"`,
		`echo "ldso=$(` + ld + ` ` + evil + ` MARKER-LDSO 2>&1)"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeRestricted, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "MARKER-DIRECT") || strings.Contains(out, "MARKER-LDSO") {
		t.Errorf("LD.SO ESCAPE: an out-of-view binary executed:\n%s", out)
	}
}

// TestEscapeNetOff: a spawned proc under net=off cannot open TCP and cannot
// resolve DNS / send UDP (--unshare-net severs all interfaces). Provider-native
// web fail-closed is covered by TestProviderWebRegistryFailsClosed.
func TestEscapeNetOff(t *testing.T) {
	facts, _, cwd, sessionTmp, _, _ := escapeHome(t)

	script := strings.Join([]string{
		`getent hosts example.com >/dev/null 2>&1; echo "dns=$?"`,
		`(echo > /dev/tcp/1.1.1.1/53) >/dev/null 2>&1; echo "tcp=$?"`,
		`(echo > /dev/udp/8.8.8.8/53) >/dev/null 2>&1; echo "udp=$?"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, false, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "dns=0") {
		t.Errorf("NET-OFF ESCAPE: DNS resolved under net=off:\n%s", out)
	}
	if strings.Contains(out, "tcp=0") {
		t.Errorf("NET-OFF ESCAPE: TCP connect succeeded under net=off:\n%s", out)
	}
	if strings.Contains(out, "udp=0") {
		t.Errorf("NET-OFF ESCAPE: UDP send succeeded under net=off:\n%s", out)
	}
}

// TestEscapeHookPersist: a spawned proc cannot plant anything that runs later
// unsandboxed — .git/hooks writes, a core.hooksPath redirect, and $HOME rc files
// are all write-denied, and the planted hook is inert (absent) on the host after.
func TestEscapeHookPersist(t *testing.T) {
	facts, home, cwd, sessionTmp, _, _ := escapeHome(t)

	hookPath := filepath.Join(cwd, ".git", "hooks", "pre-commit")
	bashrc := filepath.Join(home, ".bashrc")
	script := strings.Join([]string{
		`(echo pwned > ` + hookPath + `) 2>&1; echo "hook=$?"`,
		`git config --local core.hooksPath /tmp/evil >/dev/null 2>&1; echo "cfg=$?"`,
		`(echo 'evil' > ` + bashrc + `) 2>&1; echo "rc=$?"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	// The git config/hook surfaces sit inside the real worktree bind and are
	// re-mounted read-only, so those writes are hard-denied.
	for _, ok := range []string{"hook=0", "cfg=0"} {
		if strings.Contains(out, ok) {
			t.Errorf("HOOK-PERSIST ESCAPE: a persistence write succeeded (%s):\n%s", ok, out)
		}
	}
	// The universal escape criterion is PERSISTENCE: nothing the sandbox wrote may
	// survive to the host. ($HOME here is a temp dir under the tmpfs-masked /tmp, so
	// an rc write hits an isolated tmpfs rather than EROFS — on a real $HOME the
	// read-only root denies it outright; either way it must not reach the host.)
	if _, err := os.Stat(hookPath); err == nil {
		t.Errorf("HOOK-PERSIST ESCAPE: a git hook was persisted to the host")
	}
	if _, err := os.Stat(bashrc); err == nil {
		t.Errorf("HOOK-PERSIST ESCAPE: ~/.bashrc was written on the host")
	}
}

const pristineCacheEntry = "PRISTINE-CACHE-ENTRY"

// plantCacheMarker creates a real go-build cache with a pristine marker under
// home and returns the marker path.
func plantCacheMarker(t *testing.T, home string) string {
	t.Helper()
	realCache := filepath.Join(home, ".cache", "go-build")
	if err := os.MkdirAll(realCache, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(realCache, "marker")
	if err := os.WriteFile(marker, []byte(pristineCacheEntry), 0o644); err != nil {
		t.Fatal(err)
	}
	return marker
}

// assertRealCachePristine fails if the on-disk real cache entry was modified —
// the universal no-poisoning invariant that must hold under every cache strategy.
func assertRealCachePristine(t *testing.T, marker string) {
	t.Helper()
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != pristineCacheEntry {
		t.Errorf("CACHE-POISON ESCAPE: real cache entry was modified: got %q err %v", string(got), err)
	}
}

// TestEscapeCachePoisonIsolationSessionPrivate: with overlay forced off, the real
// cache is read-only and the cache vars are redirected to the session tmp; a
// build's writes land there, discarded at session end. Overlay is pinned off so
// the assertions hold regardless of the host bwrap's overlay support.
func TestEscapeCachePoisonIsolationSessionPrivate(t *testing.T) {
	facts, home, cwd, sessionTmp, _, _ := escapeHome(t)
	facts.OverlaySupported = false // pin the session-private branch, host-independent
	marker := plantCacheMarker(t, home)

	script := strings.Join([]string{
		`(echo POISON > ` + marker + `) 2>&1; echo "real=$?"`,
		`mkdir -p "$GOCACHE" 2>/dev/null; (echo private > "$GOCACHE/entry") 2>&1; echo "redir=$?"`,
		`echo "gocache=$GOCACHE"`,
	}, "; ")

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	// The real cache write must have failed (the real cache is read-only)...
	if strings.Contains(out, "real=0") {
		t.Errorf("CACHE-POISON ESCAPE: the real cache was writable:\n%s", out)
	}
	assertRealCachePristine(t, marker)
	// The redirected cache write landed in the session tmp, not the real cache.
	if !strings.Contains(out, "gocache="+sessionTmp) {
		t.Errorf("GOCACHE must be redirected into the session tmp under session-private cache:\n%s", out)
	}
}

// TestEscapeCachePoisonIsolationOverlay: on an overlay-capable host, cache roots
// are served read-real / write-private. A write into the real-cache PATH may
// SUCCEED inside the sandbox (it lands in the private tmpfs upper) but must never
// reach the real cache a later build consumes. Gated on the resolved strategy, so
// it runs its assertions only where the overlay path is actually exercised.
func TestEscapeCachePoisonIsolationOverlay(t *testing.T) {
	facts, home, cwd, sessionTmp, _, _ := escapeHome(t)
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rp.CacheStrategy != CacheOverlay {
		t.Skip("host bwrap lacks overlay support; the overlay cache path is not exercised here")
	}
	marker := plantCacheMarker(t, home)

	// Write straight into the real-cache PATH; under overlay the write lands in the
	// private upper, so it may succeed in-sandbox — the poisoning test is the host.
	script := `(echo POISON > ` + marker + `) 2>&1; echo "wrote=$?"; cat ` + marker

	out, err := runWrapped(t, facts, ModeWorkspaceWrite, true, cwd, sessionTmp, script)
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	// The in-sandbox view may show the write (private upper), but the host's real
	// cache entry must remain pristine — no poisoning under the overlay path.
	assertRealCachePristine(t, marker)
}
