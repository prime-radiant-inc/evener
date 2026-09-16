package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
// instances, launch config, marketplaces/plugins, auth/credentials, the
// personal AGENTS.md, and the host-dependent discovery helpers the remote
// settings panes and the spawn form call. Nothing else.
var remoteHostAdminMethods = map[string]struct{}{
	// Provider instances (hubInstancesController, app_instances.go). The
	// settings panes drive these five handlers remotely; the catalog's newer
	// evener/instance/setModelDisabled and evener/instance/refreshModels are
	// deliberately absent here and in the policy table, so the proxy refuses
	// them (fail closed).
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

	// Host-dependent discovery — the remote settings panes' and the spawn form's
	// own filesystem/discovery calls (component 06 §"Frontend changes"). Every
	// one is answered by the host hub against ITS local environment, which is the
	// point: path validation, auto-completion, directory creation, recent
	// projects, harnesses, and the slash catalog must describe the selected host,
	// not the controller. They are read-mostly; the only mutation is creating the
	// directory the user just asked for (dirs/create), and none exposes a secret
	// the admin families above do not already carry. Without these rows a remote
	// settings pane or spawn form fails closed with appwire.InvalidParams.
	appwire.MethodEvenerPathsComplete:     {},
	appwire.MethodEvenerPathValidate:      {},
	appwire.MethodEvenerDirsCreate:        {},
	appwire.MethodEvenerProjectsRecent:    {},
	appwire.MethodEvenerHarnessesList:     {},
	appwire.MethodEvenerSpawnSlashCatalog: {},
	// evener/git/head is read-only branch metadata for a remote working
	// directory (protocol.go: ScopeHub, GitHeadParams/GitHeadResponse); the
	// spawn form needs the branch of the path it is about to launch.
	appwire.MethodEvenerGitHead: {},
	// model/list is ScopeBoth rather than ScopeHub — the serve daemon answers it
	// too — but the hub answers it, and the spawn form asks the selected host for
	// that host's own model inventory.
	appwire.MethodModelList: {},
}

// remoteHostAdminMutationMethods is the non-idempotent subset of
// remoteHostAdminMethods: the forwarded methods that change the remote host's
// durable state — provider instances, launch layers and repo trust,
// marketplaces and plugins (including the on-demand auto-upgrade pass behind
// plugin/checkNow), credentials and login state, the personal AGENTS.md, and
// the spawn form's directory creation.
//
// The proxy forwards these on the outcome-unknown error mapping instead of the
// read methods' SessionUnavailable mapping. A transport failure mid-call cannot
// be told apart from one where the remote applied the change and only the
// answer was lost, and none of these carries an idempotency key or a
// clientMutationId the remote could dedup on, so a caller that retried a
// SessionUnavailable would risk a second instance, plugin install, or
// credential write. The mutating call is answered with
// ErrorMutationOutcomeUnknown and RetryDispositionBlocked so the outcome is
// reported as unknown and no retry is implied.
//
// Like the allow-list, this is an EXACT-NAME set: every name must be a member
// of remoteHostAdminMethods, every allow-listed name is classified here or as
// an explicit read (see TestHostAdminMutationClassificationMatchesAllowList),
// and a method added to the allow-list without a classification fails that
// test.
//
// The read-only remainder — the families whose effect is a lookup or a
// refetch, so an identical retry is harmless: instance/list, launch/{resolve,
// schema,getLayer}, marketplace/{list,browse,refresh}, plugin/{list,preview},
// auth/{status,test,list}, settings/agentsDoc/get, the discovery
// helpers (paths/complete, path/validate, projects/recent, harnesses/list,
// spawn/slashCatalog, git/head), and model/list — stays on AdminCall.
var remoteHostAdminMutationMethods = map[string]struct{}{
	// Provider instances: create/edit/remove/setDefault all write the host's
	// instance config.
	appwire.MethodEvenerInstanceCreate:     {},
	appwire.MethodEvenerInstanceEdit:       {},
	appwire.MethodEvenerInstanceRemove:     {},
	appwire.MethodEvenerInstanceSetDefault: {},

	// Launch config: writing a layer and trusting a repo both mutate the host.
	appwire.MethodEvenerLaunchSetLayer:  {},
	appwire.MethodEvenerLaunchTrustRepo: {},

	// Marketplaces and plugins: the add/remove/edit/install/upgrade/toggle
	// surface, plus checkNow. (browse/list/refresh/preview are reads.)
	appwire.MethodEvenerMarketplaceAdd:       {},
	appwire.MethodEvenerMarketplaceRemove:    {},
	appwire.MethodEvenerMarketplaceEdit:      {},
	appwire.MethodEvenerPluginInstall:        {},
	appwire.MethodEvenerPluginUpgrade:        {},
	appwire.MethodEvenerPluginRemove:         {},
	appwire.MethodEvenerPluginEnable:         {},
	appwire.MethodEvenerPluginDisable:        {},
	appwire.MethodEvenerPluginSetAutoUpgrade: {},
	// checkNow is a mutation despite its name: it runs one auto-upgrade daemon
	// pass on demand (app_plugin_autoupgrade.go's runPluginAutoUpgradeTick),
	// which refreshes every marketplace clone and installs the new content of
	// every opted-in git-backed plugin whose upstream moved. Its own catalog
	// description says as much — "per plugin actually upgraded" (protocol.go) —
	// and a lost response cannot be told apart from one where the pass upgraded
	// plugins and only the answer was lost. Reporting that as SessionUnavailable
	// would invite a retry of a call that already changed the host's installed
	// plugin versions, so the outcome is reported as unknown instead.
	appwire.MethodEvenerPluginCheckNow: {},

	// Auth and credentials: a login flow, a logout, or a credential write
	// changes what the host can authenticate as. device/poll can complete the
	// device flow and store credentials, so it is a mutation too.
	appwire.MethodEvenerAuthLoginStart:        {},
	appwire.MethodEvenerAuthLoginComplete:     {},
	appwire.MethodEvenerAuthLogout:            {},
	appwire.MethodEvenerAuthApiKeySet:         {},
	appwire.MethodEvenerAuthApiKeyClear:       {},
	appwire.MethodEvenerAuthCredentialJsonSet: {},
	appwire.MethodEvenerAuthDeviceStart:       {},
	appwire.MethodEvenerAuthDevicePoll:        {},

	// The personal AGENTS.md, and the spawn form's directory creation.
	appwire.MethodEvenerSettingsAgentsDocSet: {},
	appwire.MethodEvenerDirsCreate:           {},
}

// remoteHostConfigNotifications is the exact set of host-owned config
// notifications the fan-out re-emits to the controller's browser clients,
// wrapped in evener/host/notification. Any other remote notification is
// dropped: the wrapper is not a general-purpose remote-notification tunnel.
//
// This set is the Go-side contract only. The browser-side unwrapping that puts
// a wrapped notification back on a host-scoped store is component 07b (see
// appwire.HostNotificationParams); no Go-side consumer reads the wrapper here.
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

const (
	// hostNotificationRetryBase is the fan-out's first retry delay after a host
	// is offline or a subscription ends.
	hostNotificationRetryBase = time.Second
	// hostNotificationRetryMax caps the exponential retry delay. It matches the
	// component-04 supervisor's own ceiling, so a terminally failed host costs
	// one cheap in-memory Online() check per interval — never an SSH preflight
	// per second for the life of the process.
	hostNotificationRetryMax = 30 * time.Second
)

// isRemoteHostConfigNotification reports whether method is one of the host-owned
// config notifications the fan-out re-emits. It is installed on each remote
// source as the host subscription's publish-time filter: a burst of thread
// notifications must not evict the rare config update from the fan-out's bounded
// buffer. relayHostNotifications keeps its own check as defense in depth.
func isRemoteHostConfigNotification(method string) bool {
	_, ok := remoteHostConfigNotifications[method]
	return ok
}

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
// lifetime; production passes the RPC server's own lifetime handle
// (appserver.Server.Lifetime), so a shut-down server stops its fan-outs instead
// of leaving them subscribed to the previous server's sources.
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
	// Normalize once: the host registry trims what it is given, the source
	// registry looks the id up verbatim, so a padded id would pass the first
	// lookup and fail the second with a misleading "not attached".
	host := strings.TrimSpace(params.Host)
	if _, ok := c.hosts.Get(host); !ok {
		return nil, appwire.InvalidParams(fmt.Sprintf("unknown host %q", host))
	}
	// Validate the allow-list before resolving the source: a method the proxy may
	// never forward is refused identically whether the host is online or not, and
	// refused without consulting the source registry at all.
	if _, ok := remoteHostAdminMethods[params.Method]; !ok {
		return nil, appwire.InvalidParams(fmt.Sprintf("method %q is not a permitted remote admin method", params.Method))
	}
	remote, err := c.remoteSourceFor(host)
	if err != nil {
		return nil, err
	}
	// A method that mutates the remote host is forwarded on the
	// outcome-unknown mapping: a lost response reports that the change may or
	// may not have been applied rather than a plain channel failure a caller
	// might blind-retry. A read keeps AdminCall's SessionUnavailable mapping.
	var out json.RawMessage
	var callErr error
	if _, mutating := remoteHostAdminMutationMethods[params.Method]; mutating {
		callErr = remote.AdminMutationCall(ctx, params.Method, params.Params, &out)
	} else {
		callErr = remote.AdminCall(ctx, params.Method, params.Params, &out)
	}
	if callErr != nil {
		return nil, callErr
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
		// Filter at publish time, before the notification reaches the bounded
		// per-subscription buffer, so a thread-notification burst cannot evict the
		// rare config update the fan-out exists to deliver.
		remote.SetHostNotificationFilter(isRemoteHostConfigNotification)
		go c.fanOut(ctx, remote)
	}
}

// fanOut re-emits remote, a host's config notifications to the controller's
// browser clients, tagged with the host, until ctx ends. It re-subscribes
// whenever the subscription ends — SubscribeHostNotifications closes its
// channel when the host's channel (or the subscription's context) goes away —
// so a reconnect rebinds the fresh client without a restart.
//
// Each cycle runs under its own child context, cancelled before the next cycle
// begins, so the subscription's pump goroutine ends with the cycle that created
// it and a flapping host never accumulates one goroutine per reconnect.
//
// A host with no live channel is never driven through the connector: Online()
// reads the component-04 attachment signal without spawning SSH, and the
// component-04 supervisor owns (re)connecting the host. Retries use bounded
// exponential backoff, so a permanently-down host costs one in-memory check per
// interval rather than an SSH preflight every second.
func (c *hubHostAdminController) fanOut(ctx context.Context, remote *appsource.RemoteHubSource) {
	host := remote.ID()
	delay := hostNotificationRetryBase
	for ctx.Err() == nil {
		if !remote.Online() {
			if !hostNotificationBackoff(ctx, delay) {
				return
			}
			delay = nextHostNotificationBackoff(delay)
			continue
		}
		subCtx, cancel := context.WithCancel(ctx)
		notifications, err := remote.SubscribeHostNotifications(subCtx)
		if err != nil {
			cancel()
			if !hostNotificationBackoff(ctx, delay) {
				return
			}
			delay = nextHostNotificationBackoff(delay)
			continue
		}
		delay = hostNotificationRetryBase
		c.relayHostNotifications(ctx, host, notifications)
		cancel()
		if !hostNotificationBackoff(ctx, delay) {
			return
		}
		delay = nextHostNotificationBackoff(delay)
	}
}

// relayHostNotifications forwards one subscription's allow-listed config
// notifications until the subscription ends (channel close) or ctx ends. It
// selects on ctx.Done so a canceled context unblocks the loop even while the
// subscription channel is still open.
func (c *hubHostAdminController) relayHostNotifications(ctx context.Context, host string, notifications <-chan appwire.Notification) {
	for {
		select {
		case <-ctx.Done():
			return
		case notification, ok := <-notifications:
			if !ok {
				return
			}
			if _, ok := remoteHostConfigNotifications[notification.Method]; !ok {
				continue
			}
			c.broadcaster.BroadcastAll(appwire.NotifyEvenerHostNotification, appwire.HostNotificationParams{
				Host:   host,
				Method: notification.Method,
				Params: notification.Params,
			})
		}
	}
}

// hostNotificationBackoff waits delay, reporting false when ctx ended first.
func hostNotificationBackoff(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// nextHostNotificationBackoff doubles delay, capped at hostNotificationRetryMax.
func nextHostNotificationBackoff(delay time.Duration) time.Duration {
	next := delay * 2
	if next > hostNotificationRetryMax {
		return hostNotificationRetryMax
	}
	return next
}
