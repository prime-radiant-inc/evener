package sshconn

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

func TestSelectedRestartRegistrationCheck(t *testing.T) {
	t.Parallel()
	host := hostreg.Host{Name: "selected", SSH: "dev@remote", ConfigPath: "/srv/selected.toml", Generation: 3}
	if err := checkSelectedRestartRegistration(host, host); err != nil {
		t.Fatalf("unchanged registration refused: %v", err)
	}
	changed := host
	changed.ConfigPath = "/srv/other.toml"
	if err := checkSelectedRestartRegistration(host, changed); !errors.Is(err, errOwnerEvidence) {
		t.Fatalf("changed registration = %v", err)
	}
}

func TestWaitSelectedSuccessorArms(t *testing.T) {
	t.Parallel()
	host, commands := ownerCommands(t)
	pending := pendingRestartState{selected: &selectedOwner{host: host, pid: "9999", generation: "other-birth"}, expectedVersion: "release-selected"}
	facts := selectedRestartFacts()

	t.Run("wait failure surfaces after a non-successor observation", func(t *testing.T) {
		t.Parallel()
		waitFailure := errors.New("wait aborted")
		fr, index := ownerRunner(t, commands)
		m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error {
			return waitFailure
		}})
		if err := m.waitSelectedSuccessor(t.Context(), host, facts, pending); !errors.Is(err, waitFailure) {
			t.Fatalf("wait failure = %v", err)
		}
		if *index != len(commands) {
			t.Fatalf("observed the host with %d/%d commands", *index, len(commands))
		}
	})
	t.Run("caller cancellation after the observation wins", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		fr, index := ownerRunner(t, commands)
		base := fr.runFn
		fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			if *index == len(commands)-1 {
				cancel()
			}
			return base(ctx, argv, stdin)
		}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.waitSelectedSuccessor(ctx, host, facts, pending); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
		if *index != len(commands) {
			t.Fatalf("observed the host with %d/%d commands", *index, len(commands))
		}
	})
	t.Run("exhausted health attempts report the observed versions", func(t *testing.T) {
		t.Parallel()
		linkFailure := errors.New("ssh link died")
		fr, _ := failingOwnerRunner(t, nil, "hub endpoint --json", linkFailure)
		waits := 0
		m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error {
			waits++
			return nil
		}})
		err := m.waitSelectedSuccessor(t.Context(), host, facts, pending)
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("exhaustion = %v, want ErrRestart", err)
		}
		if waits != restartHealthAttempts {
			t.Fatalf("waits = %d, want %d", waits, restartHealthAttempts)
		}
		if len(fr.recordedStarts()) != 0 {
			t.Fatal("health wait started a process")
		}
	})
}

func TestFreshSelectedOwnerArms(t *testing.T) {
	t.Parallel()
	report := hubendpoint.Report{ConfigPath: "/srv/selected.toml", HubStateRoot: "/srv/state"}

	t.Run("invalid explicit listener is refused before any command", func(t *testing.T) {
		t.Parallel()
		host := hostreg.Host{Name: "selected", SSH: "dev@remote", Addr: "not-a-listen-addr"}
		fr, index := ownerRunner(t, nil)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if _, err := m.freshSelectedOwner(t.Context(), host, report); !errors.Is(err, ErrHostAddr) {
			t.Fatalf("listener refusal = %v", err)
		}
		if *index != 0 {
			t.Fatalf("listener refusal issued %d commands", *index)
		}
	})
	t.Run("pwd read failure surfaces", func(t *testing.T) {
		t.Parallel()
		host, _ := ownerCommands(t)
		linkFailure := errors.New("ssh link died")
		fr, _ := failingOwnerRunner(t, nil, "pwd", linkFailure)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if _, err := m.freshSelectedOwner(t.Context(), host, report); !errors.Is(err, linkFailure) {
			t.Fatalf("pwd failure = %v", err)
		}
	})
	t.Run("ambiguous pwd output is refused", func(t *testing.T) {
		t.Parallel()
		host, _ := ownerCommands(t)
		commands := []ownerCommand{{"pwd", "one\ntwo\n"}}
		fr, index := ownerRunner(t, commands)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if _, err := m.freshSelectedOwner(t.Context(), host, report); !errors.Is(err, errOwnerEvidence) {
			t.Fatalf("ambiguous pwd = %v", err)
		}
		if *index != len(commands) {
			t.Fatalf("ambiguous pwd consumed %d/%d commands", *index, len(commands))
		}
	})
	t.Run("environment read failure surfaces", func(t *testing.T) {
		t.Parallel()
		host, _ := ownerCommands(t)
		linkFailure := errors.New("ssh link died")
		fr, _ := failingOwnerRunner(t, []ownerCommand{{"pwd", "/home/dev\n"}}, "EVENER_HUB_LISTEN_ADDR", linkFailure)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if _, err := m.freshSelectedOwner(t.Context(), host, report); !errors.Is(err, linkFailure) {
			t.Fatalf("environment read failure = %v", err)
		}
	})
	t.Run("ambiguous environment is refused", func(t *testing.T) {
		t.Parallel()
		host, _ := ownerCommands(t)
		commands := []ownerCommand{{"pwd", "/home/dev\n"}, {freshOwnerEnvironmentRemote, "JUNK\x00"}}
		fr, index := ownerRunner(t, commands)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if _, err := m.freshSelectedOwner(t.Context(), host, report); !errors.Is(err, errOwnerEvidence) {
			t.Fatalf("ambiguous environment = %v", err)
		}
		if *index != len(commands) {
			t.Fatalf("ambiguous environment consumed %d/%d commands", *index, len(commands))
		}
	})
}

func TestRevalidateStoppedServiceListingFailure(t *testing.T) {
	t.Parallel()
	host, _ := ownerCommands(t)
	owner := selectedOwner{host: host, supervisor: supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}, report: hubendpoint.Report{}}
	linkFailure := errors.New("ssh link died")
	fr, _ := failingOwnerRunner(t, nil, "systemctl list-units", linkFailure)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if err := m.revalidateStoppedService(t.Context(), host, selectedRestartFacts(), owner); !errors.Is(err, linkFailure) {
		t.Fatalf("listing failure = %v", err)
	}
}
