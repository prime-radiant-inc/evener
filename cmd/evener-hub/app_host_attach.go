package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
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
func registerHostAttachHandler(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry, hosts *hostreg.Registry) {
	// hosts is the one live registry the server constructor resolved — in
	// production the same *hostreg.Registry the SSH manager dials through and
	// the host-management surface mutates, so a host added at runtime
	// validates here without a restart. The constructor builds the fallback
	// from the configured entries once (tests, embedders) and hands the same
	// instance to every host handler, so add and attach can never validate
	// against divergent copies.
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostAttach, func(ctx context.Context, params appwire.HostAttachParams) (appwire.HostAttachResponse, error) {
		return hubHostAttach(ctx, cfg, sources, hosts, params)
	})
}

// hostRegistryFromConfig returns the cfg's live host registry when one was
// threaded through WebConfig, else the fallback the server constructors
// (newWebServer and newHubAppServerWithNavigationAndTrace) build ONCE and
// share with every host-dependent handler. With a threaded SSH manager the
// fallback is the MANAGER's registry (sshconn.Manager.Registry) — the instance
// Ensure validates against and AddHost/RemoveHost mutate — never a fresh copy
// from the configured entries: a fresh copy alongside a manager would split
// the surfaces, because the host-management surface commits runtime adds and
// removals through the manager while boot sidecar entries load into whatever
// registry it was handed (sidecar entries would land where the manager never
// dials, Ensure answering ErrHostNotFound, and a runtime Add would insert
// where host/list and host/attach never read).
// A manager with no registry keeps the fresh copy: its AddHost and RemoveHost
// both refuse loudly, so neither an add nor a removal can silently diverge
// from it.
//
// Without a manager, config loading already validated the entries (main.go
// builds the same registry from the same entries), so the error path is the
// impossible-duplicate fallback; an empty registry keeps the surface serving
// refusals instead of panicking.
func hostRegistryFromConfig(cfg hubcore.WebConfig) *hostreg.Registry {
	if cfg.RemoteHostSSHManager != nil {
		if reg := cfg.RemoteHostSSHManager.Registry(); reg != nil {
			return reg
		}
	}
	hosts, err := hostreg.New(cfg.RemoteHosts)
	if err != nil {
		hosts, _ = hostreg.New(nil)
	}
	return hosts
}

// hubHostAttach attaches one configured remote host and reports its post-attach
// state. It is idempotent: Ensure returns the installed channel's client when
// the host is already attached, so a repeated Connect is safe and simply returns
// the current facts.
func hubHostAttach(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, hosts *hostreg.Registry, params appwire.HostAttachParams) (appwire.HostAttachResponse, error) {
	name := strings.TrimSpace(params.Host)
	if _, ok := hosts.Get(name); !ok {
		return appwire.HostAttachResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	// The dialing seam is wired by main.go for every production hub. A hub with
	// no seam (an embedder or a hermetic test) cannot attach anything.
	if cfg.RemoteHostClient == nil {
		return appwire.HostAttachResponse{}, appwire.Unavailable(fmt.Sprintf("host %q cannot be attached", name))
	}
	// dialRemoteHost applies the shared host-routing origin guard before the
	// Ensure-backed dial: a remote-originated request (it arrived over a peer
	// hub's attach bridge) may not make this hub attach a host, which would
	// bypass the depth-1 topology cap (component 07, §"Host-routing origin
	// guard").
	client, err := dialRemoteHost(ctx, cfg, name)
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
	resp := appwire.HostAttachResponse{Attached: true, Host: name}
	// The handshake seam is optional (tests and embedders may leave it unset).
	// When wired it answers from the channel the dial produced, so the response
	// carries the attach contract's server identity and the caller can render
	// the row online without a second probe (component 06 §"Go changes" item 5:
	// "serverName/version from the attach handshake, plus the host ID").
	if cfg.RemoteHostHandshake != nil && client != nil {
		if handshake, ok := cfg.RemoteHostHandshake(name, client); ok {
			resp.ServerName = handshake.ServerInfo.Name
			resp.ServerVersion = handshake.ServerInfo.Version
			if resp.ProtocolVersion == "" {
				resp.ProtocolVersion = handshake.ProtocolVersion
			}
		} else {
			// A false handshake means the installed channel is a different
			// generation than the dial produced (or nothing is attached at
			// all): the host the dial attached is already gone, so reporting
			// Attached:true would hand the UI an online row for a host that is
			// no longer attached. Refuse with the same typed liveness refusal
			// the attached-only lookup and the facts seam produce.
			return appwire.HostAttachResponse{}, appwire.SessionUnavailable("remote hub unavailable: " + name)
		}
	}
	// The facts seam is optional (tests and embedders may leave it unset); when
	// wired it answers from the channel the dial produced, so the caller can
	// render the row online without a second probe.
	if cfg.RemoteHostFacts != nil && client != nil {
		facts, factsErr := cfg.RemoteHostFacts(ctx, name, client)
		if factsErr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return appwire.HostAttachResponse{}, cerr
			}
			// A typed liveness error means the channel disappeared or changed
			// generation after Ensure, so the host is no longer attached and
			// the attach itself failed — surface it rather than reporting
			// Attached:true. Only non-liveness fact-read errors stay
			// suppressed below (the dial-authoritative behavior).
			if isSessionUnavailableError(factsErr) {
				return appwire.HostAttachResponse{}, factsErr
			}
			// The dial succeeded and the host is attached — the attach event has
			// already flipped it online. A failure reading the post-attach facts
			// is not a failed Connect, so the successful dial stays authoritative
			// and the response carries what the handshake supplied rather than
			// making the browser show a failure toast for a host that is online.
			return resp, nil
		}
		// The facts block owns the fields AppWire cannot report on an
		// already-initialized connection, but it must not clobber the version the
		// attach handshake just negotiated: ensureOnce explicitly tolerates an
		// empty preflight ProtocolVersion, and writing that empty value over the
		// handshake's would make the same host report different versions from
		// attach and from probe (where the handshake is authoritative).
		if facts.ProtocolVersion != "" {
			resp.ProtocolVersion = facts.ProtocolVersion
		}
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
//
// The source's classifier handles the transient/transport sentinels
// (ErrSSHStart, ErrRestart, and the transport shapes) as the typed
// SessionUnavailable the auto-resume/refusal gates match. The manager's
// terminal classes are deliberately left raw by that classifier — they name a
// host that will never attach and must not be retried — so they are typed here
// instead, rather than reaching the browser as a generic internal error through
// appserver.WireError.
//
// The manager's deploy-family sentinels (ErrDeploy, ErrVersionMismatch,
// ErrControllerDirty, ErrRunTargetUnservable, ErrDeployArtifactUnusable,
// ErrDeployUnstamped, ErrExecutableMissing) win over the source's generic
// deadline mapping: a timed-out deployment still carries the deploy sentinel in
// its chain, and it must reach the browser as the typed HubLaunchError the
// Connect surface matches, not as SessionUnavailable. Every other error keeps
// the source's mapping, so a genuine transport timeout (the deadline chain with
// no deploy sentinel) still becomes SessionUnavailable.
func classifyHostAttachError(sources *appsource.Registry, host string, err error) error {
	// Preserve the manager's sentinel precedence before the source's
	// transport mapping can claim the chain: a deploy-family error wrapped
	// with a deadline still names a failed deploy, not a lost transport.
	wire := hostAttachWireError(err)
	if w, ok := errors.AsType[appwire.WireError](wire); ok {
		return w
	}
	mapped := err
	remapped := false
	if sources != nil {
		if source, ok := sources.Source(host); ok {
			if classifier, ok := source.(attachErrorClassifier); ok {
				mapped = classifier.MapAttachError(err)
				remapped = true
			}
		}
	}
	if _, ok := errors.AsType[appwire.WireError](mapped); ok {
		return mapped
	}
	if !remapped {
		// Nothing remapped the error, and hostAttachWireError already ran for
		// exactly this input: it carried no terminal sentinel then and passed
		// through unchanged, so there is nothing to recompute.
		return err
	}
	return hostAttachWireError(mapped)
}

// hostAttachWireError maps the manager's terminal attach sentinels to the typed
// wire errors the attach contract specifies for clients (component 06 §"Go
// changes" item 5 "Error mapping"; §"Connect action failure"):
//
//   - protocol/contract refusals (protocol incompatibility, unsupported host,
//     launch contract, unparseable preflight, unusable host address, unknown
//     host, closed manager) → Unavailable (actionUnavailable): the host refused
//     the controller, and no retry of this attach can change that.
//   - deploy failures (ErrDeploy), the dirty-controller deploy refusal
//     (ErrControllerDirty), the unservable-run-target deploy refusal
//     (ErrRunTargetUnservable), the post-deploy identity refusal
//     (ErrDeployUnstamped), the unusable-artifact refusal
//     (ErrDeployArtifactUnusable: the artifact targets another platform or is
//     not evener), a version mismatch a deploy would have to fix, and the
//     missing-executable refusal a host with no deploy path produces
//     (ErrExecutableMissing) → HubLaunchError (hubLaunch): the controller could
//     not install or match its build on the host, so the host cannot be
//     attached/launched.
//
// An unrecognized error is returned unchanged, so it still surfaces as an
// internal error rather than being mislabelled as a typed refusal.
func hostAttachWireError(err error) error {
	switch {
	case errors.Is(err, sshconn.ErrProtocolIncompatible),
		errors.Is(err, sshconn.ErrUnsupportedHost),
		errors.Is(err, sshconn.ErrLaunchContract),
		errors.Is(err, sshconn.ErrPreflightDecode),
		errors.Is(err, sshconn.ErrHostNotFound),
		errors.Is(err, sshconn.ErrHostAddr),
		errors.Is(err, sshconn.ErrManagerClosed):
		return appwire.Unavailable("host attach refused: " + err.Error())
	case errors.Is(err, sshconn.ErrDeploy),
		errors.Is(err, sshconn.ErrVersionMismatch),
		errors.Is(err, sshconn.ErrControllerDirty),
		errors.Is(err, sshconn.ErrRunTargetUnservable),
		errors.Is(err, sshconn.ErrDeployArtifactUnusable),
		errors.Is(err, sshconn.ErrDeployUnstamped),
		errors.Is(err, sshconn.ErrExecutableMissing):
		return appwire.HubLaunchError("host attach deploy failed: " + err.Error())
	default:
		return err
	}
}
