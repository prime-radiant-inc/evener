package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// remoteHostAdminMethods is the exact set of hub-scoped admin RPCs the
// evener/host/request proxy forwards to a remote host (component 07a).
//
// It is an EXACT-NAME set on purpose. A prefix rule
// (strings.HasPrefix(method, "evener/instance/")) would auto-allow whatever
// sensitive ScopeHub method is added to the catalog next — a hypothetical
// evener/instance/deleteAll — turning the proxy into a generic hub-to-hub
// tunnel by default. Every method must be named deliberately here, and a
// method the catalog gains after this list was written is refused until it is
// added. The remote hub is a trusted peer, but the browser that drives this
// proxy is not (design §6 "secret handling").
//
// The families are the settings panes' own hub-scoped handlers: provider
// instances, launch config, marketplaces/plugins, auth/credentials, and the
// personal AGENTS.md. Nothing else.
var remoteHostAdminMethods = map[string]struct{}{
	// Provider instances (hubInstancesController, app_instances.go). These are
	// exactly the five handlers the catalog defines; there is no
	// evener/instance/setModelDisabled and no evener/instance/refreshModels.
	appwire.MethodEvenerInstanceList:       {},
	appwire.MethodEvenerInstanceCreate:     {},
	appwire.MethodEvenerInstanceEdit:       {},
	appwire.MethodEvenerInstanceRemove:     {},
	appwire.MethodEvenerInstanceSetDefault: {},

	// Launch config (hubLaunchController, app_launch.go). Its credential-env
	// refusal must reach the browser verbatim; the proxy never launders it.
	appwire.MethodEvenerLaunchResolve:   {},
	appwire.MethodEvenerLaunchSchema:    {},
	appwire.MethodEvenerLaunchGetLayer:  {},
	appwire.MethodEvenerLaunchSetLayer:  {},
	appwire.MethodEvenerLaunchTrustRepo: {},

	// Marketplaces and plugins (hubPluginsController, app_plugins.go). The
	// pane's UI reaches plugin/disable and plugin/setAutoUpgrade too.
	appwire.MethodEvenerMarketplaceList:      {},
	appwire.MethodEvenerMarketplaceAdd:       {},
	appwire.MethodEvenerMarketplaceRemove:    {},
	appwire.MethodEvenerMarketplaceRefresh:   {},
	appwire.MethodEvenerMarketplaceEdit:      {},
	appwire.MethodEvenerMarketplaceBrowse:    {},
	appwire.MethodEvenerPluginList:           {},
	appwire.MethodEvenerPluginInstall:        {},
	appwire.MethodEvenerPluginUpgrade:        {},
	appwire.MethodEvenerPluginRemove:         {},
	appwire.MethodEvenerPluginEnable:         {},
	appwire.MethodEvenerPluginDisable:        {},
	appwire.MethodEvenerPluginSetAutoUpgrade: {},
	appwire.MethodEvenerPluginPreview:        {},
	appwire.MethodEvenerPluginCheckNow:       {},

	// Auth and credentials (hubAuthController, app_auth.go). The host's own
	// refusals (a stored key under a Codex or gcp-adc instance) pass through
	// unchanged; 07c's credential push uses apiKey/set through this same list.
	appwire.MethodEvenerAuthStatus:            {},
	appwire.MethodEvenerAuthTest:              {},
	appwire.MethodEvenerAuthList:              {},
	appwire.MethodEvenerAuthLoginStart:        {},
	appwire.MethodEvenerAuthLoginComplete:     {},
	appwire.MethodEvenerAuthLogout:            {},
	appwire.MethodEvenerAuthApiKeySet:         {},
	appwire.MethodEvenerAuthApiKeyClear:       {},
	appwire.MethodEvenerAuthCredentialJsonSet: {},
	appwire.MethodEvenerAuthDeviceStart:       {},
	appwire.MethodEvenerAuthDevicePoll:        {},

	// Personal AGENTS.md (registerAgentsDocHandlers, app_rpc_agents_doc.go).
	appwire.MethodEvenerSettingsAgentsDocGet: {},
	appwire.MethodEvenerSettingsAgentsDocSet: {},
}

// remoteHostConfigNotifications is the exact set of host-owned config
// notifications the fan-out re-emits to the controller's browser clients,
// wrapped in evener/host/notification. Any other remote notification is
// dropped: the wrapper is not a general-purpose remote-notification tunnel.
//
// notifyInstanceUpdated reuses evener/auth/updated for provider-instance
// mutations (app_rpc.go), so instance edits arrive here as evener/auth/updated
// and carry through unchanged.
var remoteHostConfigNotifications = map[string]struct{}{
	appwire.NotifyEvenerAuthUpdated:              {},
	appwire.NotifyEvenerLaunchUpdated:            {},
	appwire.NotifyEvenerMarketplaceUpdated:       {},
	appwire.NotifyEvenerPluginUpdated:            {},
	appwire.NotifyEvenerSettingsAgentsDocChanged: {},
}

// hostNotificationRetry bounds how long the fan-out waits before re-binding a
// host whose client is not available yet (offline) or went away (reconnect).
const hostNotificationRetry = time.Second

// hostNotificationBroadcaster is the slice of *appserver.Server the fan-out
// needs. It is an interface so the fan-out can be driven by a recorder in
// tests without standing up connections.
type hostNotificationBroadcaster interface {
	BroadcastAll(method string, params any)
}

// hubHostAdminController serves evener/host/request: it forwards one
// allow-listed hub-scoped admin RPC to a named remote host's hub, and re-emits
// that host's config notifications to the controller's browser clients tagged
// with the host.
//
// It adds no canonical state: it resolves a host, checks the channel is live,
// checks the method may be forwarded, and hands the rest to the host's own hub.
// Local execution never happens for a remote host request.
type hubHostAdminController struct {
	broadcaster hostNotificationBroadcaster
	// hosts is the component-03 registry of validated [[hosts]] entries. Its
	// Get is the unknown-host authority.
	hosts *hostreg.Registry
	// sources is the component-05 registry; a remote host's source is where
	// attachment state (Online) and the per-host client live.
	sources *appsource.Registry
}

func newHubHostAdminController(broadcaster hostNotificationBroadcaster, hosts *hostreg.Registry, sources *appsource.Registry) *hubHostAdminController {
	return &hubHostAdminController{broadcaster: broadcaster, hosts: hosts, sources: sources}
}

// registerHostAdminHandlers installs the proxy handler and starts one
// notification fan-out per remote host. ctx bounds the fan-out goroutines'
// lifetime; production passes a process-lifetime context, mirroring the hub's
// other background workers.
func registerHostAdminHandlers(ctx context.Context, server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	hosts, err := hostreg.New(cfg.RemoteHosts)
	if err != nil {
		// Config loading already validated every entry (main.go builds the
		// same registry from the same entries), so this cannot fail in
		// production. Fall back to an empty registry rather than a nil one, so
		// a hypothetical duplicate refuses every host instead of panicking.
		hosts, _ = hostreg.New(nil)
	}
	controller := newHubHostAdminController(server, hosts, sources)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostRequest, controller.Request)
	controller.start(ctx)
}

// Request forwards one allow-listed admin RPC to the named host's hub.
func (c *hubHostAdminController) Request(ctx context.Context, params appwire.HostRequestParams) (json.RawMessage, error) {
	if _, ok := c.hosts.Get(params.Host); !ok {
		return nil, appwire.InvalidParams(fmt.Sprintf("unknown host %q", params.Host))
	}
	remote, err := c.remoteSourceFor(params.Host)
	if err != nil {
		return nil, err
	}
	if _, ok := remoteHostAdminMethods[params.Method]; !ok {
		return nil, appwire.InvalidParams(fmt.Sprintf("method %q is not a permitted remote admin method", params.Method))
	}
	var out json.RawMessage
	if err := remote.AdminCall(ctx, params.Method, params.Params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// remoteSourceFor returns the attached component-05 source for host, or the
// typed refusal for a host that cannot serve a proxied call. A host configured
// but not registered as a source (component 04 wired no client) is as
// unavailable as one whose channel is down: neither may fall back to local
// execution.
func (c *hubHostAdminController) remoteSourceFor(host string) (*appsource.RemoteHubSource, error) {
	// Some embedders and tests build a hub server with no source registry at
	// all (newHubAppServerWithNavigation(cfg, nil, …)); with no sources there is
	// no attached channel either.
	if c.sources == nil {
		return nil, appwire.Unavailable(fmt.Sprintf("host %q is not attached", host))
	}
	source, ok := c.sources.Source(host)
	if !ok {
		return nil, appwire.Unavailable(fmt.Sprintf("host %q is not attached", host))
	}
	remote, ok := source.(*appsource.RemoteHubSource)
	if !ok || !remote.Online() {
		return nil, appwire.Unavailable(fmt.Sprintf("host %q is not attached", host))
	}
	return remote, nil
}

// start launches one fan-out goroutine per remote host source.
func (c *hubHostAdminController) start(ctx context.Context) {
	if c.sources == nil {
		return
	}
	for _, source := range c.sources.All() {
		remote, ok := source.(*appsource.RemoteHubSource)
		if !ok {
			continue
		}
		go c.fanOut(ctx, remote)
	}
}

// fanOut re-emits remote, a host's config notifications to the controller's
// browser clients, tagged with the host, until ctx ends. It re-subscribes
// whenever the subscription ends — SubscribeHostNotifications closes its
// channel when the host's channel (or the subscription's context) goes away —
// so a reconnect rebinds the fresh client without a restart.
func (c *hubHostAdminController) fanOut(ctx context.Context, remote *appsource.RemoteHubSource) {
	host := remote.ID()
	for ctx.Err() == nil {
		notifications, err := remote.SubscribeHostNotifications(ctx)
		if err != nil {
			if !hostNotificationBackoff(ctx) {
				return
			}
			continue
		}
		for notification := range notifications {
			if _, ok := remoteHostConfigNotifications[notification.Method]; !ok {
				continue
			}
			c.broadcaster.BroadcastAll(appwire.NotifyEvenerHostNotification, appwire.HostNotificationParams{
				Host:   host,
				Method: notification.Method,
				Params: notification.Params,
			})
		}
		if !hostNotificationBackoff(ctx) {
			return
		}
	}
}

// hostNotificationBackoff waits one retry interval, reporting false when ctx
// ended first.
func hostNotificationBackoff(ctx context.Context) bool {
	timer := time.NewTimer(hostNotificationRetry)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
