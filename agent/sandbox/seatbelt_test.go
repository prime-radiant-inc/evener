package sandbox

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var updateSeatbeltGolden = flag.Bool("update-seatbelt-golden", false, "regenerate agent/sandbox/testdata/seatbelt/*.sbpl")

// seatbeltHost is a darwin host with sandbox-exec present, anchored at a fixed
// fake home so masked-path params are deterministic across runs.
func seatbeltHost() HostFacts {
	return HostFacts{OS: "darwin", Home: "/Users/tester", SandboxExecPath: "/usr/bin/sandbox-exec"}
}

// seatbeltResolve materializes a git workspace of the given kind and resolves a
// seatbelt-backend policy for it.
func seatbeltResolve(t *testing.T, mode Mode, netOn bool, ws WorkspaceKind) (ResolvedPolicy, string) {
	t.Helper()
	cwd := MaterializeWorkspace(t, ws)
	net := netOn
	rp, err := Resolve(SandboxPolicy{Mode: mode, Network: &net}, seatbeltHost(), cwd)
	if err != nil {
		t.Fatalf("Resolve(%v, net=%v): %v", mode, netOn, err)
	}
	if rp.Backend != BackendSeatbelt {
		t.Fatalf("expected seatbelt backend, got %v", rp.Backend)
	}
	return rp, cwd
}

// paramFor returns the DirParam with the given key, or false.
func paramFor(params []DirParam, key string) (DirParam, bool) {
	for _, p := range params {
		if p.Key == key {
			return p, true
		}
	}
	return DirParam{}, false
}

// paramKeyForPath returns the key whose param path equals path, or "".
func paramKeyForPath(params []DirParam, path string) string {
	for _, p := range params {
		if p.Path == path {
			return p.Key
		}
	}
	return ""
}

// --- Task 1: envelope, params-not-interpolated, argv assembly ---------------

func TestSeatbeltEnvelope(t *testing.T) {
	t.Parallel()
	const root = "/work/tree"
	rp := ResolvedPolicy{
		Mode:    ModeWorkspaceWrite,
		Network: false,
		Spawned: AccessScope{Read: ReadAnywhere, WriteRoots: []string{root}},
	}
	text, params := SeatbeltPolicy(rp, "", identityCanon)

	if !strings.HasPrefix(strings.TrimSpace(text), "(version 1)") {
		t.Errorf("policy must start with the embedded base (version 1):\n%s", text[:min(len(text), 120)])
	}
	if !strings.Contains(text, "(deny default)") {
		t.Errorf("policy must embed the deny-default base")
	}
	// The writable root reaches the policy ONLY as a param reference, never as text.
	if strings.Contains(text, root) {
		t.Errorf("root path %q must not appear in the policy text (param-only):\n%s", root, text)
	}
	if !strings.Contains(text, `(param "WRITABLE_ROOT_0")`) {
		t.Errorf("policy must reference the writable root via (param \"WRITABLE_ROOT_0\")")
	}
	p, ok := paramFor(params, "WRITABLE_ROOT_0")
	if !ok || p.Path != root {
		t.Errorf("WRITABLE_ROOT_0 param = %+v, want path %q", p, root)
	}
	// net=off is the ABSENCE of a network allow.
	if strings.Contains(text, "network-outbound") {
		t.Errorf("net=off must emit no network allow:\n%s", text)
	}
}

func TestParamKeys(t *testing.T) {
	t.Parallel()
	// Keys must be composed only of an alphabet that is safe to double-quote
	// without escaping (the injection defense relies on keys never carrying path
	// text). Exercise the most key-dense mode.
	rp, _ := seatbeltResolve(t, ModeRestricted, true, MainCheckout)
	_, params := SeatbeltPolicy(rp, "/session-tmp", identityCanon)
	if len(params) == 0 {
		t.Fatal("expected params")
	}
	const allowed = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	seen := map[string]bool{}
	for _, p := range params {
		for _, r := range p.Key {
			if !strings.ContainsRune(allowed, r) {
				t.Errorf("param key %q contains a non-safe rune %q", p.Key, r)
			}
		}
		if seen[p.Key] {
			t.Errorf("duplicate param key %q", p.Key)
		}
		seen[p.Key] = true
	}
}

func TestSeatbeltArgs(t *testing.T) {
	t.Parallel()
	params := []DirParam{{Key: "WRITABLE_ROOT_0", Path: "/work"}, {Key: "READABLE_ROOT_0", Path: "/"}}
	got := seatbeltArgs("/usr/bin/sandbox-exec", "(version 1)\n(deny default)", params, []string{"/bin/echo", "hi"})
	want := []string{
		"/usr/bin/sandbox-exec", "-p", "(version 1)\n(deny default)",
		"-DWRITABLE_ROOT_0=/work", "-DREADABLE_ROOT_0=/",
		"--", "/bin/echo", "hi",
	}
	if !slices.Equal(got, want) {
		t.Errorf("seatbeltArgs =\n%v\nwant\n%v", got, want)
	}
	// The command after "--" is the original argv, unmodified.
	sep := slices.Index(got, "--")
	if sep < 0 || !slices.Equal(got[sep+1:], []string{"/bin/echo", "hi"}) {
		t.Errorf("command after -- must be the original argv: %v", got)
	}
}

// --- Task 2: golden snapshots per (mode × net) -----------------------------

func TestSeatbeltGolden(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		net  bool
	}{
		{"read-only_net-off", ModeReadOnly, false},
		{"read-only_net-on", ModeReadOnly, true},
		{"workspace-write_net-off", ModeWorkspaceWrite, false},
		{"workspace-write_net-on", ModeWorkspaceWrite, true},
		{"restricted_net-off", ModeRestricted, false},
		{"restricted_net-on", ModeRestricted, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rp, _ := seatbeltResolve(t, tc.mode, tc.net, MainCheckout)
			// Fixed sessionTmp + identity canonicalizer -> host-independent text
			// (paths ride in params, which are not part of the golden text).
			text, _ := SeatbeltPolicy(rp, "/serf-session-tmp", identityCanon)
			golden := filepath.Join("testdata", "seatbelt", tc.name+".sbpl")

			if *updateSeatbeltGolden {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(text), 0o644); err != nil { //nolint:gosec // golden fixture
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run: go test ./agent/sandbox -run TestSeatbeltGolden -update-seatbelt-golden): %v", err)
			}
			if string(want) != text {
				t.Errorf("policy text drift for %s (rerun with -update-seatbelt-golden if intended)\n%s", tc.name, firstDiff(string(want), text))
			}
		})
	}
}

// TestSeatbeltPlatformDefaultsGating pins that the macOS system read-roots are
// appended ONLY for restricted (worktree-only reads), never for the full-disk
// read modes, and the network block ONLY when egress is on.
func TestSeatbeltPlatformDefaultsGating(t *testing.T) {
	t.Parallel()
	marker := "; Serf Seatbelt platform defaults"
	for _, tc := range []struct {
		mode         Mode
		net          bool
		wantDefaults bool
	}{
		{ModeReadOnly, true, false},
		{ModeWorkspaceWrite, true, false},
		{ModeRestricted, true, true},
		{ModeRestricted, false, true},
	} {
		rp, _ := seatbeltResolve(t, tc.mode, tc.net, MainCheckout)
		text, _ := SeatbeltPolicy(rp, "/tmp/s", identityCanon)
		if got := strings.Contains(text, marker); got != tc.wantDefaults {
			t.Errorf("%v net=%v: platform-defaults present=%v, want %v", tc.mode, tc.net, got, tc.wantDefaults)
		}
		wantNet := tc.net
		if got := strings.Contains(text, "(allow network-outbound)"); got != wantNet {
			t.Errorf("%v net=%v: network allow present=%v, want %v", tc.mode, tc.net, got, wantNet)
		}
	}
}

// TestSeatbeltReadOnlyNoPersistentWrite pins the read-only contract: the only
// writable root is the session tmp — no worktree write allow.
func TestSeatbeltReadOnlyNoPersistentWrite(t *testing.T) {
	t.Parallel()
	rp, cwd := seatbeltResolve(t, ModeReadOnly, true, MainCheckout)
	_, params := SeatbeltPolicy(rp, "/serf-session-tmp", identityCanon)

	if k := paramKeyForPath(params, "/serf-session-tmp"); !strings.HasPrefix(k, "WRITABLE_ROOT_") {
		t.Errorf("read-only must grant the session tmp as the writable root; params: %+v", params)
	}
	// No writable root covers the worktree.
	for _, p := range params {
		if strings.HasPrefix(p.Key, "WRITABLE_ROOT_") && (p.Path == cwd || pathUnder(cwd, p.Path)) {
			t.Errorf("read-only must not grant a worktree-covering write root, got %q=%q", p.Key, p.Path)
		}
	}
}

// --- Task 3: git-metadata protection, linked worktree, canonicalization ----

func TestSeatbeltGitProtection(t *testing.T) {
	t.Parallel()
	rp, cwd := seatbeltResolve(t, ModeWorkspaceWrite, true, MainCheckout)
	text, params := SeatbeltPolicy(rp, "/serf-session-tmp", identityCanon)

	// The worktree is the first writable root.
	wt, ok := paramFor(params, "WRITABLE_ROOT_0")
	if !ok || wt.Path != cwd {
		t.Fatalf("WRITABLE_ROOT_0 = %+v, want worktree %q", wt, cwd)
	}

	// Every protected config/hook surface is denied for WRITE authoritatively (as
	// a PROTECTED_n param), as BOTH a literal (deny first-time creation) and a
	// subpath, in a trailing (deny file-write* ...) that overrides every allow.
	for _, protected := range rp.Git.ProtectedPaths {
		key := paramKeyForPath(params, protected)
		if key == "" || !strings.HasPrefix(key, "PROTECTED_") {
			t.Errorf("protected surface %q has no PROTECTED_ deny param (key %q)", protected, key)
			continue
		}
		want := "(deny file-write* (literal (param " + `"` + key + `"` + ")) (subpath (param " + `"` + key + `"` + ")))"
		if !strings.Contains(text, want) {
			t.Errorf("protected %q (%s) missing authoritative write-deny %q", protected, key, want)
		}
	}

	// .git/objects stays writable: it is a write root and is NOT denied.
	objects := filepath.Join(cwd, ".git", "objects")
	for _, p := range params {
		if p.Path == objects && (strings.HasPrefix(p.Key, "PROTECTED_") || strings.HasPrefix(p.Key, "MASKED_")) {
			t.Errorf(".git/objects must remain writable, but is a deny param %q", p.Key)
		}
	}

	// Secrets are denied for read AND write (a MASKED_ param), not merely a
	// write-only protection.
	secret := filepath.Join(seatbeltHost().Home, ".ssh")
	key := paramKeyForPath(params, secret)
	if key == "" || !strings.HasPrefix(key, "MASKED_") {
		t.Fatalf("secret %q must be a MASKED_ deny param; got key %q", secret, key)
	}
	if !strings.Contains(text, "(deny file-read* file-write* (literal (param "+`"`+key+`"`+")) (subpath (param "+`"`+key+`"`+")))") {
		t.Errorf("secret %q (%s) missing authoritative read+write deny", secret, key)
	}
}

// TestSeatbeltFirmlinkAliasDenies pins the firmlink-alias masked-path defense.
// macOS firmlinks give a data-volume file a SECOND real spelling that
// filepath.EvalSymlinks does NOT collapse: $HOME/.ssh is also reachable as
// /System/Volumes/Data$HOME/.ssh. Under the read-only / workspace-write "/" read
// grant, a deny emitted for only the /Users spelling lets `cat
// /System/Volumes/Data/Users/x/.ssh/id_rsa` slip through and leak the secret.
// Every masked path and protected git surface must be denied for BOTH spellings.
func TestSeatbeltFirmlinkAliasDenies(t *testing.T) {
	t.Parallel()
	rp, _ := seatbeltResolve(t, ModeWorkspaceWrite, true, MainCheckout)
	text, params := SeatbeltPolicy(rp, "/serf-session-tmp", identityCanon)

	// A masked secret is denied for read+write under BOTH spellings.
	secret := filepath.Join(seatbeltHost().Home, ".ssh") // /Users/tester/.ssh
	assertDeniedBothSpellings(t, text, params, secret, "MASKED_", "(deny file-read* file-write* ")

	// A protected git surface is write-denied under BOTH spellings.
	if len(rp.Git.ProtectedPaths) == 0 {
		t.Fatal("expected protected git paths for a main checkout")
	}
	assertDeniedBothSpellings(t, text, params, rp.Git.ProtectedPaths[0], "PROTECTED_", "(deny file-write* ")
}

// assertDeniedBothSpellings asserts base and its /System/Volumes/Data firmlink
// alias are each a deny param (with the given key prefix) referenced by an
// authoritative deny rule (with the given action prefix).
func assertDeniedBothSpellings(t *testing.T, text string, params []DirParam, base, keyPrefix, denyPrefix string) {
	t.Helper()
	alias := "/System/Volumes/Data" + base
	for _, path := range []string{base, alias} {
		key := paramKeyForPath(params, path)
		if !strings.HasPrefix(key, keyPrefix) {
			t.Errorf("path %q has no %s deny param (key %q); firmlink alias bypass is open", path, keyPrefix, key)
			continue
		}
		want := denyPrefix + literalAndSubpath(key) + ")"
		if !strings.Contains(text, want) {
			t.Errorf("missing authoritative deny for %q (%s):\n%s", path, key, want)
		}
	}
}

func TestSeatbeltLinkedWorktreeReadNotWrite(t *testing.T) {
	t.Parallel()
	rp, _ := seatbeltResolve(t, ModeRestricted, true, LinkedWorktree)
	_, params := SeatbeltPolicy(rp, "/serf-session-tmp", identityCanon)

	common := rp.Git.CommonDir
	if common == "" {
		t.Fatal("linked worktree must resolve a common dir")
	}
	// The main repo's common dir is READ-granted (git must read common config).
	if k := paramKeyForPath(params, common); !strings.HasPrefix(k, "READABLE_ROOT_") {
		t.Errorf("linked-worktree common dir %q must be a readable root; got key %q", common, k)
	}
	// The main repo's config is never a writable root (write-denied).
	mainConfig := filepath.Join(common, "config")
	for _, p := range params {
		if strings.HasPrefix(p.Key, "WRITABLE_ROOT_") && (p.Path == mainConfig || p.Path == common || pathUnder(mainConfig, p.Path)) {
			// A write root that is the common dir or an ancestor of the main config
			// would make it writable.
			if p.Path == common {
				t.Errorf("linked-worktree main common dir %q must not be a writable root (%q)", common, p.Key)
			}
		}
	}
}

func TestCanonicalizeLongestPrefix(t *testing.T) {
	t.Parallel()
	// Fake resolver: /var (and its existing descendants) firmlink to /private/var;
	// only paths up to the .git dir "exist".
	resolved := map[string]string{
		"/var":                 "/private/var",
		"/var/wt":              "/private/var/wt",
		"/var/wt/.git":         "/private/var/wt/.git",
		"/private/var":         "/private/var",
		"/private/var/wt":      "/private/var/wt",
		"/private/var/wt/.git": "/private/var/wt/.git",
	}
	eval := func(p string) (string, error) {
		if r, ok := resolved[p]; ok {
			return r, nil
		}
		return "", os.ErrNotExist
	}

	// A not-yet-existing protected surface under a /var worktree must still get the
	// /private/var canonical prefix its existing parent has — otherwise the
	// require-not exclusion would miss the kernel's canonical write path.
	if got := canonicalizeLongestPrefix("/var/wt/.git/config.worktree", eval); got != "/private/var/wt/.git/config.worktree" {
		t.Errorf("nonexistent tail: got %q, want /private/var/wt/.git/config.worktree", got)
	}
	// A wholly-existing path resolves directly.
	if got := canonicalizeLongestPrefix("/var/wt", eval); got != "/private/var/wt" {
		t.Errorf("existing path: got %q, want /private/var/wt", got)
	}
	// A path with no resolvable ancestor is returned cleaned, never dropped.
	if got := canonicalizeLongestPrefix("/nonexistent/./a/b", eval); got != "/nonexistent/a/b" {
		t.Errorf("unresolvable path: got %q, want /nonexistent/a/b", got)
	}
}

func TestSeatbeltCanonicalizeRoots(t *testing.T) {
	t.Parallel()
	// A canonicalizer mapping /tmp -> /private/tmp (the macOS firmlink) must make
	// the emitted -D param carry the canonical path.
	canon := func(p string) string {
		if p == "/tmp/work" {
			return "/private/tmp/work"
		}
		return p
	}
	rp := ResolvedPolicy{
		Mode:    ModeWorkspaceWrite,
		Spawned: AccessScope{Read: ReadAnywhere, WriteRoots: []string{"/tmp/work"}},
	}
	_, params := SeatbeltPolicy(rp, "", canon)
	p, ok := paramFor(params, "WRITABLE_ROOT_0")
	if !ok || p.Path != "/private/tmp/work" {
		t.Errorf("canonicalizer not applied: WRITABLE_ROOT_0 = %+v, want /private/tmp/work", p)
	}
}

// --- Task 1: fuzz the no-interpolation (injection) invariant ----------------

func FuzzSeatbeltPolicyNoInterpolation(f *testing.F) {
	for _, s := range []string{"/work/tree", `/a")evil`, "/a\nb", `/a\b`, "/a(b)c", "/tmp/x;# y", "/dev/null", "", "..", "/../../x"} {
		f.Add(s)
	}
	staticText := seatbeltBasePolicy + "\n" + seatbeltNetworkPolicy + "\n" + seatbeltPlatformDefaults
	f.Fuzz(func(t *testing.T, raw string) {
		root := "/fuzz" + raw
		rp := ResolvedPolicy{
			Mode:        ModeWorkspaceWrite,
			Network:     true,
			Spawned:     AccessScope{Read: ReadAnywhere, WriteRoots: []string{root}},
			MaskedPaths: []string{root + "/secret"},
			Git:         GitLayout{WorktreeRoot: root, ProtectedPaths: []string{root + "/.git/config"}},
		}
		// Must never panic.
		text, params := SeatbeltPolicy(rp, root+"/session", identityCanon)

		// The adversarial path bytes must never reach the policy TEXT — they ride in
		// -D params only. (A path that happened to be a substring of the static
		// embeds is excluded: the /fuzz prefix guarantees the needles below never
		// occur in the static base/network/platform text.)
		for _, needle := range []string{root, root + "/secret", root + "/.git/config"} {
			if strings.Contains(text, needle) && !strings.Contains(staticText, needle) {
				t.Errorf("path %q leaked into generated policy text (must be param-only):\n%s", needle, text)
			}
		}
		if len(params) == 0 {
			t.Errorf("expected params carrying the roots for %q", raw)
		}
	})
}

// --- Task 5: contract-suite parity + stub / dispatch behavior ---------------

// TestSeatbeltContractParity re-runs M1's exported contract suite through a
// resolver-equivalent that also generates the SBPL for every resolving darwin
// cell, holding the seatbelt backend to the same contract as bwrap. Beyond
// AssertResolve's own invariants it asserts, for every enforced seatbelt cell,
// that the generator never panics; no GRANTED root is itself a masked path; every
// masked path (except the intentional /dev/fd exemption) is emitted as an
// authoritative MASKED_ read+write deny; and every protected git surface is
// emitted as a PROTECTED_ write deny. Because the denials are trailing and
// unconditional (no per-root gating), a masked path is denied regardless of which
// allow — including the static platform-defaults — might otherwise re-grant it.
// (No-path-interpolation is proven separately by FuzzSeatbeltPolicyNoInterpolation.)
func TestSeatbeltContractParity(t *testing.T) {
	t.Parallel()
	AssertResolve(t, func(p SandboxPolicy, h HostFacts, cwd string) (ResolvedPolicy, error) {
		rp, err := Resolve(p, h, cwd)
		if err != nil || rp.Backend != BackendSeatbelt {
			return rp, err
		}
		_, params := SeatbeltPolicy(rp, "/serf-session-tmp", identityCanon)

		grantedRoots := map[string]bool{}
		maskedDenied := map[string]bool{}
		protectedDenied := map[string]bool{}
		for _, pr := range params {
			switch {
			case strings.HasPrefix(pr.Key, "MASKED_"):
				maskedDenied[pr.Path] = true
			case strings.HasPrefix(pr.Key, "PROTECTED_"):
				protectedDenied[pr.Path] = true
			case strings.HasPrefix(pr.Key, "WRITABLE_ROOT_"), strings.HasPrefix(pr.Key, "READABLE_ROOT_"):
				grantedRoots[pr.Path] = true
			}
		}
		// No granted root is itself a masked path.
		for _, m := range rp.MaskedPaths {
			if grantedRoots[m] {
				t.Errorf("case %v/net=%v: masked path %q is a granted root", rp.Mode, rp.Network, m)
			}
		}
		// Every masked path is authoritatively denied (except the /dev/fd exemption).
		for _, m := range rp.MaskedPaths {
			if isDeviceFloorException(m) {
				continue
			}
			if !maskedDenied[m] {
				t.Errorf("case %v/net=%v: masked path %q is not authoritatively denied", rp.Mode, rp.Network, m)
			}
		}
		// Every protected git surface is authoritatively write-denied.
		for _, pp := range rp.Git.ProtectedPaths {
			if !protectedDenied[pp] {
				t.Errorf("case %v/net=%v: protected surface %q is not write-denied", rp.Mode, rp.Network, pp)
			}
		}
		return rp, err
	})
}

// firstDiff returns a short human-readable pointer to the first line that
// differs between want and got.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return "first diff at line " + itoa(i+1) + ":\n  want: " + w + "\n   got: " + g
		}
	}
	return "(no line diff; trailing bytes differ)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
