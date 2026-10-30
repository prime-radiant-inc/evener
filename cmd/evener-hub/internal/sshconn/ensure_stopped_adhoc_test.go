package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// The Manager operation, pending state, Ensure decision, recorder gate and
// selected recovery are real. SSH process/service reports and the recorder's
// durable-store boundary are fixtures, no remote process is actually launched.
func TestEnsureRetainedStoppedAdhocStart(t *testing.T) {
	for _, mode := range []string{"no-pending", "failed-explicit-start", "unwired-recorder", "completed-successor", "recorded-bootstrap-command"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "fixture.invalid", EvenerPath: "/opt/evener/bin/evener"}
			launches, attaches, recorded, finished := 0, 0, 0, 0
			attachBoundary := errors.New("fixture attach boundary")
			launch := "CDPATH='' cd -P -- /home/dev || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; nohup /opt/evener/bin/evener hub </dev/null >/dev/null 2>&1 &"
			base := cannedRun(nil)
			var f *selectedProcessFixture
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				if strings.Join(argv[12:], " ") == launch {
					launches++
					if launches > 1 && recorded != 1 {
						t.Fatal("background mutation preceded recorder")
					}
					if mode == "completed-successor" {
						f.stopped, f.mutated = false, true
					}
					return nil, errReplacementFixture
				}
				return base(ctx, argv, in)
			}, startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
				attaches++
				return nil, attachBoundary
			}}
			f = modelSelectedProcess(t, fr, host.EvenerPath, "dev", "dev")
			f.stopped, f.adhoc = true, true
			m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error { return nil }})
			host, _ = m.reg.Get(host.Name)
			facts := selectedRestartFacts()
			facts.Version = "dev"
			if mode != "unwired-recorder" {
				m.SetEnsureRestartHook(func(hostreg.Host) (func(error), error) {
					recorded++
					return func(err error) {
						finished++
						if mode == "completed-successor" {
							if err != nil {
								t.Errorf("successor reconciliation: %v", err)
							}
						} else if !errors.Is(err, errReplacementFixture) {
							t.Errorf("recovery failure lost: %v", err)
						}
					}, nil
				})
			}
			if mode == "no-pending" {
				deploy, restart := m.ensureDecision(host.Name, facts, false, false)
				_, err := m.ensureOnce(t.Context(), host, false)
				if deploy || restart || !errors.Is(err, attachBoundary) || launches != 0 || attaches != 1 || recorded != 0 || m.pendingRestart(host.Name).selected != nil {
					t.Fatalf("unrequested background start: deploy=%t restart=%t launches=%d attaches=%d recorded=%d err=%v", deploy, restart, launches, attaches, recorded, err)
				}
				return
			}
			// RestartForOperation assumes caller admission and gate ownership, as
			// operations.go documents. Only that outer durable admission is omitted.
			var err error
			if mode == "recorded-bootstrap-command" {
				err = m.bootstrapHub(t.Context(), host, facts, "dev", "")
			} else {
				_, err = m.RestartForOperation(t.Context(), host, facts, nil)
			}
			if !errors.Is(err, errReplacementFixture) || launches != 1 {
				t.Fatalf("explicit first start: launches=%d err=%v", launches, err)
			}
			pending := m.pendingRestart(host.Name)
			if pending.selected == nil || (mode == "recorded-bootstrap-command" && pending.command == "") {
				t.Fatalf("missing retained intent: %+v", pending)
			}
			deploy, restart := m.ensureDecision(host.Name, facts, false, false)
			if deploy || !restart {
				t.Errorf("retained selection missed: command-present=%t deploy=%t restart=%t", pending.command != "", deploy, restart)
			}
			_, err = m.ensureOnce(t.Context(), host, false)
			switch mode {
			case "unwired-recorder":
				if err == nil || errors.Is(err, attachBoundary) || launches != 1 || attaches != 0 || m.pendingRestart(host.Name).selected == nil {
					t.Fatalf("recorder gate bypassed: launches=%d attaches=%d err=%v", launches, attaches, err)
				}
			case "completed-successor":
				if !errors.Is(err, attachBoundary) || launches != 1 || attaches != 1 || recorded != 1 || finished != 1 || m.pendingRestart(host.Name).selected != nil {
					t.Fatalf("successor relaunched or unreconciled: launches=%d attaches=%d recorded=%d finished=%d pending=%+v err=%v", launches, attaches, recorded, finished, m.pendingRestart(host.Name), err)
				}
			default:
				if !errors.Is(err, errReplacementFixture) || launches != 2 || attaches != 0 || recorded != 1 || finished != 1 || m.pendingRestart(host.Name).selected == nil {
					t.Fatalf("retained recovery not retried: launches=%d attaches=%d recorded=%d finished=%d err=%v", launches, attaches, recorded, finished, err)
				}
			}
		})
	}
}
