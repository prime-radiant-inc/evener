package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
)

// TestRound8PushDeployReturnsResolvedTarget pins the round-eight High that the
// push deploy resolved the path it installed to and then discarded it: deploy's
// build branch returned "" so ensureOnce never recorded host.EvenerPath. On a
// fresh host that path is the installer default ~/.local/bin/evener, and without
// it the post-deploy launch-check addressed the literal `evener`, which the
// non-interactive PATH cannot resolve.
func TestRound8PushDeployReturnsResolvedTarget(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
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
		t.Fatalf("deploy: %v", err)
	}
	if target != "/home/dev/.local/bin/evener" {
		t.Fatalf("deploy target = %q, want the resolved fresh-host install path /home/dev/.local/bin/evener", target)
	}
}

// TestRound8EnsureFreshHostPushDeployRecordsTarget pins the same High end to end:
// after a push deploy on a host with no evener_path and no evener on PATH, the
// post-deploy launch-check (refreshLaunchContract) must address the recorded
// install target. Before the fix host.EvenerPath stayed empty, the re-probe ran
// the literal `evener`, and the terminal errExecutableMissing refused a host the
// push had just provisioned.
func TestRound8EnsureFreshHostPushDeployRecordsTarget(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	launched := false
	var launchPaths []string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
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
				// No evener_path and nothing on the non-interactive PATH.
				return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
			}
			launchPaths = append(launchPaths, joined)
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "command -v evener >/dev/null 2>&1"):
			// The dedicated executable probe: absent.
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "command -v evener"):
			return []byte(evenerPathMissingMarker + "\n"), nil
		case strings.Contains(joined, "mkdir -p /home/dev/.local/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/home/dev/.local/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
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
		BuildBinary:               writeStageBinary,
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v (a fresh-host push deploy must record the resolved target so the post-deploy probe addresses it)", err)
	}
	found := false
	for _, p := range launchPaths {
		if strings.Contains(p, "/home/dev/.local/bin/evener launch-check") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no launch-check addressed the recorded install target; launch checks seen: %v", launchPaths)
	}
}

// TestRound8InstallerPreservesSymlinkedInstallLayout pins the round-eight High
// that the installer fallback canonicalized the user-facing `bin/evener` symlink
// to the real binary under `share/evener/bin` before deriving BINDIR. installerDirs
// then produced nested paths like share/evener/bin/share/evener/bin and installed
// to the wrong location. The run target and BINDIR must be the user-facing symlink
// and its directory; only identity checks may canonicalize.
func TestRound8InstallerPreservesSymlinkedInstallLayout(t *testing.T) {
	origSHA, origDirty, origChannel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = origSHA, origDirty, origChannel
	})
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = "newsha", "", "snapshot"

	const link = "/home/dev/.local/bin/evener"
	const canonical = "/home/dev/.local/share/evener/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	var installerJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte(link + " hub -addr 127.0.0.1:9180\n"), nil
		case strings.Contains(joined, "evener_resolve "+link):
			return []byte(canonical + "\n"), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte(link + "\n"), nil
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
	if target != link {
		t.Fatalf("run target = %q, want the user-facing symlink path %q (not the canonical %q)", target, link, canonical)
	}
	if !strings.Contains(installerJoined, "BINDIR="+shellquote.RemoteWord("/home/dev/.local/bin")) {
		t.Fatalf("installer BINDIR is not the symlink's directory: %q", installerJoined)
	}
	if !strings.Contains(installerJoined, "EVENER_SHARE_BINDIR="+shellquote.RemoteWord("/home/dev/.local/share/evener/bin")) {
		t.Fatalf("installer EVENER_SHARE_BINDIR is wrong: %q", installerJoined)
	}
	if strings.Contains(installerJoined, "share/evener/bin/share/evener") {
		t.Fatalf("installer targeted a nested, wrong layout: %q", installerJoined)
	}
}

// TestRound8IgnoredGoFileRefusedInBuildSource pins the round-eight Medium that
// `git status --porcelain` omits ignored untracked files, so an ignored .go source
// file could be compiled into the build while the deployed binary kept the
// repository's unchanged clean SHA. The revision check must refuse ignored Go
// sources; the ignored (and required) embedded frontend dist must still not count.
func TestRound8IgnoredGoFileRefusedInBuildSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.gen.go\n"), 0o644); err != nil {
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

	// An ignored generated Go file is invisible to `git status --porcelain` but is
	// still part of what `go build` compiles.
	if err := os.WriteFile(filepath.Join(root, "generated.gen.go"), []byte("package extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status := gitIn(t, root, "status", "--porcelain"); status != "" {
		t.Fatalf("the generated file was not ignored; porcelain status = %q", status)
	}
	_, err := verifyBuildSource(root)
	if err == nil {
		t.Fatal("verifyBuildSource accepted a build source with an ignored Go file")
	}
	if !strings.Contains(err.Error(), "ignored") || !strings.Contains(err.Error(), "refusing to deploy") {
		t.Fatalf("error does not explain the ignored Go source: %v", err)
	}

	// Removing it returns the source to a deployable state, so the refusal is keyed
	// off the ignored Go input and not the checkout.
	if err := os.Remove(filepath.Join(root, "generated.gen.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBuildSource(root); err != nil {
		t.Fatalf("verifyBuildSource(clean again): %v", err)
	}
}

// TestRound8InstallerVersionMismatchIsTerminal pins the round-eight Medium that a
// snapshot-channel installer ref is the mutable `snapshot` tag: once main moves
// past the controller's commit the fetched artifact no longer matches, and the
// mismatch was wrapped in ErrDeploy (non-terminal), so the supervisor re-fetched
// and re-rejected it forever. It must be a terminal ErrVersionMismatch instead.
func TestRound8InstallerVersionMismatchIsTerminal(t *testing.T) {
	origSHA, origDirty, origChannel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = origSHA, origDirty, origChannel
	})
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = "newsha", "", "snapshot"

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "evener-install.XXXXXX"):
			return nil, nil
		case strings.Contains(joined, "launch-check"):
			// The installer succeeded but fetched a newer snapshot than this
			// controller's commit.
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldersha","launch_flags":["api-log"]}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha"})

	_, err := m.deployInstaller(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("err = %v, want ErrVersionMismatch (a moved snapshot tag must not be retried as ErrDeploy)", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("err = %v still wraps ErrDeploy, so the supervisor would retry forever", err)
	}
	if !isTerminal(err) {
		t.Fatalf("err = %v, want a terminal refusal rather than an endless retry", err)
	}
}
