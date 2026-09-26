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

	resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
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
		resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
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

	_, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "ghost"})
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

	_, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
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

			_, err := hubHostAttach(context.Background(), cfg, sources, hosts, appwire.HostAttachParams{Host: "alpha"})
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
	_, err := hubHostAttach(ctx, cfg, sources, hosts, appwire.HostAttachParams{Host: "alpha"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attach error %T=%v, want context.Canceled", err, err)
	}
	if wire, ok := errors.AsType[appwire.WireError](err); ok {
		t.Fatalf("canceled attach classified as host unavailability: %+v", wire)
	}
}

// A timed-out deployment still names the manager's deploy refusal: the error
// carries the ErrDeploy sentinel wrapped with the deadline chain, so the
// manager's terminal class must win over the source's generic deadline mapping
// (SessionUnavailable) and reach the browser as the typed hubLaunch failure.
// A genuine transport timeout — the deadline chain with no Deploy sentinel —
// keeps the source's SessionUnavailable mapping. Before the fix the source's
// MapAttachError ran first and returned SessionUnavailable for both, so
// hostAttachWireError never saw the Deploy sentinel.
func TestHostAttachTimedOutDeployIsHubLaunchError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attachErr error
		info      appwire.ErrorInfo
	}{
		{
			name:      "deploy wrapped with deadline chain",
			attachErr: fmt.Errorf("%w: host %q build: %w", sshconn.ErrDeploy, "alpha", context.DeadlineExceeded),
			info:      appwire.ErrorHubLaunch,
		},
		{
			name:      "genuine transport timeout",
			attachErr: fmt.Errorf("attach alpha: %w", context.DeadlineExceeded),
			info:      appwire.ErrorSessionUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{
				RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
				RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
					return nil, tc.attachErr
				},
			}
			// A real remote hub source: the precedence under test is between its
			// own deadline mapping and the manager's terminal classes.
			sources := appsource.NewRegistry()
			sources.Add(appsource.NewRemoteHubSource("alpha", nil, cfg.RemoteHostClient))
			hosts := hostAttachRegistry(t, cfg)

			_, err := hubHostAttach(context.Background(), cfg, sources, hosts, appwire.HostAttachParams{Host: "alpha"})
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("attach failure %T=%v, want a typed WireError", err, err)
			}
			data, _ := wire.Data.(appwire.ErrorData)
			if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != tc.info {
				t.Fatalf("attach failure wire=%+v, want code=%d info=%q", wire, appwire.CodeUnavailable, tc.info)
			}
			if !strings.Contains(wire.Message, "alpha") {
				t.Fatalf("attach failure message %q does not name host alpha", wire.Message)
			}
		})
	}
}

// A remote-originated request — one forwarded over a peer hub's attach bridge —
// may not make this hub attach a host. The shared host-routing origin guard
// refuses it typed before any Ensure-backed dial, so a peer cannot use this hub
// to reach a third host and break the depth-1 topology cap (component 07,
// §"Host-routing origin guard"; component 06 §"Go changes" item 5).
func TestHostAttachRefusesRemoteOriginatedDial(t *testing.T) {
	var dials atomic.Int64
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return &appwire.Client{}, nil
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	ctx := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
	_, err := hubHostAttach(ctx, cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("remote-originated attach error %T=%v, want WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("remote-originated attach wire=%+v, want invalid params", wire)
	}
	if !strings.Contains(wire.Message, hostRoutingOriginBridge) {
		t.Fatalf("remote-originated refusal %q does not name the origin", wire.Message)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("remote-originated attach dialed %d times, want 0", got)
	}
}

// The response carries the attach contract's server identity: the configured
// host ID and the handshake's ServerInfo name/version. HubVersion stays
// facts-owned, so it reports the host's running build, not the handshake's
// package-constant ServerInfo.Version.
func TestHostAttachPopulatesHostIdentityAndHandshake(t *testing.T) {
	live := &appwire.Client{}
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			return live, nil
		},
		RemoteHostFacts: func(context.Context, string, *appwire.Client) (appsource.HostFacts, error) {
			return appsource.HostFacts{ProtocolVersion: "1", HubVersion: "9.9.9"}, nil
		},
		RemoteHostHandshake: func(host string, client *appwire.Client) (appwire.InitializeResponse, bool) {
			if host != "alpha" || client != live {
				return appwire.InitializeResponse{}, false
			}
			return appwire.InitializeResponse{
				ProtocolVersion: "1",
				SourceID:        "local",
				ServerInfo:      appwire.ServerInfo{Name: "evener-hub", Version: "0.1.0"},
			}, true
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if resp.Host != "alpha" || resp.ServerName != "evener-hub" || resp.ServerVersion != "0.1.0" {
		t.Fatalf("attach response = %+v, want host ID + handshake server name/version", resp)
	}
	if resp.HubVersion != "9.9.9" {
		t.Fatalf("HubVersion = %q, want the facts' build version (not the handshake constant)", resp.HubVersion)
	}
}

// The manager's terminal attach failures reach the browser as typed wire
// errors, not generic internal errors: a protocol/contract refusal is
// actionUnavailable, and a deploy failure (including the dirty-controller
// refusal) is hubLaunch (component 06 §"Connect action failure").
func TestHostAttachTerminalFailuresAreTypedWireErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attachErr error
		code      int
		info      appwire.ErrorInfo
	}{
		{
			name:      "protocol incompatible",
			attachErr: fmt.Errorf("%w: host %q protocol %q", sshconn.ErrProtocolIncompatible, "alpha", "x"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorActionUnavailable,
		},
		{
			name:      "unsupported host",
			attachErr: fmt.Errorf("%w: host %q", sshconn.ErrUnsupportedHost, "alpha"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorActionUnavailable,
		},
		{
			name:      "deploy failed",
			attachErr: fmt.Errorf("%w: host %q build", sshconn.ErrDeploy, "alpha"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorHubLaunch,
		},
		{
			name:      "dirty controller refused",
			attachErr: fmt.Errorf("%w: host %q", sshconn.ErrControllerDirty, "alpha"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorHubLaunch,
		},
		{
			// A run target that cannot serve a hub is the same shape: the
			// controller can never install a binary the hub can run at that
			// path, so it is a typed hub-launch failure rather than a generic
			// internal error.
			name:      "unservable run target refused",
			attachErr: fmt.Errorf("%w: host %q evener_path %q", sshconn.ErrRunTargetUnservable, "alpha", "/opt/evener/bin/evener-dev"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorHubLaunch,
		},
		{
			// The deploy ran but left a build that is not this controller's:
			// retrying re-pushes the same artifact, so it is the same terminal
			// hub-launch shape as the refusals above.
			name:      "deployed build not stamped by the controller refused",
			attachErr: fmt.Errorf("%w: host %q still reports version %q", sshconn.ErrDeployUnstamped, "alpha", "othersha"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorHubLaunch,
		},
		{
			// The operator's -deploy-binary targets another platform, so it can
			// never be installed on this host and every retry re-reads the same
			// file: the same terminal hub-launch shape as the refusals above.
			name:      "wrong-platform deploy artifact refused",
			attachErr: fmt.Errorf("host %q build linux/amd64: %w: -deploy-binary %q targets linux/arm64, but the host needs linux/amd64", "alpha", sshconn.ErrDeployArtifactUnusable, "/tmp/evener"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorHubLaunch,
		},
		{
			// No deploy path exists and the host has no evener at the resolved
			// path: like the dirty-controller refusal, the controller cannot
			// install or match its build, so it is a typed hub-launch failure
			// rather than a generic internal error.
			name:      "executable missing with no deploy path",
			attachErr: fmt.Errorf("%w: host %q", sshconn.ErrExecutableMissing, "alpha"),
			code:      appwire.CodeUnavailable,
			info:      appwire.ErrorHubLaunch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{
				RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
				RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
					return nil, tc.attachErr
				},
			}
			// A real remote hub source: the terminal classes under test are the
			// ones its own classifier deliberately leaves raw.
			sources := appsource.NewRegistry()
			sources.Add(appsource.NewRemoteHubSource("alpha", nil, cfg.RemoteHostClient))
			hosts := hostAttachRegistry(t, cfg)

			_, err := hubHostAttach(context.Background(), cfg, sources, hosts, appwire.HostAttachParams{Host: "alpha"})
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("attach failure %T=%v, want a typed WireError", err, err)
			}
			data, _ := wire.Data.(appwire.ErrorData)
			if wire.Code != tc.code || data.EvenerErrorInfo != tc.info {
				t.Fatalf("attach failure wire=%+v, want code=%d info=%q", wire, tc.code, tc.info)
			}
			if !strings.Contains(wire.Message, "alpha") {
				t.Fatalf("attach failure message %q does not name host alpha", wire.Message)
			}
		})
	}
}

// The attach handshake is authoritative for ProtocolVersion when the preflight
// facts carry none: ensureOnce explicitly tolerates an empty facts
// ProtocolVersion, and overwriting the negotiated version with that empty string
// would make the same host report different versions from attach and from probe
// (where the handshake wins). Before the fix the facts block clobbered it
// unconditionally, discarding the version the handshake had just supplied.
func TestHostAttachKeepsHandshakeProtocolVersionWhenFactsAreEmpty(t *testing.T) {
	live := &appwire.Client{}
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			return live, nil
		},
		RemoteHostFacts: func(context.Context, string, *appwire.Client) (appsource.HostFacts, error) {
			// An empty ProtocolVersion is the shape ensureOnce tolerates.
			return appsource.HostFacts{HubVersion: "9.9.9"}, nil
		},
		RemoteHostHandshake: func(string, *appwire.Client) (appwire.InitializeResponse, bool) {
			return appwire.InitializeResponse{ProtocolVersion: "7", SourceID: "local"}, true
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if resp.ProtocolVersion != "7" {
		t.Fatalf("ProtocolVersion = %q, want the handshake's negotiated version 7", resp.ProtocolVersion)
	}
	if resp.HubVersion != "9.9.9" {
		t.Fatalf("HubVersion = %q, want the facts' build version", resp.HubVersion)
	}
}

// A successful dial is authoritative: if only the post-attach facts read fails
// (a link drop or a reconnect that swaps the channel), the response still
// reports Attached true — the attach event has already flipped the host online,
// and a failure toast for an online host makes the user retry needlessly. The
// facts are omitted, and the handshake identity survives.
func TestHostAttachKeepsSuccessfulDialWhenFactsReadFails(t *testing.T) {
	live := &appwire.Client{}
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			return live, nil
		},
		RemoteHostFacts: func(context.Context, string, *appwire.Client) (appsource.HostFacts, error) {
			return appsource.HostFacts{}, errors.New("link dropped between the dial and the facts read")
		},
		RemoteHostHandshake: func(string, *appwire.Client) (appwire.InitializeResponse, bool) {
			return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "evener-hub", Version: "0.1.0"}}, true
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
	if err != nil {
		t.Fatalf("attach after a successful dial must not fail on the facts read: %v", err)
	}
	if !resp.Attached || resp.Host != "alpha" {
		t.Fatalf("attach response = %+v, want the successful dial authoritative", resp)
	}
	if resp.HubVersion != "" || resp.OS != "" || resp.Arch != "" || resp.Features != nil {
		t.Fatalf("facts present in %+v, want them omitted after a failed facts read", resp)
	}
	if resp.ServerName != "evener-hub" || resp.ServerVersion != "0.1.0" {
		t.Fatalf("handshake identity lost from %+v", resp)
	}
}

// A handshake generation mismatch after a successful dial is an attach failure,
// not a success: the seam reports false when the installed channel is a
// different generation than the dial produced (or nothing is attached), so the
// UI must not receive attached:true for a host that is no longer attached.
func TestHostAttachRefusesHandshakeGenerationMismatch(t *testing.T) {
	live := &appwire.Client{}
	var dials atomic.Int64
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return live, nil
		},
		RemoteHostHandshake: func(string, *appwire.Client) (appwire.InitializeResponse, bool) {
			return appwire.InitializeResponse{}, false
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
	if err == nil {
		t.Fatalf("handshake-mismatch attach = %+v, want an attach failure (not Attached:true)", resp)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("handshake-mismatch attach error %T=%v, want a typed WireError", err, err)
	}
	data, _ := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("handshake-mismatch attach wire=%+v, want session unavailable", wire)
	}
	if !strings.Contains(wire.Message, "alpha") {
		t.Fatalf("handshake-mismatch attach message %q does not name host alpha", wire.Message)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("handshake-mismatch attach dialed %d times, want 1", got)
	}
}

// A typed liveness error from the post-attach facts read is an attach failure,
// not a suppressed read error: SessionUnavailable means the channel disappeared
// or changed generation after Ensure, so the host is no longer attached. Only
// non-liveness fact-read errors stay suppressed (the dial-authoritative L1
// behavior pinned by TestHostAttachKeepsSuccessfulDialWhenFactsReadFails).
func TestHostAttachRefusesFactsLivenessError(t *testing.T) {
	live := &appwire.Client{}
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "alpha", SSH: "alpha"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			return live, nil
		},
		RemoteHostFacts: func(context.Context, string, *appwire.Client) (appsource.HostFacts, error) {
			return appsource.HostFacts{}, appwire.SessionUnavailable("remote hub unavailable: alpha")
		},
		RemoteHostHandshake: func(string, *appwire.Client) (appwire.InitializeResponse, bool) {
			return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "evener-hub", Version: "0.1.0"}}, true
		},
	}
	hosts := hostAttachRegistry(t, cfg)

	resp, err := hubHostAttach(context.Background(), cfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "alpha"})
	if err == nil {
		t.Fatalf("liveness-error attach = %+v, want an attach failure (not Attached:true)", resp)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("liveness-error attach error %T=%v, want a typed WireError", err, err)
	}
	data, _ := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("liveness-error attach wire=%+v, want session unavailable", wire)
	}
	if !strings.Contains(wire.Message, "alpha") {
		t.Fatalf("liveness-error attach message %q does not name host alpha", wire.Message)
	}
}
