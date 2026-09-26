package hub

import (
	"context"
	"fmt"
	"net/http"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// bridgeOriginHeader is the cooperative marker `evener hub attach --stdio`
// presents on its loopback dial of the host hub's AppWire edge (component 02,
// §Contract "Bridge marker"; component 05, §"The origin signal is an explicit
// bridge marker on the connection"). The marker is client-asserted and carries
// no secret — any token holder can set or omit it — so it is not a boundary
// against a hostile peer: it terminates an honest A→B→A cycle, and the v1 trust
// assumption is that peer hubs are cooperative.
const bridgeOriginHeader = "X-Evener-Bridge"

// hostRoutingOriginBridge is the origin stamped on a request that arrived over
// the attach bridge. Any non-empty origin marks a request remote-originated.
const hostRoutingOriginBridge = "bridge"

// withHostRoutingOrigin stamps origin onto ctx. It is applied once, at the
// hub's /rpc edge, from the bridge marker header; every handler context for
// that connection inherits it (component 05, §"The origin signal is an explicit
// bridge marker on the connection").
//
// The context plumbing lives in the remote-routing package
// (appsource.WithHostRoutingOrigin) because the origin guard's shared dispatch
// seam — every remote-hub client resolution — sits there, and both the dial
// guard below and that seam have to read the same value.
func withHostRoutingOrigin(ctx context.Context, origin string) context.Context {
	return appsource.WithHostRoutingOrigin(ctx, origin)
}

// hostRoutingOrigin returns the request's routing origin, or "" for a
// local-originated request.
func hostRoutingOrigin(ctx context.Context) string {
	return appsource.HostRoutingOrigin(ctx)
}

// isBridgeOriginRequest reports whether r carries the bridge marker, i.e. it
// arrived over `evener hub attach --stdio` rather than a local client.
func isBridgeOriginRequest(r *http.Request) bool {
	return r.Header.Get(bridgeOriginHeader) != ""
}

// guardRemoteHostDial refuses a remote-originated request before any
// Ensure-backed dial, with the typed appwire.InvalidParams naming the origin
// (component 07, §"Host-routing origin guard"). It is the one shared guard for
// every remote attach trigger: a peer hub must not be able to make this hub
// attach a configured host, which would bypass the depth-1 topology cap
// (component 06's Connect action and the explicit-SourceIDs thread/list attach
// both dial through dialRemoteHost below).
//
// It is the dial half of the guard. The dispatch half lives at the remote-hub
// client seam every RemoteHubSource call resolves through
// (appsource.guardRemoteDispatch): without it a bridge-originated request could
// forward a call over an already-attached source, reaching a second host
// without ever dialing.
func guardRemoteHostDial(ctx context.Context) error {
	return refuseRemoteOrigin(ctx, "attach a remote host")
}

// guardControllerLocalHosts refuses a remote-originated host-management
// request (evener/host/add|list|status|remove|update). Those methods act on the
// controller's own config and channels — a peer hub reached over its attach
// bridge must not enumerate or mutate this hub's host registry — the same
// controller-local rule that keeps them off the remote forward allow-list
// (TestHostManageNotForwarded). It reads the same origin accessor the dial
// guard above does (component 07, §"Host-routing origin guard").
// refuseRemoteOrigin is the one refusal body the origin guards share: nil
// for a local-originated request, the typed InvalidParams naming the origin
// and the action it may not take otherwise.
func refuseRemoteOrigin(ctx context.Context, action string) error {
	origin := hostRoutingOrigin(ctx)
	if origin == "" {
		return nil
	}
	return appwire.InvalidParams(fmt.Sprintf("remote-originated request (origin %q) may not %s", origin, action))
}

func guardControllerLocalHosts(ctx context.Context) error {
	return refuseRemoteOrigin(ctx, "manage this hub's hosts")
}

// guardRemoteSpawnSource refuses a remote-originated thread/start that would
// resolve to any source but local, with the same typed InvalidParams the other
// origin guards return (design §2 "Topology"; component 05, §"The receiving hub
// must reject a non-local resolution for a remote-originated thread/start").
// A remote-originated spawn is served from this hub's local state only, so a
// preserved harness naming one of this hub's configured hosts cannot fan the
// spawn out to it; a local client's spawn is unaffected.
func guardRemoteSpawnSource(ctx context.Context, sourceID string) error {
	if sourceID == "" || sourceID == "local" {
		return nil
	}
	return refuseRemoteOrigin(ctx, "route a spawn to another host source")
}

// dialRemoteHost is the single Ensure-backed dialing seam for the hub's attach
// triggers. Every caller that may dial a remote host goes through it, so the
// host-routing origin guard cannot be omitted by a new remote attach path
// (component 07, §"Host-routing origin guard": "Any future remote dispatch path
// passes through the seam").
func dialRemoteHost(ctx context.Context, cfg hubcore.WebConfig, host string) (*appwire.Client, error) {
	if err := guardRemoteHostDial(ctx); err != nil {
		return nil, err
	}
	return cfg.RemoteHostClient(ctx, host)
}
