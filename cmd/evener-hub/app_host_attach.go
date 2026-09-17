package hub

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// registerHostAttachHandler installs evener/host/attach, the browser-reachable
// explicit attach trigger (component 06's Connect action, component 08's host
// management surface).
//
// It is the one hub-scoped method that may dial a remote host on the user's
// behalf: it wraps the Ensure-backed dialing seam (cfg.RemoteHostClient), which
// every other remote path deliberately does not touch (they resolve through the
// attached-only lookup instead). Nothing else attaches a configured host — the
// snapshot walk and the non-explicit thread/list fan-out stay attached-only —
// so without this method a configured [[hosts]] entry is dead UI.
//
// It is a controller-LOCAL method, not a forwarded evener/host/request admin
// call: there is no host to forward to until the attach succeeds, so it is
// deliberately absent from remoteHostAdminMethods (see app_host_admin.go).
func registerHostAttachHandler(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	hosts, err := hostreg.New(cfg.RemoteHosts)
	if err != nil {
		// Config loading already validated every entry (main.go builds the same
		// registry from the same entries), so this cannot fail in production.
		// Fall back to an empty registry rather than a nil one, so a
		// hypothetical duplicate refuses every host instead of panicking.
		hosts, _ = hostreg.New(nil)
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostAttach, func(ctx context.Context, params appwire.HostAttachParams) (appwire.HostAttachResponse, error) {
		return hubHostAttach(ctx, cfg, sources, hosts, params)
	})
}

// hubHostAttach attaches one configured remote host and reports its post-attach
// state. It is idempotent: Ensure returns the installed channel's client when
// the host is already attached, so a repeated Connect is safe and simply returns
// the current facts.
func hubHostAttach(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, hosts *hostreg.Registry, params appwire.HostAttachParams) (appwire.HostAttachResponse, error) {
	name := strings.TrimSpace(params.Name)
	if _, ok := hosts.Get(name); !ok {
		return appwire.HostAttachResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	// The dialing seam is wired by main.go for every production hub. A hub with
	// no seam (an embedder or a hermetic test) cannot attach anything.
	if cfg.RemoteHostClient == nil {
		return appwire.HostAttachResponse{}, appwire.Unavailable(fmt.Sprintf("host %q cannot be attached", name))
	}
	client, err := cfg.RemoteHostClient(ctx, name)
	if err != nil {
		// A caller cancellation or deadline is the caller's own context ending,
		// not host unavailability, so it stays raw before the classifier runs —
		// exactly as the explicit thread/list fan-out leaves it raw. Every other
		// attach failure is classified through the source's own MapAttachError so
		// sshconn's transient attach failures and transport losses reach the
		// caller as the typed SessionUnavailable the auto-resume/refusal gates
		// match, rather than as a raw transport error.
		if cerr := ctx.Err(); cerr != nil {
			return appwire.HostAttachResponse{}, cerr
		}
		return appwire.HostAttachResponse{}, classifyHostAttachError(sources, name, err)
	}
	resp := appwire.HostAttachResponse{Attached: true}
	// The facts seam is optional (tests and embedders may leave it unset); when
	// wired it answers from the channel the dial produced, so the caller can
	// render the row online without a second probe.
	if cfg.RemoteHostFacts != nil && client != nil {
		facts, factsErr := cfg.RemoteHostFacts(ctx, name, client)
		if factsErr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return appwire.HostAttachResponse{}, cerr
			}
			return appwire.HostAttachResponse{}, factsErr
		}
		resp.ProtocolVersion = facts.ProtocolVersion
		resp.HubVersion = facts.HubVersion
		resp.OS = facts.OS
		resp.Arch = facts.Arch
		features := facts.Features
		resp.Features = &features
	}
	return resp, nil
}

// classifyHostAttachError maps one attach failure the way the host's own source
// maps its connect failures. A host with no registered source (or a source that
// does not implement the classifier) leaves the error raw, preserving every
// other caller's behavior.
func classifyHostAttachError(sources *appsource.Registry, host string, err error) error {
	if sources == nil {
		return err
	}
	source, ok := sources.Source(host)
	if !ok {
		return err
	}
	classifier, ok := source.(attachErrorClassifier)
	if !ok {
		return err
	}
	return classifier.MapAttachError(err)
}
