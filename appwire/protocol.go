package appwire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MethodScope records which serf binaries expose a request method.
type MethodScope string

const (
	// ScopeBoth: handled by both serf-hub and the serf serve daemon.
	ScopeBoth MethodScope = "both"
	// ScopeHub: handled only by serf-hub (auth, launch config, provider
	// instances, model listing — hub-mediated concerns).
	ScopeHub MethodScope = "hub"
	// ScopeDaemon: handled only by the serf serve daemon engine.
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
// cross-check tests (cmd/serf-hub, server) fail if a routed method is
// registered without a matching catalog entry or vice versa.
var Methods = []MethodSpec{
	{MethodInitialize, InitializeParams{}, InitializeResponse{}, ScopeConnection, "Handshake; must be the first request. Returns server info, protocol version, source ID, and feature set."},
	{MethodPing, EmptyParams{}, EmptyResponse{}, ScopeConnection, "Connection keepalive, answered directly before the initialize gate (the browser's app-level heartbeat)."},
	{MethodThreadList, ThreadListParams{}, ThreadListResponse{}, ScopeBoth, "Lists threads; the daemon returns its single session."},
	{MethodThreadRead, ThreadReadParams{}, ThreadReadResponse{}, ScopeBoth, "Reads one thread and optionally subscribes to its live updates."},
	{MethodThreadTurnsList, ThreadTurnsListParams{}, ThreadTurnsListResponse{}, ScopeBoth, "Pages turns backward (older) for lazy transcript loading; the cold load seeds the latest window via thread/read(turnLimit)."},
	{MethodThreadTurnItemsList, ThreadTurnItemsListParams{}, ThreadTurnItemsListResponse{}, ScopeUnimplemented, "Codex-parity: paginated items for one turn. Experimental even in Codex (returns method-not-supported) and served by no serf router."},
	{MethodThreadStart, ThreadStartParams{}, ThreadStartResponse{}, ScopeHub, "Starts a new thread and attaches a live-update relay."},
	{MethodThreadResume, ThreadResumeParams{}, ThreadResumeResponse{}, ScopeHub, "Resumes an existing session and attaches its relay."},
	{MethodThreadFork, ThreadForkParams{}, ThreadForkResponse{}, ScopeHub, "Forks a thread from a source turn, either replacing the turn with edited input or deferring the original input back to the client for editing (deferInput, mutually exclusive with editedInput). With `aside: true` (local serf threads only; mutually exclusive with sourceTurnId/editedInput/deferInput/label), forks the session at its tip into a side thread that inherits the parent's permissions and config."},
	{MethodThreadClear, ThreadClearParams{}, ThreadClearResponse{}, ScopeBoth, "Clears the thread's conversation (rejected while a turn is processing)."},
	{MethodThreadModelSet, ThreadModelSetParams{}, EmptyResponse{}, ScopeBoth, "Changes the session's model/provider."},
	{MethodSerfThreadNameSet, ThreadNameSetParams{}, EmptyResponse{}, ScopeBoth, "Sets a user-chosen session title (rename)."},
	{MethodThreadReasoningEffortSet, ThreadReasoningEffortSetParams{}, EmptyResponse{}, ScopeBoth, "Sets reasoning effort, normalizing and validating the value."},
	{MethodThreadCompactStart, ThreadCompactStartParams{}, EmptyResponse{}, ScopeBoth, "Starts a context-compaction pass on the session."},
	{MethodThreadShutdown, ThreadShutdownParams{}, EmptyResponse{}, ScopeBoth, "Shuts the session down (the daemon runs it asynchronously)."},
	{MethodTurnStart, TurnStartParams{}, TurnStartResponse{}, ScopeBoth, "Starts a new user turn and reserves a turn ID."},
	{MethodTurnSteer, TurnSteerParams{}, TurnSteerResponse{}, ScopeBoth, "Injects a steering message into the active turn."},
	{MethodTurnInterrupt, TurnInterruptParams{}, TurnInterruptResponse{}, ScopeBoth, "Cancels the active turn matching expectedTurnId."},
	{MethodTurnQueue, TurnQueueParams{}, TurnQueueResponse{}, ScopeBoth, "Queues a user message for after the active turn completes."},
	{MethodTurnDrainAsSteer, TurnDrainAsSteerParams{}, TurnDrainAsSteerResponse{}, ScopeBoth, "Drains the input queue and injects it as a single steering message."},
	{MethodTurnPromoteQueuedAsSteer, TurnPromoteQueuedAsSteerParams{}, TurnPromoteQueuedAsSteerResponse{}, ScopeBoth, "Removes one queued message by index and injects it as user-sourced steering into the in-flight turn."},
	{MethodTurnCancelQueued, TurnCancelQueuedParams{}, TurnCancelQueuedResponse{}, ScopeBoth, "Removes one queued message by index so it is never consumed (cancel; also the removal half of edit-and-recompose)."},
	{MethodGoalSet, GoalSetParams{}, GoalSetResponse{}, ScopeBoth, "Sets or clears the session's /goal objective."},
	{MethodSerfTasksList, TaskListParams{}, TaskListResponse{}, ScopeBoth, "Lists the session's tasks."},
	{MethodSerfJobsList, JobsListParams{}, JobsListResponse{}, ScopeBoth, "Lists the session's jobs (shell and delegate). Hub-served for exited sessions via the persisted jobs.jsonl fallback."},
	{MethodSerfJobsOutput, JobsOutputParams{}, JobsOutputResponse{}, ScopeBoth, "Reads a byte tail of one job's output. Hub-served for exited sessions via the persisted jobs.jsonl fallback."},
	{MethodSerfThreadTranscriptsList, ThreadTranscriptListParams{}, ThreadTranscriptListResponse{}, ScopeHub, "Lists transcript targets (subagents/related threads) for a ref."},
	{MethodSerfSubagentPreview, SerfSubagentPreviewParams{}, SerfSubagentPreviewResponse{}, ScopeHub, "Reads a bounded lazy preview of a subagent transcript's latest direct items."},
	{MethodSerfPathsComplete, PathsCompleteParams{}, PathsCompleteResponse{}, ScopeHub, "Path autocompletion for a prefix."},
	{MethodSerfProjectsRecent, ProjectsRecentParams{}, ProjectsRecentResponse{}, ScopeHub, "Lists the most recently used project working directories (session creation path-dropdown options; default cap 15)."},
	{MethodSerfPathValidate, PathValidateParams{}, PathValidateResponse{}, ScopeHub, "Validates a launch path."},
	{MethodSerfHarnessesList, HarnessListParams{}, HarnessListResponse{}, ScopeHub, "Lists available harness descriptors."},
	{MethodSerfUpgrade, UpgradeParams{}, UpgradeResponse{}, ScopeHub, "Performs or reports a serf binary upgrade."},
	{MethodSerfAuthStatus, AuthStatusParams{}, AuthStatusResponse{}, ScopeHub, "Reports auth/credential status for a provider."},
	{MethodSerfAuthTest, AuthTestParams{}, AuthTestResponse{}, ScopeHub, "Tests the effective credentials for one configured provider instance without starting a session."},
	{MethodSerfAuthLoginStart, AuthLoginStartParams{}, AuthLoginStartResponse{}, ScopeHub, "Begins an OAuth login flow; returns a flow ID and URL."},
	{MethodSerfAuthLoginComplete, AuthLoginCompleteParams{}, AuthLoginCompleteResponse{}, ScopeHub, "Completes OAuth login; broadcasts serf/auth/updated."},
	{MethodSerfAuthLogout, AuthLogoutParams{}, AuthLogoutResponse{}, ScopeHub, "Logs out a provider; broadcasts serf/auth/updated."},
	{MethodSerfAuthList, EmptyParams{}, AuthListResponse{}, ScopeHub, "Lists auth status for all providers."},
	{MethodSerfAuthApiKeySet, AuthApiKeySetParams{}, AuthStatusResponse{}, ScopeHub, "Stores a provider API key; broadcasts serf/auth/updated."},
	{MethodSerfAuthDeviceStart, AuthDeviceStartParams{}, AuthDeviceStartResponse{}, ScopeHub, "Begins a device-code auth flow (or signals fallback)."},
	{MethodSerfAuthDevicePoll, AuthDevicePollParams{}, AuthDevicePollResponse{}, ScopeHub, "Polls a device-code flow; broadcasts serf/auth/updated when authorized."},
	{MethodSerfLaunchResolve, LaunchConfigResolveParams{}, LaunchConfigResolved{}, ScopeHub, "Resolves the effective launch config for a cwd."},
	{MethodSerfLaunchSchema, EmptyParams{}, LaunchOptionSchemaResponse{}, ScopeHub, "Returns the launch-option schema."},
	{MethodSerfLaunchGetLayer, LaunchConfigGetLayerParams{}, LaunchConfigLayer{}, ScopeHub, "Reads one launch-config layer (global/project)."},
	{MethodSerfLaunchSetLayer, LaunchConfigSetLayerParams{}, LaunchConfigResolved{}, ScopeHub, "Writes a launch-config layer; broadcasts serf/launch/updated."},
	{MethodSerfLaunchTrustRepo, LaunchConfigTrustRepoParams{}, LaunchConfigResolved{}, ScopeHub, "Trusts a repo's launch config by hash; broadcasts serf/launch/updated."},
	{MethodModelList, ModelListParams{}, ModelListResponse{}, ScopeBoth, "Lists available models with launch diagnostics."},
	{MethodSerfInstanceList, EmptyParams{}, InstanceListResponse{}, ScopeHub, "Lists configured provider instances."},
	{MethodSerfInstanceCreate, InstanceCreateParams{}, InstanceListResponse{}, ScopeHub, "Creates a provider instance; returns the updated list."},
	{MethodSerfInstanceEdit, InstanceEditParams{}, InstanceListResponse{}, ScopeHub, "Edits a provider instance; returns the updated list."},
	{MethodSerfInstanceRemove, InstanceRemoveParams{}, InstanceListResponse{}, ScopeHub, "Removes a provider instance; returns the updated list."},
	{MethodSerfInstanceSetDefault, InstanceSetDefaultParams{}, InstanceListResponse{}, ScopeHub, "Sets the default provider instance; returns the updated list."},
	{MethodSerfPluginCheckNow, EmptyParams{}, PluginCheckNowResponse{}, ScopeHub, "Runs one auto-upgrade daemon pass on demand; broadcasts serf/plugin/updated per plugin actually upgraded."},
	{MethodSerfMarketplaceList, EmptyParams{}, MarketplaceListResponse{}, ScopeHub, "Lists registered plugin marketplaces."},
	{MethodSerfMarketplaceAdd, MarketplaceAddParams{}, MarketplaceListResponse{}, ScopeHub, "Registers a plugin marketplace; returns the updated list."},
	{MethodSerfMarketplaceRemove, MarketplaceNameParams{}, MarketplaceListResponse{}, ScopeHub, "Unregisters a plugin marketplace; returns the updated list."},
	{MethodSerfMarketplaceRefresh, MarketplaceNameParams{}, MarketplaceListResponse{}, ScopeHub, "Pulls a marketplace's latest catalog; returns the updated list."},
	{MethodSerfMarketplaceBrowse, MarketplaceBrowseParams{}, MarketplaceBrowseResponse{}, ScopeHub, "Lists a marketplace's plugin catalog for browsing/install."},
	{MethodSerfPluginList, EmptyParams{}, PluginListResponse{}, ScopeHub, "Lists installed plugins."},
	{MethodSerfPluginInstall, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Installs a plugin from a marketplace; returns the updated list."},
	{MethodSerfPluginUpgrade, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Upgrades an installed plugin to its marketplace's latest; returns the updated list."},
	{MethodSerfPluginRemove, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Removes an installed plugin; returns the updated list."},
	{MethodSerfPluginEnable, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Enables an installed plugin; returns the updated list."},
	{MethodSerfPluginDisable, PluginRefParams{}, PluginListResponse{}, ScopeHub, "Disables an installed plugin; returns the updated list."},
	{MethodSerfPluginSetAutoUpgrade, PluginSetAutoUpgradeParams{}, PluginListResponse{}, ScopeHub, "Sets an installed plugin's auto-upgrade flag; returns the updated list."},
	{MethodSerfCommandList, EmptyParams{}, CommandListResponse{}, ScopeHub, "Lists loaded plugin slash commands (name, plugin, description) for catalog/autocomplete display."},
	{MethodSerfSettingsOverview, EmptyParams{}, SettingsOverviewResponse{}, ScopeHub, "Returns the settings overview field bag: hub/runtime, storage, agent roster, codex launch configs, and probed MCP servers — the six template-only settings sections' data."},
	{MethodSerfSandboxEscalationResolve, SandboxEscalationResolveParams{}, EmptyResponse{}, ScopeBoth, "Delivers a human's approve/deny decision for a pending sandbox-exemption escalation (M7); the daemon unblocks the waiting tool-exec goroutine, the hub relays."},
}

// ValidateMutationParams enforces the flag-day v2 identity and precondition
// fields before a mutation reaches either Hub or daemon handlers.
func ValidateMutationParams(method string, raw json.RawMessage) error {
	required := map[string][]string{
		MethodTurnStart:                {"clientMutationId"},
		MethodTurnSteer:                {"clientMutationId", "expectedTurnId"},
		MethodTurnInterrupt:            {"clientMutationId", "expectedTurnId"},
		MethodTurnQueue:                {"clientMutationId", "expectedTurnId"},
		MethodTurnDrainAsSteer:         {"clientMutationId", "expectedTurnId", "expectedQueueRevision"},
		MethodTurnPromoteQueuedAsSteer: {"clientMutationId", "expectedTurnId", "expectedEntryId"},
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
// 4j2t retired the last holdout, serf/attention/changed, by relocating its
// payload type from hubcore into this package). Notifications that carry no
// fields at all declare EmptyParams, so "no payload" and "payload
// undescribed" read differently. The constant NotifySerfContextPressure is
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
	{NotifyTurnStarted, TurnStartedParams{}, "A new turn began (inProgress)."},
	{NotifyTurnCompleted, TurnCompletedParams{}, "A turn reached a terminal state (completed/failed/interrupted)."},
	{NotifyItemStarted, ItemLifecycleParams{}, "A thread item began streaming."},
	{NotifyItemCompleted, ItemLifecycleParams{}, "A thread item finished."},
	{NotifyAgentMessageDelta, AgentMessageDeltaParams{}, "Incremental assistant-message text chunk for an item."},
	{NotifyAgentMessageReset, AgentMessageResetParams{}, "Discard the in-progress assistant item (a retry replaces it)."},
	{NotifyReasoningSummaryDelta, ReasoningSummaryDeltaParams{}, "Incremental reasoning-summary text chunk for a reasoning item."},
	{NotifyToolOutputDelta, ToolOutputDeltaParams{}, "Incremental tool-output chunk for a tool-call item."},
	{NotifyWarning, WarningParams{}, "Non-fatal diagnostic. Also used for cancelled turns and relay-attach failures."},
	{NotifySerfThreadModelRetry, ThreadModelRetryParams{}, "A model call failed with a retryable error and will be retried after a wait. Ephemeral liveness state, not a thread item."},
	{NotifySerfSteeringInjected, SerfSteeringInjectedParams{}, "A steering message was injected into the active turn."},
	{NotifySerfJobStarted, SerfJobParams{}, "A background job started."},
	{NotifySerfJobFinished, SerfJobParams{}, "A background job finished; the job carries status/reason/exitCode/output."},
	{NotifySerfAuthUpdated, SerfAuthUpdatedParams{}, "Broadcast after a successful auth mutation. Clients refresh auth state."},
	{NotifySerfLaunchUpdated, SerfLaunchUpdatedParams{}, "Broadcast after a launch layer/trust mutation. Clients refresh launch config."},
	{NotifySerfAttentionChanged, AttentionChangedPayload{}, "Hub-derived attention transitions for live sessions plus authoritative badge summary. Hub-originated; never sent by daemons."},
	{NotifySerfMarketplaceUpdated, EmptyParams{}, "Broadcast after a marketplace mutation (add/remove/refresh); no payload. Clients refresh the marketplace list."},
	{NotifySerfPluginUpdated, EmptyParams{}, "Broadcast after a plugin mutation (install/upgrade/remove/enable/disable/setAutoUpgrade); no payload. Clients refresh the plugin list."},
	{NotifySerfThreadResync, ThreadResyncParams{}, "Hub-originated hint asking clients to re-read one thread after relay recovery."},
	{NotifySerfTaskUpdated, TaskUpdatedParams{}, "The session's task-list progress (total/done) changed."},
	{NotifySerfSandboxEscalationRequested, SandboxEscalationRequested{}, "A harness-raised, human-gated sandbox-exemption approval card (M7); the tool-exec goroutine blocks until answered via serf/sandbox/escalation/resolve."},
	{NotifySerfSandboxEscalationResolved, SandboxEscalationResolved{}, "A previously-raised sandbox escalation left the pending set — resolved, turn-interrupted, or cleared by session close (M7); every OTHER subscribed client clears its now-stale copy of the card."},
	{NotifySerfTreeChanged, EmptyParams{}, "Broadcast after tree-relevant state changes (roster delta, past-index change, or an archive/favorite/rename/project-delete mutation); no payload. Clients refetch /api/tree (debounced). Hub-originated; never sent by daemons."},
}
