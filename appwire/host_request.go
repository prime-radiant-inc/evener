package appwire

import (
	"maps"
	"slices"
)

// hostRequestMethods is the exact set of requests evener/host/request may
// forward. Adding a method to Methods never grants forwarding permission:
// provider, launch, plugin, credential and discovery methods must be named here.
// The hub enforces this catalog and owns each method's retry classification;
// the TypeScript generator publishes the same names for clients.
//
// Host/controller management is deliberately excluded. The spawn form's
// discovery subset is pinned by cmd/evener-hub/host_request_methods.txt and
// integration tests on both sides of the proxy.
var hostRequestMethods = map[string]struct{}{
	// Provider instances (hubInstancesController, app_instances.go). The
	// settings panes drive these five handlers remotely; the catalog's newer
	// evener/instance/setModelDisabled and evener/instance/refreshModels are
	// deliberately absent here and in the policy table, so the proxy refuses
	// them (fail closed).
	MethodEvenerInstanceList:       {},
	MethodEvenerInstanceCreate:     {},
	MethodEvenerInstanceEdit:       {},
	MethodEvenerInstanceRemove:     {},
	MethodEvenerInstanceSetDefault: {},

	// Launch config (hubLaunchController, app_launch.go). Its credential-env
	// refusal must reach the browser verbatim; the proxy never launders it.
	MethodEvenerLaunchResolve:   {},
	MethodEvenerLaunchSchema:    {},
	MethodEvenerLaunchGetLayer:  {},
	MethodEvenerLaunchSetLayer:  {},
	MethodEvenerLaunchTrustRepo: {},

	// Marketplaces and plugins (hubPluginsController, app_plugins.go). The
	// pane's UI reaches plugin/disable and plugin/setAutoUpgrade too.
	MethodEvenerMarketplaceList:      {},
	MethodEvenerMarketplaceAdd:       {},
	MethodEvenerMarketplaceRemove:    {},
	MethodEvenerMarketplaceRefresh:   {},
	MethodEvenerMarketplaceEdit:      {},
	MethodEvenerMarketplaceBrowse:    {},
	MethodEvenerPluginList:           {},
	MethodEvenerPluginInstall:        {},
	MethodEvenerPluginUpgrade:        {},
	MethodEvenerPluginRemove:         {},
	MethodEvenerPluginEnable:         {},
	MethodEvenerPluginDisable:        {},
	MethodEvenerPluginSetAutoUpgrade: {},
	MethodEvenerPluginPreview:        {},
	MethodEvenerPluginCheckNow:       {},
	MethodEvenerPluginCheckUpdates:   {},

	// Auth and credentials (hubAuthController, app_auth.go). The host's own
	// refusals (a stored key under a Codex or gcp-adc instance) pass through
	// unchanged. apiKey/conditionalSet is on this list so a CLIENT may proxy it
	// like any other forwarded auth method - the controller's own credential
	// push does NOT go through the list (see the row for it below) - and
	// apiKey/set remains the unconditional path.
	MethodEvenerAuthStatus:        {},
	MethodEvenerAuthTest:          {},
	MethodEvenerAuthList:          {},
	MethodEvenerAuthLoginStart:    {},
	MethodEvenerAuthLoginComplete: {},
	MethodEvenerAuthLogout:        {},
	MethodEvenerAuthApiKeySet:     {},
	MethodEvenerAuthApiKeyClear:   {},
	// evener/auth/apiKey/conditionalSet is allow-listed deliberately, per the
	// spec's [07a] entry: it is the atomic replacement for the racy
	// status-then-set pair, and a CLIENT may proxy it here like any other
	// forwarded auth method. The controller's own credential push does NOT go
	// through this list: hubHostCredentialsPusher calls the method directly on
	// the shared per-host client seam (RemoteHubSource.AdminMutationCall) and
	// never consults hostRequestMethods. The row is named rather than left
	// implied because, unlike apiKey/set, this method carries no
	// ExpectedEndpointFingerprint check - its safety comes from the fence
	// (ExpectedSource/ExpectedRevision) and the host's locked classification.
	MethodEvenerAuthApiKeyConditionalSet: {},
	MethodEvenerAuthCredentialJsonSet:    {},
	MethodEvenerAuthDeviceStart:          {},
	MethodEvenerAuthDevicePoll:           {},

	// Personal AGENTS.md (registerAgentsDocHandlers, app_rpc_agents_doc.go).
	MethodEvenerSettingsAgentsDocGet: {},
	MethodEvenerSettingsAgentsDocSet: {},

	// Host-dependent discovery — the remote settings panes' and the spawn form's
	// own filesystem/discovery calls (component 06 §"Frontend changes"). Every
	// one is answered by the host hub against ITS local environment, which is the
	// point: path validation, auto-completion, directory creation, recent
	// projects, harnesses, and the slash catalog must describe the selected host,
	// not the controller. They are read-mostly; the only mutation is creating the
	// directory the user just asked for (dirs/create), and none exposes a secret
	// the admin families above do not already carry. Without these rows a remote
	// settings pane or spawn form fails closed with appwire.InvalidParams.
	MethodEvenerPathsComplete:     {},
	MethodEvenerPathValidate:      {},
	MethodEvenerDirsCreate:        {},
	MethodEvenerProjectsRecent:    {},
	MethodEvenerHarnessesList:     {},
	MethodEvenerSpawnSlashCatalog: {},
	// evener/git/head is read-only branch metadata for a remote working
	// directory (protocol.go: ScopeHub, GitHeadParams/GitHeadResponse); the
	// spawn form needs the branch of the path it is about to launch.
	MethodEvenerGitHead: {},
	// model/list is ScopeBoth rather than ScopeHub — the serve daemon answers it
	// too — but the hub answers it, and the spawn form asks the selected host for
	// that host's own model inventory.
	MethodModelList: {},
}

// IsHostRequestMethod reports whether name may be forwarded through
// evener/host/request. The check is by exact name, never by method prefix.
func IsHostRequestMethod(name string) bool {
	_, ok := hostRequestMethods[name]
	return ok
}

// HostRequestMethodNames returns the allowed forwarding methods in lexical
// order. The returned slice is independent of the catalog.
func HostRequestMethodNames() []string {
	return slices.Sorted(maps.Keys(hostRequestMethods))
}
