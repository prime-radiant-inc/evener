package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEnsureLaunchCheckCommandFailure(t *testing.T) {
	for _, mode := range []string{"live", "cold", "bridge-failed", "ssh-exit", "spawn", "cancelled", "deadline", "presence-probe", "environment"} {
		t.Run(mode, func(t *testing.T) {
			failure := error(&RunError{Err: exitStatus(t, 2)})
			switch mode {
			case "ssh-exit":
				failure = &RunError{Err: exitStatus(t, 255)}
			case "spawn":
				failure = errors.New("process did not start")
			case "cancelled":
				failure = context.Canceled
			case "deadline":
				failure = context.DeadlineExceeded
			}
			fr := &fakeRunner{startFn: goodStartFn(t)}
			bridgeFailure := errors.New("bridge transport failed")
			if mode == "bridge-failed" || mode == "cold" {
				fr.startFn = func(context.Context, []string, io.Writer) (Stdio, error) {
					return nil, bridgeFailure
				}
			}
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				command := strings.Join(argv, " ")
				switch {
				case mode == "environment" && strings.Contains(command, "uname -s"):
					return nil, failure
				case strings.Contains(command, "launch-check"):
					return nil, failure
				case strings.Contains(command, "if test -e"):
					if mode == "presence-probe" {
						return nil, &RunError{Err: exitStatus(t, 255)}
					}
					return nil, nil
				case mode == "cold" && strings.Contains(command, "api/health"):
					return nil, errors.New("no listener")
				case mode == "cold" && strings.Contains(command, "lsof -ti"):
					return []byte(noListenerMarker), nil
				default:
					return cannedRun(nil)(ctx, argv, stdin)
				}
			}
			m, effects := compatibleConnectManager(t, fr, Options{BuildBinary: writeStageBinary})
			if mode == "live" {
				requireReadOnlyConnect(t, m, effects)
				return
			}
			ch, err := m.Ensure(t.Context(), "alpha")
			wantBridges := 0
			if mode == "bridge-failed" || mode == "cold" {
				wantBridges = 1
				if !errors.Is(err, bridgeFailure) {
					t.Fatalf("bridge failure was hidden: %v", err)
				}
			}
			if ch != nil || err == nil || *effects != (connectEffects{}) || len(fr.recordedStarts()) != wantBridges {
				t.Fatalf("refused preflight: channel=%v err=%v effects=%+v bridges=%d", ch, err, effects, len(fr.recordedStarts()))
			}
		})
	}
}
