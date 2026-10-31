package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// A separate Wait gate models the process and stderr copier still being reaped
// after Kill has closed its pipes. Observing Kill alone cannot prove teardown.
type sessionToolsPeerStdio struct {
	*fakeStdio
	waitEntered chan struct{}
	waitRelease chan struct{}
	waitOnce    sync.Once
}

func (s *sessionToolsPeerStdio) Wait() error {
	err := s.fakeStdio.Wait()
	s.waitOnce.Do(func() { close(s.waitEntered) })
	<-s.waitRelease
	return err
}

func TestManagerSessionToolsPeerBeforePublication(t *testing.T) {
	for _, outcome := range []string{"accepted", "refused", "closed"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			stdio := &sessionToolsPeerStdio{
				fakeStdio:   &fakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})},
				waitEntered: make(chan struct{}), waitRelease: make(chan struct{}),
			}
			var releaseOnce sync.Once
			releaseWait := func() { releaseOnce.Do(func() { close(stdio.waitRelease) }) }
			t.Cleanup(releaseWait)
			identify := make(chan appwire.Request, 1)
			answer := make(chan bool, 1)
			go func() {
				peer := appwire.NewStreamTransport(&stdioReadWriter{in: outW, out: inR})
				for {
					msg, err := peer.Recv(ctx)
					if err != nil {
						return
					}
					if msg.Request == nil {
						continue
					}
					request := *msg.Request
					switch request.Method {
					case appwire.MethodInitialize:
						if err := peer.Send(ctx, appwire.ResponseMessage(request.ID, appwire.InitializeResponse{
							ProtocolVersion: appwire.ProtocolVersion, SourceID: "local", Features: appwire.FeatureSet{SessionTools: true},
						})); err != nil {
							return
						}
					case appwire.MethodHubSessionToolsIdentify:
						identify <- request
						select {
						case accepted := <-answer:
							response := appwire.ResponseMessage(request.ID, appwire.SessionToolsPeerResponse{HubID: "remote-hub"})
							if !accepted {
								response = appwire.ErrorMessage(request.ID, appwire.InvalidRequest("peer refused"))
							}
							if err := peer.Send(ctx, response); err != nil {
								return
							}
						case <-stdio.waitDone:
							return
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			var attached atomic.Int32
			runner := &fakeRunner{runFn: cannedRun(nil), startFn: func(context.Context, []string, io.Writer) (Stdio, error) { return stdio, nil }}
			manager := newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}), runner, Options{
				SessionToolsHubID: "configured-hub", DeployDisabled: true,
				OnEvent: func(event Event) {
					if event.Kind == EventAttached {
						attached.Add(1)
					}
				},
			})
			// Release Wait before the registered manager cleanup, including a
			// failed assertion while identification is pending.
			t.Cleanup(releaseWait)
			type result struct {
				channel *Channel
				err     error
			}
			results := make(chan result, 1)
			go func() { ch, err := manager.Ensure(ctx, "alpha"); results <- result{ch, err} }()
			var request appwire.Request
			select {
			case request = <-identify:
			case result := <-results:
				t.Fatalf("Ensure published or returned before peer identification: channel=%v err=%v", result.channel, result.err)
			case <-ctx.Done():
				t.Fatal("peer identification not requested:", ctx.Err())
			}
			var params appwire.SessionToolsPeerParams
			if err := json.Unmarshal(request.Params, &params); err != nil || params.HubID != "configured-hub" {
				t.Fatalf("identify params=%+v err=%v", params, err)
			}
			if manager.Attached("alpha") || attached.Load() != 0 {
				t.Fatal("channel or Attached event published before peer identification completed")
			}
			select {
			case result := <-results:
				t.Fatalf("Ensure returned before peer answered: %+v", result)
			default:
			}
			closed := make(chan error, 1)
			if outcome == "closed" {
				go func() { closed <- manager.Close() }()
			} else {
				answer <- outcome == "accepted"
			}
			if outcome != "accepted" {
				select {
				case <-stdio.waitEntered:
				case <-ctx.Done():
					t.Fatal("failed identification did not reap bridge:", ctx.Err())
				}
				select {
				case result := <-results:
					t.Fatalf("Ensure returned before Stdio.Wait completed: %+v", result)
				default:
				}
				select {
				case err := <-closed:
					t.Fatalf("Close returned before Stdio.Wait completed: %v", err)
				default:
				}
			}
			releaseWait()
			var got result
			select {
			case got = <-results:
			case <-ctx.Done():
				t.Fatal("Ensure did not finish:", ctx.Err())
			}
			if outcome == "accepted" {
				if got.err != nil || got.channel == nil || !manager.Attached("alpha") || attached.Load() != 1 {
					t.Fatalf("accepted peer did not publish exactly once: result=%+v attached=%d", got, attached.Load())
				}
			} else if got.err == nil || got.channel != nil || manager.Attached("alpha") || attached.Load() != 0 {
				t.Fatalf("failed identification published a channel: result=%+v attached=%d", got, attached.Load())
			}
			if outcome == "closed" {
				if !errors.Is(got.err, ErrManagerClosed) {
					t.Fatalf("close during identification=%v, want ErrManagerClosed", got.err)
				}
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("Close did not join cleanup:", ctx.Err())
				}
			}
		})
	}
}
