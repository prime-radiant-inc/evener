package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
)

// newHubSourceRegistry builds the hub's sources over cfg.Roster. The hub always
// wires a roster (main.go). Without one there is no local source at all, so a
// lookup of a local ref fails as "source not found" rather than finding a
// source that lists nothing.
func newHubSourceRegistry(cfg hubcore.WebConfig) *appsource.Registry {
	registry := appsource.NewRegistry()
	roster := cfg.Roster
	if roster != nil {
		local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
			return localDaemonEntriesFromRoster(roster.List())
		}, http.DefaultClient)
		// A daemon that leaves for good is announced by the roster, the one place
		// that sees its process or its file go; the relay tells that daemon's
		// subscribers to re-read.
		// The roster's resolved session id is passed along: a legacy entry names
		// no session of its own, and its relay session is keyed by the resolved one.
		roster.SetOnSessionGone(func(gone hubcore.LiveEntry) { local.AnnounceDaemonGone(gone.Entry, gone.SessionID) })
		registry.Add(local)
	}
	if len(cfg.RemoteHosts) > 0 {
		if cfg.RemoteHostClient == nil {
			names := make([]string, 0, len(cfg.RemoteHosts))
			for _, host := range cfg.RemoteHosts {
				names = append(names, host.Name)
			}
			_, _ = fmt.Fprintf(os.Stderr, "[hub] remote hosts skipped (no SSH client wired): %s\n", strings.Join(names, ", "))
		} else {
			for _, host := range cfg.RemoteHosts {
				source := appsource.NewRemoteHubSource(host.Name, host.Roots, cfg.RemoteHostClient)
				// The non-dialing seams every non-explicit read path resolves
				// through: the attached-only client lookup and the attach
				// handshake facts (component 05, §"Registration and
				// default-source selection").
				source.SetHostClientIfAttached(cfg.RemoteHostClientIfAttached)
				source.SetHostFacts(cfg.RemoteHostFacts)
				source.SetHostHandshake(cfg.RemoteHostHandshake)
				source.SetHostOnline(func() bool {
					return cfg.RemoteHostOnline == nil || cfg.RemoteHostOnline(host.Name)
				})
				// The identity generation is assigned before the source
				// becomes registry-visible, the same discipline the host
				// manager's runtime add follows: a refresh walk may enumerate
				// the source the moment it is added, and the walk captures
				// the source's generation immediately before it reads it
				// (round 10) — so every enumerable source must carry a
				// generation from the instant it is enumerable, or the
				// publish drops its rows as unowned.
				// Configured hosts never pass through the manager, so theirs
				// is assigned here; the local source needs none — the walk
				// skips it, so it can never own walk-published rows.
				if cfg.RemoteThreadCache != nil {
					cfg.RemoteThreadCache.RegisterSource(host.Name)
				}
				registry.Add(source)
			}
		}
	}
	return registry
}

// localDaemonEntriesFromRoster is the local source's view of a roster's live
// entries: crashed ones skipped, in-process descendants addressed as their own
// AppWire threads served by their owner's endpoint.
func localDaemonEntriesFromRoster(live []hubcore.LiveEntry) []appsource.LocalDaemonEntry {
	entries := make([]appsource.LocalDaemonEntry, 0, len(live))
	for _, item := range live {
		if item.Crashed {
			continue
		}
		entry := appsource.LocalDaemonEntry{
			Entry:         item.Entry,
			SessionID:     item.SessionID,
			Status:        item.Status,
			PendingAsk:    item.PendingAsk,
			RunningJobs:   item.RunningJobs,
			CompletedJobs: item.CompletedJobs,
			Watches:       item.Watches,
		}
		entries = append(entries, entry)
		// In-process descendants are addressed as their own AppWire
		// threads, but are served by their owner's daemon endpoint.
		for _, childID := range item.RunningSubagentIDs {
			child := entry
			child.OwnerSessionID = entry.SessionID
			child.SessionID = childID
			// The alias carries the child's OWN watches, sampled by
			// the prober into ChildWatches. Inheriting the root
			// entry's Watches would put the root's rows on the
			// child row (and, for a read-only alias, they were
			// suppressed anyway), losing the child's own.
			child.Watches = appwire.CloneEvenerWatches(item.ChildWatches[childID])
			// The child's own projected status when the daemon carries
			// it — inheriting the parent's status would render a
			// settled delegate as working (or vice versa). "" (old
			// daemon) keeps the inherited status, the pre-states
			// behavior.
			if childState := strings.TrimSpace(item.RunningSubagentStates[childID]); childState != "" {
				child.Status = childState
			}
			child.ReadOnlyAlias = true
			entries = append(entries, child)
		}
	}
	return entries
}

var (
	resolveTurnStartSource = sourceForThread
	resumeTurnStartThread  = hubThreadAutoResume
	authLoginComplete      = func(c *hubAuthController, ctx context.Context, p appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
		return c.LoginComplete(ctx, p)
	}
	authDevicePoll = func(c *hubAuthController, ctx context.Context, p appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		return c.DevicePoll(ctx, p)
	}
	launchTrustRepo = func(c *hubLaunchController, ctx context.Context, p appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
		return c.TrustRepo(ctx, p)
	}
)

type threadReadRelayPolicy interface {
	// RelayOnThreadRead reports whether a plain (non-Subscribe) thread/read
	// starts a relay. A Subscribe read still overrides it.
	RelayOnThreadRead() bool
}

// threadRelayCapableSource reports whether a source can serve thread relays at
// all. A source that cannot is never relayed, even for a Subscribe read:
// startRelay calls SubscribeThread, so relaying it would fail the read instead
// of returning the snapshot. Sources that implement only RelayOnThreadRead keep
// the Subscribe-overrides-plain-read policy.
type threadRelayCapableSource interface {
	SupportsThreadRelay() bool
}

func sourceSupportsThreadRelay(source appsource.Source) bool {
	if capable, ok := source.(threadRelayCapableSource); ok {
		return capable.SupportsThreadRelay()
	}
	return true
}

// threadReadLocalImagePolicy reports whether a source's threads describe files
// on this hub's own filesystem. A remote hub source serves its transcript but
// not its filesystem, so its CWDs and tool-argument paths name another machine.
type threadReadLocalImagePolicy interface {
	EnrichThreadFileBackedImages() bool
}

// enrichSourcedThreadImages stamps fetchable image URLs and, for a source whose
// files live on this hub, adds file-backed output-image descriptors by reading
// the session's working directory.
//
// A source whose images are not local is neither stamped nor enriched: the
// thread is returned with its remote-supplied image routes neutralized. Running
// the local file pass on a remote CWD would probe unrelated controller-local
// paths and could attach descriptors for files the thread never wrote, and
// stampThreadImageURLs mints this hub's sha-addressed /s/<session>/images/<sha>
// route, which handleSessionImage resolves against this hub's own Past index. A
// remote session is never in it, so a remote-supplied route would 404 (or, on a
// session-id collision, serve another session's bytes) once the browser
// requested it from this hub. Neutralizing it leaves the descriptor's SHA for
// the controller-side proxy that will resolve it, which is not yet part of this
// read path.
func enrichSourcedThreadImages(source appsource.Source, thread appwire.Thread) appwire.Thread {
	if !threadImagesLocal(source) {
		return stripRemoteImageRoutes(thread)
	}
	return enrichLocalSourcedThreadImages(thread)
}

// threadImagesLocal reports whether a source's threads describe files on this hub's
// own filesystem. A source that does not implement the policy serves this hub's own
// sessions, so its threads are local.
func threadImagesLocal(source appsource.Source) bool {
	if policy, ok := source.(threadReadLocalImagePolicy); ok {
		return policy.EnrichThreadFileBackedImages()
	}
	return true
}

// enrichLocalSourcedThreadImages runs the two image passes that need this hub's own
// view of the thread: the sha-addressed route stamp, which is minted from the
// session id, and the file-backed pass, which reads the thread's CWD here.
func enrichLocalSourcedThreadImages(thread appwire.Thread) appwire.Thread {
	thread = stampThreadImageURLs(thread)
	return enrichThreadFileBackedOutputImages(thread)
}

// stripRemoteImageRoutes removes hub-relative image routes from a thread whose
// images live on another hub. A relative route is meaningful only against the
// origin that minted it: left on a remote thread, it would make the browser
// request this hub's own route for a session this hub does not have. The remote
// hub mints both /s/<session>/images/<sha> (stamped by stampThreadImageURLs) and
// /doc/image?session=<session>&path=<rel> (attached to file-backed output images
// by outputImagesForToolCall/resolveOutputImageFile), so any root-relative path
// must be neutralized, not just the /s/... one. External URLs and data: URLs are
// untouched, because the browser resolves them against their own origin.
func stripRemoteImageRoutes(thread appwire.Thread) appwire.Thread {
	for turnIndex := range thread.Turns {
		items := thread.Turns[turnIndex].Items
		for itemIndex := range items {
			for imageIndex := range items[itemIndex].Images {
				if isHubRelativeImageRoute(items[itemIndex].Images[imageIndex].URL) {
					items[itemIndex].Images[imageIndex].URL = ""
				}
			}
			for imageIndex := range items[itemIndex].OutputImages {
				if isHubRelativeImageRoute(items[itemIndex].OutputImages[imageIndex].URL) {
					items[itemIndex].OutputImages[imageIndex].URL = ""
				}
			}
		}
	}
	return thread
}

// isHubRelativeImageRoute reports whether raw is an origin-relative image route
// of the form a hub mints for itself, e.g. /s/<session>/images/<sha> or
// /doc/image?session=<session>&path=<rel>. A leading slash makes a URL resolve
// against the serving origin, so it is meaningful only on the hub that minted
// it. A network-path reference (//host/...) and scheme URLs (http:, https:,
// data:, ...) name their own origin and are left untouched.
func isHubRelativeImageRoute(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//")
}

func relayOnThreadRead(source appsource.Source) bool {
	if policy, ok := source.(threadReadRelayPolicy); ok {
		return policy.RelayOnThreadRead()
	}
	return true
}

// listItemTurns returns a packed item-mode page when the source has item
// candidates or when its source page contains data. A legacy source with
// no data or a ListTurns error is left for the caller's saved-transcript
// fallback; candidate and packing errors are terminal just as they are for a
// native ItemCandidateSource.
func listItemTurns(
	ctx context.Context,
	source appsource.Source,
	params appwire.ThreadTurnsListParams,
	logf func(format string, args ...any),
) (appwire.ThreadTurnsListResponse, bool, error) {
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, true, err
	}
	params.ItemsView = string(appwire.TurnItemsViewFragment)
	var live appwire.ThreadTurnsListResponse
	var candidates transcriptItemCandidateResult
	if _, native := source.(appsource.ItemCandidateSource); native {
		candidates, err = sourceItemCandidateResultForList(ctx, source, params, live)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, true, err
		}
	} else {
		live, err = source.ListTurns(ctx, params)
		if err != nil || len(live.Data) == 0 {
			return live, false, err
		}
		candidates, err = sourceItemCandidateResultForList(ctx, source, params, live)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, true, err
		}
	}

	meta, metaErr := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false})
	if metaErr != nil && logf != nil {
		logf("thread turns metadata enrichment unavailable: %v", metaErr)
	}
	packed, packErr := packThreadTurnsItemCandidates(candidates, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		thread := appwire.Thread{Turns: response.Data}
		if metaErr == nil {
			thread.ID = meta.Thread.ID
			thread.SessionID = meta.Thread.SessionID
			thread.CWD = meta.Thread.CWD
		}
		// Image handling is independent of the optional metadata read. A remote
		// source's root-relative routes are neutralized whether or not that read
		// succeeded — nothing about neutralizing them needs the session id or CWD it
		// would have supplied, and a route left on the page resolves against this
		// hub's origin for a session this hub does not have. The two local passes do
		// need those, so they run only after a successful read.
		switch {
		case !threadImagesLocal(source):
			thread = stripRemoteImageRoutes(thread)
		case metaErr == nil:
			thread = enrichLocalSourcedThreadImages(thread)
		}
		response.Data = thread.Turns
		return response, nil
	}, itemLimit)
	if packErr != nil {
		return appwire.ThreadTurnsListResponse{}, true, packErr
	}
	return packed, true, nil
}

func blockedUnknownMutationError(clientMutationID string, err error) error {
	if isDaemonRestartRequiredError(err) || isSessionRecoveryAdmissionError(err) {
		return blockedAdmissionMutationError(err, clientMutationID)
	}
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: err.Error(),
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionBlocked,
			Cause:            "persistenceUnavailable",
		},
	}
}

// allowsPastFallbackAfterLiveReadFailure preserves atomic rejoin once a live
// relay is available. A subscribed local read with no rendezvous entry never
// acquired a relay, so it may still hydrate the persisted transcript.
func allowsPastFallbackAfterLiveReadFailure(source appsource.Source, params appwire.ThreadReadParams, err error) bool {
	if !params.Subscribe {
		return true
	}
	_, requiresLiveHandoff := source.(appsource.RelaySessionSource)
	return !requiresLiveHandoff || isDeadSessionError(err)
}

// hubLaunchConfigRoot resolves cfg.LaunchConfigRoot, falling back to
// cmdutil.DefaultConfigRoot() when unset (a zero-value WebConfig built
// directly, as some tests do).
func hubLaunchConfigRoot(cfg hubcore.WebConfig) string {
	if cfg.LaunchConfigRoot != "" {
		return cfg.LaunchConfigRoot
	}
	return cmdutil.DefaultConfigRoot()
}

// hubAuthStateRoot is where the auth controller keeps OAuth records: the
// registry's state root, because registry credential resolution reads
// auth/<instance>.json from there. The hub loads its registry at
// cmdutil.DefaultStateRoot() whatever hub_state_root says, so a record kept
// under HubStateRoot would be a login the registry, the credential probe and
// every spawned child never see. Before the first successful load, or with no
// registry wired (a bare test config), that same default is the answer.
func hubAuthStateRoot(reg *hubcore.ProviderRegistry) string {
	if reg != nil {
		if r := reg.Get(); r != nil {
			return r.StateRoot()
		}
	}
	return cmdutil.DefaultStateRoot()
}

func newHubAppServer(cfg hubcore.WebConfig, sources *appsource.Registry) *appserver.Server {
	return newHubAppServerWithNavigation(cfg, sources, nil, nil)
}

func newHubAppServerWithNavigation(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver) *appserver.Server {
	server, _, _ := newHubAppServerWithNavigationAndTrace(cfg, sources, navigation, resolve, nil)
	return server
}

// newHubAppServerWithNavigationAndTrace builds the RPC server and registers
// every handler. cfg.PluginManager, when set, is the one every plugin
// handler here uses (newWebServer constructs it and wires it, after this
// function returns, to the very server it built, so it wires nothing here);
// nil falls back to a fresh plugins.NewManager(cfg.PluginRoot), wired to this
// server directly, for a caller that never builds through newWebServer
// (most tests, and any embedder calling this constructor's exported
// wrappers directly).
func newHubAppServerWithNavigationAndTrace(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver, appwireTrace *appserver.WebSocketTrace) (*appserver.Server, *hubHostAdminController, *hubHostManager) {
	// One fallback registry when cfg carries no live one, built once here so
	// every host surface below — attach, management, and the admin proxy —
	// validates against the same instance: a host added at runtime must be
	// attachable and administrable, never "unknown" to a sibling handler
	// that built its own copy from the configured entries. main.go always
	// threads the live registry; the fallback is the embedder/test shape
	// (newWebServer nil-defaults its own cfg copy the same way).
	if cfg.RemoteHostRegistry == nil {
		cfg.RemoteHostRegistry = hostRegistryFromConfig(cfg)
	}
	capability := &appwire.NavigationCapability{Version: 1}
	var capabilityProvider func() *appwire.NavigationCapability
	if navigation != nil {
		capability = nil
		capabilityProvider = func() *appwire.NavigationCapability {
			return navigation.Capability()
		}
	}
	hubLogf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[hub] "+format+"\n", args...)
	}
	server := appserver.NewServer(appserver.ServerConfig{
		ServerName:           "evener-hub",
		Version:              Version,
		SourceID:             "local",
		WebSocketTrace:       appwireTrace,
		Navigation:           capability,
		NavigationCapability: capabilityProvider,
		Logf:                 hubLogf,
		ConnectionAdmissionContext: func(ctx context.Context) context.Context {
			return admitSessionConnection(ctx, cfg)
		},
		RequestAdmissionContext: func(ctx context.Context, message appwire.Message) context.Context {
			return admitSessionRecovery(ctx, cfg, message)
		},
		SubscriptionAdmissionResolverV2: func(msg appwire.Message) appserver.SubscriptionAdmissionResolution {
			notSubscribe := appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionNotSubscribe}
			if msg.Request == nil || (msg.Request.Method != appwire.MethodThreadRead && msg.Request.Method != appwire.MethodThreadUnsubscribe) {
				return notSubscribe
			}
			var params appwire.ThreadReadParams
			if msg.Request.Method == appwire.MethodThreadUnsubscribe {
				var unsubscribe appwire.ThreadUnsubscribeParams
				if json.Unmarshal(msg.Request.Params, &unsubscribe) != nil {
					return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
				}
				params.Ref, params.ThreadID = unsubscribe.Ref, unsubscribe.ThreadID
			} else if json.Unmarshal(msg.Request.Params, &params) != nil {
				return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
			}
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				if _, parseErr := appwire.ParseRef(strings.TrimSpace(params.Ref)); params.Ref != "" && parseErr != nil {
					return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
				}
				if msg.Request.Method == appwire.MethodThreadRead && params.Subscribe {
					if past, ok := pastEntryForRead(cfg, params); ok && past.ID != "" {
						return appserver.SubscriptionAdmissionResolution{Key: "local:" + past.ID, Intent: appserver.SubscriptionAdmissionResolved}
					}
				}
				return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionUnresolved}
			}
			if msg.Request.Method == appwire.MethodThreadRead && !params.Subscribe && !relayOnThreadRead(source) {
				return notSubscribe
			}
			// Stable refs and current IDs name one pending admission, but
			// must not rewrite the relay's notification delivery identity.
			if daemon, ok := source.(*appsource.LocalDaemonSource); ok {
				ref, err := daemon.ResolveSubscriptionAdmission(params)
				if err != nil && msg.Request.Method == appwire.MethodThreadRead && params.Subscribe {
					if past, pastOK := pastEntryForRead(cfg, params); pastOK && past.ID != "" {
						return appserver.SubscriptionAdmissionResolution{Key: "local:" + past.ID, Intent: appserver.SubscriptionAdmissionResolved}
					}
					if cfg.Roster != nil {
						if key, ok := cfg.Roster.RestartRequiredRootRef(normalizedAdmissionRef(params)); ok {
							return appserver.SubscriptionAdmissionResolution{Key: key, Intent: appserver.SubscriptionAdmissionResolved}
						}
					}
				}
				if err != nil {
					if msg.Request.Method == appwire.MethodThreadUnsubscribe {
						delivery := normalizedAdmissionRef(params)
						if source != nil {
							if resolvedDelivery, _, deliveryErr := threadRelayTarget(source, params); deliveryErr == nil {
								delivery = resolvedDelivery
							}
						}
						if delivery != "" {
							return appserver.SubscriptionAdmissionResolution{Key: delivery, SecondaryKey: normalizedAdmissionRef(params), Intent: appserver.SubscriptionAdmissionUnresolved}
						}
					}
					return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionUnresolved}
				}
				if delivery, _, deliveryErr := threadRelayTarget(source, params); deliveryErr == nil {
					return appserver.SubscriptionAdmissionResolution{Key: ref.String(), SecondaryKey: delivery, Intent: appserver.SubscriptionAdmissionResolved}
				}
				return appserver.SubscriptionAdmissionResolution{Key: ref.String(), Intent: appserver.SubscriptionAdmissionResolved}
			}
			// A federated source is keyed by the ref it was addressed with
			// (relayDeliveryTarget), so a read carrying both the stable ref and
			// the thread's current ID admits under the identity a ref-only
			// thread/unsubscribe resolves.
			key, _, err := relayDeliveryTarget(source, params)
			if err != nil {
				return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
			}
			return appserver.SubscriptionAdmissionResolution{Key: key, Intent: appserver.SubscriptionAdmissionResolved}
		},
		Features: appwire.FeatureSet{
			ThreadList:                true,
			ThreadTurnsList:           true,
			TurnStart:                 true,
			TurnSteer:                 true,
			ThreadClear:               true,
			ThreadShutdown:            true,
			ForkFromTurn:              true,
			Tasks:                     true,
			TranscriptList:            true,
			ModelList:                 true,
			DirectoryComplete:         true,
			Auth:                      true,
			TranscriptDisplaySettings: true,
			KeybindingsSettings:       true,
		},
	})
	authController := newHubAuthControllerWithStore(hubAuthStateRoot(cfg.Registry), cfg.CredsStore)
	authController.reg = cfg.Registry
	authController.providersConfigPath = cfg.ProvidersConfigPath
	authController.noUserLayer = cfg.NoUserLayer
	var instancesController *hubInstancesController
	if cfg.Registry != nil && cfg.ProvidersConfigPath != "" {
		instancesController = &hubInstancesController{
			reg:                 cfg.Registry,
			providersConfigPath: cfg.ProvidersConfigPath,
			auth:                authController,
		}
	}
	// cfg.PluginManager, when the caller (newWebServer) already set it, is the
	// one Manager every plugin surface below shares: this controller,
	// registerPluginAutoUpgradeHandlers, and — via cfg, which
	// registerThreadHandlers below captures by value — hubThreadStart and
	// hubSpawnSlashCatalog's ResolveForLaunch. A caller that never builds
	// through newWebServer (most tests, and any embedder calling
	// newHubAppServer/newHubAppServerWithNavigation directly) leaves it nil:
	// this constructs one and wires it to this server itself, the same way
	// newWebServer wires cfg.PluginManager, so plugin/marketplace mutations
	// and checkNow on this server still broadcast rather than going silent.
	mgr := cfg.PluginManager
	if mgr == nil {
		mgr = plugins.NewManager(cfg.PluginRoot)
		wirePluginStoreBroadcast(mgr, server)
	}
	cfg.PluginManager = mgr
	pluginsController := &hubPluginsController{mgr: mgr, launchConfigRoot: hubLaunchConfigRoot(cfg)}
	relayFunctions := newHubRelayFunctions(server, cfg, sources)
	if observeHubRelayFunctions != nil {
		observeHubRelayFunctions(relayFunctions)
	}
	registerThreadHandlers(server, cfg, sources, relayFunctions, hubLogf)
	registerThreadNameSetHandler(server, cfg, sources, navigation)
	registerAuthHandlers(server, authController)
	registerInstanceHandlers(server, instancesController)
	// launch.toml is user-editable configuration, so its root is the config
	// root, not HubStateRoot (machine-generated state).
	launchController := newHubLaunchController(hubLaunchConfigRoot(cfg), cfg.APILogDefault)
	registerLaunchHandlers(server, launchController)
	registerPluginHandlers(server, pluginsController)
	registerMobilePairingHandler(server, cfg)
	registerNavigationReadHandler(server, navigation)
	registerFavoriteHandler(server, cfg, navigation)
	registerArchiveHandler(server, cfg, func() *NavigationService { return navigation })
	registerDaemonHandlers(server, cfg, sources)
	registerSessionDeleteHandler(server, nil)
	registerPinSectionHandlers(server, cfg, navigation, resolve)
	registerMiscHandlers(server, cfg, sources)
	// Component 06's Connect action: the browser-reachable explicit attach
	// trigger. It wraps the Ensure-backed dialing seam and is the only method
	// that may dial a remote host on the user's behalf.
	registerHostAttachHandler(server, cfg, sources, cfg.RemoteHostRegistry)
	// Component 08 slice 1: the host registry surface (add/list/status/
	// remove). Controller-local, never dials; add/remove invalidate the
	// manifest's sources so the picker converges without a refresh tick.
	// The manager, the live host registry, and the selected hub.toml path all
	// come from cfg — main.go threads the real sshconn.Manager, the one
	// registry shared with the attach handler, and the config path whose
	// sidecar persists UI-added hosts, so the surface is wired, not a
	// placeholder. It returns the manager so newWebServer can expose it
	// (main.go binds its event recorder to the SSH manager's lifecycle).
	hostManage := registerHostManageHandlers(server, sources, cfg, cfg.RemoteHostRegistry, navigation, hubLogf)
	registerPluginAutoUpgradeHandlers(server, pluginsController.mgr)
	registerTranscriptDisplayHandlers(server, cfg.TranscriptDisplayStore)
	registerKeybindingsHandlers(server, cfg.KeybindingsStore)
	registerAgentsDocHandlers(server, hubAgentsDocPath(cfg))
	// Component 07a: the remote-admin proxy and its host-tagged config
	// notification fan-out. Nothing here reads or writes a credential.
	//
	// The fan-out is a server-lifetime worker, not a per-connection one: it must
	// stay subscribed while no browser is connected so that a host's config
	// change is still relayed when one returns, and it re-subscribes itself
	// across client reconnects. Its context is the RPC server's own lifetime
	// handle (round eight), which Shutdown cancels when shutdown begins. Bound
	// this way the fan-out stops with the server it belongs to: a hub server
	// recreated in-process no longer leaves the previous server's fan-outs
	// subscribed forever (one goroutine per remote host, each still holding the
	// old server's sources and broadcaster, which would also duplicate every
	// host notification once a replacement subscribed too). Pinned by
	// TestHostAdminFanOutStopsWhenServerShutdown here and by
	// TestHostAdminFanOutStopsWhenContextCanceled at the controller level.
	// The binding only holds if something actually shuts the server down: the
	// hub's top-level lifecycle drains it unconditionally on the way out
	// (main.go), not only on the tracing path, and does so before the SSH
	// manager closes the transports these fan-outs read from.
	// The returned controller owns the per-host fan-out wakeups: newWebServer
	// (web.go) keeps the handle so main.go can bind hostAttached to the
	// sshconn EventAttached path, waking a backoff-sleeping fan-out the
	// moment its host's fresh channel is installed.
	hostAdmin := registerHostAdminHandlers(server.Lifetime(), server, cfg.RemoteHostRegistry, sources)
	return server, hostAdmin, hostManage
}

func normalizedAdmissionRef(params appwire.ThreadReadParams) string {
	if ref := strings.TrimSpace(params.Ref); ref != "" {
		return ref
	}
	if threadID := strings.TrimSpace(params.ThreadID); threadID != "" {
		return "local:" + threadID
	}
	return ""
}

// registerThreadHandlers registers the thread- and turn-lifecycle RPC handlers
// on the server. The relay closures (startRelay, startTurn, startRelayForThread)
// are constructed by newHubAppServer and passed in so the handlers close over
// the same relay state.
func registerThreadHandlers(
	server *appserver.Server,
	cfg hubcore.WebConfig,
	sources *appsource.Registry,
	relays hubRelayFunctions,
	logf func(format string, args ...any),
) {
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return hubThreadList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if err := appwire.ValidateThreadReadParams(params); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		if params.Ref != "" {
			if _, err := appwire.ParseRef(params.Ref); err != nil {
				return appwire.ThreadReadResponse{}, appwire.InvalidParams(err.Error())
			}
		}
		if cfg.Roster != nil {
			if _, required, ownershipErr := restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID); required || ownershipErr != nil {
				if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
					// Refresh may fail on an unrelated marker. Recheck the target
					// before allowing its saved, non-authoritative projection.
					_, required, ownershipErr = restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID)
					if ctx.Err() != nil || (!required && !isDaemonDiscoveryError(ownershipErr)) {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(err.Error())
					}
				}
			}
		}
		source, err := sourceForThreadWithDeletionFence(cfg, sources, params.Ref, params.ThreadID)
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.ThreadReadResponse{}, err
			}
			resp, ok, pastErr := unavailableThreadReadResponse(ctx, cfg, sources, params)
			if pastErr != nil {
				return appwire.ThreadReadResponse{}, pastErr
			}
			if ok {
				return resp, nil
			}
			return appwire.ThreadReadResponse{}, err
		}
		read, err := relays.readThread(ctx, source, params)
		if err != nil {
			allowPast := allowsPastFallbackAfterLiveReadFailure(source, params, err)
			if _, local := localPastThreadID(params); local && cfg.Roster != nil && daemonOwnershipMayHaveChanged(err) {
				refreshErr := hubRosterRefresh(ctx, cfg.Roster)
				_, required, ownershipErr := restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID)
				if ctx.Err() != nil {
					return appwire.ThreadReadResponse{}, ctx.Err()
				}
				if required {
					allowPast = true
					err = daemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, "")
				} else if isDaemonDiscoveryError(ownershipErr) {
					allowPast = true
				} else {
					if refreshErr != nil {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(refreshErr.Error())
					}
					if ownershipErr != nil {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(ownershipErr.Error())
					}
				}
			}
			if allowPast {
				saved, ok, pastErr := unavailableThreadReadResponse(ctx, cfg, sources, params)
				if pastErr != nil {
					return appwire.ThreadReadResponse{}, pastErr
				}
				if ok {
					return saved, nil
				}
			}
			return appwire.ThreadReadResponse{}, err
		}
		resp := read.response
		liveItemCandidatesEmpty := false
		if params.IncludeTurns {
			if read.hasItemCandidates {
				liveItemCandidatesEmpty = len(read.itemCandidates.Candidates.Candidates) == 0
			} else if candidates, candidateErr := itemCandidateResultFromReadResponse(resp); candidateErr == nil {
				liveItemCandidatesEmpty = len(candidates.Candidates.Candidates) == 0
			}
		}
		resp.Thread, err = mergePastThreadForRead(ctx, cfg, params, resp.Thread)
		resp.Thread = applyThreadResumeRequirement(ctx, cfg, params.Ref, params.ThreadID, resp.Thread)
		resp.Thread = applyHubForkCapability(cfg, resp.Thread)
		if err != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, err
		}
		if params.IncludeTurns {
			usedPastItemPage := false
			if liveItemCandidatesEmpty && len(resp.Thread.Turns) > 0 {
				past, ok, pastErr := pastThreadItemReadResponse(ctx, cfg, params)
				if pastErr != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, pastErr
				}
				if ok {
					resp.Thread.Turns = past.Thread.Turns
					resp.OlderCursor = past.OlderCursor
					resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
					annotateThreadProjects([]appwire.Thread{resp.Thread})
					usedPastItemPage = true
				}
			}
			if !usedPastItemPage {
				candidates := transcriptItemCandidateResultFromSource(read.itemCandidates)
				if !read.hasItemCandidates {
					var candidateErr error
					candidates, candidateErr = sourceItemCandidateResultForRead(ctx, source, params, resp)
					if candidateErr != nil {
						read.finish(false)
						return appwire.ThreadReadResponse{}, candidateErr
					}
				}
				packed, packErr := packThreadReadItemCandidates(candidates, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
					response.Thread = threadWithPackedTurns(resp.Thread, response.Thread.Turns)
					// A live daemon's turns carry sha-addressed tool-result descriptors
					// with no route on them (the daemon does not serve the bytes; this
					// hub does), so route stamping stays inside the final packer.
					response.Thread = enrichSourcedThreadImages(source, response.Thread)
					annotateThreadProjects([]appwire.Thread{response.Thread})
					return response, nil
				}, itemLimit)
				if packErr != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, packErr
				}
				resp = packed
			}
		} else {
			// A live daemon's turns carry sha-addressed tool-result descriptors with
			// no route on them (the daemon does not serve the bytes; this hub does),
			// so the route is stamped here before the file-backed pass adds any
			// /doc/image descriptors of its own.
			resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
			annotateThreadProjects([]appwire.Thread{resp.Thread})
		}
		// Local forks copy persisted history in the hub. A live daemon's
		// own unsupported fork flag does not describe this hub-owned action.
		resp.Thread = applyHubForkCapability(cfg, resp.Thread)
		if err := appwire.ValidateThreadReadItemResponse(resp); err != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, err
		}
		read.response = resp
		if read.handoff != nil {
			if !relays.captureThreadRead(ctx, params, read) {
				return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread subscription is unavailable")
			}
		} else if sourceSupportsThreadRelay(source) && (params.Subscribe || relayOnThreadRead(source)) {
			// A source with no relay fan-out is never relayed, even for a
			// Subscribe read: startRelay calls SubscribeThread, so relaying one
			// would fail the read. Subscribing callers still get the snapshot.
			if err := relays.startRelay(ctx, source, params, resp.Thread); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
		}
		return resp, nil
	})
	// thread/unsubscribe drops only the calling connection's downstream
	// subscription — the browser's own read of a thread it is navigating away
	// from. The relay key is derived by the same helper thread/read's relay
	// uses (relayDeliveryTarget), so the removal lands on the exact registry
	// entry Subscribe created. That helper is deliberately NOT
	// threadRelayTarget: for a federated source (anything but the local
	// daemon) the ref's suffix wins over the caller's bare threadId, because
	// the ref is the stable identity that survives an identity replacement
	// while the thread's current ID moves. Keying such a read by the threadId
	// registered the relay under "host:<currentID>" while a ref-addressed
	// unsubscribe resolved "host:<stableRef>", so the downstream entry — and
	// the source-side subscription behind it — was never dropped. Both ends
	// must keep resolving through relayDeliveryTarget; re-deriving either from
	// threadRelayTarget reintroduces that mismatch. Resolution deliberately
	// uses the plain registry lookup without session activation because an
	// unsubscribe must not start a session just to stop delivering to it. When
	// no source resolves, the ref's own namespace (parsed from the ref itself)
	// is the best key available; Unsubscribe is conn-scoped and idempotent, so
	// a missed key costs only a subscription the connection-close cleanup
	// reaps anyway.
	appserver.HandleTyped(server.Router(), appwire.MethodThreadUnsubscribe, func(ctx context.Context, params appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.EmptyResponse{}, err
			}
			if parsed, parseErr := appwire.ParseRef(strings.TrimSpace(params.Ref)); parseErr == nil && parsed.SourceID != "" {
				appserver.UnsubscribeLifecycle(ctx, parsed.SourceID+":"+parsed.ThreadID)
				return appwire.EmptyResponse{}, nil
			}
			appserver.UnsubscribeLifecycle(ctx, "local:"+strings.TrimSpace(params.ThreadID))
			return appwire.EmptyResponse{}, nil
		}
		relayKey, _, keyErr := relayDeliveryTarget(source, appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
		if keyErr != nil {
			return appwire.EmptyResponse{}, keyErr
		}
		appserver.UnsubscribeLifecycle(ctx, relayKey)
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if err := appwire.ValidateThreadTurnsListParams(params); err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		// Live source first; fall back to the saved transcript (paged on the
		// hub) for past/not-loaded sessions.
		source, srcErr := sourceForThreadWithDeletionFence(cfg, sources, params.Ref, params.ThreadID)
		if isTargetDeletedError(srcErr) {
			return appwire.ThreadTurnsListResponse{}, srcErr
		}
		var live appwire.ThreadTurnsListResponse
		var liveErr error
		var liveItemHandled bool
		if srcErr == nil {
			_, liveItemNative := source.(appsource.ItemCandidateSource)
			live, liveItemHandled, liveErr = listItemTurns(ctx, source, params, logf)
			if liveItemHandled && liveErr == nil && (!liveItemNative || len(live.Data) > 0) {
				return live, nil
			}
			if liveErr == nil && len(live.Data) > 0 {
				if meta, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false}); err == nil {
					// File-backed output-image enrichment is intentionally page-local
					// here: args can only be correlated from command-call items present
					// in this returned page (or on the completed item itself).
					thread := enrichSourcedThreadImages(source, appwire.Thread{
						ID:        meta.Thread.ID,
						SessionID: meta.Thread.SessionID,
						CWD:       meta.Thread.CWD,
						Turns:     live.Data,
					})
					live.Data = thread.Turns
				}
				return live, nil
			}
		}
		saved, ok, pastErr := pastThreadTurnsList(ctx, cfg, params)
		if pastErr != nil {
			return appwire.ThreadTurnsListResponse{}, pastErr
		}
		if ok {
			return saved, nil
		}
		if srcErr != nil {
			return appwire.ThreadTurnsListResponse{}, srcErr
		}
		return live, liveErr
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSubagentPreview, func(ctx context.Context, params appwire.EvenerSubagentPreviewParams) (appwire.EvenerSubagentPreviewResponse, error) {
		ref := strings.TrimSpace(params.Ref)
		if ref == "" {
			return appwire.EvenerSubagentPreviewResponse{}, appwire.InvalidParams("ref required")
		}
		source, err := sourceForThreadWithDeletionFence(cfg, sources, ref, "")
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.EvenerSubagentPreviewResponse{}, err
			}
			thread, ok, pastErr := pastThreadForRead(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
			if pastErr != nil {
				return appwire.EvenerSubagentPreviewResponse{}, pastErr
			}
			if ok {
				return subagentPreviewFromThread(thread, ref, params.Limit), nil
			}
			return appwire.EvenerSubagentPreviewResponse{}, err
		}
		resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			thread, ok, pastErr := pastThreadForRead(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
			if pastErr != nil {
				return appwire.EvenerSubagentPreviewResponse{}, pastErr
			}
			if ok {
				return subagentPreviewFromThread(thread, ref, params.Limit), nil
			}
			return appwire.EvenerSubagentPreviewResponse{}, err
		}
		// A remote source returns the remote hub's origin-relative image routes,
		// which only resolve against the remote origin; neutralization must run
		// here exactly as it does on the thread/read path.
		resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
		return subagentPreviewFromThread(resp.Thread, ref, params.Limit), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		resp, err := hubThreadStart(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		if err := relays.startRelayForThread(ctx, resp.Thread); err != nil {
			appserver.Notify(ctx, appwire.NotifyWarning, appwire.WarningParams{
				ThreadID: resp.Thread.ID,
				Ref:      resp.Thread.Evener.Ref,
				Source:   "hub",
				Title:    "Live updates unavailable",
				Message:  "thread started, but Hub could not attach live updates: " + err.Error(),
			})
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resp, err := hubThreadResume(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		if err := relays.startRelayForThread(ctx, resp.Thread); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadFork, func(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
		return hubThreadFork(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnStartResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		resolved := false
		attemptStart := func() (appwire.TurnStartResponse, error) {
			source, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appsource.Source, error) {
				return resolveTurnStartSource(sources, params.Ref, params.ThreadID)
			})
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
			resolved = true
			return relays.startTurn(ctx, source, params)
		}
		resp, err := attemptStart()
		if err == nil {
			return resp, nil
		}
		if !resolved {
			if wire, ok := errors.AsType[appwire.WireError](err); ok && wire.Code == appwire.CodeInvalidParams {
				return appwire.TurnStartResponse{}, err
			}
			if isTargetDeletedError(err) || isDaemonRestartRequiredError(err) || isSessionRecoveryAdmissionError(err) {
				return appwire.TurnStartResponse{}, err
			}
			if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
				return appwire.TurnStartResponse{}, blockedUnknownMutationError(params.ClientMutationID, resumeErr)
			}
			resolved = false
			return attemptStart()
		}
		if isLifecycleRetiringError(err) {
			// The owning daemon refused the mutation because it is retiring and
			// still owns the session. Resolve the race under existing recovery
			// authority — admission fences, ownership alias locks, confirmed exit,
			// one resume — then retry the original request verbatim.
			if resumeErr := resumeAfterConfirmedRetirement(ctx, cfg, sources, params); resumeErr != nil {
				return appwire.TurnStartResponse{}, resumeErr
			}
			resolved = false
			return attemptStart()
		}
		if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
			return appwire.TurnStartResponse{}, err
		}
		if !shouldResumeAfterTurnStartError(err) {
			return appwire.TurnStartResponse{}, err
		}
		if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
			return appwire.TurnStartResponse{}, blockedUnknownMutationError(params.ClientMutationID, resumeErr)
		}
		resolved = false
		return attemptStart()
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnSteer, func(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnSteerResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.TurnSteerResponse{}, err
			}
			if err := ensureSkillInputSupported(ctx, source, params.Ref, params.ThreadID, params.Input); err != nil {
				return appwire.TurnSteerResponse{}, err
			}
			return source.SteerTurn(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnInterrupt, func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnInterruptResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnInterruptResponse, error) {
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.TurnInterruptResponse{}, err
			}
			return source.InterruptTurn(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSandboxEscalationResolve, func(ctx context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
		return withSessionActionOwnership(ctx, cfg, params.Ref, params.ThreadID, func() (appwire.EmptyResponse, error) {
			if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, ""); err != nil {
				return appwire.EmptyResponse{}, err
			}
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.EmptyResponse{}, err
			}
			return appwire.EmptyResponse{}, source.ResolveSandboxEscalation(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnQueue, func(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnQueueResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnQueueResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnQueueResponse{}, err
			}
			if err := ensureSkillInputSupported(ctx, source, params.Ref, "", params.Input); err != nil {
				return appwire.TurnQueueResponse{}, err
			}
			return source.QueueTurn(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnDrainAsSteer, func(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnDrainAsSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnDrainAsSteerResponse{}, err
			}
			if err := ensureSkillInputSupported(ctx, source, params.Ref, "", params.Input); err != nil {
				return appwire.TurnDrainAsSteerResponse{}, err
			}
			return source.DrainAsSteer(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnPromoteQueuedAsSteer, func(ctx context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
		if params.Index < 0 {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("index must be >= 0")
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedEntryID) == "" {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("expectedEntryId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnPromoteQueuedAsSteerResponse{}, err
			}
			return source.PromoteQueuedAsSteer(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnCancelQueued, func(ctx context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
		if params.Index < 0 {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("index must be >= 0")
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedEntryID) == "" {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("expectedEntryId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnCancelQueuedResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnCancelQueuedResponse{}, err
			}
			return source.CancelQueued(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadClear, func(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.ThreadClearResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedInstanceID) == "" {
			return appwire.ThreadClearResponse{}, appwire.InvalidParams("expectedInstanceId is required")
		}
		return clearThreadWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadCompactStart, func(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, compactThreadWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadForceStop, func(ctx context.Context, params appwire.ThreadForceStopParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, forceStopThread(ctx, cfg, params, sources)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadShutdown, func(ctx context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, shutdownThreadTolerateExited(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, setThreadModelWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadVisionModelSet, func(ctx context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, setThreadVisionModelWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadReasoningEffortSet, func(ctx context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
		return withSessionActionOwnership(ctx, cfg, params.Ref, "", func() (appwire.EmptyResponse, error) {
			if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, "", ""); err != nil {
				return appwire.EmptyResponse{}, err
			}
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.EmptyResponse{}, err
			}
			// No capability gate: there is no reasoning-effort thread capability, and
			// the daemon/source already reject the call when it is unsupported (a
			// non-evener source, or a daemon without the effort hook).
			return appwire.EmptyResponse{}, source.SetThreadReasoningEffort(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodGoalSet, func(ctx context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		return setGoalWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodNotesHumanSet, func(ctx context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
		return setNotesHumanWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodUrlsRemove, func(ctx context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
		return removeURLWithResume(ctx, cfg, sources, params)
	})
}

// registerAuthHandlers registers the evener/auth/* RPC handlers, routed to the
// auth controller. Successful mutations broadcast evener/auth/updated.
func registerAuthHandlers(server *appserver.Server, authController *hubAuthController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthStatus, func(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
		return authController.Status(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthTest, func(ctx context.Context, params appwire.AuthTestParams) (appwire.AuthTestResponse, error) {
		return authController.TestCredentials(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLoginStart, func(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
		return authController.LoginStart(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLoginComplete, func(ctx context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
		resp, err := authLoginComplete(authController, ctx, params)
		notifyAuthWrite(server, err, resp.Status, params.OriginClientId)
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLogout, func(ctx context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
		resp, err := authController.Logout(params)
		notifyAuthWrite(server, err, resp.Status, params.OriginClientId)
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthList, func(_ context.Context, params appwire.EmptyParams) (appwire.AuthListResponse, error) {
		return authController.List(params)
	})
	// ApiKeySet, ApiKeyClear and CredentialJsonSet all answer with a bare
	// AuthStatusResponse, so one closure covers the broadcast every one of
	// them owes evener/auth/updated when its write applied.
	authStatusWrite := func(originClientID string, call func() (appwire.AuthStatusResponse, error)) (appwire.AuthStatusResponse, error) {
		resp, err := call()
		notifyAuthWrite(server, err, resp, originClientID)
		return resp, err
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthApiKeySet, func(ctx context.Context, params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
		return authStatusWrite(params.OriginClientId, func() (appwire.AuthStatusResponse, error) { return authController.ApiKeySet(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthApiKeyClear, func(ctx context.Context, params appwire.AuthApiKeyClearParams) (appwire.AuthStatusResponse, error) {
		return authStatusWrite(params.OriginClientId, func() (appwire.AuthStatusResponse, error) { return authController.ApiKeyClear(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthCredentialJsonSet, func(ctx context.Context, params appwire.AuthCredentialJsonSetParams) (appwire.AuthStatusResponse, error) {
		return authStatusWrite(params.OriginClientId, func() (appwire.AuthStatusResponse, error) { return authController.CredentialJsonSet(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthDeviceStart, func(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
		return authController.DeviceStart(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthDevicePoll, func(ctx context.Context, params appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		resp, err := authDevicePoll(authController, ctx, params)
		// A pending poll wrote nothing, which the state says; an authorized one
		// wrote the record, whether or not the status read after it failed.
		if resp.State == "authorized" {
			notifyAuthWrite(server, err, *resp.Status, params.OriginClientId)
		}
		return resp, err
	})
}

// registerInstanceHandlers registers the evener/instance/* CRUD handlers. When no
// instances controller is configured (providers.toml path unset), no handlers
// are registered — matching the original inline guard. Successful mutations
// broadcast evener/auth/updated (see notifyInstanceUpdated) so every other
// connected client refetches its now-stale instance list.
func registerInstanceHandlers(server *appserver.Server, instancesController *hubInstancesController) {
	if instancesController == nil {
		return
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceList, func(_ context.Context, _ appwire.EmptyParams) (appwire.InstanceListResponse, error) {
		return instancesController.List(), nil
	})
	// Every instance write answers the same way: a write that applied is
	// announced, whether or not the step after it failed (writeDidApply), and
	// the error still goes back to the client that asked, which is the only
	// one that can act on what was left behind. Only a CLEANLY applied write
	// echoes the caller's own originClientId, so that client recognizes its own
	// change; an applied write that still returned an error broadcasts without
	// one, because the issuing client cannot treat an errored mutation's echo as
	// its own success - the broadcast can beat the failing reply, and consuming
	// the marker then would suppress the invalidation a failed operation owes.
	instanceWrite := func(originClientId string, apply func() error) (appwire.InstanceListResponse, error) {
		err := apply()
		if writeDidApply(err) {
			if err != nil {
				notifyInstanceUpdated(server, "")
			} else {
				notifyInstanceUpdated(server, originClientId)
			}
		}
		if err != nil {
			return appwire.InstanceListResponse{}, err
		}
		return instancesController.List(), nil
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceCreate, func(_ context.Context, params appwire.InstanceCreateParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() error { return instancesController.Create(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceEdit, func(_ context.Context, params appwire.InstanceEditParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() error { return instancesController.Edit(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceRemove, func(_ context.Context, params appwire.InstanceRemoveParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() error { return instancesController.Remove(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceSetDefault, func(_ context.Context, params appwire.InstanceSetDefaultParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() error { return instancesController.SetDefault(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceSetModelDisabled, func(_ context.Context, params appwire.InstanceSetModelDisabledParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() error { return instancesController.SetModelDisabled(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceRefreshModels, func(ctx context.Context, params appwire.InstanceRefreshModelsParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() error { return instancesController.RefreshModels(ctx, params) })
	})
}

// registerLaunchHandlers registers the evener/launch/* RPC handlers, routed to the
// launch controller. Successful layer/trust mutations broadcast evener/launch/updated.
func registerLaunchHandlers(server *appserver.Server, launchController *hubLaunchController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchResolve, func(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
		return launchController.Resolve(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchSchema, func(ctx context.Context, params appwire.EmptyParams) (appwire.LaunchOptionSchemaResponse, error) {
		return launchController.Schema(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchGetLayer, func(ctx context.Context, params appwire.LaunchConfigGetLayerParams) (appwire.LaunchConfigLayer, error) {
		return launchController.GetLayer(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchSetLayer, func(ctx context.Context, params appwire.LaunchConfigSetLayerParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchController.SetLayer(ctx, params)
		if writeDidApply(err) {
			notifyLaunchUpdated(server, params.CWD, params.Layer)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchTrustRepo, func(ctx context.Context, params appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchTrustRepo(launchController, ctx, params)
		if writeDidApply(err) {
			notifyLaunchUpdated(server, params.CWD, "repo")
		}
		return resp, err
	})
}

// registerPluginHandlers registers the evener/marketplace/* and evener/plugin/*
// RPC handlers, routed to the plugins controller. Every mutation here runs
// through pluginsController.mgr, which newWebServer wires (wirePluginStoreBroadcast)
// to broadcast evener/marketplace/updated and/or evener/plugin/updated for
// whatever its own lockStore session actually wrote — the sole notification
// path for this surface; no handler below calls
// notifyMarketplaceUpdated/notifyPluginUpdated itself.
func registerPluginHandlers(server *appserver.Server, pluginsController *hubPluginsController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.ListMarketplaces(ctx)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceAdd, func(ctx context.Context, params appwire.MarketplaceAddParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.AddMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceRemove, func(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.RemoveMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceRefresh, func(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.RefreshMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceEdit, func(ctx context.Context, params appwire.MarketplaceEditParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.EditMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceBrowse, func(ctx context.Context, params appwire.MarketplaceBrowseParams) (appwire.MarketplaceBrowseResponse, error) {
		return pluginsController.Browse(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.PluginListResponse, error) {
		return pluginsController.ListPlugins(ctx)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginPreview, func(ctx context.Context, params appwire.PluginPreviewParams) (appwire.PluginPreviewResponse, error) {
		return pluginsController.Preview(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginInstall, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Install(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginUpgrade, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Upgrade(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginRemove, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Remove(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginEnable, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Enable(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginDisable, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Disable(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginSetAutoUpgrade, func(ctx context.Context, params appwire.PluginSetAutoUpgradeParams) (appwire.PluginListResponse, error) {
		return pluginsController.SetAutoUpgrade(ctx, params)
	})
}

// notifyMarketplaceUpdated broadcasts a evener/marketplace/updated notification
// to all connected clients.
func notifyMarketplaceUpdated(server hostNotificationBroadcaster) {
	server.BroadcastAll(appwire.NotifyEvenerMarketplaceUpdated, map[string]string{})
}

// notifyPluginUpdated broadcasts a evener/plugin/updated notification to all
// connected clients.
func notifyPluginUpdated(server hostNotificationBroadcaster) {
	server.BroadcastAll(appwire.NotifyEvenerPluginUpdated, map[string]string{})
}

// wirePluginStoreBroadcast installs an OnStoreChanged callback (issue #1634)
// on mgr that broadcasts evener/marketplace/updated and/or
// evener/plugin/updated for whatever a lockStore session actually wrote —
// the sole path that broadcasts a plugin-store write reaching every Manager
// this package constructs: the RPC handlers above, the auto-upgrade daemon
// and its checkNow handler, hubSeedDefaults, hubPluginGC, and the resolver
// path a launch reaches through cfg.PluginManager, without any of them
// needing to call notify*/know this happened.
//
// server takes hostNotificationBroadcaster (app_host_admin.go), the same
// *appserver.Server-shaped seam the host-admin fan-out tests drive with a
// recorder, rather than *appserver.Server itself, so a test can assert on
// the real broadcasts this sends without standing up a connection.
func wirePluginStoreBroadcast(mgr *plugins.Manager, server hostNotificationBroadcaster) {
	mgr.OnStoreChanged(func(changed plugins.StoreChanged) {
		if changed.Marketplaces {
			notifyMarketplaceUpdated(server)
		}
		if changed.Plugins {
			notifyPluginUpdated(server)
		}
	})
}

// newWiredPluginManager constructs a *plugins.Manager rooted at root and
// wires it to broadcaster in one step, for a caller that already has a live
// broadcaster to hand it (main_background.go's three background-maintenance
// sites, each with web.appRPC in hand): the construct-then-wire pair
// wirePluginStoreBroadcast's own doc comment describes, without repeating it
// at every call site.
func newWiredPluginManager(root string, broadcaster hostNotificationBroadcaster) *plugins.Manager {
	mgr := plugins.NewManager(root)
	wirePluginStoreBroadcast(mgr, broadcaster)
	return mgr
}

// recentProjectDirsLimit is the session creation flows' path-dropdown option
// count (issue #35): the 15 most recently used projects.
const recentProjectDirsLimit = 15

// registerMiscHandlers registers hub RPC handlers that are not owned by a
// focused controller registration.
func registerMiscHandlers(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpgrade, hubUpgrade)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateCheck, hubUpdateCheck)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateApply, hubUpdateApply)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSearch, func(_ context.Context, params appwire.SearchParams) (appwire.SearchResponse, error) {
		return hubSearch(cfg, params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return hubModelList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerTasksList, func(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
		return hubTasksList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerJobsList, func(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
		return hubJobsList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerJobsOutput, func(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
		return hubJobsOutput(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadTranscriptsList, func(ctx context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
		return hubThreadTranscriptList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPathsComplete, func(_ context.Context, params appwire.PathsCompleteParams) (appwire.PathsCompleteResponse, error) {
		return fspaths.CompletePaths(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDirsCreate, func(_ context.Context, params appwire.DirsCreateParams) (appwire.DirsCreateResponse, error) {
		return hubDirsCreate(cfg, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerProjectsRecent, func(_ context.Context, params appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
		limit := params.Limit
		if limit <= 0 {
			limit = recentProjectDirsLimit
		}
		// Non-nil even when there is nothing to report: a nil slice marshals as
		// JSON null, which contradicts the wire type's own non-nullable
		// `data: string[]` and crashes any client that trusts it.
		dirs := []string{}
		if cfg.Past != nil {
			dirs = append(dirs, cfg.Past.RecentProjectDirs(limit)...)
		}
		return appwire.ProjectsRecentResponse{Data: dirs}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPathValidate, func(_ context.Context, params appwire.PathValidateParams) (appwire.PathValidateResponse, error) {
		return fspaths.ValidateLaunchPath(params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerGitHead, func(ctx context.Context, params appwire.GitHeadParams) (appwire.GitHeadResponse, error) {
		return hubGitHead(ctx, cfg, params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: launchHarnessDescriptors()}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerCommandList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.CommandListResponse, error) {
		return hubCommandList(ctx, cfg)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSpawnSlashCatalog, func(ctx context.Context, params appwire.SpawnSlashCatalogParams) (appwire.SpawnSlashCatalogResponse, error) {
		return hubSpawnSlashCatalog(ctx, cfg, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsOverview, func(ctx context.Context, _ appwire.EmptyParams) (appwire.SettingsOverviewResponse, error) {
		return hubSettingsOverview(ctx, cfg)
	})
}

// hubCommandList answers evener/command/list by loading every plugin a real
// session would load — internal/plugins.Manager.ResolveForLaunch (explicit
// --plugin-dir-equivalent PluginDirs first, then every installed+enabled
// registry entry) — and flattening their discovered slash commands into a
// catalog. This used to mirror discoverPluginsForSettings's display-only scan
// (web_settings.go, pluginDirsFromConfig: an immediate-subdirectory glob of
// the plugin store) instead, which could never see a plugin installed via the
// marketplace/registry system (living at cache/<marketplace>/<plugin>/<sha>,
// not a direct child of the plugins root) — so a registry-installed plugin's
// commands never appeared here even though a spawned session loaded them.
// The hub catalog combines enabled plugin commands with evener-wide commands.
// Evener-wide discovery receives a nil environment because the hub is
// multi-project: project commands are per-session and must never appear here.
// Loading is fail-soft (plugin.LoadAllFailSoft), so one broken or mid-edit
// plugin dir cannot blank out the whole command catalog.
func hubCommandList(ctx context.Context, cfg hubcore.WebConfig) (appwire.CommandListResponse, error) {
	resolution, err := hubResolvePlugins(ctx, cfg.PluginRoot, cfg.PluginDirs, nil, cfg.PluginManager)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: listing plugins: %v\n", err)
	}
	loaded, _ := plugin.LoadAllFailSoft(resolution.SelectedDirs)
	evenerwide, _ := plugin.DiscoverEvenerWideCommands(nil)
	merged := plugin.MergeCommands(loaded, evenerwide)
	var commands []appwire.CommandDescriptor
	for _, cmd := range merged {
		commands = append(commands, appwire.CommandDescriptor{
			Name:         cmd.Name,
			PluginName:   cmd.PluginName,
			Description:  cmd.Description,
			ArgumentHint: cmd.ArgumentHint,
			Source:       cmd.Source,
		})
	}
	sortCommandDescriptors(commands)
	return appwire.CommandListResponse{Commands: commands}, nil
}

// sortCommandDescriptors orders command rows by (Name, PluginName, Source).
// It is stable so rows with equal keys keep their discovery order instead of
// shuffling nondeterministically under sort.Slice's unstable pdqsort.
func sortCommandDescriptors(commands []appwire.CommandDescriptor) {
	sort.SliceStable(commands, func(i, j int) bool {
		if commands[i].Name != commands[j].Name {
			return commands[i].Name < commands[j].Name
		}
		if commands[i].PluginName != commands[j].PluginName {
			return commands[i].PluginName < commands[j].PluginName
		}
		return commands[i].Source < commands[j].Source
	})
}

// notifyAuthUpdated broadcasts a evener/auth/updated notification to all connected clients.
// originClientId is the originating client's own id, echoed back from the
// mutation that produced this broadcast; empty when the caller sent none.
func notifyAuthUpdated(server *appserver.Server, provider, activeSource, originClientId string) {
	// Still map[string]string, not appwire.EvenerAuthUpdatedParams (kcb5):
	// provider/activeSource (from AuthStatus) are legitimately empty when no
	// provider is active, but this map always emits both keys anyway; both
	// fields are tagged `omitempty` on the struct, so a typed literal would
	// drop them whenever blank. Not provably byte-identical; left as a map.
	payload := map[string]string{
		"provider":     provider,
		"activeSource": activeSource,
	}
	// The originator id rides along only when the caller sent one. An empty
	// value (the TUI, an older web build) must leave the payload exactly as it
	// was before the field existed: consumers with no id on the notification
	// keep their provider-plus-timing fallback, and one that would see an
	// empty-string id would attribute nothing.
	if originClientId != "" {
		payload["originClientId"] = originClientId
	}
	server.BroadcastAll(appwire.NotifyEvenerAuthUpdated, payload)
}

// notifyAuthWrite broadcasts what a credential or OAuth write actually
// knows. A clean read (err == nil) has the real provider and active source,
// so it broadcasts those. A write that applied but whose status read failed
// (writeApplied's shape) has neither: status is the read's own zero-value
// fallback (AuthStatusResponse{Provider: name}, ActiveSource == ""), and
// broadcasting that would announce "nothing active" as fact when the truth
// was never read. That case broadcasts the no-data form notifyInstanceUpdated
// uses instead, so clients refetch rather than adopt a fabricated
// activeSource. A write that never applied broadcasts nothing.
func notifyAuthWrite(server *appserver.Server, err error, status appwire.AuthStatusResponse, originClientID string) {
	switch {
	case err == nil:
		notifyAuthUpdated(server, status.Provider, status.ActiveSource, originClientID)
	case writeDidApply(err):
		// The no-data form, deliberately WITHOUT the origin. The originating
		// credential mutation has already retired its own marker on the error,
		// so it cannot attribute this broadcast anyway - and echoing the origin
		// would make this provider-less broadcast structurally identical to a
		// provider-instance echo, letting it consume an instance mutation's
		// marker and turn that mutation's own echo foreign.
		notifyInstanceUpdated(server, "")
	}
}

// notifyInstanceUpdated broadcasts a evener/auth/updated notification to all
// connected clients after a provider-instance CRUD mutation (create, edit,
// remove, setDefault, setModelDisabled, refreshModels), and it is also the
// no-data form notifyAuthWrite uses for a credential write that applied but
// whose status read failed. It deliberately reuses the auth/updated channel
// rather than minting a new notification type: the client-side handler
// (notifications.js) already treats evener/auth/updated as payload-agnostic —
// "credentials or instances changed, refetch" — reloading both the instances
// panel and the providers settings tab on receipt.
//
// Provider and activeSource stay empty: there is no single provider/activeSource
// pair that honestly summarizes "the instance list changed." originClientId is
// the originating client's own id, echoed back from the mutation that produced
// this broadcast so that client can recognize its own echo by id instead of
// refetching as if another client had changed the list; empty when the caller
// sent none (an older build, the TUI), which leaves the payload exactly as it
// was before the field existed. The credential no-data form notifyAuthWrite
// uses passes no origin even when the caller sent one: that caller's own marker
// was retired by the failure, so the broadcast is unattributable, and carrying
// an id would make it look like a provider-instance echo to the SDK.
func notifyInstanceUpdated(server *appserver.Server, originClientId string) {
	server.BroadcastAll(appwire.NotifyEvenerAuthUpdated, appwire.EvenerAuthUpdatedParams{OriginClientId: originClientId})
}

// notifyLaunchUpdated broadcasts a evener/launch/updated notification to all connected clients.
func notifyLaunchUpdated(server *appserver.Server, cwd, layer string) {
	server.BroadcastAll(appwire.NotifyEvenerLaunchUpdated, appwire.EvenerLaunchUpdatedParams{
		CWD:   cwd,
		Layer: layer,
	})
}
