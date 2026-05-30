# Serf improvement specs after mux review

Date: 2026-05-29

This document replaces the earlier mux-comparison draft. The goal is no longer
"copy what mux has." The goal is to identify the changes that would make Serf a
better agent SDK and harness in Serf's existing architecture.

The conclusion from the mux review still holds: Serf is already the stronger
harness. Mux is useful mostly as a compact reference for SDK ergonomics:
small public agent APIs, approval-aware tool execution, child-agent scope
validation, async handles, and reusable runtime primitives.

This revision incorporates two adversarial review rounds. The important
corrections are:

- build source-organization and package-boundary guardrails before extracting
  new packages;
- split single-input turn execution from queue draining before adding SDK
  handles;
- do not expose `Session.ProcessInput` directly as the SDK run primitive;
- add in-process event fan-out without blocking session publishers and without
  replaying raw events into AppWire by accident;
- make tool policy affect both model-visible tools and final execution;
- treat plugin hooks as a separate trust boundary, because `PreToolUse` hooks
  can run commands or LLM calls before any post-hook approval;
- add provider-feature policy for native web search and similar non-tool
  capabilities;
- reuse the existing `llm.Middleware` abstraction instead of adding a second
  one;
- migrate retry/rate limiting carefully so caller-level retry and middleware
  retry cannot multiply attempts;
- make cancellable construction reach `NewSession` and restore internals, not
  just an SDK wrapper;
- scope caching, resource coordination, MCP diagnostics, and runtime injection
  narrowly, with explicit invalidation and process-boundary limits.

## Decisions

- Do not add a mux-inspired adaptive reasoning policy workstream. Serf already
  has explicit `ReasoningEffort`, task-level effort, provider capability
  metadata, and stuck-loop escalation. Reasoning changes stay out of this spec.
- Do not copy mux's event bus. Serf already has hub/AppWire for remote
  consumers. The in-process event work here is fan-out for embedding and live
  projection, not a competing bus.
- Do keep both an embeddable SDK facade and AppWire. They solve different
  integration problems.
- Do not make the SDK facade the first stable artifact. First build the
  internal lifecycle, event, policy, and runtime primitives that the SDK and
  AppWire can both use.
- Do treat retry, caching, rate limiting, and logical resource coordination as
  one runtime-services suite, but start with process-local defaults and one
  concrete integration per service.
- Do not create new subpackages until the dependency direction is proven. In Go,
  subdirectories are package boundaries; premature extraction will create import
  cycles or awkward interface shims.

## AppWire versus SDK

Yes, we still want an embeddable SDK even if AppWire exists.

AppWire is the right boundary for out-of-process clients:

- hub UI and external apps;
- language-neutral remote control;
- multi-client session observation;
- daemon-owned connection lifecycle;
- stable thread/turn projections rather than Go implementation details;
- permission request/response flows for remote approval clients.

The SDK facade is the right boundary for in-process Go users:

- embedding Serf inside another Go service or test harness;
- constructing custom tools, environments, providers, and approval callbacks
  without going through a daemon;
- deterministic unit and integration tests;
- direct lifecycle handles for turns, child agents, and background work;
- typed callbacks and errors instead of JSON/RPC projections.

The seam is not "SDK replaces AppWire" or "AppWire wraps SDK." The seam should
be an internal runtime/lifecycle layer above low-level `agent.Session` mutation
and below both the public SDK and server/AppWire projection:

```text
        SDK facade                  AppWire/server projection
             |                               |
             +------- runtime/lifecycle -----+
                         |
                    agent.Session
```

AppWire may keep protocol-specific concepts such as reserved turn IDs, active
turn IDs, capability announcements, cursor replay, and multi-client notification
delivery. The shared layer should own the Serf semantics underneath those wire
concepts: single-flight turns, queue/steer/interrupt behavior, event fan-out,
runtime services, and policy decisions.

The hub remains out-of-process. Runtime services are process-local unless a
specific durable backend is added later. A hub-spawned `serf serve` process gets
its own default runtime; cross-process rate limits or resource ownership need an
explicit hub or durable backend design.

## Spec 0: Source organization and package boundaries

### Goal

Improve source organization without turning the refactor into an import-cycle
fight.

### Current state

Serf concentrates too many concerns in a few files, especially
`agent/session.go` and the hub/TUI command files. Mux is better factored into
agent, orchestrator, tool, hooks, MCP, coordinator, permission, and LLM
packages, but Serf cannot copy that layout directly because Serf has more
provider, environment, AppWire, persistence, plugin, and hub responsibilities.

### Proposed behavior

Start with same-package file splits. This improves readability without changing
public APIs or import graphs:

- `agent/session_config.go`;
- `agent/session_state.go`;
- `agent/session_events.go`;
- `agent/session_queue.go`;
- `agent/session_lifecycle.go`;
- `agent/session_init.go`;
- `agent/session_tools.go`;
- `agent/session_prompts.go`.

Only promote a seam into a subpackage when imports point one way. Initial
package-placement rules:

- Runtime contracts that need to be injected into `agent.SessionConfig` must
  either live in `agent` or in a lower package that does not import `agent`,
  `server`, or `internal/appwire`.
- SDK packages may import `agent`; `agent` must not import SDK packages.
- Server/AppWire adapters may import `agent` and lower runtime contracts;
  lower runtime contracts must not import server/AppWire packages.
- Tool policy/catalog types should stay in `agent` until `ExecutionEnvironment`,
  tool descriptors, source/risk metadata, and provider feature policy are stable.
- AppWire approval request/response types should stay in AppWire/server layers,
  with only a small approval interface visible to lower layers.

### Implementation plan

1. Split large files inside their existing packages with no behavior changes.
2. Add compile/test checks after each mechanical split.
3. Extract only leaf packages first, such as runtime cache/resource helpers that
   do not import `agent`.
4. Extract architectural packages only after their interfaces have been used by
   at least two callers without import cycles.

## Spec 1: Lifecycle controller, single-input turns, and run handles

### Goal

Expose a stable in-process API for embedding Serf as an agent SDK while
preserving CLI, hub, and AppWire behavior.

### Current state

Serf has powerful session and subagent behavior, but the reusable API boundary
is not clean:

- `Session.ProcessInput` is a compatibility wrapper for a whole input drain. It
  can consume followups and queued inputs, then return one joined output.
- `serf serve` serializes inbound inputs and manages interrupt/drain behavior
  around `ProcessInput`.
- AppWire/server code owns reserved/active turn IDs and processing state.
- Subagents have async behavior, status, result storage, `done` channels, wait,
  resume, and close, but that lifecycle is local to the subagent tool.
- `agent.Session` construction has setup work such as environment
  initialization, live model metadata, plugin hooks, and MCP startup that needs
  cancellable context for embedders.
- `SessionSnapshot` is not the public persistence shape we want to freeze for
  SDK users.

### Proposed behavior

First split execution into two layers:

- a single-input primitive that processes exactly one user input or one
  controller-created followup and returns one result;
- a lifecycle controller that owns queue draining, followups, interrupts,
  steer, active run identity, and handle assignment.

`ProcessInput` should remain as a compatibility wrapper around the controller.
SDK `Run`/`Continue` must not call today's draining behavior directly, because a
queued SDK run needs its own handle, status, result, and cancellation semantics.

The controller owns the rules for one session's work:

- only one active turn mutates conversation state at a time;
- concurrent `Run`/`Continue` calls have explicit behavior: queue, reject, or
  steer according to options;
- each controller-owned queue item maps to a distinct handle;
- wait cancellation is separate from run cancellation;
- child agent work can outlive the parent wait context where existing subagent
  behavior requires it;
- interrupts, queued messages, followups, restored continuation, and drain
  behavior are represented in one place;
- server/AppWire can keep protocol turn IDs while delegating Serf lifecycle
  semantics to the controller.

```go
type TurnMode string
const (
    TurnQueue  TurnMode = "queue"
    TurnReject TurnMode = "reject"
    TurnSteer  TurnMode = "steer"
)

type RunStatus string
const (
    RunPending   RunStatus = "pending"
    RunRunning   RunStatus = "running"
    RunCompleted RunStatus = "completed"
    RunFailed    RunStatus = "failed"
    RunCancelled RunStatus = "cancelled"
)

type RunResult[T any] struct {
    Value      T
    Err        error
    Status     RunStatus
    StartedAt  time.Time
    FinishedAt time.Time
}

type RunHandle[T any] struct { /* done, cancel, result, status */ }

func (h *RunHandle[T]) Done() <-chan struct{}
func (h *RunHandle[T]) Wait(ctx context.Context) (RunResult[T], error)
func (h *RunHandle[T]) Poll() RunResult[T]
func (h *RunHandle[T]) Cancel() bool
```

The handle's run lifecycle context and the caller's wait context must be
separate. `Wait(ctx)` timing out must not automatically cancel detached
subagent execution. Explicit cancellation or `close_agent` should cancel the
owned run where cancellation is supported.

Add context-aware construction in core agent APIs before SDK construction:

```go
func NewSessionContext(ctx context.Context, client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, cfg SessionConfig) (*Session, error)
func RestoreSessionContext(ctx context.Context, client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, snap SessionSnapshot, stateDir string) (*Session, error)
func RestoreSessionFromMetaContext(ctx context.Context, client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, meta SessionMeta, stateDir string) (*Session, error)
```

The existing constructors can call these with `context.Background()` for
compatibility. The context must flow through environment initialization, live
model metadata, `initSessionState`, plugin `SessionStart` hooks, MCP discovery
and connect, and any future startup cache/provider metadata calls.

The SDK facade should be deliberately small and initially marked experimental:

```go
type Runtime struct {
    LLM         *llm.Client
    Cache       Cache
    RateLimiter RateLimiter
    Resources   ResourceCoordinator
    Events      EventBroker
}

type SDKTool struct {
    Name        string
    Description string
    Schema      map[string]any
    Source      ToolSourceKind
    Risks       []ToolRisk
    OutputLimit agent.ToolOutputLimit
    Execute     func(context.Context, agent.ExecutionEnvironment, map[string]any) (any, error)
}

type AgentConfig struct {
    Profile       agent.ProviderProfile
    Environment   agent.ExecutionEnvironment
    Tools         []SDKTool
    ToolPolicy    ToolPolicy
    Hooks         Hooks
    Runtime       *Runtime
    SessionConfig agent.SessionConfig
}

func NewRuntime(opts RuntimeOptions) (*Runtime, error)
func NewAgent(ctx context.Context, cfg AgentConfig) (*Agent, error)
func (a *Agent) Run(ctx context.Context, input Input, opts RunOptions) (*RunHandle[Result], error)
func (a *Agent) Continue(ctx context.Context, input Input, opts RunOptions) (*RunHandle[Result], error)
func (a *Agent) Events(ctx context.Context, opts EventOptions) EventStream
func (a *Agent) Snapshot(ctx context.Context) AgentSnapshot
```

`SDKTool` adapts into Serf `RegisteredTool` plus source/risk metadata. It must
not be a bare `llm.Tool`, because Serf needs environment-aware execution, output
limits, schema validation, source metadata, and policy metadata.

Runtime and hook fields injected through session config must be non-persisted
(`json:"-"`) and rebuilt on restore.

`AgentSnapshot` should not expose the deprecated full `SessionSnapshot` shape as
the primary public DTO. It should prefer meta, transcript location, usage,
model, state, pending queue, and runtime-independent state, with full history as
an opt-in debugging export.

### Implementation plan

1. Add context-aware `NewSessionContext` and restore variants.
2. Add race-safe `RunHandle` and tests.
3. Split single-input processing from queue/followup draining.
4. Add an internal lifecycle controller around the single-input primitive.
5. Port `serf serve` interrupt/drain/processing behavior onto the controller.
6. Refactor subagent bookkeeping to use the handle without changing existing
   tool JSON shapes or detached-child semantics.
7. Add cancellable SDK construction and an experimental `Agent` facade.
8. Add an SDK snapshot DTO that does not freeze deprecated persistence shapes.
9. Add fake-model SDK tests with custom tools and concurrent calls.

### Tests

- Constructor cancellation reaches environment initialization, model metadata,
  plugin startup hooks, and MCP startup.
- Handle status transitions and `Done` close exactly once.
- `Wait(ctx)` timeout does not cancel detached work.
- Explicit cancellation closes the correct run.
- Concurrent `Run` calls queue/reject/steer according to options.
- Queued SDK calls receive distinct handles and results.
- `ProcessInput` remains compatible as a drain wrapper.
- Server/AppWire processing behavior remains unchanged.
- Subagent wait/resume/close behavior remains unchanged.
- SDK facade can run a fake-model tool loop without hub/AppWire.

## Spec 2: Event fan-out and state/lifecycle observability

### Goal

Improve internal lifecycle observability without replacing hub/AppWire.

### Current state

Serf has typed session events and a best-effort session event channel. That
channel is a single-consumer channel, and `emit` may drop events when the buffer
is full. AppWire/server code already consumes the event stream and separately
tracks processing status, notification replay, and turn state.

Directly exposing `Session.Events()` as SDK `Events()` would let SDK consumers
steal events from AppWire or each other.

### Proposed behavior

Add an in-process event broker:

- one publisher API for session/lifecycle/runtime events;
- multiple subscribers with independent buffers;
- no blocking on the session publisher path;
- bounded subscriber queues with drop-newest, drop-oldest, or close-with-error
  overflow policies;
- optional bounded replay for SDK subscribers;
- event metadata identifying session, sequence, and known IDs;
- compatibility adapter for the existing `Session.Events()` API.

Do not allow a slow SDK subscriber to block while session locks or
`responseSideEffectsMu` are held. If a future subscriber needs blocking
delivery, put that behind a fan-out goroutine that is outside session locks and
has its own backpressure policy.

Replay is not automatically safe for AppWire. AppWire already has notification
replay, and its projector is stateful. AppWire/server bridge should initially
use a live, no-replay subscription. Raw event replay should remain an SDK/debug
feature unless the AppWire projector becomes cursor-aware and idempotent.

Phase 1 metadata should not pretend to know lifecycle IDs that do not exist yet.
Before the lifecycle controller exists, event metadata can include session ID
and broker sequence. Turn/run IDs should be added only after the controller owns
those identities.

Add a validated internal state transition helper for agent-internal state, but
do not pretend it is the only status authority. AppWire/server processing state
and wire turn reservations are separate lifecycle concerns and should either be
fed by the lifecycle controller or explicitly documented as protocol state.

Add `STATE_CHANGED` only for `SessionState` transitions. Do not overload it to
mean AppWire reserved/active turn state or server processing state.

### Implementation plan

1. Add `EventBroker` as fan-out only, with no replay for AppWire/server.
2. Route session event emission through the broker while preserving existing
   `Events()` behavior.
3. Teach AppWire/server bridge to use a live subscription.
4. Add optional SDK/debug replay after live fan-out is stable.
5. Add state transition helper and transition table tests.
6. Replace direct `s.state =` assignments in session code.
7. Add `STATE_CHANGED` events from the helper.
8. Add a grep/static test preventing new direct state writes outside restore,
   close, and the helper.

### Tests

- Multiple subscribers see the same ordered live events.
- Slow SDK subscriber does not starve AppWire or block session close.
- Overflow behavior is deterministic and observable.
- AppWire live subscription does not replay or duplicate turns.
- Optional replay subscribers receive documented replay behavior.
- Normal turn, tool-use, await-reply, interrupt, error, and close transitions.
- Existing AppWire status projections remain compatible.

## Spec 3: Tool policy, provider-feature policy, approvals, and child scope

### Goal

Make tool and provider-feature access a first-class policy rather than a
mixture of registry mutation, plugin hooks, subagent-specific rules, MCP
defaults, and provider-specific request flags.

### Current state

Serf already has:

- `AllowedToolNames` and `DeniedToolNames`;
- registry restriction/removal that affects tool visibility;
- tool middleware;
- plugin `PreToolUse` denial and argument rewrite;
- root-only subagent management tool removal;
- `grant_tools`;
- permission-denied error types;
- provider-native features such as web search that are enabled during request
  construction rather than through the tool registry.

The gap is that these are not one reusable policy model.

### Proposed behavior

Policy has three surfaces:

1. Visibility and availability policy filters the effective tool catalog before
   tools are shown to the model.
2. Provider-feature policy gates request-construction features that are not
   registry tools, such as provider-native web search.
3. Final execution policy runs after schema validation and any trusted argument
   rewrite, before the actual tool execution.

Plugin hooks are a separate trust boundary. Current `PreToolUse` hooks can run
commands and prompt hooks can make LLM calls, so a post-hook approval does not
gate all side effects. The policy design must choose one of these modes:

- trusted-plugin mode: plugin hooks are trusted extension code outside tool
  approval; final approval runs after `PreToolUse` so rewritten args are
  checked before the tool executes;
- strict-approval mode: run an early approval/trust check before effectful
  hooks, allow only pure rewrite/deny hooks before final approval, and then run
  final approval on validated rewritten args.

The initial implementation should use trusted-plugin mode for compatibility and
document it explicitly. Strict-approval mode requires a hook API split between
pure transformers and effectful hooks.

Every final tool or provider-feature request receives one decision:

- `allow`;
- `ask`;
- `deny`.

Policy names are canonical Serf names. Legacy names, Claude-compatible names,
provider-visible names, and MCP names are normalized before policy evaluation.
Tests must cover aliases such as `exec_command` versus `shell`.

Policy evaluates canonical name, source metadata, dynamic risk metadata, raw
arguments, validated parsed arguments, explicit allow/deny lists, approval
requirements, parent-session scope, and non-interactive behavior. Risk cannot be
only static tool metadata; some calls are safe or dangerous depending on args.

```go
type PermissionMode string
const (
    PermissionModeAuto PermissionMode = "auto"
    PermissionModeAsk  PermissionMode = "ask"
    PermissionModeDeny PermissionMode = "deny"
)

type PolicyDecision string
const (
    PolicyDecisionAllow PolicyDecision = "allow"
    PolicyDecisionAsk   PolicyDecision = "ask"
    PolicyDecisionDeny  PolicyDecision = "deny"
)

type ToolSourceKind string
const (
    ToolSourceCore     ToolSourceKind = "core"
    ToolSourceMCP      ToolSourceKind = "mcp"
    ToolSourcePlugin   ToolSourceKind = "plugin"
    ToolSourceCustom   ToolSourceKind = "custom"
    ToolSourceProvider ToolSourceKind = "provider"
)

type ToolRisk string
const (
    ToolRiskSafe     ToolRisk = "safe"
    ToolRiskWrite    ToolRisk = "write"
    ToolRiskShell    ToolRisk = "shell"
    ToolRiskNetwork  ToolRisk = "network"
    ToolRiskExternal ToolRisk = "external"
)

type ToolPolicy struct {
    Mode               PermissionMode `json:"mode,omitempty"`
    AllowedTools       []string       `json:"allowed_tools,omitempty"`
    DeniedTools        []string       `json:"denied_tools,omitempty"`
    RequireApproval    []string       `json:"require_approval,omitempty"`
    MCPDefaultDecision PolicyDecision `json:"mcp_default_decision,omitempty"`
}

type ProviderFeaturePolicy struct {
    WebSearch PolicyDecision `json:"web_search,omitempty"`
}

type PolicyRequest struct {
    CanonicalName  string
    ProviderName   string
    Source         ToolSourceKind
    Risks          []ToolRisk
    RawArgs        json.RawMessage
    ParsedArgs     map[string]any
    ParentScope    *EffectiveToolSet
    NonInteractive bool
}

type ApprovalFunc func(ctx context.Context, req PolicyRequest) (approved bool, reason string, err error)
```

For provider-native web search, policy must run before building `llm.Request`.
If web search is denied, `req.WebSearch` must be false and an audit event should
explain why. If web search requires `ask` and no approval path exists,
non-interactive contexts must deny by default unless an explicit compatibility
flag allows existing behavior.

`ask` approval has two deployment modes:

- SDK mode: invoke the in-process `ApprovalFunc`.
- AppWire mode: emit a wire-level approval request and await a response.

Until AppWire approval request/response is implemented, `ask` should be SDK-only
or degrade to deny in non-interactive AppWire/hub contexts. The AppWire contract
must define request IDs, timeout, cancellation, multi-client arbitration,
default non-interactive behavior, and audit events before remote approvals are
enabled.

Child agents should be limited by an `EffectiveToolSet` snapshot from the
parent. A child can never add a tool the parent cannot call. Parent deny and ask
requirements remain in force. SDK-registered custom tools must have explicit
propagation rules, because fresh subagent sessions do not automatically share a
parent session's live registry.

```go
type EffectiveToolSet struct {
    Tools   []ToolDescriptor
    Policy  ToolPolicy
    Sources map[string]ToolSourceKind
}
```

### Implementation plan

1. Add canonical tool-name normalization and tests.
2. Add tool descriptors with source metadata and dynamic risk classifiers.
3. Add pure policy evaluator tests.
4. Normalize legacy allow/deny config into `ToolPolicy`.
5. Apply visibility policy before sending tool definitions to the model.
6. Add provider-feature policy for native web search at request construction.
7. In trusted-plugin mode, run final execution policy after schema validation
   and plugin `PreToolUse`, using both raw and validated parsed args.
8. Insert final policy at the registry validation/execution boundary so policy
   evaluates the same args the executor uses.
9. Emit policy decision events with canonical name, source, risk, decision, and
   reason.
10. Add SDK approval callback.
11. Define AppWire approval protocol before enabling remote `ask`.
12. Refactor subagent scope resolution to use `EffectiveToolSet`.
13. Keep default root behavior permissive for compatibility.
14. Make MCP approval opt-in first; do not silently break existing MCP users.

### Tests

- Canonical name normalization.
- Visibility filtering prevents denied tools from being advertised.
- Final approval checks rewritten `PreToolUse` args.
- Policy request includes raw and validated parsed args.
- Trusted-plugin mode documents that `PreToolUse` can have side effects.
- Provider-native web search is disabled when policy denies it.
- Provider-native web search `ask` behavior is defined when no approver exists.
- Auto/ask/deny behavior.
- Deny precedence.
- Approval allowed, denied, timeout, and missing callback behavior.
- Policy denial prevents execution.
- Policy allow plus hook denial still works.
- Parent allow/deny/ask inheritance for subagents.
- SDK custom-tool propagation to child agents.
- `tools: all` means parent-effective all, not process-global all.

## Spec 4: Runtime services suite

### Goal

Create shared runtime services that can be used by sessions, subagents, MCP,
SDK embedders, and hub-spawned server processes:

- retry;
- cache;
- rate limiter;
- resource coordinator;
- event sink.

These are related because they control work around the model/tool loop: when to
try again, when to reuse known data, when to wait, and who owns a logical
operation.

Runtime defaults are process-local. Cross-process sharing requires an explicit
hub or durable backend design.

```go
type Runtime struct {
    LLM         *llm.Client
    Cache       Cache
    RateLimiter RateLimiter
    Resources   ResourceCoordinator
    Events      EventBroker
}
```

### Retry and rate limiting

Serf already has:

- `llm.Retry`;
- `RetryPolicy`;
- error classification;
- `LLMRetryPolicy`;
- session model-call retry wiring;
- `llm.Client` middleware for `Complete` and `Stream`.

The improvement is concrete middleware and migration, not a new middleware
interface.

Retry and rate limiting should use the existing `llm.Middleware` where possible.
However, migration must avoid nested retry. `Session.callModel` and
`llm.Generate` already apply caller-level retry. Installing retry middleware
before removing or disabling caller-level retry can multiply attempts and change
billing, latency, and fallback behavior.

Proposed work:

- implement concrete retry, rate-limit, and observability middleware using
  existing `llm.Middleware`;
- add attempt-count tests before changing retry placement;
- migrate one caller path at a time;
- disable caller-level retry when retry middleware is installed, or postpone
  retry middleware until `Session.callModel` and `llm.Generate` are refactored;
- preserve current retry semantics: retry stream creation failures only, not
  mid-stream partial output failures;
- compose retry observability with any caller-provided `RetryPolicy.OnRetry`
  instead of clobbering it;
- acquire rate-limit tokens for each retry attempt, not just the first attempt;
- feed `Retry-After` and provider rate-limit headers into bucket state where
  available;
- keep base `llm.Client` non-retrying unless runtime/profile/session installs
  middleware.

Named rate-limit buckets should cover:

- provider/model LLM calls;
- provider metadata/model listing calls;
- MCP server calls;
- web/network tools;
- SDK custom tools that opt in.

`ListModels` is not currently covered by `Complete`/`Stream` middleware. Either
extend the client abstraction with model-list middleware, or add a provider
metadata service with explicit retry/cache/rate limiting. Do not assume
`llm.Middleware` covers model discovery until this is designed.

```go
type RateLimiter interface {
    Take(ctx context.Context, bucket string, tokens float64) error
    TryTake(bucket string, tokens float64) bool
    Snapshot() []RateLimitBucket
}
```

### Cache

Serf already has specialized caches: system prompt/tool definition caches,
project docs loaded once per session, web fetch disk cache, model catalogs, and
context token measurements.

The improvement is a reusable TTL cache for expensive, deterministic metadata.
Start with safe, under-keyed-risk-low targets:

- schema conversion results by input hash;
- provider/model metadata by provider, endpoint, auth scope, and model source;
- plugin/skill discovery metadata by resolved filesystem/config/env hash.

MCP discovery is a later cache target, not the first one. MCP servers may be
dynamic, may send tool-list change notifications, and registered MCP tools
close over live client sessions. If MCP discovery is cached, cache only
serializable tool metadata keyed by resolved config, transport, env, server
identity, and collision context. Always bind fresh execution closures to a live
session. Support TTL, explicit invalidation, and notification-driven
invalidation where the upstream SDK exposes it.

Do not cache arbitrary tool outputs by default. That changes semantics and can
leak sensitive data. Tool-output caching requires explicit tool metadata and a
caller-provided cache-key policy.

```go
type Cache interface {
    Get(ctx context.Context, key string) (any, bool)
    Set(ctx context.Context, key string, value any, ttl time.Duration)
    Delete(ctx context.Context, key string)
    Clear(ctx context.Context)
}
```

Start with an in-memory TTL cache. Add disk-backed caches only for cases that
already need persistence.

### Resource coordinator

A resource coordinator is not a distributed lock manager. The initial version is
a process-local logical ownership table for operations where Serf wants to say
"owner X owns resource Y right now."

Do not claim workspace, port, or artifact safety without durable cross-process
locking. Start with one concrete subsystem that already has real contention:

- hub relay dedupe for one thread in one server process; or
- subagent ownership diagnostics inside one session/runtime.

Candidate resource IDs after the first integration proves useful:

- `thread:<id>`;
- `subagent:<id>`;
- `mcp-server:<name>`;
- `artifact:<path>`.

Use owner IDs that can actually conflict. `session:<unique-id>` as a resource
does not add value unless there is a shared operation keyed by that session.

```go
type ResourceCoordinator interface {
    TryAcquire(ownerID, resourceID string) (Lease, error)
    Acquire(ctx context.Context, ownerID, resourceID string) (Lease, error)
    Release(ownerID, resourceID string) error
    ReleaseAll(ownerID string)
    Snapshot() []ResourceLock
}
```

### SessionConfig and persistence

Runtime injection through `SessionConfig` must be non-persisted and rebuilt on
restore. Do not serialize live clients, caches, rate limiters, hooks, or
resource coordinators into transcripts or snapshots.

### Implementation plan

1. Define package placement for runtime contracts to avoid import cycles.
2. Add attempt-count tests for current retry paths.
3. Implement concrete observability/rate-limit middleware using existing
   `llm.Middleware`.
4. Decide whether retry moves to middleware or remains caller-level with shared
   observability.
5. Add model-list/provider-metadata retry/cache/rate-limit design.
6. Add rate limiter with no-op default config.
7. Add TTL cache and integrate one deterministic metadata path first.
8. Add resource coordinator and use it in one low-risk subsystem.
9. Expose runtime injection through the experimental SDK facade and
   non-persisted session config fields.

### Tests

- Existing retry attempt counts are characterized before migration.
- Retry event emission and no mid-stream retry.
- Existing `OnRetry` callback composition.
- Middleware retry does not nest with caller-level retry.
- Rate limiter tokens are acquired per retry attempt.
- `Retry-After` updates bucket behavior where supported.
- Side LLM calls pass through the intended middleware or metadata service.
- Model listing is covered by explicit retry/cache/rate limiting.
- Cache hit/miss/expiry/concurrency.
- MCP metadata cache never reuses live call closures.
- Resource coordinator acquire/release/conflict/release-all/concurrency.
- Runtime fields are not persisted in snapshots.

## Spec 5: MCP policy and diagnostics

### Goal

Make MCP tools fit the same policy, runtime, and diagnostic model as native
Serf tools.

### Current state

Serf supports stdio, SSE, and Streamable HTTP MCP through the upstream MCP Go
SDK. Remote tools are namespaced and registered into the tool registry. Current
MCP manager state is mostly name/session/tools, and failed connect or tool-list
errors abort manager creation.

The gap is not transport support. The gap is source metadata, policy
integration, runtime service integration, and diagnostics.

### Proposed behavior

- Register MCP tools with `Source: mcp` and dynamic risk defaulting to
  `external`.
- Let `ToolPolicy.MCPDefaultDecision` control whether MCP tools default to
  allow or ask.
- Use runtime rate limiter per MCP server.
- Add per-server diagnostics in partial-start mode:
  - name;
  - sanitized transport type/config shape;
  - connected/disconnected/failed;
  - discovered tool names;
  - last error;
  - notification count and last notification if exposed by the SDK;
  - session expiry if observable.
- Decide explicitly whether MCP startup remains fail-fast or becomes partial.

There are two valid diagnostic modes:

- fail-fast mode: session creation fails if any configured MCP server fails;
  there is no session/AppWire diagnostics surface, so the deliverable is an
  enriched construction error with sanitized per-server context;
- partial mode: session creation can continue with failed MCP servers recorded
  as disconnected/failed diagnostics.

The spec should not promise disconnected-server diagnostics until partial mode
exists.

MCP discovery caching must follow the cache rules in Spec 4: metadata only,
fresh live execution bindings, TTL, config/env fingerprinting, and invalidation
on tool-list changes where possible.

### Tests

- Existing MCP config parsing remains covered.
- MCP tool registration includes source/risk metadata.
- MCP default ask integrates with tool policy.
- Fail-fast construction errors include sanitized server context.
- Partial-start diagnostics behavior is covered if implemented.
- Fake HTTP/SSE tests cover session expiry and notifications where the upstream
  SDK exposes enough behavior.
- Diagnostics never expose configured headers or secrets.
- Cached MCP metadata never calls through a stale client session.

## Spec 6: SDK hooks and presets

### Goal

Expose a small in-process hook and preset API for SDK embedders without
replacing plugin hooks.

### Proposed behavior

Plugin hooks remain the compatibility and extension story for Claude-style
plugins. SDK hooks are typed Go callbacks for embedders. Hook ordering must be
explicit:

- lifecycle hooks observe controller/session lifecycle events;
- tool visibility policy runs before model tool definition exposure;
- provider-feature policy runs before request construction;
- plugin `PreToolUse` runs under the selected plugin trust mode;
- final policy and SDK `OnToolPolicy` observe the final validated args and
  decision before tool execution;
- blocking hooks must honor context cancellation and have documented timeout
  behavior.

```go
type Hooks struct {
    OnSessionStart  func(context.Context, SessionStart) error
    OnSessionEnd    func(context.Context, SessionEnd) error
    OnPolicy        func(context.Context, PolicyDecisionEvent) error
    OnToolStart     func(context.Context, ToolCall) error
    OnToolEnd       func(context.Context, ToolResult) error
    OnSubagentStart func(context.Context, SubagentStart) error
    OnSubagentEnd   func(context.Context, SubagentEnd) error
}
```

Add preset helpers for common SDK agent shapes:

- explorer/read-only;
- implementer/write-capable;
- reviewer/read-only;
- researcher/web-enabled if available.

These should be normal Go constructors over `AgentConfig`. Built-in and plugin
markdown agents continue to work as they do today.

### Tests

- Hook ordering around plugin `PreToolUse` and final policy.
- Hook cancellation/timeout behavior.
- Provider-feature policy hooks fire before request construction.
- Presets produce expected effective tool catalogs.
- Presets do not bypass parent-effective child-agent policy.

## Recommended order

1. Same-package mechanical splits for the largest files.
2. Context-aware `NewSessionContext` and restore constructors.
3. `RunHandle` primitive and single-input turn primitive.
4. Internal lifecycle controller around the single-input primitive.
5. Event broker live fan-out, with AppWire live no-replay subscription.
6. Port server/AppWire and subagent lifecycle onto the controller.
7. Tool catalog descriptors, canonical names, visibility policy, provider
   feature policy, and final execution policy.
8. SDK-only approval callback, then AppWire approval protocol before remote
   `ask`.
9. Existing `llm.Middleware`-based observability/rate-limit middleware plus
   retry attempt-count characterization.
10. Retry placement migration, if attempt-count tests show middleware is safe.
11. Provider metadata/model-list retry/cache/rate-limit design.
12. TTL cache with one deterministic metadata integration.
13. Resource coordinator with one concrete process-local integration.
14. MCP metadata, policy, fail-fast construction errors, partial diagnostics,
    and safe discovery caching.
15. Experimental SDK facade and hooks/presets.
16. Stabilize SDK DTOs only after controller, event, policy, and runtime
    contracts have settled.

The first eight items create the real seam. The SDK facade should expose that
seam after it exists rather than freezing today's session internals.

## Non-goals

- No adaptive reasoning policy in this effort.
- No distributed lock manager.
- No replacement for hub/AppWire.
- No wholesale mux event bus.
- No blocking event delivery from session publisher paths.
- No replay of raw session events into AppWire without idempotent projector
  design.
- No default behavior change that makes existing MCP/tool calls require
  approval without an explicit migration flag.
- No public SDK guarantee around deprecated `SessionSnapshot` shape.
- No arbitrary default tool-output caching.
- No second `llm.Client` middleware abstraction.
