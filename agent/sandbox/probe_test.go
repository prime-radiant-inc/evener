package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/identifier"
)

// TestParseGitCeilingDirs keeps only absolute, resolved entries and ignores
// relative ones, as git does (#3724).
func TestParseGitCeilingDirs(t *testing.T) {
	abs := t.TempDir()
	got := parseGitCeilingDirs("relative" + string(os.PathListSeparator) + abs + string(os.PathListSeparator))
	if want := []string{identifier.ResolveClean(abs)}; !slices.Equal(got, want) {
		t.Fatalf("parseGitCeilingDirs = %v, want %v", got, want)
	}
	if got := parseGitCeilingDirs(""); got != nil {
		t.Fatalf("parseGitCeilingDirs(\"\") = %v, want nil", got)
	}
}

// The capability probe must exercise the exact version-gated flags Wrap emits,
// so an older bwrap that lacks --argv0 / --new-session (Ubuntu 22.04 = 0.6.1,
// Debian 12 = 0.8.0) fails the probe and is reported not-capable — rather than
// probing "capable" and then failing every real spawn with "unknown option
// --argv0" per the fail-closed floor.
func TestBwrapProbeExercisesWrapFlags(t *testing.T) {
	t.Parallel()
	args := bwrapProbeArgs("/usr/bin/bwrap")
	for _, want := range []string{"--argv0", "--new-session", "--unshare-user", "--unshare-pid", "--proc", "--dev"} {
		if !slices.Contains(args, want) {
			t.Errorf("probe argv must exercise %q (Wrap emits it), got %v", want, args)
		}
	}
}

// The per-root overlay probe runs the same namespaced base as the capability
// probe, plus the exact overlay flag pair buildBwrapArgv emits for the root, so
// its verdict is whether that root can be an unprivileged overlay lower (#4220).
func TestBwrapOverlayProbeArgsUseTheEmittedOverlayFlags(t *testing.T) {
	t.Parallel()
	home := overlayFixtureDir(t)
	cache := filepath.Join(home, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	emitted := buildBwrapArgv(ResolvedPolicy{
		Mode: ModeWorkspaceWrite, Backend: BackendBwrap, CacheStrategy: CacheOverlay,
		CacheRoots: []string{cache}, Spawned: AccessScope{Read: ReadAnywhere},
		resolveHost: HostFacts{OS: "linux", Home: home},
	}, "", "/work")
	i := slices.Index(emitted, "--overlay-src")
	if i < 0 || i+4 > len(emitted) {
		t.Fatalf("buildBwrapArgv emitted no overlay for %q: %v", cache, emitted)
	}
	want := emitted[i : i+4]

	args := bwrapOverlayProbeArgs("/usr/bin/bwrap", cache)
	for _, want := range []string{"--unshare-user", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev"} {
		if !slices.Contains(args, want) {
			t.Errorf("overlay probe argv must keep the base flag %q, got %v", want, args)
		}
	}
	j := slices.Index(args, "--overlay-src")
	if j < 0 || !slices.Equal(args[j:j+4], want) {
		t.Errorf("overlay probe argv must emit the pair buildBwrapArgv does %v, got %v", want, args)
	}
}

// The probe reports a root the trivial overlay mount cannot be made on as
// un-overlayable, and a root it can as fine (#4220).
func TestBwrapOverlayLowerWorksReportsTheProbeResult(t *testing.T) {
	t.Parallel()
	if bwrapOverlayLowerWorks(overlayProbeSystem{runErr: errors.New("EINVAL")}, "/usr/bin/bwrap", "/cache/root") {
		t.Error("a failing trivial overlay mount must report the root un-overlayable")
	}
	if !bwrapOverlayLowerWorks(overlayProbeSystem{}, "/usr/bin/bwrap", "/cache/root") {
		t.Error("a succeeding trivial overlay mount must report the root overlaid")
	}
}

// scriptedOverlaySystem answers every command with the error its hook returns,
// so a test can change what the host can overlay between probes.
type scriptedOverlaySystem struct {
	stubProbeSystem
	runErr func() error
}

func (s scriptedOverlaySystem) run(context.Context, string, ...string) error { return s.runErr() }

// A cache root can be retargeted under a session (a symlinked parent moved onto
// another filesystem), so a remembered verdict is keyed by the root's resolved
// location: a retarget is a new key and is probed again. A stale "fine" would
// put the bad overlay back in the argv and abort every spawn (#4220).
func TestOverlayLowerProbeReJudgesARetargetedRoot(t *testing.T) {
	t.Parallel()
	dir := overlayFixtureDir(t)
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	unoverlayable := false
	probe := memoizedOverlayLower(scriptedOverlaySystem{runErr: func() error {
		if unoverlayable {
			return errors.New("EINVAL")
		}
		return nil
	}}, "/usr/bin/bwrap")
	if !probe(link) {
		t.Fatal("a symlinked root on a filesystem the overlay takes must probe fine")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	unoverlayable = true
	if probe(link) {
		t.Error("a root retargeted onto an un-overlayable filesystem must be probed again")
	}
}

// A root that is not a directory yet is not overlaid, and its failure must not
// be remembered: once its storage appears on a filesystem the overlay takes, it
// is judged afresh and served (#4220).
func TestOverlayLowerProbeDoesNotMemoizeAnUnavailableRoot(t *testing.T) {
	t.Parallel()
	dir := overlayFixtureDir(t)
	drive := filepath.Join(dir, "unmounted", "drive")
	link := filepath.Join(dir, "cache")
	if err := os.Symlink(drive, link); err != nil {
		t.Fatal(err)
	}
	unoverlayable := true
	probe := memoizedOverlayLower(scriptedOverlaySystem{runErr: func() error {
		if unoverlayable {
			return errors.New("EINVAL")
		}
		return nil
	}}, "/usr/bin/bwrap")
	if probe(link) {
		t.Fatal("a root that is not a directory must probe as un-overlayable")
	}
	if err := os.MkdirAll(drive, 0o755); err != nil {
		t.Fatal(err)
	}
	unoverlayable = false
	if !probe(link) {
		t.Error("a root that appears after being unavailable must be judged afresh, not answered from the memo")
	}
}

// overlayProbeSystem is stubProbeSystem whose bwrap reports (or does not report)
// overlay support, with a settable per-command result.
type overlayProbeSystem struct {
	stubProbeSystem
	help   []byte
	runErr error
}

func (s overlayProbeSystem) lookPath(name string) (string, error) {
	if name == "bwrap" {
		return "/usr/bin/bwrap", nil
	}
	return "", errors.New("not found")
}

func (s overlayProbeSystem) combinedOutput(context.Context, string, ...string) ([]byte, error) {
	return s.help, nil
}

func (s overlayProbeSystem) run(context.Context, string, ...string) error { return s.runErr }

// The per-root overlay probe is wired only on a host whose bwrap supports
// overlay: without support every root would report un-overlayable, and the
// overlay strategy would be lost on a build that cannot emit the flags at all
// (#4220).
func TestProbeHostWiresTheOverlayLowerProbeOnlyWithOverlaySupport(t *testing.T) {
	t.Parallel()
	supported := probeHost(overlayProbeSystem{help: []byte("--overlay-src")})
	if supported.OverlayLowerOK == nil {
		t.Fatal("an overlay-capable bwrap must supply the per-root overlay probe")
	}
	if !supported.OverlayLowerOK("/cache") {
		t.Error("the probe must report a succeeding trivial overlay mount as fine")
	}
	unsupported := probeHost(overlayProbeSystem{help: []byte("no overlay here")})
	if unsupported.OverlayLowerOK != nil {
		t.Error("a bwrap without overlay support must supply no per-root overlay probe")
	}
}

// TestFakeProberRepresentsFloorRows proves every host tier of the spec's
// fail-closed floor matrix is representable as HostFacts and that the derived
// capability predicate (SeatbeltAvailable) reads correctly.
// The resolver keys the run-vs-refuse decision off exactly these facts, so if a
// tier were unrepresentable the contract suite could not express it.
func TestFakeProberRepresentsFloorRows(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		facts        HostFacts
		wantBwrap    bool
		wantSeatbelt bool
		wantOverlay  bool
	}{
		{
			name:        "bwrap-capable linux with overlay",
			facts:       HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true},
			wantBwrap:   true,
			wantOverlay: true,
		},
		{
			name:        "bwrap-capable linux without overlay",
			facts:       HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false},
			wantBwrap:   true,
			wantOverlay: false,
		},
		{
			name:      "non-bwrap linux",
			facts:     HostFacts{OS: "linux"},
			wantBwrap: false,
		},
		{
			name:      "windows",
			facts:     HostFacts{OS: "windows"},
			wantBwrap: false,
		},
		{
			name:         "darwin with sandbox-exec",
			facts:        HostFacts{OS: "darwin", SandboxExecPath: "/usr/bin/sandbox-exec"},
			wantSeatbelt: true,
		},
		{
			name:         "darwin without sandbox-exec",
			facts:        HostFacts{OS: "darwin"},
			wantSeatbelt: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Prober = FakeProber{Facts: tc.facts}
			got := p.Probe()
			if !reflect.DeepEqual(got, tc.facts) {
				t.Fatalf("FakeProber.Probe() = %+v, want %+v", got, tc.facts)
			}
			if got.BwrapCapable != tc.wantBwrap {
				t.Errorf("BwrapCapable = %v, want %v", got.BwrapCapable, tc.wantBwrap)
			}
			if got.SeatbeltAvailable() != tc.wantSeatbelt {
				t.Errorf("SeatbeltAvailable() = %v, want %v", got.SeatbeltAvailable(), tc.wantSeatbelt)
			}
			if got.OverlaySupported != tc.wantOverlay {
				t.Errorf("OverlaySupported = %v, want %v", got.OverlaySupported, tc.wantOverlay)
			}
		})
	}
}

// TestSeatbeltRequiresDarwin guards that sandbox-exec presence on a non-darwin
// OS does not spuriously report Seatbelt available — the backend is macOS-only.
func TestSeatbeltRequiresDarwin(t *testing.T) {
	t.Parallel()
	h := HostFacts{OS: "linux", SandboxExecPath: "/usr/bin/sandbox-exec"}
	if h.SeatbeltAvailable() {
		t.Error("SeatbeltAvailable() must be false on a non-darwin OS even if sandbox-exec is present")
	}
}

// npm and cargo keep no go-env-style file for their cache location, so the probe
// reads it from the environment alone (#4225). Unset leaves the facts empty,
// which the resolver reads as "$HOME's default".
func TestProbeReadsNpmAndCargoCacheEnv(t *testing.T) {
	t.Parallel()
	facts := probeHost(stubProbeSystem{env: map[string]string{"CARGO_HOME": "/custom/cargo", "npm_config_cache": "/custom/npm"}})
	if facts.CargoHome != "/custom/cargo" || facts.NpmConfigCache != "/custom/npm" {
		t.Errorf("probeHost CargoHome=%q NpmConfigCache=%q, want the environment values", facts.CargoHome, facts.NpmConfigCache)
	}
	unset := probeHost(stubProbeSystem{})
	if unset.CargoHome != "" || unset.NpmConfigCache != "" {
		t.Errorf("unset CARGO_HOME and npm_config_cache must leave the facts empty, got %q and %q", unset.CargoHome, unset.NpmConfigCache)
	}
}

// The login user's home comes from the user database, not this process's HOME:
// a child with no HOME of its own resolves "~" to the former, so the env floor
// must judge that directory (#4220).
func TestProbeReadsTheLoginHome(t *testing.T) {
	t.Parallel()
	facts := probeHost(stubProbeSystem{loginHome: "/fixture-home/login"})
	if facts.LoginHome != "/fixture-home/login" {
		t.Errorf("probeHost LoginHome=%q, want the user database's home", facts.LoginHome)
	}
}

// npm reads its environment configuration through a lowercased key, so
// NPM_CONFIG_CACHE names the same setting as npm_config_cache. A host that sets
// the uppercase spelling must still get the root overlaid, or its cache writes
// stay read-only.
func TestProbeReadsNpmCacheEnvCaseInsensitively(t *testing.T) {
	t.Parallel()
	facts := probeHost(stubProbeSystem{env: map[string]string{"NPM_CONFIG_CACHE": "/custom/npm"}})
	if facts.NpmConfigCache != "/custom/npm" {
		t.Errorf("probeHost NpmConfigCache=%q, want the uppercase NPM_CONFIG_CACHE value", facts.NpmConfigCache)
	}
}

// npm resolves its cache location from .npmrc files too, so a host that moves
// it only there must still get the root overlaid (#4542). The probe reads the
// user and global files; the project file is read at resolution.
type npmrcProbeSystem struct {
	stubProbeSystem
	files map[string][]byte
}

func (s npmrcProbeSystem) readFile(path string) ([]byte, error) {
	if data, ok := s.files[path]; ok {
		return data, nil
	}
	return nil, errors.New("no such file")
}

func npmrcProbe(env map[string]string, files map[string][]byte) npmrcProbeSystem {
	base := stubProbeSystem{env: env}
	return npmrcProbeSystem{stubProbeSystem: base, files: files}
}

func TestProbeReadsNpmCacheFromConfigFiles(t *testing.T) {
	t.Parallel()
	const user, global = "/fixture-home/tester/npmrc-user", "/fixture-home/tester/npmrc-global"
	facts := probeHost(npmrcProbe(
		map[string]string{
			"NPM_CONFIG_USERCONFIG":   user,
			"npm_config_globalconfig": global,
		},
		map[string][]byte{
			user:   []byte("# user config\ncache = /from/user/npm\n"),
			global: []byte("[npm]\n; global config\ncache=\"/from/global/npm\"\n"),
		},
	))
	if want := []string{"/from/user/npm", "/from/global/npm"}; !slices.Equal(facts.NpmrcCaches, want) {
		t.Errorf("probeHost NpmrcCaches = %v, want %v", facts.NpmrcCaches, want)
	}
}

func TestProbeReadsDefaultUserNpmrcAndPrefixGlobalNpmrc(t *testing.T) {
	t.Parallel()
	// stubProbeSystem's user home is /Users/tester; the global file follows a
	// configured npm prefix.
	facts := probeHost(npmrcProbe(
		map[string]string{"npm_config_prefix": "/opt/node"},
		map[string][]byte{
			"/Users/tester/.npmrc": []byte("cache=/from/default/user\n"),
			"/opt/node/etc/npmrc":  []byte("cache=/from/prefix/global\n"),
		},
	))
	if want := []string{"/from/default/user", "/from/prefix/global"}; !slices.Equal(facts.NpmrcCaches, want) {
		t.Errorf("probeHost NpmrcCaches = %v, want %v", facts.NpmrcCaches, want)
	}
	// A relative cache resolves against npm's working directory, so it names no
	// stable root and is not served.
	rel := probeHost(npmrcProbe(
		map[string]string{"npm_config_userconfig": "/u/npmrc"},
		map[string][]byte{"/u/npmrc": []byte("cache=relative/npm\n")},
	))
	if len(rel.NpmrcCaches) != 0 {
		t.Errorf("a relative npmrc cache must add no root, got %v", rel.NpmrcCaches)
	}
}

func TestProbeExpandsNpmrcVariables(t *testing.T) {
	t.Parallel()
	facts := probeHost(npmrcProbe(
		map[string]string{
			"npm_config_userconfig": "/fixture-home/u/.npmrc",
			"HOME":                  "/fixture-home/u",
		},
		map[string][]byte{"/fixture-home/u/.npmrc": []byte("cache=${HOME}/.npm-cache\n")},
	))
	if want := []string{"/fixture-home/u/.npm-cache"}; !slices.Equal(facts.NpmrcCaches, want) {
		t.Errorf("probeHost NpmrcCaches = %v, want the expanded %v", facts.NpmrcCaches, want)
	}
}

// npm's config parser drops an unquoted inline comment. A comment left in the
// value would make Evener overlay a directory npm never uses (#4542 review).
func TestProbeStripsNpmrcInlineComments(t *testing.T) {
	t.Parallel()
	facts := probeHost(npmrcProbe(
		map[string]string{"npm_config_userconfig": "/fixture-home/u/.npmrc"},
		map[string][]byte{"/fixture-home/u/.npmrc": []byte(
			"cache=/from/user/npm # shared cache\n" +
				"cache=\"/quoted/path # not a comment\"\n")},
	))
	if want := []string{"/from/user/npm", "/quoted/path # not a comment"}; !slices.Equal(facts.NpmrcCaches, want) {
		t.Errorf("probeHost NpmrcCaches = %v, want %v", facts.NpmrcCaches, want)
	}
}

// TestRealProberOptIn exercises the real host prober, but only when explicitly
// opted in (EVENER_SANDBOX_PROBE_HOST=1). CI unit runs skip it so they never shell
// out to bwrap — probing is off the hermetic unit
// path. When it does run, it asserts only invariants that hold on any host: the
// probe returns the true GOOS and never panics.
func TestRealProberOptIn(t *testing.T) {
	if os.Getenv("EVENER_SANDBOX_PROBE_HOST") != "1" {
		t.Skip("set EVENER_SANDBOX_PROBE_HOST=1 to probe the real host")
	}
	facts := RealProber{}.Probe()
	if facts.OS == "" {
		t.Fatal("RealProber returned an empty OS")
	}
	t.Logf("real host facts: %+v", facts)
}
