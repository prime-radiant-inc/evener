package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestPreflightRecordsNumericEffectiveUID proves preflight records the host's
// numeric effective uid (`id -u`), which the supervised darwin restart
// interpolates into `gui/<uid>/<label>`. A non-numeric answer is recorded as
// absent rather than guessed, so the restart falls through to the ad hoc path
// instead of emitting a wrong or injected word.
func TestPreflightRecordsNumericEffectiveUID(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	facts, err := m.preflight(context.Background(), host)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if facts.UID != "1000" {
		t.Fatalf("UID = %q, want 1000", facts.UID)
	}
}

func TestParseEffectiveUID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1000\n", "1000"},
		{"  0  ", "0"},
		{"", ""},
		{"$(id -u)", ""},
		{"root", ""},
		{"1000 1000", ""},
	}
	for _, tc := range cases {
		if got := parseEffectiveUID([]byte(tc.in)); got != tc.want {
			t.Errorf("parseEffectiveUID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestOSArchMappingParity pins the uname mapping to install.sh:35-59. If
// install.sh grows a target, this table and mapOS/mapArch must change together.
func TestOSArchMappingParity(t *testing.T) {
	osCases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Linux\n", "linux", true},
		{"Darwin\n", "darwin", true},
		{"  Linux  ", "linux", true},
		{"FreeBSD\n", "", false},
		{"Windows_NT\n", "", false},
	}
	for _, tc := range osCases {
		got, ok := mapOS(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("mapOS(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}

	archCases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"x86_64\n", "amd64", true},
		{"amd64\n", "amd64", true},
		{"arm64\n", "arm64", true},
		{"aarch64\n", "arm64", true},
		{"i686\n", "", false},
		{"riscv64\n", "", false},
	}
	for _, tc := range archCases {
		got, ok := mapArch(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("mapArch(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}

	targets := []struct {
		goos, goarch string
		want         bool
	}{
		{"linux", "amd64", true},
		{"darwin", "arm64", true},
		{"linux", "arm64", false},
		{"darwin", "amd64", false},
		{"windows", "amd64", false},
	}
	for _, tc := range targets {
		if got := targetSupported(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("targetSupported(%s/%s) = %v, want %v", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestParseEnvProbe(t *testing.T) {
	env, err := parseEnvProbe([]byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=/xdg/config\n"))
	if err != nil {
		t.Fatalf("parseEnvProbe: %v", err)
	}
	if env["HOME"] != "/home/dev" || env["XDG_STATE_HOME"] != "" || env["XDG_CONFIG_HOME"] != "/xdg/config" {
		t.Fatalf("env = %v", env)
	}

	if _, err := parseEnvProbe([]byte("XDG_STATE_HOME=/x\n")); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("missing HOME err = %v, want ErrPreflightDecode", err)
	}
	if _, err := parseEnvProbe([]byte("HOME /home/dev\n")); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("malformed line err = %v, want ErrPreflightDecode", err)
	}
}

func TestResolveRootsNoXDGFallsBack(t *testing.T) {
	env := map[string]string{"HOME": "/home/dev", "XDG_STATE_HOME": "", "XDG_CONFIG_HOME": ""}
	configRoot, stateRoot := resolveRoots("linux", env)
	if want := "/home/dev/.config/evener"; configRoot != want {
		t.Errorf("configRoot = %q, want %q", configRoot, want)
	}
	if want := "/home/dev/.local/state/evener"; stateRoot != want {
		t.Errorf("stateRoot = %q, want %q", stateRoot, want)
	}
}

func TestResolveRootsRespectsXDG(t *testing.T) {
	env := map[string]string{"HOME": "/home/dev", "XDG_STATE_HOME": "/xdg/state", "XDG_CONFIG_HOME": "/xdg/config"}
	configRoot, stateRoot := resolveRoots("linux", env)
	if want := "/xdg/config/evener"; configRoot != want {
		t.Errorf("configRoot = %q, want %q", configRoot, want)
	}
	if want := "/xdg/state/evener"; stateRoot != want {
		t.Errorf("stateRoot = %q, want %q", stateRoot, want)
	}
}

func TestResolveRootsEmptyHome(t *testing.T) {
	env := map[string]string{"HOME": "", "XDG_STATE_HOME": "", "XDG_CONFIG_HOME": ""}
	configRoot, stateRoot := resolveRoots("linux", env)
	if configRoot != "" {
		t.Errorf("configRoot = %q, want empty (no HOME, no XDG_CONFIG_HOME)", configRoot)
	}
	if want := filepath.Join(".", ".local", "state", "evener"); stateRoot != want {
		t.Errorf("stateRoot = %q, want %q", stateRoot, want)
	}
}

func TestParseLaunchCheck(t *testing.T) {
	lc, err := parseLaunchCheck([]byte(goodLaunchCheck))
	if err != nil {
		t.Fatalf("parseLaunchCheck: %v", err)
	}
	if lc.Protocol != "evener-appwire-v6" || lc.Version != "dev" || len(lc.LaunchFlags) != 1 || lc.LaunchFlags[0] != "api-log" {
		t.Fatalf("launchCheck = %+v", lc)
	}

	if _, err := parseLaunchCheck([]byte("not json")); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("bad json err = %v, want ErrPreflightDecode", err)
	}
	if _, err := parseLaunchCheck([]byte(`{"version":"dev"}`)); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("missing protocol err = %v, want ErrPreflightDecode", err)
	}
	// The launch contract always reports a build version (buildinfo.Version(), or
	// "dev"). An empty one is a broken contract, not a version that merely differs:
	// reading it as a mismatch would send the host into a deploy whose identity was
	// never established.
	for _, out := range []string{
		`{"protocol":"evener-appwire-v6"}`,
		`{"protocol":"evener-appwire-v6","version":""}`,
		`{"protocol":"evener-appwire-v6","version":"   "}`,
	} {
		if _, err := parseLaunchCheck([]byte(out)); !errors.Is(err, ErrPreflightDecode) {
			t.Fatalf("parseLaunchCheck(%s) err = %v, want ErrPreflightDecode", out, err)
		}
	}
}

func TestIsProtocolMismatchOutput(t *testing.T) {
	yes := []string{
		`unsupported appwire protocol "evener-appwire-v4" (supported "evener-appwire-v6")`,
		`evener launch-check protocol "x" does not match Hub protocol "y"`,
	}
	for _, s := range yes {
		if !isProtocolMismatchOutput([]byte(s)) {
			t.Errorf("isProtocolMismatchOutput(%q) = false, want true", s)
		}
	}
	if isProtocolMismatchOutput([]byte("connection refused")) {
		t.Error("unrelated output classified as protocol mismatch")
	}
}

func TestIsAuthFailure(t *testing.T) {
	auth := []string{
		"bob@host: Permission denied (publickey).",
		"jesse@jesse-paradise-park: Permission denied (publickey,password,keyboard-interactive).",
		"Host key verification failed.",
		"no mutual signature algorithm",
		"Too many authentication failures",
	}
	for _, s := range auth {
		if !isAuthFailure(s) {
			t.Errorf("isAuthFailure(%q) = false, want true", s)
		}
	}
	// The remote command's own failures arrive on the same stream, so a bare
	// "Permission denied" must not be read as ssh refusing the key.
	notAuth := []string{
		"Connection timed out",
		"Permission denied",
		"sh: /usr/local/bin/evener: Permission denied",
		"Authentication failed",
	}
	for _, s := range notAuth {
		if isAuthFailure(s) {
			t.Errorf("isAuthFailure(%q) = true, want false", s)
		}
	}
}

// A failed Run keeps ssh's diagnostics and the remote command's stderr on the
// same stream, and ssh forwards the remote command's exit status unchanged —
// including 255, the status ssh(1) uses for its own failures. Neither signal can
// separate the two, so a completed Run that carries an auth marker stays
// retryable; only a failure to start ssh, which never ran a remote command, can
// be attributed to ssh at all — and that class is not terminal either (see
// ErrSSHAuth).
func TestSSHRunFailureRequiresSSHExitStatus(t *testing.T) {
	const marker = "bob@alpha.example: Permission denied (publickey).\n"
	sshExit := exitStatus(t, 255)
	remoteExit := exitStatus(t, 1)
	cases := []struct {
		name string
		err  error
		diag string
		want error
	}{
		{
			// ssh itself exits 255, but the remote command's own status is forwarded
			// unchanged, so 255 alone cannot prove the refusal is ssh's.
			name: "a completed ssh run exiting 255 with the marker stays retryable",
			err:  &RunError{Stderr: []byte(marker), Err: sshExit},
			diag: marker,
			want: ErrSSHStart,
		},
		{
			name: "the remote command's own exit with the marker stays retryable",
			err:  &RunError{Stderr: []byte(marker), Err: remoteExit},
			diag: marker,
			want: ErrSSHStart,
		},
		{
			name: "ssh exit 255 without the marker stays retryable",
			err:  &RunError{Stderr: []byte("ssh: connect to host alpha port 22: refused\n"), Err: sshExit},
			diag: "ssh: connect to host alpha port 22: refused",
			want: ErrSSHStart,
		},
		{
			// A failed Start never spawned ssh, so no remote command ran and the
			// marker is necessarily ssh's own: the one attributable case. It stays
			// retryable because a real spawn failure carries no diagnostic, so an
			// authentication refusal ordinarily lands in ErrSSHStart.
			name: "a failed Start never ran a remote command, so the marker decides",
			err:  errors.New("fork/exec ssh: no such file or directory"),
			diag: marker,
			want: ErrSSHAuth,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := sshRunFailure("alpha", "preflight", tc.err, tc.diag); !errors.Is(err, tc.want) {
				t.Fatalf("sshRunFailure = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestPreflightMissingExecutableCarriesTheDeployRemedy pins the remedy half of
// the terminal missing-executable refusal: with no deploy path configured the
// host has no evener and nothing can install one, so the refusal must name the
// flags that configure a deploy (Options.DeployHelp), or the library's own field
// for an embedder that names none — the same seam the terminal version refusal
// uses. Before this the operator saw only that the executable was missing.
func TestPreflightMissingExecutableCarriesTheDeployRemedy(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	newRunner := func() *fakeRunner {
		return &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(joined, "uname -s"):
				return []byte("Linux\n"), nil
			case strings.HasSuffix(joined, "uname -m"):
				return []byte("x86_64\n"), nil
			case strings.Contains(joined, "XDG_STATE_HOME"):
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.HasSuffix(joined, "id -u"):
				return []byte("1000\n"), nil
			case strings.Contains(joined, "launch-check"):
				return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
			case strings.Contains(joined, `if [ -n "${HOME-}" ]`):
				return nil, nil // nothing at the installer default either
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
	}

	const help = "set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path>"
	m := newTestManager(t, testRegistry(t, host), newRunner(), Options{DeployHelp: help})
	_, err := m.preflight(context.Background(), host)
	if !errors.Is(err, ErrExecutableMissing) {
		t.Fatalf("preflight err = %v, want ErrExecutableMissing", err)
	}
	if !strings.Contains(err.Error(), help) {
		t.Fatalf("refusal does not name the flags that configure a deploy: %v", err)
	}

	m = newTestManager(t, testRegistry(t, host), newRunner(), Options{})
	_, err = m.preflight(context.Background(), host)
	if !errors.Is(err, ErrExecutableMissing) {
		t.Fatalf("preflight err = %v, want ErrExecutableMissing", err)
	}
	if !strings.Contains(err.Error(), "set Options.BuildSource") {
		t.Fatalf("refusal does not keep the library's remedy for an embedder with no flags: %v", err)
	}
}
