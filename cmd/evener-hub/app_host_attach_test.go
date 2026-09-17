package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

func hostAttachRegistry(t *testing.T, cfg hubcore.WebConfig) *hostreg.Registry {
	t.Helper()
	hosts, err := hostreg.New(cfg.RemoteHosts)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	return hosts
}

// An explicit attach dials exactly once through the Ensure-backed seam and
// returns the host's post-attach facts, so the row can flip online without a
// second probe.
func TestHostAttachDialsOnceAndReturnsFacts(t *testing.T) {
	var dials atomic.Int64
	live := &appwire.Client{}
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return live, nil
		},
		RemoteHostFacts: func(_ context.Context, host string, client *appwire.Client) (appsource.HostFacts, error) {
			if host != "alpha" || client != live {
				return appsource.HostFacts{}, fmt.Errorf("facts asked for host %q client %p", host, client)
			}
			return appsource.HostFacts{
				ProtocolVersion: "1",
				HubVersion:      "9.9.9",
				OS:              "linux",
				Arch:            "amd64",
				Features:        appwire.FeatureSet{ThreadList: true},
			}, nil
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Name: "alpha"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !resp.Attached {
		t.Fatal("attach response did not report the host attached")
	}
	if resp.ProtocolVersion != "1" || resp.HubVersion != "9.9.9" || resp.OS != "linux" || resp.Arch != "amd64" || resp.Features == nil || !resp.Features.ThreadList {
		t.Fatalf("attach facts = %+v, want the host's post-attach state", resp)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("attach dialed %d times, want 1", got)
	}
}

// Attaching is idempotent: a repeated Connect is safe, so an already-attached
// host returns its state rather than an error.
func TestHostAttachIsIdempotent(t *testing.T) {
	live := &appwire.Client{}
	var dials atomic.Int64
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return live, nil
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	for i := range 2 {
		resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Name: "alpha"})
		if err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		if !resp.Attached {
			t.Fatalf("attach %d did not report attached", i)
		}
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want one per call (Ensure is idempotent, the handler is not cached)", got)
	}
}

// An unknown host is refused before any dial: there is nothing to attach.
func TestHostAttachUnknownHostIsInvalidParams(t *testing.T) {
	var dials atomic.Int64
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return &appwire.Client{}, nil
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	_, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Name: "ghost"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("unknown-host attach error %T=%v, want WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("unknown-host attach wire=%+v, want invalid params", wire)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("unknown host dialed %d times, want 0", got)
	}
}

// A hub with no dialing seam cannot attach anything; it reports unavailable
// rather than panicking.
func TestHostAttachWithoutDialSeamIsUnavailable(t *testing.T) {
	cfg := hubcore.WebConfig{RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}}}
	hosts := hostAttachRegistry(t, cfg)

	_, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Name: "alpha"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("seamless attach error %T=%v, want WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("seamless attach wire=%+v, want unavailable", wire)
	}
}

// An attach failure is classified through the host's own source exactly as its
// call path classifies a connect failure: sshconn's transient attach failure
// and the deadline chain reach the caller as the typed SessionUnavailable the
// auto-resume/refusal gates match, naming the host.
func TestHostAttachClassifiesAttachFailureAsSessionUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attachErr error
	}{
		{
			name:      "ssh start chain",
			attachErr: fmt.Errorf("%w: host %q link dropped before the channel was usable", sshconn.ErrSSHStart, "alpha"),
		},
		{
			name:      "deadline exceeded chain",
			attachErr: fmt.Errorf("attach alpha: %w", context.DeadlineExceeded),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{
				RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
				RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
					return nil, tc.attachErr
				},
			}
			// A real remote hub source: the classification under test is the
			// mapping its own call path uses.
			sources := appsource.NewRegistry()
			sources.Add(appsource.NewRemoteHubSource("alpha", nil, cfg.RemoteHostClient))
			hosts := hostAttachRegistry(t, cfg)

			_, err := hubHostAttach(context.Background(), cfg, sources, hosts, appwire.HostAttachParams{Name: "alpha"})
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("attach failure %T=%v, want a typed WireError", err, err)
			}
			data, _ := wire.Data.(appwire.ErrorData)
			if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
				t.Fatalf("attach failure wire=%+v, want session unavailable", wire)
			}
			if !strings.Contains(wire.Message, "alpha") {
				t.Fatalf("attach failure message %q does not name host alpha", wire.Message)
			}
		})
	}
}

// A caller cancellation is the caller's own context ending, not host
// unavailability: it stays raw so the auto-resume gate is not fired for a
// request the caller abandoned. The dial is still attempted (the handler has no
// earlier cancellation point), but the error is the raw context error.
func TestHostAttachKeepsCallerCancellationRaw(t *testing.T) {
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(ctx context.Context, _ string) (*appwire.Client, error) {
			return nil, ctx.Err()
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewRemoteHubSource("alpha", nil, cfg.RemoteHostClient))
	hosts := hostAttachRegistry(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := hubHostAttach(ctx, cfg, sources, hosts, appwire.HostAttachParams{Name: "alpha"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attach error %T=%v, want context.Canceled", err, err)
	}
	if wire, ok := errors.AsType[appwire.WireError](err); ok {
		t.Fatalf("canceled attach classified as host unavailability: %+v", wire)
	}
}
