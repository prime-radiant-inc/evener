package hub

import (
	"context"
	"fmt"
	"net/http"

	"primeradiant.com/evener/appwire"
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

// hostRoutingOriginKey carries the request's routing origin through the
// connection and request contexts. A request with no origin (an ordinary
// browser, TUI, or CLI session of this hub) is local.
type hostRoutingOriginKey struct{}

// withHostRoutingOrigin stamps origin onto ctx. It is applied once, at the
// hub's /rpc edge, from the bridge marker header; every handler context for
// that connection inherits it (component 05, §"The origin signal is an explicit
// bridge marker on the connection").
func withHostRoutingOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, hostRoutingOriginKey{}, origin)
}

// hostRoutingOrigin returns the request's routing origin, or "" for a
// local-originated request.
func hostRoutingOrigin(ctx context.Context) string {
	origin, _ := ctx.Value(hostRoutingOriginKey{}).(string)
	return origin
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
func guardRemoteHostDial(ctx context.Context) error {
	origin := hostRoutingOrigin(ctx)
	if origin == "" {
		return nil
	}
	return appwire.InvalidParams(fmt.Sprintf("remote-originated request (origin %q) may not attach a remote host", origin))
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
