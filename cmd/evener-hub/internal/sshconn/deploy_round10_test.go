package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/remoteinstall"
)

// TestRound10PushVerifiesByteCount pins the round-ten Medium that the push had
// no integrity check: ssh reports a truncated stream as a successful EOF, so
// `cat > "$tmp"` exits 0 on a partial transfer and the mv would install a
// truncated binary over a working one. The remote command must compare the
// streamed temp file's size to the staged file's size before the mv.
func TestRound10PushVerifiesByteCount(t *testing.T) {
	const staged = "staged-binary" // 13 bytes
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	var pushJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
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
			return os.WriteFile(out, []byte(staged), 0o755)
		},
	})

	if _, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	wantCheck := `[ "$v" = ` + strconv.Itoa(len(staged)) + " ]"
	if !strings.Contains(pushJoined, wantCheck) {
		t.Fatalf("push does not verify the streamed byte count: %q (want %q)", pushJoined, wantCheck)
	}
	if !strings.Contains(pushJoined, "wc -c") {
		t.Fatalf("push does not measure the streamed file: %q", pushJoined)
	}
	idxCheck := strings.Index(pushJoined, wantCheck)
	idxMv := strings.Index(pushJoined, "mv ")
	if idxCheck < 0 || idxMv < 0 || idxCheck > idxMv {
		t.Fatalf("byte-count check must run before the mv: %q", pushJoined)
	}
}

// TestInstallerScriptChecksTheWriteBeforeExecuting pins the round-ten Medium —
// `… | env … sh` returned sh's exit status, so a failed handoff looked
// successful and the post-install probe could misclassify — under round eleven's
// mechanism. The installer is now the embedded script rather than a download
// (round eleven's High), but the property is unchanged: the script is written to
// a temp file and only then run, so a truncated or dropped stream cannot
// half-execute.
func TestInstallerScriptChecksTheWriteBeforeExecuting(t *testing.T) {
	got := remoteinstall.Command("v1.2.3", "", "/opt/evener/bin", "/opt/evener/share/evener/bin")
	if strings.Contains(got, "| env ") || strings.Contains(got, "| sh") {
		t.Fatalf("the installer command still pipes the script into sh, masking a failed write: %q", got)
	}
	// Round twelve adds the byte count to the handoff check: the script runs only
	// when the streamed file is exactly as long as the embedded copy, so a
	// truncated stream (which ssh reports as a clean EOF, leaving `cat` at exit 0)
	// cannot half-execute.
	wantCheck := "cat > \"$tmp\" && v=$(wc -c < \"$tmp\" | tr -d '[:space:]') && [ \"$v\" = " + strconv.Itoa(len(remoteinstall.Script)) + " ] && env "
	if !strings.Contains(got, wantCheck) {
		t.Fatalf("the installer command does not check the script write's byte count before executing the installer: %q", got)
	}
	if !strings.Contains(got, "EVENER_INSTALL_VERSION=v1.2.3") {
		t.Fatalf("the installer command lost the pinned ref: %q", got)
	}
	if !strings.Contains(got, "BINDIR=/opt/evener/bin") || !strings.Contains(got, "EVENER_SHARE_BINDIR=/opt/evener/share/evener/bin") {
		t.Fatalf("the installer command lost the custom install dirs: %q", got)
	}
}

// TestRound10PreflightDiscoversInstallerDefault pins the first half of the
// round-ten Medium: preflight probed only the literal `evener`, so a host whose
// binary lives at the installer default ~/.local/bin/evener (off the
// non-interactive PATH) reported an unknown launch contract and was re-deployed
// on every reconnect. Preflight must fall back to the same default deployTarget
// uses and read the matching contract.
func TestRound10PreflightDiscoversInstallerDefault(t *testing.T) {
	const resolved = "/home/dev/.local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
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
		case strings.Contains(joined, resolved+" launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, " evener launch-check"):
			// No evener on the non-interactive PATH.
			return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
		case strings.Contains(joined, "command -v evener >/dev/null 2>&1"):
			// The dedicated executable probe: absent.
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "[ -f ") && strings.Contains(joined, ".local/bin/evener"):
			return []byte(resolved + "\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: writeStageBinary})

	pf, err := m.preflight(context.Background(), host)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !pf.LaunchCheckKnown || pf.Version != "newsha" {
		t.Fatalf("preflight did not read the installer-default binary's contract: %+v", pf)
	}
	if got := m.resolvedTarget("alpha"); got != resolved {
		t.Fatalf("preflight did not record the discovered target: got %q, want %q", got, resolved)
	}
}

// TestRound10ResolvedTargetAvoidsRedeploy pins the second half of the round-ten
// Medium: the resolved target was recorded only in a local host copy, so the
// next attempt started from the original registry host with an empty
// EvenerPath, failed preflight again, and re-ran a full cross-compile deploy.
// Two attempts must build once.
func TestRound10ResolvedTargetAvoidsRedeploy(t *testing.T) {
	const resolved = "/home/dev/.local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	installed, launched := false, false
	builds := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
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
		case strings.Contains(joined, resolved+" launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, " evener launch-check"):
			return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
		case strings.Contains(joined, "command -v evener >/dev/null 2>&1"):
			// The dedicated executable probe: absent.
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "[ -f ") && strings.Contains(joined, resolved):
			if installed {
				return []byte(resolved + "\n"), nil
			}
			return nil, nil
		case strings.Contains(joined, "list-units"):
			return nil, nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte(evenerPathMissingMarker + "\n"), nil
		case strings.Contains(joined, "mkdir -p /home/dev/.local/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "if [ -e "):
			return []byte(resolved + "\n"), nil
		case strings.Contains(joined, "cat >"):
			installed = true
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
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("staged-binary"), 0o755)
		},
	})

	// Two attempts from the SAME original host (empty EvenerPath), exactly as a
	// reconnect starts from the registry host rather than the last attempt's copy.
	for i := range 2 {
		ch, err := m.ensureOnce(context.Background(), host, true)
		if err != nil {
			t.Fatalf("ensureOnce #%d: %v", i+1, err)
		}
		if ch != nil {
			_ = ch.Close()
		}
	}
	if builds != 1 {
		t.Fatalf("cross-compile build ran %d times across two attempts, want 1 (the resolved target must persist)", builds)
	}
	if !installed {
		t.Fatal("the first attempt never installed the binary")
	}
}

// TestRound10CreateTargetErrorSurfaced pins the round-ten Low that
// resolveDeployOrCreateTarget discarded the create command's error and returned
// the original resolve error, so a missing parent directory or permission
// refusal surfaced as "does not resolve to a real file" with no create
// diagnostic.
func TestRound10CreateTargetErrorSurfaced(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "if [ -e "):
			return []byte("mkdir: cannot create directory '/opt/evener/bin': Permission denied\n"), exitStatus(t, 1)
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	_, err := m.resolveDeployOrCreateTarget(context.Background(), host, "/opt/evener/bin/evener")
	if !errors.Is(err, ErrDeploy) {
		t.Fatalf("err = %v, want ErrDeploy", err)
	}
	if !strings.Contains(err.Error(), "create install target") {
		t.Fatalf("error does not name the create attempt: %v", err)
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("error discards the create command's output: %v", err)
	}
}
