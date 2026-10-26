package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/remoteinstall"
)

// TestRound11EmbeddedInstallerMatchesTheReviewedScript pins the round-eleven
// High's trust anchor: the installer the fallback runs is compiled into this
// binary, and the only thing that makes it trustworthy is that it is the
// repository's reviewed install.sh, byte for byte. This test is the pin on that
// claim — a future edit to install.sh fails here until the embedded copy is
// re-copied from the reviewed file, so the copy cannot drift silently.
func TestRound11EmbeddedInstallerMatchesTheReviewedScript(t *testing.T) {
	if len(remoteinstall.Script) == 0 {
		t.Fatal("the embedded installer is empty")
	}
	if !bytes.HasPrefix(remoteinstall.Script, []byte("#!/bin/sh\n")) {
		t.Fatalf("the embedded installer does not start with a /bin/sh shebang: %q", remoteinstall.Script)
	}
	reviewed, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("read the repository's install.sh: %v", err)
	}
	if !bytes.Equal(remoteinstall.Script, reviewed) {
		t.Fatalf("the embedded installer (%d bytes) differs from the repository's install.sh (%d bytes); the fix is to re-copy the reviewed script into internal/remoteinstall/install.sh",
			len(remoteinstall.Script), len(reviewed))
	}
}

// TestRound11InstallerFallbackStreamsTheEmbeddedScript pins the rest of the
// High: the fallback must hand the host the embedded bytes over stdin and run
// nothing it fetched. It exercises deployInstaller end to end, so the stdin the
// host receives, the absence of any URL in the remote command, and the returned
// run target are all checked against the production seam.
func TestRound11InstallerFallbackStreamsTheEmbeddedScript(t *testing.T) {
	origChannel, origTag, origDirty := buildinfo.Channel, buildinfo.ReleaseTag, buildinfo.GitDirty
	t.Cleanup(func() { buildinfo.Channel, buildinfo.ReleaseTag, buildinfo.GitDirty = origChannel, origTag, origDirty })
	buildinfo.Channel, buildinfo.ReleaseTag, buildinfo.GitDirty = "release", "v1.2.3", ""

	const resolved = "/home/dev/.local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	installerRan := false
	var installerJoined string
	var installerStdin []byte
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "evener-install.XXXXXX"):
			installerRan = true
			installerJoined = joined
			installerStdin, _ = io.ReadAll(stdin)
			return nil, nil
		case strings.Contains(joined, "command -v lsof"):
			// The port probe: no listener, from whichever tier the host has.
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte(evenerPathMissingMarker + "\n"), nil
		case strings.Contains(joined, resolved+" launch-check"):
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha"})

	target, err := m.deployInstaller(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"}, nil)
	if err != nil {
		t.Fatalf("deployInstaller: %v", err)
	}
	if target != resolved {
		t.Fatalf("run target = %q, want %q", target, resolved)
	}
	if !installerRan {
		t.Fatal("the installer fallback never ran")
	}
	if !bytes.Equal(installerStdin, remoteinstall.Script) {
		t.Fatalf("the host received %d bytes on stdin, want the embedded installer's %d bytes", len(installerStdin), len(remoteinstall.Script))
	}
	for _, forbidden := range []string{"http://", "https://", "curl", "raw.githubusercontent.com"} {
		if strings.Contains(installerJoined, forbidden) {
			t.Fatalf("the installer command still fetches something (%q): %q", forbidden, installerJoined)
		}
	}
	if !strings.Contains(installerJoined, "sh \"$tmp\"") {
		t.Fatalf("the installer command does not run the streamed script: %q", installerJoined)
	}
}

// TestRound11PreflightDiscoveredTargetReachesTheAttachArgv pins the round-eleven
// Medium that a target discovered by preflight never reached host.EvenerPath:
// for an already-healthy host whose binary lives at the installer default (off
// the non-interactive PATH), the first explicit attach built the bridge from the
// bare word `evener` and failed with command-not-found.
func TestRound11PreflightDiscoveredTargetReachesTheAttachArgv(t *testing.T) {
	const resolved = "/home/dev/.local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	fr := &fakeRunner{
		runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(joined, "uname -s"):
				return []byte("Linux\n"), nil
			case strings.HasSuffix(joined, "uname -m"):
				return []byte("x86_64\n"), nil
			case argv[len(argv)-1] == envProbeScript:
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.HasSuffix(joined, "id -u"):
				return []byte("1000\n"), nil
			case strings.Contains(joined, resolved+" launch-check"):
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
			case strings.Contains(joined, " evener launch-check"):
				// The host's non-interactive PATH does not carry the binary.
				return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
			case strings.Contains(joined, executableProbeRemote("evener")):
				// The dedicated executable probe: absent.
				return nil, exitStatus(t, 1)
			case strings.Contains(joined, "[ -f ") && strings.Contains(joined, ".local/bin/evener"):
				return []byte(resolved + "\n"), nil
			case strings.Contains(joined, "api/health"):
				return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
		startFn: goodStartFn(t),
	}
	modelSelectedProcess(t, fr, "/home/dev/.local/bin/evener", "newsha", "newsha")
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (an already-healthy host at the installer default must attach)", err)
	}
	defer func() { _ = ch.Close() }()

	starts := fr.recordedStarts()
	if len(starts) == 0 {
		t.Fatal("no bridge was started")
	}
	joined := strings.Join(starts[len(starts)-1], " ")
	if !strings.Contains(joined, resolved+" hub attach --stdio") {
		t.Fatalf("the bridge argv does not address the preflight-discovered target: %q", joined)
	}
	if strings.Contains(joined, "alpha.example evener hub attach") {
		t.Fatalf("the bridge argv still falls back to the bare `evener`: %q", joined)
	}
}

// TestRound11FreshHostWithoutLsofIsProvisioned pins the consequence of the lsof
// fallback: a fresh host whose port probe can only answer through ss or the
// kernel tables must still be provisioned — cross-compiled, pushed, started,
// and attached — instead of failing with ErrRestart before anything was
// installed.
func TestRound11FreshHostWithoutLsofIsProvisioned(t *testing.T) {
	const resolved = "/home/dev/.local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	pushed, launched := false, false
	fr := &fakeRunner{
		runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(joined, "uname -s"):
				return []byte("Linux\n"), nil
			case strings.HasSuffix(joined, "uname -m"):
				return []byte("x86_64\n"), nil
			case argv[len(argv)-1] == envProbeScript:
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.HasSuffix(joined, "id -u"):
				return []byte("1000\n"), nil
			case strings.Contains(joined, "list-units"):
				return nil, nil
			case strings.Contains(joined, "command -v lsof"):
				// The host has neither lsof nor a listener: the fallback tier
				// proved the hub port free.
				return []byte(noListenerMarker + "\n"), nil
			case strings.Contains(joined, executableProbeRemote("evener")):
				// The dedicated executable probe: absent.
				return nil, exitStatus(t, 1)
			case strings.Contains(joined, "command -v evener"):
				return []byte(evenerPathMissingMarker + "\n"), nil
			case strings.Contains(joined, "launch-check"):
				if strings.Contains(joined, " evener launch-check") || !pushed {
					return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
				}
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
			case strings.Contains(joined, "evener_resolve"):
				// The deploy-target resolver runs before the push (nothing
				// installed yet, so it fails and the create-target fallback takes
				// over) and again when the bootstrap names the installed binary.
				if !pushed {
					return nil, exitStatus(t, 1)
				}
				return []byte(resolved + "\n"), nil
			case strings.Contains(joined, executableProbeRemote(resolved)):
				if !pushed {
					return nil, exitStatus(t, 1)
				}
				return nil, nil
			case strings.Contains(joined, "if [ -e "):
				return []byte(resolved + "\n"), nil
			case strings.Contains(joined, "if [ -n "):
				// The installer-default probe: no binary there yet.
				return nil, nil
			case strings.Contains(joined, "mkdir -p /home/dev/.local/bin"):
				return nil, nil
			case strings.Contains(joined, "cat > \"$tmp\""):
				pushed = true
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
		},
		startFn: goodStartFn(t),
	}
	selected := modelSelectedProcess(t, fr, "/home/dev/.local/bin/evener", "newsha", "newsha")
	selected.stopped, selected.adhoc = true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (a fresh host without lsof must be provisioned)", err)
	}
	defer func() { _ = ch.Close() }()
	if !pushed {
		t.Fatal("the binary was never pushed to the fresh host")
	}
	if !launched {
		t.Fatal("the hub was never started on the fresh host")
	}
	if got := m.resolvedTarget(host.Name); got != resolved {
		t.Fatalf("resolved target = %q, want %q", got, resolved)
	}
	if proto := ch.Preflight().Protocol; proto != appwire.ProtocolVersion {
		t.Fatalf("channel protocol = %q, want %q", proto, appwire.ProtocolVersion)
	}
}
