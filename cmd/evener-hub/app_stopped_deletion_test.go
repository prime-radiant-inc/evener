package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func confirmedStoppedDeletionFixture(t *testing.T) (hubcore.WebConfig, string) {
	t.Helper()
	id := hubtest.SessionID(t)
	locks := hubcore.NewResumeLocks()
	finish := locks.BeginForceStop([]string{id})
	if err := locks.PersistForceStop([]string{id}, id); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(id); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{
		RunDir: t.TempDir(), ResumeLocks: locks, DeletionStore: store,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			t.Error("deleted stopped target reached process control")
			return nil, errors.New("unexpected process control")
		}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			t.Error("deleted stopped target reached Resume launcher")
			return rendezvous.Entry{}, errors.New("unexpected launcher")
		}},
	}, id
}

func markStoppedTargetDeleted(t *testing.T, cfg hubcore.WebConfig, id string, state hubcore.DeletionState) {
	t.Helper()
	record, err := cfg.DeletionStore.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{Ref: "local:" + id, ThreadID: id}})
	if err != nil {
		t.Fatal(err)
	}
	if state == hubcore.DeletionStateDeleted {
		if err := cfg.DeletionStore.MarkDeleted(record.ProjectID, record.Generation); err != nil {
			t.Fatal(err)
		}
	}
}

func assertStoppedTargetDeletedError(t *testing.T, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("deleted stopped target error = %v, want CodeUnavailable", err)
	}
	dataJSON, marshalErr := json.Marshal(wire.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var data appwire.ErrorData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		t.Fatal(err)
	}
	if data.EvenerErrorInfo != appwire.ErrorActionUnavailable || data.MutationOutcome != appwire.MutationOutcomeTargetDeleted || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("deleted stopped target lost terminal refusal: %+v", data)
	}
}

// Exercise actual client/server serialization as well as the durable store.
// No rendezvous exists: the confirmed-stopped shortcut is the only success
// path, and it must not hide the same deletion refusal as ordinary actions.
func TestHubRPCConfirmedStoppedDeletedTargetRefused(t *testing.T) {
	for _, method := range []string{appwire.MethodEvenerThreadForceStop, appwire.MethodThreadShutdown} {
		for _, state := range []hubcore.DeletionState{hubcore.DeletionStateDeleting, hubcore.DeletionStateDeleted} {
			t.Run(method+"/"+string(state), func(t *testing.T) {
				cfg, id := confirmedStoppedDeletionFixture(t)
				hub := newHubRPCTestServer(t, cfg)
				defer hub.Close()
				client := dialHubRPC(t, hub)
				defer client.Close()
				if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
					t.Fatal(err)
				}
				// Do not let startup cleanup consume this fixture; deletion is
				// published by another request after the hub has started.
				markStoppedTargetDeleted(t, cfg, id, state)
				before := cfg.ResumeLocks.RecoveryState(id)
				var response appwire.EmptyResponse
				err := client.Request(t.Context(), method, map[string]string{"ref": "local:" + id}, &response)
				if method == appwire.MethodThreadShutdown {
					if after := cfg.ResumeLocks.RecoveryState(id); after != before {
						t.Errorf("shutdown changed recovery authority: before=%+v after=%+v", before, after)
					}
				}
				assertStoppedTargetDeletedError(t, err)
			})
		}
	}
}

// Publishing deletion while the RPC is parked under another owner's alias
// proves a pre-lock fast rejection cannot substitute for locked revalidation.
func TestHubRPCConfirmedStoppedDeletionRecheckedUnderLock(t *testing.T) {
	for _, method := range []string{appwire.MethodEvenerThreadForceStop, appwire.MethodThreadShutdown} {
		for _, state := range []hubcore.DeletionState{hubcore.DeletionStateDeleting, hubcore.DeletionStateDeleted} {
			t.Run(method+"/"+string(state), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					cfg, id := confirmedStoppedDeletionFixture(t)
					lock := cfg.ResumeLocks.For(id)
					lock.Lock()
					release := sync.OnceFunc(lock.Unlock)
					defer release()
					server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
					registerThreadHandlers(server, cfg, newHubSourceRegistry(cfg), hubRelayFunctions{}, nil)
					params, err := json.Marshal(map[string]string{"ref": "local:" + id})
					if err != nil {
						t.Fatal(err)
					}
					before := cfg.ResumeLocks.RecoveryState(id)
					completed := make(chan error, 1)
					go func() {
						_, err := server.Router().Dispatch(t.Context(), appwire.Request{Method: method, Params: params})
						completed <- err
					}()
					synctest.Wait()
					select {
					case err := <-completed:
						t.Fatalf("stopped proof did not wait for alias ownership: %v", err)
					default:
					}
					markStoppedTargetDeleted(t, cfg, id, state)
					release()
					err = <-completed
					if method == appwire.MethodThreadShutdown {
						if after := cfg.ResumeLocks.RecoveryState(id); after != before {
							t.Errorf("shutdown changed recovery authority: before=%+v after=%+v", before, after)
						}
					}
					assertStoppedTargetDeletedError(t, err)
				})
			})
		}
	}
}
