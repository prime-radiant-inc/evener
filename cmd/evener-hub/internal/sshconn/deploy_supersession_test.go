package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestExplicitDeploymentSupersessionBoundary(t *testing.T) {
	for _, mode := range []string{"valid", "renewed-binding", "validation-failed", "validation-at-write", "owner-at-write", "note-failed", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			fresh, boundary := false, false
			writes, builds, attempts, notes := 0, 0, 0, 0
			refused := errors.New("new binding refused")
			appendFailed := errors.New("journal append failed")
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract("newsha"), nil }, func(int) ([]byte, error) {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.Contains(remote, "cat >") {
					writes++
					if !fresh {
						return nil, errors.New("interrupted prior write")
					}
				}
				out, err := base(ctx, argv, in)
				if boundary && mode == "owner-at-write" && strings.Contains(remote, " hub endpoint --json") {
					return []byte(strings.ReplaceAll(string(out), "/home/dev/.local/state/evener", "/srv/changed-state")), err
				}
				return out, err
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(ctx context.Context, goos, goarch, path string) error {
				builds++
				boundary = fresh
				return writeStageBinary(ctx, goos, goarch, path)
			}})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			oldGuard := func() error {
				if fresh {
					return errors.New("prior selection changed")
				}
				return nil
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, oldGuard, &DeploymentJournal{OperationID: "prior-operation"}); err == nil {
				t.Fatal("interrupted prior write succeeded")
			}
			if mode == "renewed-binding" {
				if _, err := m.renewRestartBinding(host, oldGuard); err != nil {
					t.Fatal(err)
				}
			}
			prior := m.pendingRestart(host.Name)
			if prior.binding == nil || prior.binding.operationID != "prior-operation" || prior.selected == nil || writes != 1 {
				t.Fatalf("missing correlated prior intent: writes=%d pending=%+v", writes, prior)
			}
			fresh = true
			guard := func() error {
				if mode == "validation-failed" || (boundary && mode == "validation-at-write") {
					return refused
				}
				return nil
			}
			journal := DeploymentJournal{OperationID: "new-operation", Supersede: func(priorID string) error {
				attempts++
				if priorID != "prior-operation" || writes != 1 || !reflect.DeepEqual(m.pendingRestart(host.Name), prior) {
					t.Fatalf("note not before replacement/write: prior=%s writes=%d pending=%+v", priorID, writes, m.pendingRestart(host.Name))
				}
				if mode == "note-failed" {
					return appendFailed
				}
				notes++
				return nil
			}}
			if mode == "ordinary" {
				_, _, err = m.DeployForOperation(t.Context(), host, facts, true, guard, nil)
			} else {
				_, _, err = m.DeployForOperation(t.Context(), host, facts, true, guard, &journal)
			}
			if mode == "valid" || mode == "renewed-binding" {
				pending := m.pendingRestart(host.Name)
				if err != nil || builds != 2 || writes != 2 || attempts != 1 || notes != 1 || pending.binding == nil || pending.binding.operationID != "new-operation" || !pending.writeAcknowledged {
					t.Fatalf("supersession: err=%v builds=%d writes=%d attempts=%d notes=%d pending=%+v", err, builds, writes, attempts, notes, pending)
				}
			} else {
				if err == nil || writes != 1 || notes != 0 || !reflect.DeepEqual(m.pendingRestart(host.Name), prior) {
					t.Fatalf("refusal lost prior intent: err=%v writes=%d notes=%d pending=%+v", err, writes, notes, m.pendingRestart(host.Name))
				}
				wantAttempts := 0
				if mode == "note-failed" {
					wantAttempts = 1
					if !errors.Is(err, appendFailed) {
						t.Fatalf("lost note failure: %v", err)
					}
				}
				if attempts != wantAttempts {
					t.Fatalf("note attempts=%d want=%d", attempts, wantAttempts)
				}
			}
		})
	}
}
