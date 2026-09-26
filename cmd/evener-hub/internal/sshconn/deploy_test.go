package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/remoteinstall"
)

// TestBuildLdflagsStampsControllerBuildinfo proves the deployed binary is
// stamped with this process's own buildinfo values, so its launch-check version
// equals the controller's buildinfo.Version().
func TestBuildLdflagsStampsControllerBuildinfo(t *testing.T) {
	origSHA, origDirty, origTime, origCh, origTag := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.BuildTime, buildinfo.Channel, buildinfo.ReleaseTag
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.BuildTime, buildinfo.Channel, buildinfo.ReleaseTag = origSHA, origDirty, origTime, origCh, origTag
	})
	buildinfo.GitSHA = "abc1234"
	buildinfo.GitDirty = "true"
	buildinfo.BuildTime = "2026-09-14T00:00:00Z"
	buildinfo.Channel = "snapshot"
	buildinfo.ReleaseTag = "v1.2.3"

	got := buildLdflags()
	for _, want := range []string{
		"-X primeradiant.com/evener/buildinfo.GitSHA=abc1234",
		"-X primeradiant.com/evener/buildinfo.GitDirty=true",
		"-X primeradiant.com/evener/buildinfo.BuildTime=2026-09-14T00:00:00Z",
		"-X primeradiant.com/evener/buildinfo.Channel=snapshot",
		"-X primeradiant.com/evener/buildinfo.ReleaseTag=v1.2.3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildLdflags() = %q, missing %q", got, want)
		}
	}
}

// TestDeployBuildsPushesAtomicallyAndCleansStaging covers the primary deploy
// path: cross-compile via the seam, an ssh `cat > tmp && chmod +x && mv` push
// fed on stdin, and local staging cleanup.
func TestDeployBuildsPushesAtomicallyAndCleansStaging(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	var builtOS, builtArch, builtOut string
	var pushedBytes []byte
	var pushArgv []string

	fr := &fakeRunner{
		runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "test -d /opt/evener/bin"):
				return nil, nil
			case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "cat >"):
				pushArgv = append([]string(nil), argv...)
				if stdin != nil {
					pushedBytes, _ = io.ReadAll(stdin)
				}
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, goos, goarch, out string) error {
			builtOS, builtArch, builtOut = goos, goarch, out
			return os.WriteFile(out, []byte("binary-bytes"), 0o755)
		},
	})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if builtOS != "linux" || builtArch != "amd64" {
		t.Fatalf("build target = %s/%s, want linux/amd64", builtOS, builtArch)
	}
	if string(pushedBytes) != "binary-bytes" {
		t.Fatalf("pushed bytes = %q", pushedBytes)
	}

	wantRemote := pushBinaryRemote("/opt/evener/bin/evener", int64(len("binary-bytes")))
	want := rawCommandArgv(m.opts, host, wantRemote)
	if !equalArgv(pushArgv, want) {
		t.Fatalf("push argv:\n got %v\nwant %v", pushArgv, want)
	}
	if len(pushArgv) == 0 || pushArgv[0] != "ssh" {
		t.Fatalf("push argv[0] = %v, want ssh", pushArgv)
	}
	if _, err := os.Stat(builtOut); !os.IsNotExist(err) {
		t.Fatalf("staging file %q still present after deploy (err=%v)", builtOut, err)
	}
}

// TestDeployTargetDirectoryMissingFailsClearly covers a non-empty evener_path
// whose directory does not exist: a clear ErrDeploy, and no build.
func TestDeployTargetDirectoryMissingFailsClearly(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/nope/bin/evener"}
	buildCalled := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "test -d /nope/bin") {
			return []byte("test: /nope/bin: No such file or directory\n"), errors.New("exit status 1")
		}
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(context.Context, string, string, string) error { buildCalled = true; return nil },
	})

	_, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if !errors.Is(err, ErrDeploy) {
		t.Fatalf("err = %v, want ErrDeploy", err)
	}
	if !strings.Contains(err.Error(), "/nope/bin") {
		t.Fatalf("error does not name the missing directory: %v", err)
	}
	if buildCalled {
		t.Fatal("build ran despite missing target directory")
	}
}

// refusingRunner fails the test when any remote command runs. The run-target
// refusals must arrive before the controller touches the host, so a runner call
// is itself the defect under test.
func refusingRunner(t *testing.T) *fakeRunner {
	t.Helper()
	return &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		t.Errorf("a remote command ran for a refused run target: %v", argv)
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
}

// assertRunTargetRefusal pins the typed refusal, its terminality, and its
// message: errRunTargetUnservable, the terminal type the neighbouring refusal
// (errControllerDirty) uses, naming what the operator must fix. It is not
// ErrDeploy: a supervisor that retried that retryable sentinel would re-refuse
// the same misconfigured path forever (round thirteen's loop).
func assertRunTargetRefusal(t *testing.T, err error, wants ...string) {
	t.Helper()
	if !errors.Is(err, errRunTargetUnservable) {
		t.Fatalf("err = %v, want errRunTargetUnservable", err)
	}
	if !isTerminal(err) {
		t.Fatalf("err = %v, want a terminal refusal (a retryable one re-refuses forever)", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("err = %v still wraps ErrDeploy, so the supervisor would retry the same refusal forever", err)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
}

// TestDeployRefusesARunTargetThatCannotServeAHub pins the round-22 decision that
// the run target must be `evener`. Release archives carry both `evener` and
// `evener-dev`, but `evener-dev` is the development/test tooling binary
// (cmd/evener-dev/bin) — no `hub` subcommand and no `launch-check` — so a host
// configured to run it installs "successfully" and then fails preflight, health,
// and restart, with the controller having already written to the host. Any other
// basename is no better: the manager records this one path as the host's run
// target and probes, restarts, and attaches the binary at it. The refusal is
// therefore asserted on the recorded commands, not on the error alone: no remote
// command, install, or push may have run, and the refusal must not depend on a
// retry to become effective — repeating the deploy changes nothing on the host
// because nothing reached it the first time.
func TestDeployRefusesARunTargetThatCannotServeAHub(t *testing.T) {
	const devTooling = "/opt/evener/bin/evener-dev"
	const unshipped = "/opt/evener/bin/evener-hub"
	wants := map[string][]string{
		devTooling: {"evener-dev", "development"},
		unshipped:  {"evener-hub"},
	}

	for _, target := range []string{devTooling, unshipped} {
		t.Run(target, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: target}

			t.Run("push path", func(t *testing.T) {
				builds := 0
				fr := refusingRunner(t)
				m := newTestManager(t, testRegistry(t, host), fr, Options{
					BuildBinary: func(context.Context, string, string, string) error {
						builds++
						return nil
					},
				})

				_, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
				assertRunTargetRefusal(t, err, wants[target]...)
				// The ordering is the requirement. The first refusal is already
				// the whole answer, so a supervisor's retry re-runs it with no
				// install, push, or write to repeat.
				if _, again := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"}); !errors.Is(again, errRunTargetUnservable) {
					t.Fatalf("retry err = %v, want the same terminal run-target refusal", again)
				}
				if runs := fr.recordedRuns(); len(runs) != 0 {
					t.Fatalf("the refused deploy reached the runner: %v", runs)
				}
				if builds != 0 {
					t.Fatalf("cross-compile ran for a run target that cannot serve a hub (builds = %d)", builds)
				}
			})

			t.Run("installer fallback", func(t *testing.T) {
				// The fallback is reached only for a controller whose channel has
				// a publishable artifact, so the run-target refusal must be the
				// one that survives that admission.
				origChannel := buildinfo.Channel
				t.Cleanup(func() { buildinfo.Channel = origChannel })
				buildinfo.Channel = "snapshot"

				fr := refusingRunner(t)
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				_, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
				assertRunTargetRefusal(t, err, wants[target]...)
				if runs := fr.recordedRuns(); len(runs) != 0 {
					t.Fatalf("the refused install reached the runner: %v", runs)
				}
			})
		})
	}
}

// TestDeployTargetAcceptsEvenerRunTargets keeps the narrowing from overreaching:
// a configured evener_path named `evener`, wherever it lives, and the resolved
// default are unchanged.
func TestDeployTargetAcceptsEvenerRunTargets(t *testing.T) {
	t.Run("configured evener_path under a non-default directory", func(t *testing.T) {
		const exe = "/opt/evener/current/evener"
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: exe}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "test -d /opt/evener/current"):
				return nil, nil
			case strings.Contains(joined, "evener_resolve "+exe):
				return []byte(exe + "\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})

		got, err := m.deployTarget(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
		if err != nil {
			t.Fatalf("deployTarget: %v", err)
		}
		if got != exe {
			t.Fatalf("deployTarget = %q, want the configured evener_path %q", got, exe)
		}
	})

	t.Run("default resolution with no evener_path", func(t *testing.T) {
		const exe = "/home/dev/.local/bin/evener"
		host := hostreg.Host{Name: "beta", SSH: "beta.example"}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "lsof -ti :9180"):
				return []byte(noListenerMarker + "\n"), nil
			case strings.Contains(joined, "command -v evener"):
				return []byte(exe + "\n"), nil
			case strings.Contains(joined, "evener_resolve "+exe):
				return []byte(exe + "\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})

		got, err := m.deployTarget(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
		if err != nil {
			t.Fatalf("deployTarget: %v", err)
		}
		if got != exe {
			t.Fatalf("deployTarget = %q, want the PATH evener %q", got, exe)
		}
	})
}

// TestInstallerRefForMapsBuildChannel pins acceptance criterion 16: the installer
// artifact reference is derived from buildinfo.BuildChannel(), and
// buildinfo.Version() (a short SHA, possibly -dirty) is never passed as a tag.
func TestInstallerRefForMapsBuildChannel(t *testing.T) {
	// The remedy clause the refusals append is the caller's business (asserted
	// where it reaches an operator, in deploy_help_test.go); this table is about
	// the reference mapping alone.
	const remedy = "set -deploy-binary or -build-source"
	cases := []struct {
		name    string
		channel string
		tag     string
		dirty   string
		want    string
		wantErr bool
	}{
		{"release uses the stamped tag", "release", "v1.2.3", "", "v1.2.3", false},
		{"release without a tag refuses", "release", "", "", "", true},
		{"snapshot passes snapshot", "snapshot", "", "", "snapshot", false},
		{"dev refuses", "dev", "", "", "", true},
		{"empty channel refuses", "", "v1.2.3", "", "", true},
		{"dirty refuses even with a tag", "release", "v1.2.3", "true", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := installerRefFor(tc.channel, tc.tag, tc.dirty, remedy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("installerRefFor(%q,%q,%q) err = %v, wantErr %v", tc.channel, tc.tag, tc.dirty, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("installerRefFor(%q,%q,%q) = %q, want %q", tc.channel, tc.tag, tc.dirty, got, tc.want)
			}
		})
	}
}

// TestInstallerDirsInstallToTheRunTarget pins acceptance criterion 17: with
// evener_path set the installer's BINDIR is its directory (refusing any basename
// but `evener` — evener-dev is the development tooling binary, not a run target a
// hub can serve); with evener_path empty the installer's default
// ~/.local/bin/evener is the run target the manager records.
func TestInstallerDirsInstallToTheRunTarget(t *testing.T) {
	bindir, share, target, err := installerDirs(hostreg.Host{Name: "alpha", EvenerPath: "/opt/evener/bin/evener"}, Preflight{Home: "/home/dev"})
	if err != nil {
		t.Fatalf("installerDirs(evener_path): %v", err)
	}
	if bindir != "/opt/evener/bin" || share != "/opt/evener/share/evener/bin" || target != "/opt/evener/bin/evener" {
		t.Fatalf("installerDirs(evener_path) = (%q,%q,%q), want (/opt/evener/bin,/opt/evener/share/evener/bin,/opt/evener/bin/evener)", bindir, share, target)
	}

	// Round 22: release archives still carry evener-dev, but it is the development
	// tooling binary and can never serve a hub, so it is not a run target the
	// installer may be pointed at (component-04 criterion 17). The refusal is
	// the terminal run-target sentinel the installer shares with the push path
	// (checkRunTarget), so no retry of the install can change it.
	if bindir, share, target, err := installerDirs(hostreg.Host{Name: "alpha", EvenerPath: "/opt/evener/bin/evener-dev"}, Preflight{Home: "/home/dev"}); !errors.Is(err, errRunTargetUnservable) || !isTerminal(err) {
		t.Fatalf("installerDirs(evener-dev) err = %v, want a terminal errRunTargetUnservable", err)
	} else if bindir != "" || share != "" || target != "" {
		t.Fatalf("installerDirs(evener-dev) = (%q,%q,%q), want all empty", bindir, share, target)
	}

	bindir, share, target, err = installerDirs(hostreg.Host{Name: "alpha", EvenerPath: "/opt/evener/bin/evener-hub"}, Preflight{Home: "/home/dev"})
	if !errors.Is(err, errRunTargetUnservable) || !isTerminal(err) {
		t.Fatalf("installerDirs(unshipped basename) err = %v, want a terminal errRunTargetUnservable", err)
	}
	if bindir != "" || share != "" || target != "" {
		t.Fatalf("installerDirs(unshipped basename) = (%q,%q,%q), want all empty", bindir, share, target)
	}

	bindir, share, target, err = installerDirs(hostreg.Host{Name: "alpha"}, Preflight{Home: "/home/dev"})
	if err != nil {
		t.Fatalf("installerDirs(default): %v", err)
	}
	// Round twelve: the installer default is passed explicitly rather than left to
	// install.sh's own PREFIX/BINDIR defaults, so an inherited PREFIX in the remote
	// shell environment cannot install the binary somewhere the manager will never
	// probe, record, or relaunch while reporting success.
	if bindir != "/home/dev/.local/bin" || share != "/home/dev/.local/share/evener/bin" || target != "/home/dev/.local/bin/evener" {
		t.Fatalf("installerDirs(default) = (%q,%q,%q), want (/home/dev/.local/bin,/home/dev/.local/share/evener/bin,/home/dev/.local/bin/evener)", bindir, share, target)
	}

	if _, _, _, err := installerDirs(hostreg.Host{Name: "alpha"}, Preflight{}); !errors.Is(err, ErrDeploy) {
		t.Fatalf("installerDirs(no HOME) err = %v, want ErrDeploy", err)
	}
}

// TestInstallerCommandPinsRefAndDirs pins the exact remote installer invocation:
// the ref is pinned (never `latest`), and a custom run target passes BINDIR and
// EVENER_SHARE_BINDIR so the symlink lands at evener_path.
func TestInstallerCommandPinsRefAndDirs(t *testing.T) {
	got := remoteinstall.Command("v1.2.3", "", "/opt/evener/bin", "/opt/evener/share/evener/bin")
	// The script is written to a temp file and the write's status checked before it
	// runs; a `cat … | sh` pipeline would report sh's status and hide a failed
	// write, and any fetch here would be a mutable installer. The write's byte
	// count is checked too (round twelve): a dropped stream reaches `cat` as a
	// clean EOF, so the length is what proves the whole script arrived.
	wantCheck := "cat > \"$tmp\" && v=$(wc -c < \"$tmp\" | tr -d '[:space:]') && [ \"$v\" = " + strconv.Itoa(len(remoteinstall.Script)) + " ] && env "
	if !strings.Contains(got, wantCheck) {
		t.Fatalf("the installer command does not check the script write (and its byte count) before executing the installer: %q", got)
	}
	if strings.Contains(got, "| env ") || strings.Contains(got, "| sh") {
		t.Fatalf("the installer command still pipes the script into sh: %q", got)
	}
	if strings.Contains(got, "http") || strings.Contains(got, "curl") {
		t.Fatalf("the installer command fetches the installer script instead of streaming the embedded copy: %q", got)
	}
	want := "env EVENER_INSTALL_VERSION=v1.2.3 PREFIX='' BINDIR=/opt/evener/bin EVENER_SHARE_BINDIR=/opt/evener/share/evener/bin sh \"$tmp\""
	if !strings.HasSuffix(got, want) {
		t.Fatalf("remoteinstall.Command = %q, want suffix %q", got, want)
	}
	got = remoteinstall.Command("snapshot", "", "", "")
	if !strings.Contains(got, "EVENER_INSTALL_VERSION=snapshot") || !strings.Contains(got, "BINDIR=''") {
		t.Fatalf("remoteinstall.Command(default) = %q, want snapshot ref and BINDIR pinned to empty (its default)", got)
	}
	if strings.Contains(got, "latest") {
		t.Fatalf("remoteinstall.Command passed `latest`: %q", got)
	}
}

// TestEnsureInstallerFallbackDeploysPinnedRelease covers criterion 16 end to
// end: with a release controller, no build source, and a mismatching host,
// Ensure runs the installer pinned to the stamped tag, restarts, and attaches —
// never passing buildinfo.Version() as a release tag and never cross-compiling.
func TestEnsureInstallerFallbackDeploysPinnedRelease(t *testing.T) {
	origSHA, origDirty, origChannel, origTag := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel, buildinfo.ReleaseTag
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel, buildinfo.ReleaseTag = origSHA, origDirty, origChannel, origTag
	})
	buildinfo.GitSHA = "newsha"
	buildinfo.GitDirty = ""
	buildinfo.Channel = "release"
	buildinfo.ReleaseTag = "v1.2.3"

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launchCalls := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "evener-install.XXXXXX"):
			return nil, nil
		case strings.Contains(joined, "api/health"):
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "systemctl restart"):
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	var installed bool
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "EVENER_INSTALL_VERSION=v1.2.3") {
			installed = true
		}
		if strings.Contains(joined, "EVENER_INSTALL_VERSION=newsha") {
			t.Fatalf("passed the Git SHA as a release tag: %v", argv)
		}
		// The push path is identified by its own markers: the installer also writes
		// the streamed script with `cat > "$tmp"` and, since round twelve, checks its
		// byte count the same way, so neither of those proves a cross-compile ran.
		// The chmod and the mv into the install path do.
		if strings.Contains(joined, "chmod +x") || strings.Contains(joined, "mv \"$tmp\"") {
			t.Fatalf("cross-compiled despite the installer fallback: %v", argv)
		}
	}
	if !installed {
		t.Fatal("the installer was never pinned to the stamped release tag")
	}
}

// TestInstallerFallbackRecordsDefaultRunTarget pins criterion 17's second half:
// with no evener_path the installer's own ~/.local/bin/evener is what the
// manager attaches with afterwards.
func TestInstallerFallbackRecordsDefaultRunTarget(t *testing.T) {
	origSHA, origDirty, origChannel, origTag := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel, buildinfo.ReleaseTag
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel, buildinfo.ReleaseTag = origSHA, origDirty, origChannel, origTag
	})
	buildinfo.GitSHA = "newsha"
	buildinfo.GitDirty = ""
	buildinfo.Channel = "snapshot"
	buildinfo.ReleaseTag = ""

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	launchCalls := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "evener-install.XXXXXX"):
			return nil, nil
		case strings.Contains(joined, "api/health"):
			// This controller is on the snapshot channel (buildinfo.Channel above), so
			// the post-restart probe requires backend_git_sha to match buildinfo.GitSHA
			// (waitHealthy); the real hub reports the field (cmd/evener-hub/web_api.go).
			return []byte(`{"version":"newsha","backend_git_sha":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "systemctl restart"):
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	var installerRan bool
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "EVENER_INSTALL_VERSION=snapshot") {
			installerRan = true
			// Round twelve: the default install location is passed explicitly, so an
			// inherited PREFIX/BINDIR in the remote shell environment cannot install
			// the binary somewhere the manager will never probe or relaunch.
			for _, want := range []string{"BINDIR=/home/dev/.local/bin", "EVENER_SHARE_BINDIR=/home/dev/.local/share/evener/bin"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("installer did not pin the default install layout (%s): %v", want, argv)
				}
			}
			// The streamed script's length is verified on the host before it runs
			// (round twelve), and the length is the embedded copy's, not a constant.
			if want := "[ \"$v\" = " + strconv.Itoa(len(remoteinstall.Script)) + " ]"; !strings.Contains(joined, want) {
				t.Fatalf("installer does not verify the streamed script's byte count (%s): %v", want, argv)
			}
		}
	}
	if !installerRan {
		t.Fatal("the snapshot installer was never run")
	}
	starts := fr.recordedStarts()
	if len(starts) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(starts))
	}
	if !strings.Contains(strings.Join(starts[0], " "), "/home/dev/.local/bin/evener") {
		t.Fatalf("attach did not use the installer's resolved run target: %v", starts[0])
	}
}

// TestDeployTargetEmptyResolvesRemotePATH covers the empty evener_path case:
// the install path is whatever `evener` resolves to on the remote PATH.
func TestDeployTargetEmptyResolvesRemotePATH(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var pushJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "command -v evener"):
			return []byte("/home/dev/.local/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/home/dev/.local/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			pushJoined = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("x"), 0o755)
		},
	})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	want := pushBinaryRemote("/home/dev/.local/bin/evener", 1)
	if !strings.Contains(pushJoined, want) {
		t.Fatalf("push command = %q, want it to contain %q", pushJoined, want)
	}
}

// TestDeployQuotesRemotePaths proves a registry-configured evener_path with a
// space or shell metacharacter is quoted in every deploy command, so it neither
// breaks the command nor injects additional remote commands.
func TestDeployQuotesRemotePaths(t *testing.T) {
	// The basename itself must be `evener` (checkRunTarget): the space and the
	// shell metacharacter live in the directory, so the `test -d` probe and the
	// pushed target still have to quote a path that would break the command line
	// or inject a second remote command if it were left bare.
	const target = "/opt/my evener/bin;rm/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: target}
	var testDirRemote, pushRemote string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d"):
			testDirRemote = joined
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte(target + "\n"), nil
		case strings.Contains(joined, "cat >"):
			pushRemote = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("x"), 0o755)
		},
	})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if dir := path.Dir(target); !strings.Contains(testDirRemote, "test -d "+shellquote.RemoteWord(dir)) {
		t.Fatalf("test -d does not quote the path with a space: %q", testDirRemote)
	}
	wantPush := pushBinaryRemote(target, 1)
	if !strings.Contains(pushRemote, wantPush) {
		t.Fatalf("push does not quote the paths:\n got %q\nwant it to contain %q", pushRemote, wantPush)
	}
}

// TestDeployResolvesSymlinkTarget proves the push replaces the executable a
// symlink points at, not the symlink itself: `make install` installs evener as
// a symlink and `command -v evener` returns that link, so mv-ing onto the link
// would clobber the installed layout.
func TestDeployResolvesSymlinkTarget(t *testing.T) {
	const link = "/usr/local/bin/evener"
	const realPath = "/opt/evener/1.2.3/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: link}
	var pushRemote string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /usr/local/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve "+link):
			return []byte(realPath + "\n"), nil
		case strings.Contains(joined, "cat >"):
			pushRemote = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("x"), 0o755)
		},
	})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !strings.Contains(pushRemote, pushBinaryRemote(realPath, 1)) {
		t.Fatalf("push does not install onto the symlink's real file %q: %q", realPath, pushRemote)
	}
	if strings.Contains(pushRemote, shellquote.RemoteWord(link)) {
		t.Fatalf("push replaces the symlink %q instead of its target: %q", link, pushRemote)
	}
}

// TestResolveDeployCommandIsPortableAcrossReadlinkVariants proves the deploy path
// resolver does not depend on `readlink -f`, a GNU/coreutils extension that BSD
// `readlink` (macOS) rejects with `readlink: illegal option -- f` — the failure
// that made every darwin/arm64 deploy fail at path resolution. It runs the real
// resolver under a `readlink` shim emulating the BSD behavior and confirms the
// legacy form fails under that same shim, so the test embodies the pre-fix
// failure it guards against.
func TestResolveDeployCommandIsPortableAcrossReadlinkVariants(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
	realReadlink, err := exec.LookPath("readlink")
	if err != nil {
		t.Skipf("readlink not available: %v", err)
	}

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(realDir, "evener")
	if err := os.WriteFile(resolved, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link1 := filepath.Join(root, "link1")
	link2 := filepath.Join(root, "link2")
	if err := os.Symlink(resolved, link1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link1, link2); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  -f) echo 'readlink: illegal option -- f' >&2; echo 'usage: readlink [-n] [file ...]' >&2; exit 1;;\n" +
		"  -n) shift;;\n" +
		"esac\n" +
		"exec " + realReadlink + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "readlink"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"))

	run := func(script string) (string, string, error) {
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = env
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		err := cmd.Run()
		return out.String(), errBuf.String(), err
	}

	// The resolver the deploy uses must succeed under the BSD-style readlink.
	out, stderr, err := run(resolveDeployCommand(link2))
	if err != nil {
		t.Fatalf("resolver failed under a BSD-style readlink: %v: %s", err, stderr)
	}
	// The resolver prints the canonical path (cd -P), so the expectation must be
	// canonical too: on macOS t.TempDir lives under a symlinked /var.
	want, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != want {
		t.Fatalf("resolver output = %q, want %q", got, want)
	}

	// The pre-fix command must fail under the same shim: this is the darwin
	// failure the resolver replaces.
	if _, stderr, err := run("readlink -f " + shellquote.RemoteWord(link2)); err == nil {
		t.Fatal("legacy `readlink -f` unexpectedly succeeded under the BSD-style readlink shim")
	} else if !strings.Contains(stderr, "illegal option") {
		t.Fatalf("legacy command failed for an unexpected reason: %s", stderr)
	}

	// A path that does not resolve to an existing file prints nothing and fails,
	// so the caller reports a clear error instead of falling back to the symlink.
	out, _, err = run(resolveDeployCommand(filepath.Join(root, "missing")))
	if err == nil {
		t.Fatalf("resolver succeeded for a missing path (stdout %q)", out)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("resolver printed %q for a missing path, want nothing", out)
	}

	// A directory must not resolve either: `mv` would move the staged binary
	// inside it and report success, leaving the real executable un-upgraded. The
	// pre-fix `[ -e "$p" ]` test accepted a directory; `[ -f "$p" ]` refuses it.
	dir := filepath.Join(root, "a-directory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, _, err := run(resolveDeployCommand(dir)); err == nil {
		t.Fatalf("resolver accepted a directory (stdout %q)", out)
	}

	// A symlink cycle must fail like ELOOP instead of looping forever: the
	// unbounded `while [ -L "$p" ]` hung the whole deploy ssh command.
	cycleA := filepath.Join(root, "cycle-a")
	cycleB := filepath.Join(root, "cycle-b")
	if err := os.Symlink(cycleB, cycleA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cycleA, cycleB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cycleCmd := exec.CommandContext(ctx, "sh", "-c", resolveDeployCommand(cycleA))
	cycleCmd.Env = env
	var cycOut bytes.Buffer
	cycleCmd.Stdout = &cycOut
	if err := cycleCmd.Run(); err == nil {
		t.Fatalf("resolver accepted a symlink cycle (stdout %q)", cycOut.String())
	}
	if ctx.Err() != nil {
		t.Fatal("resolver hung on a symlink cycle")
	}
	if strings.TrimSpace(cycOut.String()) != "" {
		t.Fatalf("resolver printed %q for a symlink cycle, want nothing", cycOut.String())
	}
}

// TestVerifyBuildSourceRequiresAnEvenerCheckout proves the explicit build source
// is verified and never guessed: an unset source, a tree that is not this
// module, and a tree without a ./cmd/evener package are each refused rather than
// built. A different module (an installed hub pointed at another workspace) must
// not be mistaken for the evener checkout.
func TestVerifyBuildSourceRequiresAnEvenerCheckout(t *testing.T) {
	clearBuildSHA(t)
	if _, err := verifyBuildSource(""); err == nil || !strings.Contains(err.Error(), "no build source") {
		t.Fatalf("verifyBuildSource(\"\") err = %v, want a missing-source error", err)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBuildSource(other); err == nil || !strings.Contains(err.Error(), "not the evener checkout") {
		t.Fatalf("verifyBuildSource(other module) err = %v, want a wrong-module error", err)
	}

	evener := t.TempDir()
	if err := os.WriteFile(filepath.Join(evener, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBuildSource(evener); err == nil || !strings.Contains(err.Error(), "cmd/evener") {
		t.Fatalf("verifyBuildSource(no cmd/evener) err = %v, want a missing-package error", err)
	}
	if err := os.MkdirAll(filepath.Join(evener, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := verifyBuildSource(evener)
	if err != nil {
		t.Fatalf("verifyBuildSource(valid checkout): %v", err)
	}
	if want, _ := filepath.EvalSymlinks(evener); got != want {
		t.Fatalf("verifyBuildSource = %q, want %q", got, want)
	}

	// A source reached through a symlinked root must still come back canonical: on
	// macOS t.TempDir (and /var/folders generally) lives under a symlinked /var,
	// so callers comparing against EvalSymlinks would otherwise see two spellings
	// of the same tree.
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(evener, link); err != nil {
		t.Fatal(err)
	}
	canonical, err := verifyBuildSource(link)
	if err != nil {
		t.Fatalf("verifyBuildSource(symlinked root): %v", err)
	}
	if want, _ := filepath.EvalSymlinks(evener); canonical != want {
		t.Fatalf("verifyBuildSource(symlinked root) = %q, want canonical %q", canonical, want)
	}
}

// TestLocalBuildWithoutBuildSourceFailsClearly proves the production builder
// fails closed with a clear unavailable-source error instead of running
// `go build ./cmd/evener/` against whatever directory is nearby.
func TestLocalBuildWithoutBuildSourceFailsClearly(t *testing.T) {
	err := localBuild(context.Background(), "", "linux", "amd64", filepath.Join(t.TempDir(), "evener"))
	if err == nil {
		t.Fatal("localBuild succeeded with no build source")
	}
	if !strings.Contains(err.Error(), "build source") {
		t.Fatalf("error does not explain the missing source tree: %v", err)
	}
}

// TestDeployBuildsFromTheConfiguredSource proves an explicit BuildSource is
// honored: the production builder runs `go build` with the configured checkout
// as its working directory, never the process working directory.
func TestDeployBuildsFromTheConfiguredSource(t *testing.T) {
	clearBuildSHA(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(source, "cmd", "evener-hub", "frontend", "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("built"), 0o644); err != nil {
		t.Fatal(err)
	}

	shimDir := t.TempDir()
	marker := filepath.Join(shimDir, "go-wd")
	argsMarker := filepath.Join(shimDir, "go-args")
	shim := "#!/bin/sh\npwd > " + marker + "\nprintf '%s\\n' \"$@\" > " + argsMarker + "\n" +
		"out=\"\"\nwhile [ $# -gt 0 ]; do if [ \"$1\" = \"-o\" ]; then out=$2; fi; shift; done\n" +
		"printf fake > \"$out\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))
	t.Chdir(t.TempDir()) // run from an unrelated directory

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{BuildSource: source})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("go build did not run: %v", err)
	}
	want, _ := filepath.EvalSymlinks(source)
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("go build ran in %q, want the configured source %q", strings.TrimSpace(string(got)), want)
	}
	args, err := os.ReadFile(argsMarker)
	if err != nil {
		t.Fatalf("go build args not recorded: %v", err)
	}
	if !strings.Contains("\n"+string(args)+"\n", "\n-a\n") {
		t.Fatalf("go build did not force a rebuild of the target closure (-a): %q", args)
	}
}

// TestLocalBuildRefusesPlaceholderSPA proves the production builder will not
// deploy a binary that embeds only the tracked frontend placeholder: unlike
// build-runtime/install, localBuild has no build-web prerequisite, so without
// this check the host would serve the documented 503 "web app not built" and
// version auto-match could not detect it (it compares only buildinfo.GitSHA).
func TestLocalBuildRefusesPlaceholderSPA(t *testing.T) {
	clearBuildSHA(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(source, "cmd", "evener-hub", "frontend", "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, distPlaceholder), []byte("run make build-web\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	shimDir := t.TempDir()
	ranMarker := filepath.Join(shimDir, "go-ran")
	shim := "#!/bin/sh\nprintf ran > " + ranMarker + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))

	err := localBuild(context.Background(), source, "linux", "amd64", filepath.Join(t.TempDir(), "evener"))
	if err == nil {
		t.Fatal("localBuild succeeded with only the placeholder SPA")
	}
	if !strings.Contains(err.Error(), "build-web") {
		t.Fatalf("error does not tell the operator to run build-web: %v", err)
	}
	if _, statErr := os.Stat(ranMarker); statErr == nil {
		t.Fatal("go build ran despite the placeholder SPA")
	}
}

// TestDeployDoesNotBuildFromAnUnconfiguredWorkingDirectory proves the production
// builder never discovers its source by looking around the working directory.
// A directory that merely looks like an evener checkout (same module path) must
// not be built: an installed hub launched inside an unrelated or ancestor
// checkout would silently deploy that tree's code. Before the fix localBuild
// walked up from the working directory, found this decoy checkout, and shelled
// out to `go build` (the shim on PATH recorded it), so this test failed with
// "deploy built from the working-directory checkout"; after the fix it fails
// closed with a clear missing-source error before any build runs.
func TestDeployDoesNotBuildFromAnUnconfiguredWorkingDirectory(t *testing.T) {
	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(decoy, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(decoy)

	shimDir := t.TempDir()
	marker := filepath.Join(shimDir, "go-ran")
	shim := "#!/bin/sh\nprintf '%s' \"$PWD\" > " + marker + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	// No BuildBinary seam and no explicit source: the production builder runs.
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	// The push path specifically: with no build source configured it must not
	// build from the working-directory checkout. (deploy would take the installer
	// fallback here.)
	_, err := m.deployPush(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if err == nil {
		t.Fatal("deploy succeeded with no configured build source")
	}
	if got, statErr := os.ReadFile(marker); statErr == nil {
		t.Fatalf("deploy built from the working-directory checkout (%q): %v", got, err)
	}
	if !strings.Contains(err.Error(), "build source") {
		t.Fatalf("error does not explain the missing build source: %v", err)
	}
}

// TestPushBinaryRemoteCleansTempOnFailure pins the finding that a failed push
// left its mktemp file beside the target: repeated failures accumulated partial
// evener.tmp.* files in the install directory. The remote command now arms a trap
// before streaming, so any failure path removes the temp name.
func TestPushBinaryRemoteCleansTempOnFailure(t *testing.T) {
	got := pushBinaryRemote("/opt/evener/bin/evener", 12)
	cleanup := `trap 'rm -f "$tmp"' EXIT`
	idxCleanup := strings.Index(got, cleanup)
	if idxCleanup < 0 {
		t.Fatalf("pushBinaryRemote does not arm a temp cleanup trap: %q", got)
	}
	if idxCat := strings.Index(got, "cat >"); idxCat < 0 || idxCleanup > idxCat {
		t.Fatalf("the cleanup trap must be armed before the push: %q", got)
	}
	if !strings.Contains(got, "mktemp /opt/evener/bin/evener.tmp.XXXXXX") {
		t.Fatalf("pushBinaryRemote no longer creates a unique temp name: %q", got)
	}
}

// TestEnsureWebBuiltRequiresIndexHTML pins the finding that any stray dist entry
// counted as a built SPA, so a .DS_Store in an unbuilt dist passed and the
// deployed binary still served the 503 placeholder. A real vite build writes
// dist/index.html, so that is the artifact required.
func TestEnsureWebBuiltRequiresIndexHTML(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, filepath.FromSlash(frontendDist))
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ensureWebBuilt(root)
	if err == nil {
		t.Fatal("ensureWebBuilt accepted a dist with no index.html")
	}
	if !strings.Contains(err.Error(), "index.html") {
		t.Fatalf("error does not name the missing artifact: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureWebBuilt(root); err != nil {
		t.Fatalf("ensureWebBuilt rejected a built dist: %v", err)
	}
}

// TestVerifyBuildSourceRevisionMustMatchController pins the finding that
// localBuild stamps the controller's buildinfo into whatever BuildSource points
// at: a stale checkout would report the controller's version while running
// different code, and version auto-match would accept it. A stamped controller
// now requires the source checkout's HEAD to be its own commit.
func TestVerifyBuildSourceRevisionMustMatchController(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "first")
	short := gitIn(t, root, "rev-parse", "HEAD")[:7]

	orig := buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.GitSHA = orig })
	buildinfo.GitSHA = short
	if _, err := verifyBuildSource(root); err != nil {
		t.Fatalf("verifyBuildSource(matching revision): %v", err)
	}

	// Advance the checkout: the controller's stamp now names different code.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "second")
	_, err := verifyBuildSource(root)
	if err == nil {
		t.Fatal("verifyBuildSource accepted a checkout at a revision other than the controller's build")
	}
	if !strings.Contains(err.Error(), short) || !strings.Contains(err.Error(), "refusing to deploy") {
		t.Fatalf("error does not explain the revision mismatch: %v", err)
	}

	// A commit the checkout does not contain must also fail closed.
	buildinfo.GitSHA = "deadbee"
	if _, err := verifyBuildSource(root); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("verifyBuildSource(unknown revision) = %v, want a fail-closed error", err)
	}
}

// TestVerifyBuildRevisionRefusesDirtyController pins the finding that
// verifyBuildRevision matched only GitSHA and ignored GitDirty. The builder
// stamps this process's own GitDirty into the deployed binary, so a dirty
// controller's build reports "<sha>-dirty" while the source tree it compiled may
// carry a different set of uncommitted changes; HEAD equality cannot see that, so
// the deploy must be refused rather than accepted by version auto-match.
func TestVerifyBuildRevisionRefusesDirtyController(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "first")
	short := gitIn(t, root, "rev-parse", "HEAD")[:7]

	origSHA, origDirty := buildinfo.GitSHA, buildinfo.GitDirty
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty = origSHA, origDirty })
	buildinfo.GitSHA, buildinfo.GitDirty = short, "true"

	_, err := verifyBuildSource(root)
	if err == nil {
		t.Fatal("verifyBuildSource accepted a dirty controller's build source")
	}
	if !strings.Contains(err.Error(), "dirty") || !strings.Contains(err.Error(), "refusing to deploy") {
		t.Fatalf("error does not explain the dirty controller: %v", err)
	}

	// The same checkout with a clean controller stamp is accepted, so the refusal
	// is keyed off GitDirty and not the checkout.
	buildinfo.GitDirty = ""
	if _, err := verifyBuildSource(root); err != nil {
		t.Fatalf("verifyBuildSource(clean controller): %v", err)
	}
}

// TestDeployTargetUsesFirstLineOfCommandV pins the finding that deployTarget ran
// the whole `command -v evener` output through TrimSpace: trailing noise would be
// folded into the path handed to the resolver, unlike resolveDeployTarget's own
// first-line read.
func TestDeployTargetUsesFirstLineOfCommandV(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var resolverJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "command -v evener"):
			return []byte("/usr/local/bin/evener\nnote: wrapper\n"), nil
		case strings.HasSuffix(joined, "evener_resolve /usr/local/bin/evener"):
			resolverJoined = joined
			return []byte("/usr/local/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	got, err := m.deployTarget(context.Background(), host, Preflight{})
	if err != nil {
		t.Fatalf("deployTarget: %v (the first line of `command -v` output must be the path)", err)
	}
	if got != "/usr/local/bin/evener" {
		t.Fatalf("deployTarget = %q, want the first line", got)
	}
	if resolverJoined == "" {
		t.Fatal("the resolver was not invoked with the first line alone")
	}
}

// gitIn runs a git command in dir for the build-source tests, failing on error.
// The env overrides keep it independent of the developer's global git config.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// clearBuildSHA makes the build-source revision check a no-op for tests that
// build from a throwaway checkout with no git history, independent of whether
// the test binary itself carries buildinfo ldflags.
func clearBuildSHA(t *testing.T) {
	t.Helper()
	orig := buildinfo.GitSHA
	buildinfo.GitSHA = ""
	t.Cleanup(func() { buildinfo.GitSHA = orig })
}

// TestVerifyBuildRevisionRefusesDirtySource pins the Medium finding that a clean
// controller accepted a BuildSource whose HEAD matched but whose tracked files
// were modified (or which carried untracked files). localBuild compiles the
// working tree, so that source produces a different binary that still reports the
// controller's clean version, and version auto-match would accept it. HEAD
// equality alone cannot see the difference; the worktree must be clean.
func TestVerifyBuildRevisionRefusesDirtySource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "first")
	short := gitIn(t, root, "rev-parse", "HEAD")[:7]

	orig := buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.GitSHA = orig })
	buildinfo.GitSHA = short

	if _, err := verifyBuildSource(root); err != nil {
		t.Fatalf("verifyBuildSource(clean source): %v", err)
	}

	// A modified tracked file builds different code that still claims the
	// controller's clean version.
	if err := os.WriteFile(tracked, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := verifyBuildSource(root)
	if err == nil {
		t.Fatal("verifyBuildSource accepted a build source with modified tracked files")
	}
	if !strings.Contains(err.Error(), "uncommitted") || !strings.Contains(err.Error(), "refusing to deploy") {
		t.Fatalf("error does not explain the dirty source: %v", err)
	}

	// An untracked file is part of what `go build` compiles, so it counts too.
	gitIn(t, root, "checkout", "--", "tracked.txt")
	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBuildSource(root); err == nil {
		t.Fatal("verifyBuildSource accepted a build source with an untracked file")
	}

	// Removing the untracked file returns the source to a deployable state, so the
	// refusal is keyed off the worktree and not the checkout.
	if err := os.Remove(filepath.Join(root, "untracked.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBuildSource(root); err != nil {
		t.Fatalf("verifyBuildSource(clean again): %v", err)
	}
}

// TestEnsureMissingEvenerReachesTheInstallerFallback pins the round-five High that
// a host whose evener is absent from the non-interactive PATH was refused with
// ErrSSHStart before any deploy ran. The installer fallback's default target
// ~/.local/bin/evener is deliberately not on that PATH, so a missing executable is
// a fact about the binary, not a transport failure: with a deploy configured the
// launch contract is recorded as unknown and the installer installs a matching
// build at the run target.
func TestEnsureMissingEvenerReachesTheInstallerFallback(t *testing.T) {
	origChannel, origTag := buildinfo.Channel, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.ReleaseTag = origChannel, origTag })
	buildinfo.Channel, buildinfo.ReleaseTag = "release", "v1.2.3"

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	installerRan, launched := false, false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			if strings.Contains(joined, " evener launch-check") {
				// No evener on the host's non-interactive PATH.
				return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, executableProbeRemote("evener")):
			// The dedicated executable probe: absent.
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "list-units"):
			return nil, nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "evener-install.XXXXXX"):
			installerRan = true
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/home/dev/.local/bin/evener\n"), nil
		case strings.Contains(joined, "nohup"):
			launched = true
			return nil, nil
		case strings.Contains(joined, "api/health"):
			if !launched {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v (a host with no evener on PATH must reach the installer fallback)", err)
	}
	if !installerRan {
		t.Fatal("the installer fallback never ran")
	}
	if !launched {
		t.Fatal("the installed hub was never started")
	}
}

// TestDeployInstallerPreservesExistingHubTarget pins the fix for the default
// deploy path: with no configured evener_path the installer must replace the
// binary the host's hub actually runs, not an unrelated ~/.local/bin/evener.
// Restart identity validation compares the running hub's canonical executable to
// the manager's target, so deploying to a different path made every upgrade of a
// hub installed elsewhere fail.
func TestDeployInstallerPreservesExistingHubTarget(t *testing.T) {
	origSHA, origDirty, origChannel, origTag := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel, buildinfo.ReleaseTag
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel, buildinfo.ReleaseTag = origSHA, origDirty, origChannel, origTag
	})
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel, buildinfo.ReleaseTag = "newsha", "", "release", "v1.2.3"

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	var installerJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			// The running hub lives at /usr/local/bin, not the installer default.
			return []byte("/usr/local/bin/evener hub -addr 127.0.0.1:9180\n"), nil
		case strings.Contains(joined, "evener_resolve /usr/local/bin/evener"):
			return []byte("/usr/local/bin/evener\n"), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/usr/local/bin/evener\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "evener-install.XXXXXX"):
			installerJoined = joined
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha"})

	target, err := m.deployInstaller(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
	if err != nil {
		t.Fatalf("deployInstaller: %v", err)
	}
	if target != "/usr/local/bin/evener" {
		t.Fatalf("run target = %q, want the existing hub's executable /usr/local/bin/evener", target)
	}
	if !strings.Contains(installerJoined, "BINDIR="+shellquote.RemoteWord("/usr/local/bin")) {
		t.Fatalf("installer did not target the existing install dir: %q", installerJoined)
	}
}

// TestDeployPushCreatesMissingEvenerPathTarget proves a push deploy can provision
// a fresh host: an evener_path whose file has not been installed yet is a
// creatable target, not a resolution failure. The existing symlink and directory
// guards still hold (createTargetCommand only fires when the path does not exist).
func TestDeployPushCreatesMissingEvenerPathTarget(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	var pushJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			// The file is not installed yet.
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "if [ -e "):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			pushJoined = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("x"), 0o755)
		},
	})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v (a not-yet-installed evener_path must be creatable)", err)
	}
	if !strings.Contains(pushJoined, pushBinaryRemote("/opt/evener/bin/evener", 1)) {
		t.Fatalf("push = %q, want an install at the creatable target", pushJoined)
	}
}

// TestDeployPushFallsBackToDefaultTargetOnFreshHost proves a push deploy with no
// evener_path and no evener on the host PATH still installs somewhere: the
// installer's own default ~/.local/bin/evener, whose directory the deploy
// creates. Without this a fresh host could never be provisioned by the push path.
func TestDeployPushFallsBackToDefaultTargetOnFreshHost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var pushJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "command -v evener"):
			return []byte(evenerPathMissingMarker + "\n"), nil
		case strings.Contains(joined, "mkdir -p /home/dev/.local/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "if [ -e "):
			return []byte("/home/dev/.local/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			pushJoined = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: writeStageBinary})

	target, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
	if err != nil {
		t.Fatalf("deploy: %v (a fresh host with no evener must be provisionable)", err)
	}
	if !strings.Contains(pushJoined, pushBinaryRemote("/home/dev/.local/bin/evener", int64(len("staged-binary")))) {
		t.Fatalf("push = %q, want the installer default target", pushJoined)
	}
	if target != "/home/dev/.local/bin/evener" {
		t.Fatalf("deploy target = %q, want the resolved default target /home/dev/.local/bin/evener", target)
	}
}

// TestDeployPrefersTheOperatorArtifactOverTheBuildSource pins acceptance
// criterion 3 at the runner seam, not by inference: with both seams configured
// the bytes the push streams are the operator artifact's, and the recorded argv
// is the push command — no cross-compile ran. The build source is a directory
// that is not an evener checkout, so the test also fails if the dispatch ever
// prefers the source: the source's verification refuses the deploy instead of
// pushing the artifact.
func TestDeployPrefersTheOperatorArtifactOverTheBuildSource(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	var pushArgv []string
	var pushed []byte
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			pushArgv = append([]string(nil), argv...)
			if stdin != nil {
				pushed, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("artifact-bytes"), 0o755)
		},
		BuildSource: t.TempDir(),
	})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if string(pushed) != "artifact-bytes" {
		t.Fatalf("pushed bytes = %q, want the operator artifact's; the build source was compiled instead", pushed)
	}
	want := rawCommandArgv(m.opts, host, pushBinaryRemote("/opt/evener/bin/evener", int64(len("artifact-bytes"))))
	if !equalArgv(pushArgv, want) {
		t.Fatalf("push argv:\n got %v\nwant %v", pushArgv, want)
	}
}

// TestDevControllerWithoutADeployPathAttaches covers acceptance criterion 5's
// first half under the rule that a build VERSION is not an attach gate: an
// identity-less "dev" controller with no deploy path attaches to a host running
// another build, because it speaks the same protocol. The accepted difference is
// reported (ensureOnce's notice at the attach), so it is not silent. The
// installer refusals still reject an artifact an unstamped controller cannot
// identify, and the deploy-configured half of criterion 5 is unchanged
// (TestDevControllerWithADeployPathForcesTheDeploy).
func TestDevControllerWithoutADeployPathAttaches(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(map[string][]byte{
			"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`),
			// A running hub matching the on-disk build, so nothing is deployed,
			// restarted, or bootstrapped: this is the attach path.
			"api/health": []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`),
		}),
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "dev"})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: a dev controller speaks the same protocol as the host", err)
	}
	if starts := len(fr.recordedStarts()); starts != 1 {
		t.Fatalf("bridge Start calls = %d, want 1 (attach to the host's own build)", starts)
	}
}

// TestDevControllerWithADeployPathForcesTheDeploy pins acceptance criterion 5's
// second half: with a deploy path configured, an unstamped "dev" controller
// deploys even when the host reports the very same "dev", because equality on a
// version that carries no identity proves nothing about the code. It drives both
// halves of that rule: the decision (ensureDecision's devUnverified branch) and
// the deploy the decision produces, so a decision that stopped forcing the
// deploy could not pass on the strength of the helper alone.
func TestDevControllerWithADeployPathForcesTheDeploy(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	facts := Preflight{
		Host:             host.Name,
		LaunchCheckKnown: true,
		Protocol:         appwire.ProtocolVersion,
		Version:          "dev",
		LaunchFlags:      []string{requiredLaunchFlag},
	}

	// No deploy path: there is nothing to install, so no deploy is decided.
	none := newTestManager(t, testRegistry(t, host), &fakeRunner{}, Options{controllerVersionOverride: "dev"})
	if none.deployRequired(host.Name, facts, "dev") {
		t.Fatal("a dev controller with no deploy path required a deploy it cannot perform")
	}

	// With a deploy path the equal "dev" on both sides is still not a match.
	builds := 0
	var pushed []byte
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				pushed, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("dev-build"), 0o755)
		},
	})
	if !m.deployRequired(host.Name, facts, "dev") {
		t.Fatal("a dev controller with a deploy path did not force the deploy, so it would attach to a build code equality cannot verify")
	}
	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if builds != 1 {
		t.Fatalf("cross-compiles = %d, want 1 (the forced deploy must install the controller's own build)", builds)
	}
	if string(pushed) != "dev-build" {
		t.Fatalf("pushed bytes = %q, want the controller's own unstamped build", pushed)
	}
}

// TestDirtyControllerRefusalsNameTheRemedy pins acceptance criterion 6: a dirty
// controller's build has no reproducible identity, so the push path and the
// installer fallback each refuse, and each refusal carries a remedy an operator
// can act on — a clean rebuild for the push path, and the hub's flags for the
// installer fallback through Options.DeployHelp. The refusal's type and
// terminality are pinned by TestRound13DirtyControllerDeployRefusalIsTerminal;
// this adds the remedy clauses, which is the half the criterion names.
func TestDirtyControllerRefusalsNameTheRemedy(t *testing.T) {
	const dirty = "abc1234-dirty"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}

	t.Run("push path", func(t *testing.T) {
		builds := 0
		m := newTestManager(t, testRegistry(t, host), refusingRunner(t), Options{
			controllerVersionOverride: dirty,
			BuildBinary: func(context.Context, string, string, string) error {
				builds++
				return nil
			},
		})
		_, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
		if !errors.Is(err, errControllerDirty) {
			t.Fatalf("err = %v, want errControllerDirty", err)
		}
		if !strings.Contains(err.Error(), "rebuild the controller from a clean checkout") {
			t.Fatalf("push-path refusal does not name the remedy: %v", err)
		}
		if builds != 0 {
			t.Fatalf("cross-compiles = %d, want 0 (a dirty controller has no deployable build)", builds)
		}
	})

	t.Run("installer fallback", func(t *testing.T) {
		const help = "set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path>"
		err := installerRefusal(t, host, help, "release", "true")
		if !errors.Is(err, ErrDeploy) {
			t.Fatalf("err = %v, want ErrDeploy", err)
		}
		if !strings.Contains(err.Error(), help) {
			t.Fatalf("installer refusal does not name the remedy: %v", err)
		}
		if strings.Contains(err.Error(), "Options.") {
			t.Fatalf("installer refusal still names a library-internal field: %v", err)
		}
	})
}

// postDeployBuildRunner answers the whole ensure sequence for a linux host whose
// evener is at /opt/evener/bin/evener and whose hub is NOT running: preflight
// (whose launch-check reports oldsha), the deploy-target resolution, the push,
// and the post-deploy launch-check, which reports postDeploy — the build the
// artifact the push streamed really carries. The health probe fails before
// anything is launched, so the host reads as "no hub present" and the deploy is a
// fresh install rather than a restart; afterwards it answers postDeploy, the
// version the launched binary reports.
func postDeployBuildRunner(t *testing.T, postDeploy string) *fakeRunner {
	t.Helper()
	launchCalls, launches := 0, 0
	return &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(fmt.Sprintf(`{"protocol":"evener-appwire-v5","version":%q,"launch_flags":["api-log"]}`, postDeploy)), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor: an ad hoc host
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil // nothing is listening
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(joined, "nohup"):
			launches++
			return nil, nil
		case strings.Contains(joined, "api/health"):
			if launches == 0 {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(fmt.Sprintf(`{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`, postDeploy)), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
}

// TestEnsurePostDeployBuildMismatchRefusesTerminally pins acceptance criterion 8:
// a deploy whose freshly re-read launch contract still disagrees with the
// controller is a failed verification and never an attach. The artifact path is
// the one the pre-push check cannot cover — an operator-supplied binary that
// targets the right platform but was built from another tree — so the only
// evidence the controller has is the host's own launch-check after the write.
// The refusal is terminal: retrying re-pushes the same artifact, so it cannot
// converge. The matching case beside it pins that a deploy which does pin the
// controller's build attaches exactly as before.
func TestEnsurePostDeployBuildMismatchRefusesTerminally(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	opts := Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	}

	// The stopped-host shape: there is no hub to answer a health probe, so the
	// deploy is a fresh install and the refusal must fire before the first-attach
	// bootstrap starts anything.
	t.Run("a stopped host whose installed artifact reports another build", func(t *testing.T) {
		fr := postDeployBuildRunner(t, "othersha")
		m := newTestManager(t, testRegistry(t, host), fr, opts)

		_, err := m.Ensure(context.Background(), "alpha")
		requireUnstampedRefusal(t, err)
		requireNoAttach(t, fr)
		for _, argv := range fr.recordedRuns() {
			if strings.Contains(strings.Join(argv, " "), "nohup") {
				t.Fatalf("a hub was started on the mismatched build: %v", argv)
			}
		}
	})

	// The running-host shape, which is the one that attached outright before this
	// gate existed: a live hub unit answers the restart's health probe with its own
	// build, so the restart reads as successful while the binary the controller
	// addressed reports another build. Without the gate the bridge starts and the
	// host is served on a build the controller never stamped — and the next
	// reconnect deploys again, so it re-deploys forever while attached.
	t.Run("a running host left on a build the controller did not stamp", func(t *testing.T) {
		fr := deployRunner(t,
			func(call int) ([]byte, error) {
				if call == 0 {
					return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
				}
				return []byte(`{"protocol":"evener-appwire-v5","version":"othersha","launch_flags":["api-log"]}`), nil
			},
			func(int) ([]byte, error) {
				return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			},
		)
		m := newTestManager(t, testRegistry(t, host), fr, opts)

		_, err := m.Ensure(context.Background(), "alpha")
		requireUnstampedRefusal(t, err)
		requireNoAttach(t, fr)
	})

	t.Run("a deployed build that matches still attaches", func(t *testing.T) {
		fr := deployRunner(t,
			func(call int) ([]byte, error) {
				if call == 0 {
					return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
				}
				return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
			},
			func(int) ([]byte, error) {
				return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			},
		)
		m := newTestManager(t, testRegistry(t, host), fr, opts)

		ch, err := m.Ensure(context.Background(), "alpha")
		if err != nil {
			t.Fatalf("Ensure: %v (a deploy whose fresh facts match the controller must still attach)", err)
		}
		if got := ch.Preflight().Version; got != "newsha" {
			t.Fatalf("channel version = %q, want newsha (the deployed build)", got)
		}
		if starts := fr.recordedStarts(); len(starts) != 1 {
			t.Fatalf("bridge Start calls = %d, want 1", len(starts))
		}
	})
}

// TestDeployArtifactUnusableIsTerminal pins the operator-artifact sibling of the
// run-target refusal: a -deploy-binary built for another platform (the hub's
// copyDeployBinary wraps errDeployArtifactUnusable for exactly that) is a
// permanent operator mistake — every retry re-reads the same file — so the
// refusal must be terminal and must not ride the retryable ErrDeploy class. The
// build seam fails before anything is staged or pushed, so the runner must see
// only the prebuild probes: no push, no restart, no start.
func TestDeployArtifactUnusableIsTerminal(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(int) ([]byte, error) {
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return nil, errors.New("curl: (7) Failed to connect")
		},
	)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary: func(context.Context, string, string, string) error {
			return fmt.Errorf("%w: -deploy-binary %q targets linux/arm64, but the host needs linux/amd64; supply an evener built for linux/amd64",
				errDeployArtifactUnusable, "/tmp/evener")
		},
	})

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, errDeployArtifactUnusable) {
		t.Fatalf("Ensure err = %v, want errDeployArtifactUnusable", err)
	}
	if !isTerminal(err) {
		t.Fatalf("Ensure err = %v, want a terminal refusal (retrying re-reads the same artifact)", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("Ensure err = %v still carries ErrDeploy, so the retryable class would win", err)
	}
	if !strings.Contains(err.Error(), "-deploy-binary") || !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("refusal does not name the flag and the host target: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "cat >") {
			t.Fatalf("the unusable artifact was pushed: %v", argv)
		}
		if strings.Contains(joined, "systemctl restart") || strings.Contains(joined, "nohup") {
			t.Fatalf("the refused artifact restarted or started a hub: %v", argv)
		}
	}
	hostGate := m.hostLock(host.Name)
	defer m.releaseHostLock(host.Name)
	if more := m.reconnectOnce(context.Background(), host, hostGate); more {
		t.Fatal("reconnectOnce asked for another attempt, so the supervisor would loop on the terminal refusal")
	}
}

// TestEnsureWrongArtifactAgainstRunningHubRefusesBeforeRestart pins the ordering
// hole a running hub opened for the operator-artifact refusal: the deploy wrote a
// build the controller did not stamp, so the restart onto it can never report the
// expected version, and waitHealthy failed retryably (ErrRestart) BEFORE the
// post-deploy identity gate ran. The supervisor then re-pushed and re-restarted
// the same artifact forever. The deployed build is now judged on the fresh on-disk
// facts before the restart path, so the permanent cause — the unstamped artifact —
// is what surfaces, and the runner sees no restart at all.
func TestEnsureWrongArtifactAgainstRunningHubRefusesBeforeRestart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				// The on-disk binary before the deploy.
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			// The artifact the deploy wrote: right platform, foreign identity.
			return []byte(`{"protocol":"evener-appwire-v5","version":"othersha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			// The running hub, and any process restarted onto the wrong artifact,
			// report the foreign build.
			return []byte(`{"version":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})

	_, err := m.Ensure(context.Background(), "alpha")
	requireUnstampedRefusal(t, err)
	requireNoAttach(t, fr)

	// The recorded commands are the proof the retryable restart path was never
	// entered: one push of the wrong artifact, no restart of the hub onto it (the
	// restart is where ErrRestart used to surface and drive the loop).
	pushes, restarts := 0, 0
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "cat >"):
			pushes++
		case strings.Contains(joined, "systemctl restart"):
			restarts++
		}
	}
	if pushes != 1 {
		t.Fatalf("push count = %d, want exactly 1 (the wrong artifact is pushed once)", pushes)
	}
	if restarts != 0 {
		t.Fatalf("restart count = %d, want 0 (the permanent cause must be judged before the restart path)", restarts)
	}

	// The supervisor's own iteration stands down on the terminal refusal, so it
	// cannot re-push and re-restart the same artifact. (A fresh manual attempt
	// pushes again — that is the manager's per-attempt behavior, not a loop; the
	// loop the ordering opened was deploy -> restart -> ErrRestart -> deploy.)
	hostGate := m.hostLock(host.Name)
	defer m.releaseHostLock(host.Name)
	if more := m.reconnectOnce(context.Background(), host, hostGate); more {
		t.Fatal("reconnectOnce asked for another attempt, so the supervisor would loop on the terminal refusal")
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "systemctl restart") {
			t.Fatalf("the terminal refusal still restarted the hub onto the wrong artifact: %v", argv)
		}
	}
}

// requireUnstampedRefusal asserts the terminal refusal a deploy that did not pin
// the controller's build must produce: the sentinel, its terminality, and the
// message naming both the build the host reports and the one it should carry.
func requireUnstampedRefusal(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errDeployUnstamped) {
		t.Fatalf("Ensure err = %v, want errDeployUnstamped (a deploy that did not pin the controller's build must not attach)", err)
	}
	if !isTerminal(err) {
		t.Fatalf("Ensure err = %v, want a terminal refusal (retrying re-pushes the same artifact)", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("Ensure err = %v still wraps ErrDeploy, so a supervisor would retry the same refusal forever", err)
	}
	for _, want := range []string{`"othersha"`, `"newsha"`, "not built from this controller's tree"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not name %q: %v", want, err)
		}
	}
}

// requireNoAttach asserts no bridge process was started for the host.
func requireNoAttach(t *testing.T, fr *fakeRunner) {
	t.Helper()
	if starts := fr.recordedStarts(); len(starts) != 0 {
		t.Fatalf("bridge Start calls = %d, want 0 (never attach to a build the controller did not deploy)", len(starts))
	}
}

// TestEnsureRestartOnlyMismatchAttachesTheServingBuild covers the restart-only
// half of the attach rule. A pass that restarts the hub launches the build already
// on disk, and the bridge attaches to the RUNNING process, which the wait has
// already made prove it reports the controller's build — here "newsha" after the
// restart, while the on-disk file re-reads as "othersha" (a binary swapped under
// the controller, or just a host that keeps its own build). Judging that file
// refused a host whose serving hub is exactly the build this controller asked for.
func TestEnsureRestartOnlyMismatchAttachesTheServingBuild(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				// The on-disk binary already matches, so ensureDecision chooses no
				// deploy and falls to the stale-process restart.
				return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
			}
			// The re-read after the restart finds the on-disk file is not the
			// controller's build. That is the host's business: nothing was deployed.
			return []byte(`{"protocol":"evener-appwire-v5","version":"othersha","launch_flags":["api-log"]}`), nil
		},
		func(call int) ([]byte, error) {
			if call == 0 {
				// The running hub is the stale process a restart replaces.
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			}
			// The restarted hub is the controller's build: what the wait requires,
			// and what the bridge then talks to.
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: the serving hub reports the controller's build", err)
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (attach to the verified serving process)", got)
	}
	restarted := false
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "systemctl restart") {
			restarted = true
		}
		if strings.Contains(joined, "cat >") {
			t.Fatalf("a restart-only attempt pushed a binary: %v", argv)
		}
	}
	if !restarted {
		t.Fatal("no restart ran, so the pass under test was not the restart-only one")
	}
}
