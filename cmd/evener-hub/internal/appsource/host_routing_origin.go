package appsource

import (
	"context"
	"fmt"

	"primeradiant.com/evener/appwire"
)

// hostRoutingOriginKey carries a request's hub-routing origin through the
// connection and request contexts. A request with no origin (an ordinary
// browser, TUI, or CLI session of the hub serving it) is local; a non-empty
// origin marks a request that arrived over a peer hub's attach bridge
// (component 05, §"The origin signal is an explicit bridge marker on the
// connection"). The value is installed once, at the hub's /rpc edge, and every
// handler context for that connection inherits it.
type hostRoutingOriginKey struct{}

// WithHostRoutingOrigin stamps origin onto ctx. The hub's /rpc edge applies it
// from the cooperative bridge marker; everything downstream — including this
// package's remote-routing seams — reads it back through HostRoutingOrigin.
func WithHostRoutingOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, hostRoutingOriginKey{}, origin)
}

// HostRoutingOrigin returns the request's routing origin, or "" for a
// local-originated request.
func HostRoutingOrigin(ctx context.Context) string {
	origin, _ := ctx.Value(hostRoutingOriginKey{}).(string)
	return origin
}

// guardRemoteDispatch refuses a remote-originated request before any request is
// sent over a remote-hub client, with the typed appwire.InvalidParams naming the
// origin (component 07, §"Host-routing origin guard").
//
// It is the one shared guard for every non-local dispatch. dialRemoteHost (the
// hub's Ensure-backed dialing seam) covers the attach triggers, but it only
// protects paths that attach: a request arriving over a peer hub's bridge could
// still forward a call over an ALREADY-ATTACHED RemoteHubSource — the
// evener/host/request admin proxy, an explicit remote-SourceID thread read or
// mutation, a capability probe — and reach a second host, bypassing the depth-1
// topology cap. Every one of those dispatches resolves its client through
// resolveClient, so enforcing the guard there refuses them all regardless of
// attach state, while a request reading this hub's own cached state (snapshot
// rows, fleet manifests, tombstones) never reaches this seam and keeps working.
func guardRemoteDispatch(ctx context.Context) error {
	origin := HostRoutingOrigin(ctx)
	if origin == "" {
		return nil
	}
	return appwire.InvalidParams(fmt.Sprintf("remote-originated request (origin %q) may not dispatch to a remote host", origin))
}
