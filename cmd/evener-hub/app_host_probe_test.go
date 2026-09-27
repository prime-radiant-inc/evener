package hub

// The gated probe primitive's tests (deploy pipeline 08b §6 step 2, §10, §12's
// "The running probe" row): the probe's success shape, its handler-absent
// classification for a remote that predates the method, its unauthenticated
// refusal, and its deadline. They drive the primitive against a real
// sshconn.Manager attached to an in-process AppWire bridge, so the call path is
// the production one — a live channel's client, no dial, no preflight — and the
// only stubbed layer is ssh itself.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// probeStream presents the fake bridge's pipes as the single
// io.ReadWriteCloser an AppWire stream transport needs: it reads what the
// controller wrote and writes what the controller reads.
type probeStream struct {
	in  io.WriteCloser
	out io.ReadCloser
}

func (s *probeStream) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *probeStream) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *probeStream) Close() error {
	_ = s.in.Close()
	return s.out.Close()
}

// probeFakeStdio is an in-memory sshconn.Stdio: the bridge server reads stdin
// and writes stdout, and the controller reads the pipe's other ends.
type probeFakeStdio struct {
	inW  *io.PipeWriter
	outR *io.PipeReader
	inR  *io.PipeReader
	outW *io.PipeWriter

	killOnce sync.Once
	waitDone chan struct{}
}

func (s *probeFakeStdio) Stdin() io.WriteCloser { return s.inW }
func (s *probeFakeStdio) Stdout() io.ReadCloser { return s.outR }
func (s *probeFakeStdio) Kill() error {
	s.killOnce.Do(func() {
		_ = s.inW.Close()
		_ = s.outR.Close()
		_ = s.outW.Close()
		_ = s.inR.Close()
		close(s.waitDone)
	})
	return nil
}
func (s *probeFakeStdio) Wait() error {
	<-s.waitDone
	return nil
}

// probeFakeRunner is the sshconn.Runner seam: Run answers the attach preflight,
// and Start hands back one in-process bridge.
type probeFakeRunner struct {
	stdio *probeFakeStdio
}

const probeGoodLaunchCheck = `{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`

func (r *probeFakeRunner) Run(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
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
	case strings.Contains(joined, "launch-check"):
		return []byte(probeGoodLaunchCheck), nil
	case strings.Contains(joined, "api/health"):
		return []byte(`{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	default:
		return nil, errors.New("unexpected remote command: " + joined)
	}
}

func (r *probeFakeRunner) Start(context.Context, []string, io.Writer) (sshconn.Stdio, error) {
	return r.stdio, nil
}

// probeBridge answers AppWire frames the way a host hub's router would: the
// initialize handshake, then evener/host/running through answer (nil answer
// means the method is not served — the pre-handler remote).
func probeBridge(answer func(appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError)) *probeFakeStdio {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stdio := &probeFakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})}
	go func() {
		server := appwire.NewStreamTransport(&probeStream{in: outW, out: inR})
		defer func() { _ = server.Close() }()
		for {
			msg, err := server.Recv(context.Background())
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			var response appwire.Message
			switch msg.Request.Method {
			case appwire.MethodInitialize:
				response = appwire.ResponseMessage(msg.Request.ID, appwire.InitializeResponse{
					ProtocolVersion: appwire.ProtocolVersion,
					SourceID:        "local",
				})
			case appwire.MethodEvenerHostRunning:
				if answer == nil {
					response = appwire.ErrorMessage(msg.Request.ID, appwire.MethodNotFound(msg.Request.Method))
					break
				}
				var params appwire.HostRunningParams
				if err := json.Unmarshal(msg.Request.Params, &params); err != nil {
					response = appwire.ErrorMessage(msg.Request.ID, appwire.InvalidParams(err.Error()))
					break
				}
				result, wireErr := answer(params)
				if wireErr.Code != 0 {
					response = appwire.ErrorMessage(msg.Request.ID, wireErr)
					break
				}
				response = appwire.ResponseMessage(msg.Request.ID, result)
			default:
				response = appwire.ErrorMessage(msg.Request.ID, appwire.MethodNotFound(msg.Request.Method))
			}
			if err := server.Send(context.Background(), response); err != nil {
				return
			}
		}
	}()
	return stdio
}

// probeTestManager attaches one host through a real Manager against the fake
// bridge and returns the manager, the entry, and the attached client.
func probeTestManager(t *testing.T, answer func(appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError)) (*sshconn.Manager, hostreg.Host, *appwire.Client) {
	t.Helper()
	host := hostreg.Host{Name: "m4", SSH: "m4.example"}
	registry, err := hostreg.New([]hostreg.Host{host})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	runner := &probeFakeRunner{stdio: probeBridge(answer)}
	manager := sshconn.New(registry, sshconn.Options{Runner: runner, Stderr: io.Discard})
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := manager.Ensure(ctx, "m4"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	channel, ok := manager.ChannelIfAttached("m4")
	if !ok {
		t.Fatal("the host did not attach")
	}
	entry, _ := registry.Get("m4")
	return manager, entry, channel.Client()
}

// TestProbeHostRunningServesTheRunningShape pins the probe's success path: the
// response's buildRevision and healthy flag become the probe value, and a
// carried processStartTime is parsed as its RFC3339 instant.
func TestProbeHostRunningServesTheRunningShape(t *testing.T) {
	manager, host, client := probeTestManager(t, func(params appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError) {
		if params.FencingEpoch.BootID != "boot-1" || params.FencingEpoch.OpSeq != 4 {
			t.Errorf("the probe presented %+v, want boot-1/4", params.FencingEpoch)
		}
		return appwire.HostRunningResponse{
			BuildRevision:    "v1.2.3",
			Healthy:          true,
			ProcessStartTime: "2026-09-27T12:00:00Z",
		}, appwire.WireError{}
	})
	probe, err := probeHostRunning(context.Background(), manager, host, client,
		appwire.FencingEpoch{BootID: "boot-1", OpSeq: 4}, time.Second)
	if err != nil {
		t.Fatalf("probeHostRunning: %v", err)
	}
	if probe.Version != "v1.2.3" || !probe.RunningHealthy {
		t.Fatalf("probe = %+v, want the served revision and health", probe)
	}
	if probe.ProcessStartTime == nil || probe.ProcessStartTime.Format(time.RFC3339) != "2026-09-27T12:00:00Z" {
		t.Fatalf("probe processStartTime = %v, want the served instant", probe.ProcessStartTime)
	}
}

// TestProbeHostRunningClassifiesHandlerAbsent pins §6 step 2's one distinct
// classification: a remote whose hub predates the method answers
// method-not-found, and the probe reports the `handler-absent` arm
// (PlanHandlerAbsentError), never a generic probe failure.
func TestProbeHostRunningClassifiesHandlerAbsent(t *testing.T) {
	manager, host, client := probeTestManager(t, nil)
	_, err := probeHostRunning(context.Background(), manager, host, client,
		appwire.FencingEpoch{BootID: "boot-1", OpSeq: 1}, time.Second)
	if !errors.Is(err, errPlanHandlerAbsent) {
		t.Fatalf("probe error = %v, want the handler-absent classification", err)
	}
}

// TestProbeHostRunningReportsAnUnauthenticatedRefusal pins the unauthenticated
// arm: the serving hub's admission refusal (unavailable class) reaches the plan
// as a probe failure whose message names the unauthenticated probe, never as
// handler-absent.
func TestProbeHostRunningReportsAnUnauthenticatedRefusal(t *testing.T) {
	manager, host, client := probeTestManager(t, func(appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError) {
		return appwire.HostRunningResponse{}, appwire.Unavailable("evener/host/running: unauthenticated probe: served only over an attached controller session")
	})
	_, err := probeHostRunning(context.Background(), manager, host, client,
		appwire.FencingEpoch{BootID: "boot-1", OpSeq: 1}, time.Second)
	if err == nil {
		t.Fatal("the probe succeeded against an unauthenticated refusal")
	}
	if errors.Is(err, errPlanHandlerAbsent) {
		t.Fatalf("an unauthenticated refusal was classified handler-absent: %v", err)
	}
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Fatalf("probe error %q does not name the unauthenticated refusal", err)
	}
}

// TestProbeHostRunningTimesOut pins the deadline: a remote that answers the
// handshake but never the probe is abandoned at the probe timeout, with the
// timeout named in the refusal.
func TestProbeHostRunningTimesOut(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	manager, host, client := probeTestManager(t, func(appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError) {
		<-blocked
		return appwire.HostRunningResponse{}, appwire.WireError{}
	})
	start := time.Now()
	_, err := probeHostRunning(context.Background(), manager, host, client,
		appwire.FencingEpoch{BootID: "boot-1", OpSeq: 1}, 100*time.Millisecond)
	if err == nil {
		t.Fatal("the probe succeeded against a hung remote")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("probe error %q does not name the timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the probe took %s to time out, want the 100ms deadline", elapsed)
	}
}

// TestIsRunningHandlerAbsentKeepsOtherWireErrorsOut pins the classification
// boundary: only method-not-found is handler-absent.
func TestIsRunningHandlerAbsentKeepsOtherWireErrorsOut(t *testing.T) {
	if !isRunningHandlerAbsent(appwire.MethodNotFound(appwire.MethodEvenerHostRunning)) {
		t.Fatal("a method-not-found answer was not classified handler-absent")
	}
	for _, other := range []error{
		appwire.Unavailable("no"),
		appwire.InvalidParams("no"),
		errors.New("transport reset"),
	} {
		if isRunningHandlerAbsent(other) {
			t.Fatalf("%v was classified handler-absent", other)
		}
	}
}
