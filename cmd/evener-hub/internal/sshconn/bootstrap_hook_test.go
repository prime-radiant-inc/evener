package sshconn

// The first-attach repair is crash-fencing §6:131's one exempt delivery step.
// These tests pin the sshconn half of the wiring: the manager asks the hub's
// first-contact caller to run before the launch's first mutating remote command,
// and a bootstrap refusal starts nothing. The caller's own behavior (the fence,
// the claim, the delivery) is the hub package's to pin.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// stoppedHostManager builds the stopped-host fixture the first-attach tests use:
// a host whose hub is not running, whose supervisor unit is identifiable and
// dormant, and whose recorded start is observable through order.
func stoppedHostManager(t *testing.T, order *[]string, hook BootstrapHook) *Manager {
	t.Helper()
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	started := false
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
			return []byte(`{"protocol":"evener-appwire-v6","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			if !started {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			// The started on-disk build is the host's own: it answers "oldsha".
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "systemctl start"):
			if order != nil {
				*order = append(*order, "launch")
			}
			started = true
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	if hook != nil {
		m.SetBootstrapHook(hook)
	}
	return m
}

// TestFirstAttachRunsTheBootstrapHookBeforeTheLaunch pins §6:131's ordering on
// the first-attach repair: the hub's first-contact caller runs before the
// launch's first mutating remote command, with the host row the ladder resolved.
func TestFirstAttachRunsTheBootstrapHookBeforeTheLaunch(t *testing.T) {
	var order []string
	m := stoppedHostManager(t, &order, func(_ context.Context, host hostreg.Host) error {
		if host.Name != "alpha" || host.SSH != "alpha.example" || host.Generation == 0 {
			t.Fatalf("the hook received host %+v, want the registry's resolved row", host)
		}
		order = append(order, "bootstrap")
		return nil
	})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil", err)
	}
	if len(order) != 2 || order[0] != "bootstrap" || order[1] != "launch" {
		t.Fatalf("order = %v, want the bootstrap before the launch", order)
	}
}

// TestFirstAttachRefusesTheLaunchWhenTheBootstrapRefuses pins §6:135/:137's
// fail-closed posture: a first-contact refusal (the typed helper-gate classes,
// or any other) starts nothing.
func TestFirstAttachRefusesTheLaunchWhenTheBootstrapRefuses(t *testing.T) {
	var order []string
	refusal := errors.New("host \"alpha\" refuses: fencing-helper-absent")
	m := stoppedHostManager(t, &order, func(context.Context, hostreg.Host) error { return refusal })
	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, refusal) {
		t.Fatalf("Ensure = %v, want the bootstrap refusal", err)
	}
	if len(order) != 0 {
		t.Fatalf("a refused bootstrap still launched: %v", order)
	}
}
