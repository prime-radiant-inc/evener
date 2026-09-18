package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
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
//
// The spawn form's own forwarded subset is checked in at
// host_request_methods.txt, the one list the web UI's inventory test and
// TestHostAdminAllowListCoversSharedForwardedMethods both read: a method
// deleted here (with its policy row and retry classification, which keep this
// package's other tests green) still fails that cross-language test rather than
// leaving the browser to forward a call this proxy answers with
// InvalidParams.
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
	// hosts is the controller's live host registry — the one shared instance
	// the attach and host-management handlers also validate against, so a
	// host added at runtime is administrable without a restart. Its Get is
	// the unknown-host authority.
	hosts *hostreg.Registry
	// sources is the component-05 registry; a remote host's source is where
	// attachment state (Online) and the per-host client live.
	sources *appsource.Registry
	// attachWakeMu guards attachWake: the per-host wakeup channels the
	// EventAttached path signals so a backoff-sleeping fan-out rebinds its
	// host's fresh client immediately instead of waiting out the delay.
	attachWakeMu sync.Mutex
	attachWake   map[string]chan struct{}
	// fanOutMu guards fanOuts, the live per-host fan-out handles, so a
	// re-added host's fresh source replaces the removed entry's loop instead
	// of running beside it and double-delivering every notification.
	fanOutMu sync.Mutex
	fanOuts  map[string]context.CancelFunc
}

func newHubHostAdminController(broadcaster hostNotificationBroadcaster, hosts *hostreg.Registry, sources *appsource.Registry) *hubHostAdminController {
	return &hubHostAdminController{broadcaster: broadcaster, hosts: hosts, sources: sources, attachWake: map[string]chan struct{}{}, fanOuts: map[string]context.CancelFunc{}}
}

// registerHostAdminHandlers installs the proxy handler and starts one
// notification fan-out per remote host. ctx bounds the fan-out goroutines'
// lifetime; production passes the RPC server's own lifetime handle
// (appserver.Server.Lifetime), so a shut-down server stops its fan-outs instead
// of leaving them subscribed to the previous server's sources.
//
// It returns the controller so the caller can wire its hostAttached wakeup
// into the sshconn attach-event path (main.go's OnEvent): without that wiring
// an EventAttached wakes the navigation snapshot but not a fan-out sleeping in
// backoff, which may then wait up to hostNotificationRetryMax before
// subscribing while the new client's notification buffer fills.
func registerHostAdminHandlers(ctx context.Context, server *appserver.Server, hosts *hostreg.Registry, sources *appsource.Registry) *hubHostAdminController {
	// hosts is the one live registry the server constructor resolved — the
	// same instance the attach and host-management handlers share — so the
	// proxy's unknown-host authority covers a host added at runtime instead
	// of refusing it as unknown against a static copy of the configured
	// entries.
	controller := newHubHostAdminController(server, hosts, sources)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostRequest, controller.Request)
	controller.start(ctx)
	return controller
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

// start launches one fan-out goroutine per remote host source, and keeps
// launching for sources registered later: the shared source registry is
// authoritative for which hosts exist, so a host the management surface adds
// at runtime gains its notification fan-out the moment its source registers,
// rather than never (a one-shot enumeration here would miss every host added
// after construction).
func (c *hubHostAdminController) start(ctx context.Context) {
	if c.sources == nil {
		return
	}
	c.sources.SetOnAdd(func(source appsource.Source) { c.launchFanOut(ctx, source) })
	for _, source := range c.sources.All() {
		c.launchFanOut(ctx, source)
	}
}

// launchFanOut starts source's host notification fan-out, replacing any loop
// already running under the same name: a remove/re-add registers a fresh
// source, and the previous loop running beside the new one would
// double-deliver every notification. The cancelled predecessor stands down on
// its next wakeup, or as soon as its current relay ends.
func (c *hubHostAdminController) launchFanOut(ctx context.Context, source appsource.Source) {
	remote, ok := source.(*appsource.RemoteHubSource)
	if !ok {
		return
	}
	// Filter at publish time, before the notification reaches the bounded
	// per-subscription buffer, so a thread-notification burst cannot evict the
	// rare config update the fan-out exists to deliver.
	remote.SetHostNotificationFilter(isRemoteHostConfigNotification)
	fanCtx, cancel := context.WithCancel(ctx)
	c.fanOutMu.Lock()
	if stop, ok := c.fanOuts[remote.ID()]; ok {
		stop()
	}
	c.fanOuts[remote.ID()] = cancel
	c.fanOutMu.Unlock()
	go c.fanOut(fanCtx, remote)
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
//
// hostAttached wakes this host's fan-out out of backoff: the sshconn
// EventAttached path calls it once the fresh channel is installed, so the
// fan-out re-checks Online() and subscribes through ClientIfAttached
// immediately instead of sleeping up to hostNotificationRetryMax while the new
// client's notification buffer (appwire.NotificationBufferCap, a loud
// overflow) fills undrained. The wakeup carries no client: the fan-out still
// resolves the fresh client through the source's attached-only lookup, so the
// r6 liveness refusal stands — a host that dropped between the event and the
// wakeup resolves to SessionUnavailable and the loop backs off again.
func (c *hubHostAdminController) fanOut(ctx context.Context, remote *appsource.RemoteHubSource) {
	host := remote.ID()
	delay := hostNotificationRetryBase
	for ctx.Err() == nil {
		if !remote.Online() {
			if !c.hostNotificationBackoffOrAttach(ctx, host, delay) {
				return
			}
			delay = nextHostNotificationBackoff(delay)
			continue
		}
		subCtx, cancel := context.WithCancel(ctx)
		notifications, err := remote.SubscribeHostNotifications(subCtx)
		if err != nil {
			cancel()
			if !c.hostNotificationBackoffOrAttach(ctx, host, delay) {
				return
			}
			delay = nextHostNotificationBackoff(delay)
			continue
		}
		delay = hostNotificationRetryBase
		c.relayHostNotifications(ctx, host, notifications)
		cancel()
		if !c.hostNotificationBackoffOrAttach(ctx, host, delay) {
			return
		}
		delay = nextHostNotificationBackoff(delay)
	}
}

// hostAttached signals host's fan-out to cut its backoff short and re-check
// attachment now. It is the EventAttached half of the attach-event path: the
// same transition already pokes the remote-thread refresher (main.go's
// onAttach) and invalidates the navigation snapshot, and this rides that same
// event rather than inventing a second one. The signal is a buffered wakeup
// per host, created on demand: an attach with no running fan-out (an unknown
// host, or a host whose fan-out already subscribed) parks one pending wakeup
// the next backoff takes immediately, which is harmless — the fan-out still
// re-checks Online() and the attached-only lookup before subscribing.
func (c *hubHostAdminController) hostAttached(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	c.attachWakeMu.Lock()
	ch, ok := c.attachWake[host]
	if !ok {
		ch = make(chan struct{}, 1)
		c.attachWake[host] = ch
	}
	c.attachWakeMu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// hostNotificationBackoffOrAttach waits delay but returns true early when
// the host's attach wakeup fires, reporting false only
// when ctx ended first. The caller re-checks Online() and re-resolves the
// client through the attached-only lookup, so a stale or spurious wakeup
// cannot subscribe a dead generation: it just shortens one sleep.
func (c *hubHostAdminController) hostNotificationBackoffOrAttach(ctx context.Context, host string, delay time.Duration) bool {
	c.attachWakeMu.Lock()
	ch, ok := c.attachWake[host]
	if !ok {
		ch = make(chan struct{}, 1)
		c.attachWake[host] = ch
	}
	c.attachWakeMu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-ch:
		return true
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

// nextHostNotificationBackoff doubles delay, capped at hostNotificationRetryMax.
func nextHostNotificationBackoff(delay time.Duration) time.Duration {
	next := delay * 2
	if next > hostNotificationRetryMax {
		return hostNotificationRetryMax
	}
	return next
}
