package appwire

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
	{MethodThreadFork, ThreadForkParams{}, ThreadForkResponse{}, ScopeHub, "Forks a thread from a source turn, optionally with edited input."},
	{MethodThreadClear, ThreadClearParams{}, ThreadClearResponse{}, ScopeBoth, "Clears the thread's conversation (rejected while a turn is processing)."},
	{MethodThreadModelSet, ThreadModelSetParams{}, EmptyResponse{}, ScopeBoth, "Changes the session's model/provider."},
	{MethodThreadReasoningEffortSet, ThreadReasoningEffortSetParams{}, EmptyResponse{}, ScopeBoth, "Sets reasoning effort, normalizing and validating the value."},
	{MethodThreadCompactStart, ThreadCompactStartParams{}, EmptyResponse{}, ScopeBoth, "Starts a context-compaction pass on the session."},
	{MethodThreadShutdown, ThreadShutdownParams{}, EmptyResponse{}, ScopeBoth, "Shuts the session down (the daemon runs it asynchronously)."},
	{MethodTurnStart, TurnStartParams{}, TurnStartResponse{}, ScopeBoth, "Starts a new user turn and reserves a turn ID."},
	{MethodTurnSteer, TurnSteerParams{}, EmptyResponse{}, ScopeBoth, "Injects a steering message into the active turn."},
	{MethodTurnInterrupt, TurnInterruptParams{}, EmptyResponse{}, ScopeBoth, "Cancels the active turn matching expectedTurnId."},
	{MethodTurnQueue, TurnQueueParams{}, EmptyResponse{}, ScopeBoth, "Queues a user message for after the active turn completes."},
	{MethodTurnDrainAsSteer, TurnDrainAsSteerParams{}, EmptyResponse{}, ScopeBoth, "Drains the input queue and injects it as a single steering message."},
	{MethodGoalSet, GoalSetParams{}, GoalSetResponse{}, ScopeBoth, "Sets or clears the session's /goal objective."},
	{MethodSerfTasksList, TaskListParams{}, TaskListResponse{}, ScopeBoth, "Lists the session's tasks."},
	{MethodSerfThreadTranscriptsList, ThreadTranscriptListParams{}, ThreadTranscriptListResponse{}, ScopeHub, "Lists transcript targets (subagents/related threads) for a ref."},
	{MethodSerfSubagentPreview, SerfSubagentPreviewParams{}, SerfSubagentPreviewResponse{}, ScopeHub, "Reads a bounded lazy preview of a subagent transcript's latest direct items."},
	{MethodSerfDirsComplete, DirsCompleteParams{}, DirsCompleteResponse{}, ScopeHub, "Directory-path autocompletion for a prefix."},
	{MethodSerfPathValidate, PathValidateParams{}, PathValidateResponse{}, ScopeHub, "Validates a launch path."},
	{MethodSerfHarnessesList, HarnessListParams{}, HarnessListResponse{}, ScopeHub, "Lists available harness descriptors."},
	{MethodSerfUpgrade, UpgradeParams{}, UpgradeResponse{}, ScopeHub, "Performs or reports a serf binary upgrade."},
	{MethodSerfAuthStatus, AuthStatusParams{}, AuthStatusResponse{}, ScopeHub, "Reports auth/credential status for a provider."},
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
}

// Notifications is the AppWire server→client notification catalog. A nil
// Payload marks a notification emitted as an inline object at the projector
// (no dedicated Go type); its shape is given in the summary. The constants
// NotifySerfContextPressure and NotifySerfTaskUpdated are intentionally absent
// — they are defined but emitted by nothing (context pressure rides on the
// Thread snapshot instead).
var Notifications = []NotificationSpec{
	{NotifyThreadStarted, nil, "Session started; inline {threadId, ref, thread} carrying the initial Thread snapshot."},
	{NotifyThreadClosed, nil, "Session ended; inline {threadId, ref, reason}."},
	{NotifyThreadStatusChanged, ThreadStatusChangedParams{}, "Thread status (type + active flags) changed."},
	{NotifyThreadQueueChanged, ThreadQueueChangedParams{}, "The per-session input queue depth/preview changed."},
	{NotifyTurnStarted, nil, "A new turn began (inProgress); inline {threadId, ref, turn}."},
	{NotifyTurnCompleted, TurnCompletedParams{}, "A turn reached a terminal state (completed/failed/interrupted)."},
	{NotifyItemStarted, nil, "A thread item began streaming; inline {threadId, ref, turnId, item}."},
	{NotifyItemCompleted, nil, "A thread item finished; inline {threadId, ref, turnId, item}."},
	{NotifyAgentMessageDelta, AgentMessageDeltaParams{}, "Incremental assistant-message text chunk for an item."},
	{NotifyAgentMessageReset, AgentMessageResetParams{}, "Discard the in-progress assistant item (a retry replaces it)."},
	{NotifyReasoningSummaryDelta, ReasoningSummaryDeltaParams{}, "Incremental reasoning-summary text chunk for a reasoning item."},
	{NotifyToolOutputDelta, ToolOutputDeltaParams{}, "Incremental tool-output chunk for a tool-call item."},
	{NotifyWarning, nil, "Non-fatal diagnostic; inline {threadId, ref, message, source, title, hint, warning, cause?}. Also used for cancelled turns and relay-attach failures."},
	{NotifySerfSteeringInjected, nil, "A steering message was injected into the active turn; inline {threadId, ref, text, images}."},
	{NotifySerfJobStarted, nil, "A background job started; inline {threadId, ref, job}."},
	{NotifySerfJobFinished, nil, "A background job finished; inline {threadId, ref, job} with status/reason/exitCode/output."},
	{NotifySerfAuthUpdated, nil, "Broadcast after a successful auth mutation; inline {provider, activeSource}. Clients refresh auth state."},
	{NotifySerfLaunchUpdated, nil, "Broadcast after a launch layer/trust mutation; inline {cwd, layer}. Clients refresh launch config."},
}
