package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	if lc.Protocol != "evener-appwire-v7" || lc.Version != "dev" || len(lc.LaunchFlags) != 1 || lc.LaunchFlags[0] != "api-log" {
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
		`{"protocol":"evener-appwire-v7"}`,
		`{"protocol":"evener-appwire-v7","version":""}`,
		`{"protocol":"evener-appwire-v7","version":"   "}`,
	} {
		if _, err := parseLaunchCheck([]byte(out)); !errors.Is(err, ErrPreflightDecode) {
			t.Fatalf("parseLaunchCheck(%s) err = %v, want ErrPreflightDecode", out, err)
		}
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
			case strings.Contains(joined, executableProbeRemote("evener")):
				// The dedicated executable probe: absent.
				return nil, exitStatus(t, 1)
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

// TestPreflightExecutableMissingComesFromTheProbe pins spec 04's "Missing
// executable" contract: the verified absent result comes from a dedicated
// executable probe's own sentinel (exit 0 present / exit 1 absent), never from
// the failed launch-check's exit status or its not-found text. ssh writes its
// own diagnostics and the remote command's stderr to one merged stream, so the
// text is not separable evidence; and ssh exits 255 for its own failures, so a
// 255 or a spawn failure is a transport failure, while only the probe's 1 is the
// verified absent result. Anything else leaves the launch-check failure in its
// retryable class and never runs the installer.
func TestPreflightExecutableMissingComesFromTheProbe(t *testing.T) {
	const target = "/opt/evener/bin/evener"
	const notFound = "sh: 1: " + target + ": not found\n"
	cases := []struct {
		name        string
		launchOut   []byte
		launchErr   error
		probeOut    []byte
		probeErr    error
		wantMissing bool
		deploy      bool
	}{
		{
			// The launch-check's 127 and its text are deliberately not
			// not-found shaped: only the probe proves absence now.
			name:        "the probe's own 1 verifies absence without not-found text",
			launchOut:   []byte("evener: launch failed\n"),
			launchErr:   exitStatus(t, 127),
			probeErr:    exitStatus(t, 1),
			wantMissing: true,
		},
		{
			name:        "not-found text is not read when the probe proves presence",
			launchOut:   []byte(notFound),
			launchErr:   exitStatus(t, 127),
			probeOut:    []byte(target + "\n"),
			wantMissing: false,
		},
		{
			name:        "ssh's own 255 is a transport failure, not absence",
			launchOut:   []byte(notFound),
			launchErr:   exitStatus(t, 127),
			probeErr:    exitStatus(t, 255),
			wantMissing: false,
		},
		{
			name:        "an unsearchable ancestor cannot verify absence",
			launchOut:   []byte(notFound),
			launchErr:   exitStatus(t, 126),
			probeErr:    exitStatus(t, 2),
			wantMissing: false,
		},
		{
			name:        "a probe that cannot run is not absence",
			launchOut:   []byte(notFound),
			launchErr:   exitStatus(t, 127),
			probeErr:    errors.New("fork/exec ssh: no such file or directory"),
			wantMissing: false,
		},
		{
			name:        "verified absence with a deploy configured defers to the install ladder",
			launchOut:   []byte("evener: launch failed\n"),
			launchErr:   exitStatus(t, 127),
			probeErr:    exitStatus(t, 1),
			wantMissing: true,
			deploy:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: target}
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
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
					return tc.launchOut, tc.launchErr
				case strings.Contains(joined, executableProbeRemote(target)):
					return tc.probeOut, tc.probeErr
				case strings.Contains(joined, `if [ -n "${HOME-}" ]`):
					return nil, nil // nothing at the installer default either
				default:
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}
			}}
			opts := Options{DeployHelp: "deploy help"}
			if tc.deploy {
				opts.BuildBinary = writeStageBinary
			}
			m := newTestManager(t, testRegistry(t, host), fr, opts)

			pf, err := m.preflight(context.Background(), host)
			if tc.wantMissing {
				if tc.deploy {
					if err != nil {
						t.Fatalf("preflight err = %v, want the deploy ladder to defer the verified missing executable", err)
					}
					if !pf.ExecutableMissing {
						t.Fatal("Preflight.ExecutableMissing = false, want the verified fact recorded")
					}
					return
				}
				if !errors.Is(err, ErrExecutableMissing) {
					t.Fatalf("preflight err = %v, want ErrExecutableMissing from the probe's 1", err)
				}
				if !pf.ExecutableMissing {
					t.Fatal("Preflight.ExecutableMissing = false, want the verified fact recorded")
				}
				var probed bool
				for _, argv := range fr.recordedRuns() {
					if strings.Contains(strings.Join(argv, " "), executableProbeRemote(target)) {
						probed = true
					}
				}
				if !probed {
					t.Fatalf("preflight did not run the dedicated executable probe: %v", fr.recordedRuns())
				}
				return
			}
			if errors.Is(err, ErrExecutableMissing) {
				t.Fatalf("preflight err = %v; a missing executable must be verified by the probe, not by the launch-check's status or text", err)
			}
			if tc.probeErr == nil {
				if !errors.Is(err, ErrEndpointMigration) || !isTerminal(err) {
					t.Fatalf("present binary without contract: %v", err)
				}
				return
			}

			if !errors.Is(err, ErrSSHStart) {
				t.Fatalf("preflight err = %v, want the retryable ErrSSHStart class", err)
			}
		})
	}
}

// A present selected path, including an unreadable file, cannot authorize a
// fresh install. Execute the actual shell probes rather than simulate statuses.
func TestPreflightPresentFileIsNotFreshInstall(t *testing.T) {
	for _, selection := range []string{"fallback", "explicit"} {
		for _, mode := range []os.FileMode{0600, 0000} {
			t.Run(fmt.Sprintf("%s/%o", selection, mode), func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("HOME", root)
				t.Setenv("PATH", root)
				target := filepath.Join(root, ".local", "bin", "evener")
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("sentinel installed file"), mode); err != nil {
					t.Fatal(err)
				}
				host := hostreg.Host{Name: "selected", SSH: "remote"}
				if selection == "explicit" {
					host.EvenerPath = target
				}
				base := cannedRun(nil)
				fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					if strings.Contains(remote, "launch-check") || strings.HasPrefix(remote, "if ") {
						return (execRunner{}).Run(ctx, []string{"/bin/sh", "-c", remote}, in)
					}
					return base(ctx, argv, in)
				}}
				bridgeFailure := errors.New("present junk binary cannot start a bridge")
				fr.startFn = func(context.Context, []string, io.Writer) (Stdio, error) {
					return nil, bridgeFailure
				}
				m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: func(context.Context, string, string, string) error {
					t.Error("present file reached fresh-install build")
					return errors.New("unexpected build")
				}})
				pf, err := m.preflight(t.Context(), host)
				if pf.ExecutableMissing || !errors.Is(err, ErrEndpointMigration) || !isTerminal(err) {
					t.Fatalf("present mode %o: ExecutableMissing=%v err=%v", mode, pf.ExecutableMissing, err)
				}
				if ch, err := m.Ensure(t.Context(), host.Name); ch != nil || !errors.Is(err, ErrEndpointMigration) || !errors.Is(err, bridgeFailure) {
					t.Fatalf("Ensure channel=%v err=%v", ch, err)
				}
				if len(fr.recordedStarts()) != 1 {
					t.Fatal("present file must get one read-only bridge attempt")
				}
				for _, argv := range fr.recordedRuns() {
					remote := strings.Join(argv[12:], " ")
					if strings.Contains(remote, "api/health") || strings.Contains(remote, "systemctl") || strings.Contains(remote, "kill") || strings.Contains(remote, "mktemp") {
						t.Fatalf("present file reached lifecycle: %v", argv)
					}
				}
			})
		}
	}
}

func TestPreflightGenuinelyMissingFileCanInstall(t *testing.T) {
	for _, selection := range []string{"fallback absent", "explicit absent", "fallback disappeared"} {
		t.Run(selection, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("PATH", root)
			target := filepath.Join(root, ".local", "bin", "evener")
			host := hostreg.Host{Name: "selected", SSH: "remote"}
			if selection == "explicit absent" {
				host.EvenerPath = target
			}
			if selection == "fallback disappeared" {
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("installed sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			base := cannedRun(nil)
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.Contains(remote, "launch-check") || strings.HasPrefix(remote, "if ") {
					out, err := (execRunner{}).Run(ctx, []string{"/bin/sh", "-c", remote}, in)
					if selection == "fallback disappeared" && strings.HasPrefix(remote, `if [ -n "${HOME-}" ]`) {
						if removeErr := os.Remove(target); removeErr != nil {
							t.Fatal(removeErr)
						}
					}
					return out, err
				}
				return base(ctx, argv, in)
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: writeStageBinary})
			pf, err := m.preflight(t.Context(), host)
			if err != nil || !pf.ExecutableMissing {
				t.Fatalf("genuinely absent target: ExecutableMissing=%v err=%v", pf.ExecutableMissing, err)
			}
		})
	}
}

func TestExecutableProbeFilesystemPresence(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		mode os.FileMode
		want int
	}{
		{"missing", 0, 1},
		{"missing/parent/evener", 0, 1},
		{"nonexecutable", 0600, 0},
		{"unreadable", 0000, 0},
		{"executable", 0700, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(root, tc.name)
			if tc.want == 0 {
				if err := os.WriteFile(target, []byte("installed sentinel"), tc.mode); err != nil {
					t.Fatal(err)
				}
			}
			_, err := (execRunner{}).Run(t.Context(), []string{"/bin/sh", "-c", executableProbeRemote(target)}, nil)
			got := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				got = exit.ExitCode()
			}
			if got != tc.want {
				t.Fatalf("probe exit=%d err=%v, want %d", got, err, tc.want)
			}
		})
	}
	t.Run("dangling symlink", func(t *testing.T) {
		target := filepath.Join(root, "dangling")
		if err := os.Symlink(filepath.Join(root, "missing-target"), target); err != nil {
			t.Fatal(err)
		}
		if _, err := (execRunner{}).Run(t.Context(), []string{"/bin/sh", "-c", executableProbeRemote(target)}, nil); err != nil {
			t.Fatalf("present symlink: %v", err)
		}
	})
	t.Run("inaccessible ancestor", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory search permissions")
		}
		dir := filepath.Join(root, "inaccessible")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "evener")
		if err := os.WriteFile(target, []byte("installed sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(dir, 0700); err != nil {
				t.Error(err)
			}
		})
		_, err := (execRunner{}).Run(t.Context(), []string{"/bin/sh", "-c", executableProbeRemote(target)}, nil)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("inaccessible file must remain unknown, err=%v", err)
		}
	})
}

// Pin path quoting independently, with a literal ancestor-search contract.
func TestExecutableProbeRemote(t *testing.T) {
	const ancestorSearch = `; while :; do
case "$p" in */*) p=${p%/*}; [ -n "$p" ] || p=/ ;; *) p=. ;; esac
if test -e "$p" || test -L "$p"; then
  if test -d "$p" && test -x "$p"; then exit 1; else exit 2; fi
fi
done`
	cases := []struct {
		target string
		want   string
	}{
		{"/opt/evener/bin/evener", "if test -e /opt/evener/bin/evener || test -L /opt/evener/bin/evener; then exit 0; fi; p=/opt/evener/bin/evener" + ancestorSearch},
		{"/opt/my evener/bin/evener", "if test -e '/opt/my evener/bin/evener' || test -L '/opt/my evener/bin/evener'; then exit 0; fi; p='/opt/my evener/bin/evener'" + ancestorSearch},
		{"/opt/evener/bin/evener; rm -rf /", "if test -e '/opt/evener/bin/evener; rm -rf /' || test -L '/opt/evener/bin/evener; rm -rf /'; then exit 0; fi; p='/opt/evener/bin/evener; rm -rf /'" + ancestorSearch},
		{"evener", "if command -v evener >/dev/null 2>&1; then exit 0; else exit 1; fi"},
		{"my evener", "if command -v 'my evener' >/dev/null 2>&1; then exit 0; else exit 1; fi"},
	}
	for _, tc := range cases {
		if got := executableProbeRemote(tc.target); got != tc.want {
			t.Errorf("executableProbeRemote(%q) =\n %q\nwant %q", tc.target, got, tc.want)
		}
	}
}
