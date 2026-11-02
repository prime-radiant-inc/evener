package appwire

import (
	"strconv"
	"strings"
)

// ProtocolVersion is compared exactly at the handshake
// (internal/appserver/server.go), so bumping it makes a mixed pair of binaries
// fail once, loudly, at initialize -- rather than agreeing there and then
// disagreeing on every request. Daemon reads must identify authoritative
// mutation state: a client cannot safely release uncertain sends from a peer
// that does not supply that evidence.
//
// v7 serves job output as lossless byte pages, with explicit retention bounds
// and an optional exclusive end selector that preserves zero.
// v6 serves history only as history/updated from recorded entries and live
// unrecorded state only as overlay/* notifications: turn/started,
// turn/completed, item/* and evener/steering/injected are gone, and a client
// announcing an older version is refused with upgradeRequired.
// v4 makes transcript reads item-only and rejects retired paging fields. v3
// dropped expectedTurnId from turn/steer, turn/queue, turn/interrupt,
// turn/drainAsSteer and turn/promoteQueuedAsSteer: control is session-scoped and
// names no turn. A v2 daemon still requires that field, so a v3 client talking
// to one would get a Conflict per button press and the user would read it as
// "Steer and Stop are broken again" instead of as a version skew. The pair is
// reachable in ordinary operation because daemons outlive the hub that spawned
// them, so an operator who rebuilds and restarts the hub has one.
const ProtocolVersion = "evener-appwire-v7"

// ProtocolVersionNumber returns the N in "evener-appwire-vN". Only the
// canonical decimal spelling is a protocol version, so "v05" and "v5-rc1" are
// not.
func ProtocolVersionNumber(version string) (int, bool) {
	digits, ok := strings.CutPrefix(version, "evener-appwire-v")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 || strconv.Itoa(n) != digits {
		return 0, false
	}
	return n, true
}

// OlderProtocolVersion reports whether version is a canonical AppWire protocol
// version older than this build's ProtocolVersion.
func OlderProtocolVersion(version string) bool {
	n, ok := ProtocolVersionNumber(version)
	current, currentOK := ProtocolVersionNumber(ProtocolVersion)
	return ok && currentOK && n < current
}

// NewerProtocolVersion reports whether version is a canonical AppWire protocol
// version newer than this build's ProtocolVersion.
func NewerProtocolVersion(version string) bool {
	n, ok := ProtocolVersionNumber(version)
	current, currentOK := ProtocolVersionNumber(ProtocolVersion)
	return ok && currentOK && n > current
}

// ThreadStatusRestartRequired identifies a live daemon that cannot serve this
// hub's protocol. Its current activity is unavailable until explicitly restarted.
const ThreadStatusRestartRequired = "restartRequired"

const (
	MethodInitialize                  = "initialize"
	MethodInitialized                 = "initialized"
	MethodPing                        = "ping"
	MethodThreadList                  = "thread/list"
	MethodThreadRead                  = "thread/read"
	MethodThreadUnsubscribe           = "thread/unsubscribe"
	MethodThreadTurnsList             = "thread/turns/list"
	MethodThreadTurnItemsList         = "thread/turns/items/list"
	MethodThreadStart                 = "thread/start"
	MethodThreadResume                = "thread/resume"
	MethodThreadFork                  = "thread/fork"
	MethodThreadClear                 = "thread/clear"
	MethodThreadModelSet              = "thread/model/set"
	MethodThreadReasoningEffortSet    = "thread/reasoning-effort/set"
	MethodThreadVisionModelSet        = "thread/vision-model/set"
	MethodThreadCompactStart          = "thread/compact/start"
	MethodThreadShutdown              = "thread/shutdown"
	MethodEvenerThreadForceStop       = "evener/thread/forceStop"
	MethodTurnStart                   = "turn/start"
	MethodTurnSteer                   = "turn/steer"
	MethodTurnInterrupt               = "turn/interrupt"
	MethodTurnQueue                   = "turn/queue"
	MethodTurnDrainAsSteer            = "turn/drainAsSteer"
	MethodTurnPromoteQueuedAsSteer    = "turn/promoteQueuedAsSteer"
	MethodTurnCancelQueued            = "turn/cancelQueued"
	MethodGoalSet                     = "goal/set"
	MethodNotesHumanSet               = "notes/human/set"
	MethodNotesAgentSet               = "notes/agent/set"
	MethodUrlsAdd                     = "urls/add"
	MethodUrlsRemove                  = "urls/remove"
	MethodEvenerTasksList             = "evener/tasks/list"
	MethodEvenerJobsList              = "evener/jobs/list"
	MethodEvenerJobsOutput            = "evener/jobs/output"
	MethodEvenerJobsGet               = "evener/jobs/get"
	MethodEvenerDaemonList            = "evener/daemon/list"
	MethodEvenerDaemonRetire          = "evener/daemon/retire"
	MethodEvenerDaemonStatus          = "evener/daemon/status"
	MethodEvenerDaemonIdleTimeoutSet  = "evener/daemon/idle-timeout/set"
	MethodEvenerThreadNameSet         = "evener/thread/name/set"
	MethodEvenerThreadTranscriptsList = "evener/thread/transcripts/list"
	MethodEvenerThreadActivityRead    = "evener/thread/activity/read"
	MethodEvenerThreadDelegatesList   = "evener/thread/delegates/list"
	MethodEvenerThreadJobsList        = "evener/thread/jobs/list"
	MethodEvenerThreadWatchesList     = "evener/thread/watches/list"
	MethodEvenerSubagentPreview       = "evener/subagentPreview"
	MethodEvenerPathsComplete         = "evener/paths/complete"
	MethodEvenerDirsCreate            = "evener/dirs/create"
	MethodEvenerProjectsRecent        = "evener/projects/recent"
	MethodEvenerPathValidate          = "evener/path/validate"
	MethodEvenerGitHead               = "evener/git/head"
	MethodEvenerMobilePairing         = "evener/mobile/pairing"
	MethodEvenerDevicePairingCreate   = "evener/devicePairing/create"
	MethodEvenerDevicePairingRead     = "evener/devicePairing/read"
	MethodEvenerDevicePairingApprove  = "evener/devicePairing/approve"
	MethodEvenerDevicePairingCancel   = "evener/devicePairing/cancel"

	MethodEvenerNavigationRead           = "evener/navigation/read"
	MethodEvenerFavoriteSet              = "evener/favorite/set"
	MethodEvenerArchiveSet               = "evener/archive/set"
	MethodEvenerProjectDelete            = "evener/project/delete"
	MethodEvenerSessionDelete            = "evener/session/delete"
	MethodEvenerPinSectionRename         = "evener/pin-section/rename"
	MethodEvenerPinSectionDelete         = "evener/pin-section/delete"
	MethodEvenerSessionPinAssign         = "evener/session-pin/assign"
	MethodEvenerSessionPinUnpin          = "evener/session-pin/unpin"
	MethodEvenerSessionSeenSet           = "evener/session/seen/set"
	MethodEvenerSearch                   = "evener/search"
	MethodEvenerArchivedList             = "evener/archived/list"
	MethodEvenerActivityRead             = "evener/activity/read"
	MethodEvenerNoticesList              = "evener/notices/list"
	MethodEvenerHarnessesList            = "evener/harnesses/list"
	MethodEvenerUpgrade                  = "evener/upgrade"
	MethodEvenerUpdateCheck              = "evener/update/check"
	MethodEvenerUpdateApply              = "evener/update/apply"
	MethodEvenerHubRestart               = "evener/hub/restart"
	MethodEvenerAuthStatus               = "evener/auth/status"
	MethodEvenerAuthTest                 = "evener/auth/test"
	MethodEvenerAuthLoginStart           = "evener/auth/login/start"
	MethodEvenerAuthLoginComplete        = "evener/auth/login/complete"
	MethodEvenerAuthLogout               = "evener/auth/logout"
	MethodEvenerAuthList                 = "evener/auth/list"
	MethodEvenerAuthApiKeySet            = "evener/auth/apiKey/set"
	MethodEvenerAuthApiKeyClear          = "evener/auth/apiKey/clear"
	MethodEvenerAuthApiKeyConditionalSet = "evener/auth/apiKey/conditionalSet"
	MethodEvenerAuthCredentialJsonSet    = "evener/auth/credentialJson/set"
	MethodEvenerAuthDeviceStart          = "evener/auth/device/start"
	MethodEvenerAuthDevicePoll           = "evener/auth/device/poll"
	MethodEvenerLaunchResolve            = "evener/launch/resolve"
	MethodEvenerLaunchSchema             = "evener/launch/schema"
	MethodEvenerLaunchGetLayer           = "evener/launch/getLayer"
	MethodEvenerLaunchSetLayer           = "evener/launch/setLayer"
	MethodEvenerLaunchTrustRepo          = "evener/launch/trustRepo"
	MethodModelList                      = "model/list"
	MethodEvenerInstanceList             = "evener/instance/list"
	MethodEvenerInstanceCreate           = "evener/instance/create"
	MethodEvenerInstanceEdit             = "evener/instance/edit"
	MethodEvenerInstanceRemove           = "evener/instance/remove"
	MethodEvenerInstanceSetDefault       = "evener/instance/setDefault"
	MethodEvenerInstanceSetModelDisabled = "evener/instance/setModelDisabled"
	MethodEvenerInstanceRefreshModels    = "evener/instance/refreshModels"
	MethodEvenerPluginCheckNow           = "evener/plugin/checkNow"
	MethodEvenerPluginPreview            = "evener/plugin/preview"
	MethodEvenerMarketplaceList          = "evener/marketplace/list"
	MethodEvenerMarketplaceAdd           = "evener/marketplace/add"
	MethodEvenerMarketplaceRemove        = "evener/marketplace/remove"
	MethodEvenerMarketplaceRefresh       = "evener/marketplace/refresh"
	MethodEvenerMarketplaceEdit          = "evener/marketplace/edit"
	MethodEvenerMarketplaceBrowse        = "evener/marketplace/browse"
	MethodEvenerPluginList               = "evener/plugin/list"
	MethodEvenerPluginInstall            = "evener/plugin/install"
	MethodEvenerPluginUpgrade            = "evener/plugin/upgrade"
	MethodEvenerPluginRemove             = "evener/plugin/remove"
	MethodEvenerPluginEnable             = "evener/plugin/enable"
	MethodEvenerPluginDisable            = "evener/plugin/disable"
	MethodEvenerPluginSetAutoUpgrade     = "evener/plugin/setAutoUpgrade"
	MethodEvenerPluginCheckUpdates       = "evener/plugin/checkUpdates"
	MethodEvenerCommandList              = "evener/command/list"
	MethodEvenerSpawnSlashCatalog        = "evener/spawn/slashCatalog"

	// MethodEvenerSettingsOverview returns the field bag behind five settings
	// sections whose only data path today is Go-template variables:
	// hub/runtime, storage, agent roster, and probed MCP servers. See
	// SettingsOverviewResponse's doc comment.
	MethodEvenerSettingsOverview = "evener/settings/overview"
	// MethodEvenerSandboxEscalationResolve delivers a human's approve/deny decision
	// for a pending sandbox-exemption escalation (M7). Client→server; ScopeBoth
	// (daemon serves it; hub relays). It is a UI-only request, never advertised to
	// the model.
	MethodEvenerSandboxEscalationResolve = "evener/sandbox/escalation/resolve"
	// MethodEvenerDelegateStop ends one subagent's current run at the user's
	// request (S6): that subagent alone, never the subagents it started. It
	// targets the root session (ref/threadId) and names the delegate.
	// ScopeBoth (the root's daemon serves it; the hub relays). A UI-only
	// request, never advertised to the model.
	MethodEvenerDelegateStop = "evener/delegate/stop"
	// MethodEvenerJobsStop asks the owning session to run its existing job_stop
	// owner for one typed job or stable owned-delegate target. ScopeBoth.
	MethodEvenerJobsStop = "evener/jobs/stop"
	// MethodEvenerHostRequest forwards one hub-scoped admin RPC to a named
	// remote host's hub (component 07a). Host is the component-03 source ID;
	// Method must be in the proxy's exact allow-list. See HostRequestParams.
	MethodEvenerHostRequest = "evener/host/request"
	// MethodEvenerHostAttach explicitly attaches one configured remote host
	// (component 06's Connect action, component 08's host management surface).
	// It is the browser-reachable trigger that wraps the Ensure-backed dialing
	// seam; every other remote path is attached-only. See HostAttachParams.
	MethodEvenerHostAttach = "evener/host/attach"
	// MethodEvenerHostAdd registers one host entry (component 08 slice 1: name
	// + SSH address + key path). The entry is written into the machine-managed
	// hub.toml; a duplicate of a live name is refused. See HostAddParams.
	MethodEvenerHostAdd = "evener/host/add"
	// MethodEvenerHostList returns every known host with truthful online state
	// (component 08 slice 1). Attached rows report live channel facts; rows
	// without a live channel render last-known state as offline. See
	// HostListResponse.
	MethodEvenerHostList = "evener/host/list"
	// MethodEvenerHostStatus returns one host's row (component 08 slice 1): the
	// same HostRow evener/host/list serves, for a single named host. Never
	// dials. See HostStatusParams.
	MethodEvenerHostStatus = "evener/host/status"
	// MethodEvenerHostRemove deregisters one live host entry (component 08
	// slice 1): its supervisor stops, its channel drops, and the name is gone
	// until re-added. Every live host is removable here. See
	// HostRemoveParams.
	MethodEvenerHostRemove = "evener/host/remove"
	// MethodEvenerHostUpdate edits one live host entry in place: every field
	// except the name is mutable, the name is the request's target, and the
	// entry's advancement of the registry generation retires the host's
	// channel. Every live host is editable here. See HostUpdateParams.
	MethodEvenerHostUpdate = "evener/host/update"
	// MethodEvenerHostPlan plans one deploy against a named host and mints the
	// single-use confirmation token evener/host/deploy consumes (deploy pipeline
	// 08b §10). It is a mutation — it persists the token — so it admits like the
	// settings mutations and refuses a remote origin. Its result is the
	// HostPlanResult union: the plan plus token, or the no-token arm naming why
	// nothing was minted. See HostPlanParams.
	MethodEvenerHostPlan = "evener/host/plan"
	// MethodEvenerHostDeploy consumes a plan's confirmation token and starts
	// the deploy operation it names (deploy pipeline 08b §6, §10): dedup-first
	// on the client operation ID, the confirmed token's single use, and a
	// durable operation record whose worker runs outside the RPC. It is a
	// mutation and admits like the settings mutations; it refuses a remote
	// origin. See HostDeployParams.
	MethodEvenerHostDeploy = "evener/host/deploy"
	// MethodEvenerHostRestart starts a restart operation for one named host
	// (deploy pipeline 08b §6, §10): no token — restart has no install step —
	// but the same durable record, dedup and gate model as deploy, with the
	// request naming the intended (generation, incarnation id) pair. See
	// HostRestartParams.
	MethodEvenerHostRestart = "evener/host/restart"
	// MethodEvenerHostOperations reads the operation store's records (deploy
	// pipeline 08b §8, §10): one bounded page ascending by the
	// controller-assigned id, resumed by an opaque cursor, either host-pinned
	// or unfiltered across hosts. It is a read — no gate, no dial, no registry
	// resolution — so a removed host's retained history stays readable, and it
	// admits like the rest of the host surface: controller-local, refusing a
	// remote origin. See HostOperationsParams.
	MethodEvenerHostOperations = "evener/host/operations"
	// MethodEvenerHostTeardownRetry resumes one named teardown remnant
	// (registry spec 08 §6/§11): it looks the remnant up by its opaque id — the
	// lookup never requires a current live entry — try-acquires the host's
	// gate, claims under the mutation lock, runs the pinned teardown to
	// completion with no lock held, and finalizes from the observed result. Its
	// result is the six-arm `outcome` x `hostKind` union. See
	// HostTeardownRetryParams.
	MethodEvenerHostTeardownRetry = "evener/host/teardown-retry"
	// MethodEvenerHostTeardownRecover clears a remnant whose pinned target is
	// unresolvable, on an authenticated operator's audited attestation
	// (registry spec 08 §6/§11): gate first, the safety checks immediately
	// before the clearing write, and the attestation recorded on the original
	// receipt beside `remnantResolvedAt`. See HostTeardownRecoverParams.
	MethodEvenerHostTeardownRecover = "evener/host/teardown-recover"
	// MethodEvenerHostRunning serves one hub's own running build and health to
	// the controller probing it (deploy pipeline 08b §6 step 2, §10). It is
	// served locally by every hub and admitted only over an attached controller
	// session peered by the handshake; its params carry the caller's fencing
	// epoch (required on the wire, never a default) and its response is the
	// local build revision, the authoritative health flag, and the local process
	// start time when known. See HostRunningParams.
	MethodEvenerHostRunning = "evener/host/running"
	// MethodEvenerHostPushCredentials copies the controller's local
	// provider-instance keys to one named remote host (component 07c). The unit
	// of the push is the local credentials-store entry; each key is sent
	// verbatim as the Provider value only to the two provider-keyed auth
	// methods, and the host's own conditional set does the write. See
	// HostPushCredentialsParams.
	MethodEvenerHostPushCredentials = "evener/host/pushCredentials"
	// MethodEvenerSessionImage fetches one image out of the recipient hub's own
	// local session state (multi-host component 05). The controller's
	// host-qualified image routes proxy through it when the route id names
	// another source, so bytes stamped by a remote hub never resolve against
	// the controller's filesystem. See SessionImageParams.
	MethodEvenerSessionImage = "evener/session/image"
	// MethodEvenerSessionDocument reads one document out of the recipient hub's
	// own local session state (S7): the controller's /doc/file proxies through
	// it when the session id names another source, as the image routes do
	// through MethodEvenerSessionImage. See SessionDocumentParams.
	MethodEvenerSessionDocument = "evener/session/document"
)

const (
	NotifyThreadStarted       = "thread/started"
	NotifyThreadClosed        = "thread/closed"
	NotifyThreadStatusChanged = "thread/status/changed"
	NotifyThreadQueueChanged  = "thread/queueChanged"
	NotifyThreadNameChanged   = "evener/thread/name/changed"
	// NotifyThreadModelChanged pushes a mid-session model/provider switch so
	// clients converge without re-reading the thread. See ThreadModelChangedParams.
	NotifyThreadModelChanged = "thread/model/changed"
	// NotifyThreadReasoningEffortChanged pushes a mid-session reasoning-effort
	// change. See ThreadReasoningEffortChangedParams.
	NotifyThreadReasoningEffortChanged = "thread/reasoning-effort/changed"
	// NotifyThreadVisionModelChanged pushes a mid-session vision-model change.
	// See ThreadVisionModelChangedParams.
	NotifyThreadVisionModelChanged    = "thread/vision-model/changed"
	NotifyWarning                     = "warning"
	NotifyEvenerContextPressure       = "evener/thread/contextPressure/updated"
	NotifyEvenerThreadModelRetry      = "evener/thread/modelRetry"
	NotifyEvenerThreadResync          = "evener/thread/resync"
	NotifyEvenerThreadActivityChanged = "evener/thread/activity/changed"
	NotifyEvenerTaskUpdated           = "evener/task/updated"
	NotifyEvenerGoalUpdated           = "evener/goal/updated"
	NotifyEvenerNotesUpdated          = "evener/notes/updated"
	NotifyEvenerUrlsUpdated           = "evener/urls/updated"
	NotifyEvenerJobStarted            = "evener/job/started"
	NotifyEvenerJobFinished           = "evener/job/finished"
	NotifyEvenerDelegateUpdated       = "evener/delegate/updated"
	NotifyEvenerJobsTreeUpdated       = "evener/jobs/treeUpdated"
	NotifyEvenerAuthUpdated           = "evener/auth/updated"
	NotifyEvenerLaunchUpdated         = "evener/launch/updated"
	NotifyEvenerAttentionChanged      = "evener/attention/changed"
	NotifyEvenerNavigationInvalidated = "evener/navigation/invalidated"
	NotifyEvenerMarketplaceUpdated    = "evener/marketplace/updated"
	NotifyEvenerPluginUpdated         = "evener/plugin/updated"
	NotifyEvenerNoticesChanged        = "evener/notices/changed"
	// NotifyEvenerSandboxEscalationRequested pushes a harness-raised, human-gated
	// sandbox-exemption approval card to the client (M7). The tool-exec goroutine
	// blocks until the client answers with MethodEvenerSandboxEscalationResolve.
	NotifyEvenerSandboxEscalationRequested = "evener/sandbox/escalation/requested"
	// NotifyEvenerSandboxEscalationResolved pushes notice that a previously-raised
	// escalation left the pending set (M7, wire-honesty spec Part B): resolved
	// explicitly, cleared by turn-interrupt, or cleared by session close. Every
	// OTHER subscribed client uses it to clear its own now-stale copy of the
	// card. Emitted exactly once per escalation, from the convergence point in
	// agent/session_escalation.go's escalateOnSandboxDenial.
	NotifyEvenerSandboxEscalationResolved = "evener/sandbox/escalation/resolved"
	// NotifyEvenerHostNotification re-emits one host-owned config notification
	// (evener/auth/updated, evener/launch/updated, evener/marketplace/updated,
	// evener/plugin/updated, evener/settings/agentsDoc/changed) tagged with the
	// source host (component 07a). Local notifications keep their own unwrapped
	// methods; this wrapper is emitted only for remote hosts. See
	// HostNotificationParams: the emitter is this component's hub-side fan-out,
	// and the browser-side consumer that unwraps it is component 07b.
	NotifyEvenerHostNotification = "evener/host/notification"
)

const (
	ThreadStatusIdle        = "idle"
	ThreadStatusActive      = "active"
	ThreadStatusAwaiting    = "awaiting"
	ThreadStatusWarning     = "warning"
	ThreadStatusClosed      = "closed"
	ThreadStatusNotLoaded   = "notLoaded"
	ThreadStatusSystemError = "systemError"
)

const (
	TurnStatusInProgress  = "inProgress"
	TurnStatusCompleted   = "completed"
	TurnStatusFailed      = "failed"
	TurnStatusInterrupted = "interrupted"
)
