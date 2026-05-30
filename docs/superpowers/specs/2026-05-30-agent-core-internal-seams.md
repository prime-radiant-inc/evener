# Agent-core internal seams — cutting the four `Session` back-cycles

Date: 2026-05-30 · Ticket: PRI-1947 · Follows: PRI-1938 (session.go split), PRI-1940 (leaf-harvest), the modularization survey + agent dependency map (`2026-05-30-agent-package-*`).

## Problem

A `go/types` symbol-reference analysis of the `agent` package found that **13 of 14 concern-clusters form a single strongly-connected component** with the central `Session` type. The knot is held shut by **four direct back-cycles into `Session`**: `tools⇄session`, `context⇄session`, `subagents⇄session`, `sessiondata⇄session`. No package boundary along any concern line is cycle-free until those four edges are cut.

This spec cuts all four with the **minimum mechanism per edge** so the core becomes reasonable to read and test. It is not a package split — see the packaging verdict.

## Scope & non-goals (set by Jesse)

- **Internal seams only.** No public SDK facade — no `Agent`/`Runtime`/`RunHandle` public types.
- **No new approvals/policy system.** The mux Spec 3 collapses to just the tool-handler decoupling — no `PolicyDecision`/`ask`/`ApprovalFunc`/provider-feature policy/source+risk descriptors.
- **Behavior-preserving.** The existing `agent` test suite is the regression harness; every cut is gated on it.
- **KISS / YAGNI / DRY.** The smallest change that genuinely cuts the cycle wins. An interface is introduced only where the coupling is *parameterization*; where it is *capture* or *ownership*, a concrete type is used.

## Design provenance

Each open question was answered independently by **three Opus subagents** (a 9-opinion consensus panel, PRI-1947). The asymmetry below (one interface, two concrete structs) is the panel's explicit, defended conclusion: forcing interface-symmetry across all four cuts would add abstraction no caller varies — a YAGNI violation.

## The four cuts

### 1. context ⇄ session — a `StrategyHost` interface

`ContextManager` already has **zero** `*Session` references and the `ContextStrategy` interface already takes `*[]Turn` + callbacks (not `*Session`). The cycle lives only in the strategy *implementations*, which capture `*Session` in their constructors but reach into just **five** session members. Define, in package `agent`:

```go
type StrategyHost interface {
    Emit(kind EventKind, data any)
    WithResponseSideEffects(ctx context.Context, fn func()) error
    StateDir() string
    ID() string
    Profile() ProviderProfile
}

var _ StrategyHost = (*Session)(nil)
```

`*Session` satisfies it via one-line exported forwarders (`Emit→emit`, `WithResponseSideEffects→withResponseSideEffects`, `StateDir→s.stateDir`, `ID→s.id`, `Profile→s.profile`) — no behavior change. Every strategy and constructor stores/accepts `StrategyHost` instead of `*Session`. `buildRecallTool` keeps its call-time, nil-safe getter (`func() StrategyHost`). An interface is correct here because a strategy is a swappable contract.

### 2. tools ⇄ session — a `toolDeps` struct (not an interface)

The edge is a **lexical capture**: the `registerXxxTools` closures close over `s *Session`. `ToolRegistry`/`RegisteredTool`/`ExecuteCall` already take `ExecutionEnvironment`, not `*Session`, and the vision side-channel (`describeImage`) runs in the lifecycle loop *after* `ExecuteCall` returns — never inside a handler. So the cut is to capture a small concrete struct instead of `s`:

```go
type toolDeps struct {
    emit        func(kind EventKind, data any)
    steer       func(msg string)
    // ... drainSteering/prependSteering, abort, resultToolName ...
    cmdTimeouts func() (defMS, maxMS int) // LIVE getter — SetTimeout mutates at runtime
    reads       *readGuard                // wraps readFiles + readFilesMu + resolveFilePath
    tasks       *taskGuard                // task store + the 4 reminder counters, on the SAME s.mu
    web         webRunner                 // webFetch / webSearch (hides profile+client)
}
```

A struct, not an interface, because there is exactly **one** production host and the dependency is a capture, not a parameter threaded through layers. `readGuard`/`taskGuard` wrap the field clusters that already mutate together (genuine DRY). Config reads are **live getters**, never snapshots. **Stays on `*Session`** (not in `toolDeps`): `execTool` orchestration, `ExecuteCall`, `describeImage`, the registry, `canonicalizeToolNames`, `rebuildToolDefsCache`. Subagent spawn/wait/send/close are **not** here — that is cut #3. A fake host is a struct literal of funcs — no `Session`, no mock framework.

### 3. subagents ⇄ session — a `subagentManager` + one fewer pointer

The entire back-cycle is the `subagent.parent *Session` field — used for *exactly one thing*, `parent.emit(EventSubagentStart/End)`. Replace it with a captured `emit func(EventKind, any)` and the edge is cut. Consolidate the map behind a concrete type:

```go
type subagentManager struct {
    mu   sync.Mutex
    subs map[string]*subagent
    emit func(EventKind, any) // = parent Session.emit
}
```

`subagent.sess *Session` (the **child**) is kept concrete — that is downward composition, not the cycle. `spawnAgent` stays a `*Session` method: its ~16 reads are read-once-at-spawn to build the child `SessionConfig` → the existing `NewSession`. `getSub`/`closeAgent`/`sendInput`/`status`/`Close` route through the manager. **Bonus:** routing the currently-unlocked read at `session_tools.go:879` through `mgr.get(id)` fixes the **PRI-1939** data race. **Preserve:** `Close()`'s collect-under-lock / close-outside-lock ordering; detached children on `context.Background()`; the depth guard on `Session`; the `status.go` two-level lock order; the emit-once guard. The mux "lifecycle controller" (`processOneInput`/drain split) is **grep-verified unnecessary** — the lifecycle never touches the subagents map — and is explicitly out of scope.

### 4. sessiondata — SKIP

`Turn`/`TurnKind`/`SessionMeta`/`SessionSnapshot` are leaf **data** types that carry **no back-edge** into `Session` — moving them cuts zero cycle, it is pure namespacing. Worse, `SessionMeta`/`SessionSnapshot` embed `SessionConfig` (which holds `*TaskStore` and a `ProviderProfile` func) and `EnvironmentInfo` by value, so a faithful move drags half the SCC down. They are also public API (~117 external references). Leave them in package `agent`.

## Packaging verdict (unanimous)

**Break the four cycles with the seams above, in package `agent`, and STOP.** Do **not** extract `agent/internal/{contextstrategy,tools,subagents}`: a subpackage cannot import `agent` (for `RegisteredTool`/`Turn`/`NewSession`/…) while `agent` imports it back — you would first have to sink all those shared types into a base package (large blast radius) to enable a split *no external caller can observe* (zero external refs to `ContextStrategy`/`RegisteredTool`/the strategies). The existing `agent/internal/{workspace,promptpath,installid}` are pure leaves (stateless functions over primitives) — not precedent for SCC-resident clusters.

## Sequencing, execution, stop rule

- **Order:** context → tools → subagents (sequential — all edit `session*.go`).
- **Execution:** each cut in the isolated `seams` worktree, TDD (the suite stays green + one focused fake-host test), gated (`go build ./...`, `go test ./agent/...`, `go vet`, `gofmt`), then verified by a 2-agent adversarial panel, then merged to local `main`.
- **Stop rule:** the effort is **done** when a `go/types` run shows the four `Session` back-cycles are gone and the suite is green. No subpackage extraction, no data-type moves — those are a separate, later decision with their own justification.
