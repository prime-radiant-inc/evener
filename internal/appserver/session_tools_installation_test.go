package appserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
)

func installingSessionTools(t *testing.T, installErr error) (*Connection, appwire.SessionHubClient, func(), <-chan appwire.Message) {
	t.Helper()
	server := NewServer(ServerConfig{})
	published := make(chan appwire.SessionHubClient, 1)
	release := make(chan struct{})
	releaseInstall := sync.OnceFunc(func() { close(release) })
	server.InstallSessionTools(func(_ context.Context, _ appwire.SessionBinding, client appwire.SessionHubClient) error {
		published <- client
		<-release
		return installErr
	})
	conn := server.NewConnection("installing")
	conn.sessionLifetime = t.Context()
	conn.setInitialized()
	bound := make(chan appwire.Message, 1)
	go func() {
		request := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
			BindingID: "binding", Ref: "local:workspace", InstanceID: "instance", HubID: "hub",
		})
		bound <- conn.bindSessionTools(t.Context(), *request.Request)
	}()
	return conn, <-published, releaseInstall, bound
}

func assertNoSessionToolsOutput(t *testing.T, conn *Connection) {
	t.Helper()
	select {
	case frame := <-conn.send:
		t.Fatalf("unexpected session tools output: %+v", frame)
	default:
	}
}

func TestSessionToolsCallsAwaitInstallation(t *testing.T) {
	for _, outcome := range []string{"commit", "refuse", "close", "cancel-caller"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var installErr error
				if outcome == "refuse" {
					installErr = appwire.Unavailable("installation refused")
				}
				conn, client, releaseInstall, bound := installingSessionTools(t, installErr)
				defer releaseInstall()
				defer conn.closeSessionTools()
				callCtx, stopCall := context.WithCancel(t.Context())
				defer stopCall()
				type stateResult struct {
					view appwire.ManagerView
					err  error
				}
				result := make(chan stateResult, 1)
				go func() {
					view, err := client.State(callCtx)
					result <- stateResult{view, err}
				}()
				// Both goroutines have reached durable waits. A request emitted now
				// could receive its reply before the binding is routable and lose it.
				synctest.Wait()
				assertNoSessionToolsOutput(t, conn)
				select {
				case got := <-result:
					t.Fatalf("state call returned before installation settled: %+v", got)
				default:
				}
				if conn.sessionTools != nil || conn.receiveSessionFrame(appwire.ResponseMessage(appwire.NewIntID(-99), appwire.EmptyResponse{})) {
					t.Fatal("unfinished installation became routable")
				}
				if outcome == "close" {
					conn.closeSessionTools()
				}
				if outcome == "cancel-caller" {
					stopCall()
					if got := <-result; !errors.Is(got.err, context.Canceled) {
						t.Fatalf("cancelled waiting call = %+v", got)
					}
				}
				releaseInstall()
				response := <-bound
				switch outcome {
				case "commit", "cancel-caller":
					if response.Error != nil {
						t.Fatal(response.Error)
					}
					if outcome == "cancel-caller" {
						go func() {
							view, err := client.State(t.Context())
							result <- stateResult{view, err}
						}()
					}
					request := <-conn.send
					if request.Request == nil || request.Request.Method != appwire.MethodRuntimeSessionState {
						t.Fatalf("unexpected reverse request: %+v", request)
					}
					if !conn.receiveSessionFrame(appwire.ResponseMessage(request.Request.ID, appwire.ManagerView{Ref: "local:workspace", Manager: true})) {
						t.Fatal("installed binding dropped the state response")
					}
					if got := <-result; got.err != nil || got.view.Ref != "local:workspace" || !got.view.Manager {
						t.Fatalf("state after installation = %+v", got)
					}
				default:
					wantCode := appwire.CodeUnavailable
					if outcome == "close" {
						wantCode = appwire.CodeConflict
					}
					if response.Error == nil || response.Error.Error.Code != wantCode {
						t.Fatalf("binding result = %+v, want code %v", response, wantCode)
					}
					got := <-result
					var wireErr appwire.WireError
					if !errors.As(got.err, &wireErr) || wireErr.Code != appwire.CodeUnavailable {
						t.Fatalf("abandoned binding call = %+v", got)
					}
				}
				synctest.Wait()
				assertNoSessionToolsOutput(t, conn)
			})
		})
	}
}

func TestSessionToolsCancelledWatchDuringInstallation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn, client, releaseInstall, bound := installingSessionTools(t, nil)
		defer releaseInstall()
		defer conn.closeSessionTools()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := client.Watch(ctx, appwire.SessionWatchRequest{Ref: "local:workspace"})
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled watch = %v", err)
		}
		if n := len(client.(*sessionToolsConnection).watches); n != 0 {
			t.Fatalf("cancelled watch retained %d consumers", n)
		}
		assertNoSessionToolsOutput(t, conn)
		releaseInstall()
		if response := <-bound; response.Error != nil {
			t.Fatal(response.Error)
		}
		synctest.Wait()
		assertNoSessionToolsOutput(t, conn)
	})
}
