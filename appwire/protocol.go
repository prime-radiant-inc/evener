package appwire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MethodScope records which evener binaries expose a request method.
type MethodScope string

const (
	// ScopeBoth: handled by both evener hub and the evener serve daemon.
	ScopeBoth MethodScope = "both"
	// ScopeHub: handled only by evener hub (auth, launch config, provider
	// instances, model listing — hub-mediated concerns).
	ScopeHub MethodScope = "hub"
	// ScopeDaemon: handled only by the evener serve daemon engine.
	ScopeDaemon MethodScope = "daemon"
	// ScopeConnection: handled by the connection itself on every server,
	// outside the method router (initialize, ping).
	ScopeConnection MethodScope = "connection"
	// ScopeUnimplemented: a defined method with a client stub but no server
	// handler on any router (reserved / not yet served).
	ScopeUnimplemented MethodScope = "unimplemented"
)

// Routed reports whether a scope corresponds to a method registered on a
// request router (so the catalog↔router cross-check applies). Connection-level
// and unimplemented methods are intentionally absent from the routers.
func (s MethodScope) Routed() bool {
	return s == ScopeBoth || s == ScopeHub || s == ScopeDaemon
}

// ConnectionMethodNames returns the wire names of connection-level methods
// (initialize, ping). These are part of the connection handshake rather than
// application routing, so the catalog↔router cross-check excludes them even
// though initialize is, as an implementation detail, registered on the router.
func ConnectionMethodNames() []string {
	var out []string
	for _, m := range Methods {
		if m.Scope == ScopeConnection {
			out = append(out, m.Name)
		}
	}
	return out
}

// CatalogMethodNames returns the wire names of catalog methods whose scope
// includes the given router scope (ScopeHub or ScopeDaemon) — i.e. those that
// must be registered on that binary's router. ScopeBoth methods are included
// for either. Connection-level and unimplemented methods are excluded.
func CatalogMethodNames(scope MethodScope) []string {
	var out []string
	for _, m := range Methods {
		if m.Scope == scope || m.Scope == ScopeBoth {
			out = append(out, m.Name)
		}
	}
	return out
}

// MethodSpec is one request method in the AppWire catalog: the wire name, the
// Go param/result types (zero values, so the doc generator can reflect their
// JSON fields), the scope, and a one-line summary.
//
// Methods (below) is the single source of truth for the generated protocol
// reference (docs/appwire-protocol.md) and is cross-checked against the live
// hub and daemon routers in protocol_test.go, so the catalog cannot drift from
// what is actually wired.
type MethodSpec struct {
	Name    string
	Params  any
	Result  any
	Scope   MethodScope
	Summary string
}

// NotificationSpec is one server→client notification: the wire name, the Go
// payload type (zero value), and a one-line summary of when it fires.
type NotificationSpec struct {
	Name    string
	Payload any
	Summary string
}

// Methods is the AppWire request-method catalog. Order matches the Method*
// constants in types.go. Keep an entry here for every routed method; the
// cross-check tests (cmd/evener-hub, server) fail if a routed method is
// registered without a matching catalog entry or vice versa.
var Methods = []MethodSpec{
	{MethodInitialize, InitializeParams{}, InitializeResponse{}, ScopeConnection, "Handshake; must be the first request. Returns server info, protocol version, source ID, and feature set."},
	{MethodPing, EmptyParams{}, EmptyResponse{}, ScopeConnection, "Connection keepalive, answered directly before the initialize gate (the browser's app-level heartbeat)."},
	{MethodThreadList, ThreadListParams{}, ThreadListResponse{}, ScopeBoth, "Lists threads; the daemon returns its single session."},
	{MethodThreadRead, ThreadReadParams{}, ThreadReadResponse{}, ScopeBoth, "Reads one thread and optionally subscribes to its live updates."},
	{MethodThreadTurnsList, ThreadTurnsListParams{}, ThreadTurnsListResponse{}, ScopeBoth, "Pages turns backward (older) for lazy transcript loading; the cold load seeds the latest window via thread/read(turnLimit)."},
	{MethodThreadTurnItemsList, ThreadTurnItemsListParams{}, ThreadTurnItemsListResponse{}, ScopeUnimplemented, "Codex-parity: paginated items for one turn. Experimental even in Codex (returns method-not-supported) and served by no evener router."},
	{MethodThreadStart, ThreadStartParams{}, ThreadStartResponse{}, ScopeHub, "Starts a new thread and attaches a live-update relay."},
	{MethodThreadResume, ThreadResumeParams{}, ThreadResumeResponse{}, ScopeHub, "Resumes an existing session and attaches its relay."},
	{MethodThreadFork, ThreadForkParams{}, ThreadForkResponse{}, ScopeHub, "Forks a thread from a source turn, either replacing the turn with edited input or deferring the original input back to the client for editing (deferInput, mutually exclusive with editedInput). With `aside: true` (local evener threads only; mutually exclusive with sourceTurnId/editedInput/deferInput/label), forks the session at its tip into a side thread that inherits the parent's permissions and config."},
	{MethodThreadClear, ThreadClearParams{}, ThreadClearResponse{}, ScopeBoth, "Clears the thread's conversation (rejected while a turn is processing)."},
	{MethodThreadModelSet, ThreadModelSetParams{}, EmptyResponse{}, ScopeBoth, "Changes the session's model/provider."},
	{MethodEvenerThreadNameSet, ThreadNameSetParams{}, EmptyResponse{}, ScopeBoth, "Sets a user-chosen session title (rename)."},
	{MethodThreadReasoningEffortSet, ThreadReasoningEffortSetParams{}, EmptyResponse{}, ScopeBoth, "Sets reasoning effort, normalizing and validating the value."},
	{MethodThreadVisionModelSet, ThreadVisionModelSetParams{}, EmptyResponse{}, ScopeBoth, "Sets the vision side-channel routing (\"\", \"off\", or a model ref)."},
	{MethodThreadCompactStart, ThreadCompactStartParams{}, EmptyResponse{}, ScopeBoth, "Starts a context-compaction pass on the session."},
	{MethodThreadShutdown, ThreadShutdownParams{}, EmptyResponse{}, ScopeBoth, "Shuts the session down (the daemon runs it asynchronously)."},
	{MethodTurnStart, TurnStartParams{}, TurnStartResponse{}, ScopeBoth, "Starts a new user turn and reserves a turn ID."},
	{MethodTurnSteer, TurnSteerParams{}, TurnSteerResponse{}, ScopeBoth, "Injects a steering message into the active turn."},
	{MethodTurnInterrupt, TurnInterruptParams{}, TurnInterruptResponse{}, ScopeBoth, "Cancels whatever turn the session is running; the receipt names the turn actually cancelled."},
	{MethodTurnQueue, TurnQueueParams{}, TurnQueueResponse{}, ScopeBoth, "Queues a user message for after the active turn completes."},
	{MethodTurnDrainAsSteer, TurnDrainAsSteerParams{}, TurnDrainAsSteerResponse{}, ScopeBoth, "Drains the input queue and injects it as a single steering message."},
	{MethodTurnPromoteQueuedAsSteer, TurnPromoteQueuedAsSteerParams{}, TurnPromoteQueuedAsSteerResponse{}, ScopeBoth, "Removes one queued message by index and injects it as user-sourced steering into the in-flight turn."},
	{MethodTurnCancelQueued, TurnCancelQueuedParams{}, TurnCancelQueuedResponse{}, ScopeBoth, "Removes one queued message by index so it is never consumed (cancel; also the removal half of edit-and-recompose)."},
	{MethodGoalSet, GoalSetParams{}, GoalSetResponse{}, ScopeBoth, "Sets or clears the session's /goal objective."},
	{MethodEvenerTasksList, TaskListParams{}, TaskListResponse{}, ScopeBoth, "Lists the session's tasks."},
	{MethodEvenerJobsList, JobsListParams{}, JobsListResponse{}, ScopeBoth, "Returns the current-session activity tree. Hub-served for exited sessions via the persisted jobs.jsonl fallback; older daemons may still return a flat array in JobsListResponse.Data."},
	{MethodEvenerJobsOutput, JobsOutputParams{}, JobsOutputResponse{}, ScopeBoth, "Reads a byte tail of one job's output. Hub-served for exited sessions via the persisted jobs.jsonl fallback."},
	{MethodEvenerThreadTranscriptsList, ThreadTranscriptListParams{}, ThreadTranscriptListResponse{}, ScopeHub, "Lists transcript targets (subagents/related threads) for a ref."},
	{MethodEvenerSubagentPreview, EvenerSubagentPreviewParams{}, EvenerSubagentPreviewResponse{}, ScopeHub, "Reads a bounded lazy preview of a subagent transcript's latest direct items."},
	{MethodEvenerPathsComplete, PathsCompleteParams{}, PathsCompleteResponse{}, ScopeHub, "Path autocompletion for a prefix."},
	{MethodEvenerDirsCreate, DirsCreateParams{}, DirsCreateResponse{}, ScopeHub, "Creates a missing working directory and its parents for Spawn preflight."},
	{MethodEvenerProjectsRecent, ProjectsRecentParams{}, ProjectsRecentResponse{}, ScopeHub, "Lists the most recently used project working directories (session creation path-dropdown options; default cap 15)."},
	{MethodEvenerPathValidate, PathValidateParams{}, PathValidateResponse{}, ScopeHub, "Validates a launch path."},
	{MethodEvenerGitHead, GitHeadParams{}, GitHeadResponse{}, ScopeHub, "Reads git HEAD for a working directory."},
	{MethodEvenerMobilePairing, MobilePairingParams{}, MobilePairingResponse{}, ScopeHub, "Creates a validated mobile pairing URL for the authenticated web application."},
	{MethodEvenerNavigationRead, NavigationReadParams{}, NavigationReadResponse{}, ScopeHub, "Reads one bounded, revisioned hub navigation resource, optionally conditional on its ETag."},
	{MethodEvenerFavoriteSet, FavoriteSetParams{}, FavoriteSetResponse{}, ScopeHub, "Sets or clears a project favorite and returns the committed navigation invalidation targets."},
	{MethodEvenerArchiveSet, ArchiveParams{}, ArchiveResponse{}, ScopeHub, "Sets or clears an explicit project or session archive decision and returns its committed navigation receipt."},
	{MethodEvenerProjectDelete, ProjectDeleteParams{}, ProjectDeleteResponse{}, ScopeHub, "Deletes every removable session in one path-validated local project and returns detailed outcomes plus its committed navigation receipt."},
	{MethodEvenerSessionDelete, SessionDeleteParams{}, SessionDeleteResponse{}, ScopeHub, "Deletes one ended or confirmed-crashed local session; live or concurrently reserved targets are returned in skipped, and successful cleanup includes its committed navigation receipt."},
	{MethodEvenerPinSectionRename, PinSectionRenameParams{}, PinSectionRenameResponse{}, ScopeHub, "Renames a named pin section and returns its canonical summary and committed navigation receipt."},
	{MethodEvenerPinSectionDelete, PinSectionDeleteParams{}, PinSectionDeleteResponse{}, ScopeHub, "Deletes a named pin section and returns its removed membership and committed navigation receipt."},
	{MethodEvenerSessionPinAssign, SessionPinAssignParams{}, SessionPinMutationResponse{}, ScopeHub, "Assigns a top-level session to a named pin section and returns the canonical assignment and committed navigation receipt."},
	{MethodEvenerSessionPinUnpin, SessionPinUnpinParams{}, SessionPinMutationResponse{}, ScopeHub, "Removes a top-level session's named pin assignment and returns its committed navigation receipt."},
	{MethodEvenerSearch, SearchParams{}, SearchResponse{}, ScopeHub, "Searches live and persisted sessions for the hub command palette."},
	{MethodEvenerHarnessesList, HarnessListParams{}, HarnessListResponse{}, ScopeHub, "Lists available harness descriptors."},
	{MethodEvenerUpgrade, UpgradeParams{}, UpgradeResponse{}, ScopeHub, "Performs or reports a evener binary upgrade."},
	{MethodEvenerAuthStatus, AuthStatusParams{}, AuthStatusResponse{}, ScopeHub, "Reports auth/credential status for a provider."},
	{MethodEvenerAuthTest, AuthTestParams{}, AuthTestResponse{}, ScopeHub, "Tests the effective credentials for one configured provider instance without starting a session."},
	{MethodEvenerAuthLoginStart, AuthLoginStartParams{}, AuthLoginStartResponse{}, ScopeHub, "Begins an OAuth login flow; returns a flow ID and URL."},
	{MethodEvenerAuthLoginComplete, AuthLoginCompleteParams{}, AuthLoginCompleteResponse{}, ScopeHub, "Completes OAuth login; broadcasts evener/auth/updated."},
	{MethodEvenerAuthLogout, AuthLogoutParams{}, AuthLogoutResponse{}, ScopeHub, "Logs out a provider; broadcasts evener/auth/updated."},
	{MethodEvenerAuthList, EmptyParams{}, AuthListResponse{}, ScopeHub, "Lists auth status for all providers."},
	{MethodEvenerAuthApiKeySet, AuthApiKeySetParams{}, AuthStatusResponse{}, ScopeHub, "Stores a provider API key; broadcasts evener/auth/updated."},
	{MethodEvenerAuthDeviceStart, AuthDeviceStartParams{}, AuthDeviceStartResponse{}, ScopeHub, "Begins a device-code auth flow (or signals fallback)."},
	{MethodEvenerAuthDevicePoll, AuthDevicePollParams{}, AuthDevicePollResponse{}, ScopeHub, "Polls a device-code flow; broadcasts evener/auth/updated when authorized."},
	{MethodEvenerLaunchResolve, LaunchConfigResolveParams{}, LaunchConfigResolved{}, ScopeHub, "Resolves the effective launch config for a cwd."},
	{MethodEvenerLaunchSchema, EmptyParams{}, LaunchOptionSchemaResponse{}, ScopeHub, "Returns the launch-option schema."},
	{MethodEvenerLaunchGetLayer, LaunchConfigGetLayerParams{}, LaunchConfigLayer{}, ScopeHub, "Reads one launch-config layer (global/project)."},
	{MethodEvenerLaunchSetLayer, LaunchConfigSetLayerParams{}, LaunchConfigResolved{}, ScopeHub, "Writes a launch-config layer; broadcasts evener/launch/updated."},
	{MethodEvenerLaunchTrustRepo, LaunchConfigTrustRepoParams{}, LaunchConfigResolved{}, ScopeHub, "Trusts a repo's launch config by hash; broadcasts evener/launch/updated."},
	{MethodModelList, ModelListParams{}, ModelListResponse{}, ScopeBoth, "Lists available models with launch diagnostics."},
	{MethodEvenerInstanceList, EmptyParams{}, InstanceListResponse{}, ScopeHub, "Lists configured provider instances."},
	{MethodEvenerInstanceCreate, InstanceCreateParams{}, InstanceListResponse{}, ScopeHub, "Creates a provider instance; returns the updated list."},
	{MethodEvenerInstanceEdit, InstanceEditParams{}, InstanceListResponse{}, ScopeHub, "Edits a provider instance; returns the updated list."},
	{MethodEvenerInstanceRemove, InstanceRemoveParams{}, InstanceListResponse{}, ScopeHub, "Removes a provider instance; returns the updated list."},
	{MethodEvenerInstanceSetDefault, InstanceSetDefaultParams{}, InstanceListResponse{}, ScopeHub, "Sets the default provider instance; returns the updated list."},
	{MethodEvenerPluginCheckNow, EmptyParams{}, PluginCheckNowResponse{}, ScopeHub, "Runs one auto-upgrade daemon pass on demand; broadcasts evener/plugin/updated per plugin actually upgraded."},
	{MethodEvenerPluginPreview, PluginPreviewParams{}, PluginPreviewResponse{}, ScopeHub, "Previews the plugins selected for a launch without starting a session or executing plugin commands."},
	{MethodEvenerMarketplaceList, EmptyParams{}, MarketplaceListResponse{}, ScopeHub, "Lists registered plugin marketplaces."},
	{MethodEvenerMarketplaceAdd, MarketplaceAddParams{}, MarketplaceListResponse{}, ScopeHub, "Registers a plugin marketplace; returns the updated list."},
	{MethodEvenerMarketplaceRemove, MarketplaceNameParams{}, MarketplaceListResponse{}, ScopeHub, "Unregisters a plugin marketplace; returns the updated list."},
	{MethodEvenerMarketplaceRefresh, MarketplaceNameParams{}, MarketplaceListResponse{}, ScopeHub, "Pulls a marketplace's latest catalog; returns the updated list."},
	{MethodEvenerMarketplaceBrowse, MarketplaceBrowseParams{}, MarketplaceBrowseResponse{}, ScopeHub, "Lists a marketplace's plugin catalog for browsing/install."},
	{MethodEvenerPluginList, EmptyParams{}, PluginListResponse{}, ScopeHub, "Lists installed plugins."},
	{MethodEvenerPluginInstall, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Installs a plugin from a marketplace; returns the updated list."},
	{MethodEvenerPluginUpgrade, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Upgrades an installed plugin to its marketplace's latest; returns the updated list."},
	{MethodEvenerPluginRemove, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Removes an installed plugin; returns the updated list."},
	{MethodEvenerPluginEnable, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Enables an installed plugin; returns the updated list."},
	{MethodEvenerPluginDisable, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Disables an installed plugin; returns the updated list."},
	{MethodEvenerPluginSetAutoUpgrade, PluginSetAutoUpgradeParams{}, PluginListResponse{}, ScopeHub, "Sets an installed plugin's auto-upgrade flag; returns the updated list."},
	{MethodEvenerCommandList, EmptyParams{}, CommandListResponse{}, ScopeHub, "Lists loaded slash commands (name, plugin, description, source: plugin, project, or user) for catalog/autocomplete display."},
	{MethodEvenerSettingsOverview, EmptyParams{}, SettingsOverviewResponse{}, ScopeHub, "Returns the settings overview field bag: hub/runtime, storage, agent roster, codex launch configs, and probed MCP servers — the six template-only settings sections' data."},
	{MethodEvenerSettingsTranscriptDisplayGet, EmptyParams{}, TranscriptDisplayDefaults{}, ScopeHub, "Reads the canonical Desktop and Mobile transcript-display defaults."},
	{MethodEvenerSettingsTranscriptDisplayPatch, TranscriptDisplayDefaultsPatchParams{}, TranscriptDisplayPatchResponse{}, ScopeHub, "Updates one transcript-display default using an expected revision and returns the canonical value."},
	{MethodEvenerSandboxEscalationResolve, SandboxEscalationResolveParams{}, EmptyResponse{}, ScopeBoth, "Delivers a human's approve/deny decision for a pending sandbox-exemption escalation (M7); the daemon unblocks the waiting tool-exec goroutine, the hub relays."},
}

// ValidateMutationParams enforces the flag-day v2 identity and precondition
// fields before a mutation reaches either Hub or daemon handlers.
func ValidateMutationParams(method string, raw json.RawMessage) error {
	// No control mutation requires a turn id: control is session-scoped, and it
	// applies to whatever the session is running. What remains required is the
	// identity every retry-safe mutation needs, plus the preconditions that name
	// a real object rather than a moment in time.
	required := map[string][]string{
		MethodTurnStart:                {"clientMutationId"},
		MethodTurnSteer:                {"clientMutationId"},
		MethodTurnInterrupt:            {"clientMutationId"},
		MethodTurnQueue:                {"clientMutationId"},
		MethodTurnDrainAsSteer:         {"clientMutationId", "expectedQueueRevision"},
		MethodTurnPromoteQueuedAsSteer: {"clientMutationId", "expectedEntryId"},
		MethodTurnCancelQueued:         {"clientMutationId", "expectedEntryId"},
	}[method]
	if len(required) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range required {
		value, ok := fields[name]
		if !ok {
			return fmt.Errorf("%s is required", name)
		}
		if name == "expectedQueueRevision" {
			var revision uint64
			if strings.TrimSpace(string(value)) == "null" || json.Unmarshal(value, &revision) != nil {
				return fmt.Errorf("%s must be an unsigned integer", name)
			}
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil || strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

// Notifications is the AppWire server→client notification catalog. A nil
// Payload marks a notification whose shape is not described by a Go type;
// none are, today — every notification declares a real payload type (kata
// 4j2t retired the last holdout, evener/attention/changed, by relocating its
// payload type from hubcore into this package). Notifications that carry no
// fields at all declare EmptyParams, so "no payload" and "payload
// undescribed" read differently. The constant NotifyEvenerContextPressure is
// intentionally absent — it is defined but emitted by nothing (context
// pressure rides on the Thread snapshot instead).
var Notifications = []NotificationSpec{
	{NotifyThreadStarted, ThreadStartedParams{}, "Session started; carries the initial Thread snapshot."},
	{NotifyThreadClosed, ThreadClosedParams{}, "Session ended."},
	{NotifyThreadStatusChanged, ThreadStatusChangedParams{}, "Thread status (type + active flags) changed."},
	{NotifyThreadQueueChanged, ThreadQueueChangedParams{}, "The per-session input queue depth/preview changed."},
	{NotifyThreadNameChanged, ThreadNameChangedParams{}, "The session title changed (generated or user-renamed)."},
	{NotifyThreadModelChanged, ThreadModelChangedParams{}, "The session's model/provider changed mid-session (thread/model/set or an equivalent switch)."},
	{NotifyThreadReasoningEffortChanged, ThreadReasoningEffortChangedParams{}, "The session's reasoning effort changed mid-session (thread/reasoning-effort/set)."},
	{NotifyThreadVisionModelChanged, ThreadVisionModelChangedParams{}, "The session's vision side-channel routing changed mid-session (thread/vision-model/set)."},
	{NotifyTurnStarted, TurnStartedParams{}, "A new turn began (inProgress)."},
	{NotifyTurnCompleted, TurnCompletedParams{}, "A turn reached a terminal state (completed/failed/interrupted)."},
	{NotifyItemStarted, ItemLifecycleParams{}, "A thread item began streaming."},
	{NotifyItemCompleted, ItemLifecycleParams{}, "A thread item finished."},
	{NotifyAgentMessageDelta, AgentMessageDeltaParams{}, "Incremental assistant-message text chunk for an item."},
	{NotifyAgentMessageReset, AgentMessageResetParams{}, "Discard the in-progress assistant item (a retry replaces it)."},
	{NotifyReasoningSummaryDelta, ReasoningSummaryDeltaParams{}, "Incremental reasoning-summary text chunk for a reasoning item."},
	{NotifyToolOutputDelta, ToolOutputDeltaParams{}, "Incremental tool-output chunk for a tool-call item."},
	{NotifyWarning, WarningParams{}, "Non-fatal diagnostic. Also used for cancelled turns and relay-attach failures."},
	{NotifyEvenerThreadModelRetry, ThreadModelRetryParams{}, "A model call failed with a retryable error and will be retried after a wait. Ephemeral liveness state, not a thread item."},
	{NotifyEvenerSteeringInjected, EvenerSteeringInjectedParams{}, "A steering message was injected into the active turn."},
	{NotifyEvenerJobStarted, EvenerJobParams{}, "A background job started."},
	{NotifyEvenerJobFinished, EvenerJobParams{}, "A background job finished; the job carries status/reason/exitCode/output."},
	{NotifyEvenerDelegateUpdated, EvenerDelegateParams{}, "A stable delegate projection changed."},
	{NotifyEvenerJobsTreeUpdated, JobsTreeUpdatedParams{}, "The current-session activity tree changed; clients refresh the jobs tree."},
	{NotifyEvenerAuthUpdated, EvenerAuthUpdatedParams{}, "Broadcast after a successful auth mutation. Clients refresh auth state."},
	{NotifyEvenerLaunchUpdated, EvenerLaunchUpdatedParams{}, "Broadcast after a launch layer/trust mutation. Clients refresh launch config."},
	{NotifyEvenerAttentionChanged, AttentionChangedPayload{}, "Hub-derived attention transitions for live sessions plus authoritative badge summary. Hub-originated; never sent by daemons."},
	{NotifyEvenerNavigationInvalidated, NavigationInvalidatedPayload{}, "Hub-derived scoped navigation-resource invalidation. Clients conditionally revalidate only the named loaded resources."},
	{NotifyEvenerMarketplaceUpdated, EmptyParams{}, "Broadcast after a marketplace mutation (add/remove/refresh); no payload. Clients refresh the marketplace list."},
	{NotifyEvenerPluginUpdated, EmptyParams{}, "Broadcast after a plugin mutation (install/upgrade/remove/enable/disable/setAutoUpgrade); no payload. Clients refresh the plugin list."},
	{NotifyEvenerThreadResync, ThreadResyncParams{}, "Hub-originated hint asking clients to re-read one thread after relay recovery."},
	{NotifyEvenerTaskUpdated, TaskUpdatedParams{}, "The session's task-list progress (total/done) changed."},
	{NotifyEvenerSandboxEscalationRequested, SandboxEscalationRequested{}, "A harness-raised, human-gated sandbox-exemption approval card (M7); the tool-exec goroutine blocks until answered via evener/sandbox/escalation/resolve."},
	{NotifyEvenerSandboxEscalationResolved, SandboxEscalationResolved{}, "A previously-raised sandbox escalation left the pending set — resolved, turn-interrupted, or cleared by session close (M7); every OTHER subscribed client clears its now-stale copy of the card."},
	{NotifyEvenerSettingsTranscriptDisplayChanged, TranscriptDisplayChangedParams{}, "Broadcast after a transcript-display default changes; carries the layout, revision, and canonical configuration."},
}
