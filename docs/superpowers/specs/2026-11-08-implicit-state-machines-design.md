# Implicit state machines: one declaration, generated clients, agent-readable

Status: implemented on `implicit-state-machines`; retroactive spec for approval
and review. The implementation preceded this document, which the delivery flow
requires us to correct.

## Intent

The app models the same durable things several times over: session status,
turn status, job status, delegate phase, delegate run status, delegate outcome,
watch state, task state, goal state, host-operation state and client-mutation
projection. Each vocabulary had its own string set, its own terminality checks,
and its own client copy. The request was to find all of them and make the best
decision for UX, DX and AX without breaking behavior.

The failure this exposed is concrete. A delegate row carries `lifecycle`,
`phase`, `status`, `outcome`, `terminal` and `resumable`. The shared subagent
dot keyed its amber attention state on `phase` values (`awaiting_approval`,
`awaiting_input`, `needs-you`) that no producer emitted, while the real
`needsAttention` fact never reached the activity row the dot reads.

## Goals

- One declaration for each core machine: states, classes, terminality, legal
  edges where the owner drives transitions, and an initial state where the
  machine starts somewhere.
- Owners use the declared type or table instead of parallel string sets.
- Clients derive the activity failure class and the state class they ask for
  from a generated catalog. The thread-attention precedence rule stays its own
  because it ranks several facts, not one state.
- Agents can read the declaration through the doctor tool without parsing a
  transcript.
- The delegate attention dot reads the fact the daemon actually sends.
- Existing behavior, wire strings and false-valued JSON omission stay the same
  except the attention fix, which adds a truthful state.

## Non-goals

- Enforcing every declared edge in every owner. Owners keep their existing
  guards; the table is authoritative for the core machines and is used where
  the owner already had a guard.
- Migrating persisted data. No stored state changes name or meaning.
- Rewriting the client-only machines (connection, outbox, credential editor,
  provider sign-in, pane lifetime) into the registry. They are inventoried as
  adjacent machines with owners.
- A new generic workflow engine, database, or event system.
- Changing the transcript status word. `needsAttention` reaches the dot; the
  word still states the lifecycle.
- Backward compatibility code beyond accepting both delegate phase names in the
  job-status filter, which already existed.

## Approaches considered

1. **Docs-only inventory.** Finds every machine, changes no code. Cheapest, but
   leaves the parallel vocabularies and the dead attention rule in place, so it
   meets none of UX/DX/AX.
2. **One registry plus aliases and a generated client catalog (chosen).** A
   small descriptor in `appwire` declares the machines; existing internal types
   alias the declared ones; a generator emits TypeScript unions and a runtime
   catalog; the doctor command exposes the registry. It removes duplication
   without adding a framework, and every alias keeps the same string values.
3. **Per-machine explicit tables in each owning package.** Clearest local code,
   but repeats terminality and class rules and gives clients no shared
   declaration. Rejected because the duplication is the problem.

## Design

### Registry

`appwire/lifecycle.go` declares:

- `MachineKind`, `LifecycleClass`, `LifecycleState` (name, class, terminal,
  resting, summary), `LifecycleEdge`, `LifecycleMachine`.
- `LifecycleMachines()` in a stable order, with owned slices.
- `Validate()` checking edges name declared states, the initial state exists,
  and no terminal state has an outgoing edge.
- The twelve core machines: session, turn, thread status, job, delegate phase,
  delegate run, delegate outcome, watch, task, goal, host operation, mutation
  projection.
- Thread status is observed: it starts at `notLoaded` and declares no owner
  transitions. Delegate outcomes are terminal evidence: they declare no
  initial state and no transitions.
- Derived helpers: `JobOutcome`, `DelegateOutcomeRollup`,
  `DelegateRunTerminal`, `WatchState`.

Class meanings: `active` means work is in progress or owed; `waiting` means
the entity waits on a person; `idle` means no work is in progress and no run
has completed in this state (resting, not started, or resumable); `settled`
means a run finished successfully or the entity is done and may be reopened;
`failed` means something broke; `neutral` means a run was ended deliberately;
`ended` means there is no live entity to act on (closed, removed, or not
loaded), without promising terminality.

The session, job, delegate phase/outcome, delegate run, goal and task types are
declared in `appwire`. Internal owners alias them:

- `jobstore.Status` → `appwire.JobStatus`
- `delegatestore.Phase` → `appwire.DelegatePhase`
- `delegatestore.OutcomeStatus` → `appwire.DelegateOutcome`
- `agent.SessionState` → `appwire.SessionState`
- `agent.SubagentStatus` → `appwire.DelegateRunStatus`
- `goal.Status` → `appwire.GoalStatus`
- `hostops.State` → `appwire.OperationState`

A type alias preserves the string values, conversions and method set, so
callers compile unchanged and existing persisted bytes keep their meaning.

### Owners read the table

- Job terminality comes from `appwire.JobStatus.IsTerminal`.
- Delegate fold start, prepare, finish and stop guards ask
  `appwire.DelegateLifecycle().CanTransition`; outcome validation asks
  `DelegateOutcome.Valid`.
- The activity rollup uses `appwire.JobOutcome` and
  `appwire.DelegateOutcomeRollup`; the delegate terminal rule uses
  `DelegateRunTerminal`.
- The job-status filter accepts the declared job statuses plus the declared
  delegate phases, which were already the accepted compatibility set.
- Task and goal status validation use the declared sets; a goal terminalizes
  only from active.
- Watch armed/ended uses `appwire.WatchState`.
- Host-operation `Valid`, `Terminal` and `InFlight` are methods on the declared
  `OperationState`; both wire/store mapping switches become direct returns.

### Client catalog

`internal/appwirets` registers the new enums as runtime unions and emits
`LIFECYCLE_MACHINES` (states, class, terminal, resting, summary; no edges) into
`types.gen.ts`. `appwire-client/typescript/lifecycle.ts` exposes
`lifecycleMachine`, `lifecycleState`, `lifecycleStateClass`,
`lifecycleStateIsTerminal`, `lifecycleStateIsResting` and
`lifecycleStateIsFailed`. `lifecycleStateClass` returns `active` for an unknown
state, matching Go's `Class` fallback; `lifecycleState` returns no facts for
one. `activityData.ts` calls `lifecycleStateClass` for its failure rule and
`isFailedDelegateOutcome` calls `lifecycleStateIsFailed`.

`activityData.ts` classifies activity failures through the generated facts.
`testdata/lifecycle/state_classes.json` pins a representative case per machine;
a Go test and a TypeScript test both read it.

`appwire.MutationProjectionState` stays out of the generator because the
client's receipt bookkeeping accepts a local `notProjected` value outside the
wire set.

### Delegate attention

`appwire.SessionDelegate` and `appwire.JobActivityDelegate` gain
`needsAttention` (optional on the wire). The activity producers fill it from the
delegate aggregate. The client parser and activity store carry it; the shared
dot reads it before the legacy phase vocabulary and after terminality. A
terminal row still reads by its outcome.

### Agent surface

`evener doctor lifecycle` and the `doctor_evener` command `lifecycle` return the
registry as JSON; the CLI also renders a text table. The command takes no
selector and reads no state root. The inventory is `docs/product/lifecycles.md`,
linked from the subsystem map, the product index, the session-activity guide and
the bundled doctoring skill.

## Recovery and preservation

- An unknown state is not terminal and classifies as active, so a newer
  daemon's state stays visible instead of disappearing as settled. Go's `Class`
  and the client's `lifecycleStateClass` both apply that fallback.
- `needsAttention` is `omitempty`, so a false value does not change existing
  wire JSON or fingerprint comparisons.
- Historical states (`orphan-unverified`) keep their recorded state; the store
  still resolves them only to interrupted.
- Task `done` and `cancelled` stay reopened; the machine marks them settled, not
  terminal.
- The delegate phase compatibility strings remain accepted by the job filter.
- The job-list filter and its model-facing schema both come from
  `appwire.JobStatusFilterNames`, so the compatibility set cannot be accepted
  by one and omitted by the other.
- No persisted record is rewritten, and no client cache key changes.

## Compatibility

- An older daemon omits `needsAttention`; the dot falls back to the legacy
  phase vocabulary and never reads attention from an absent fact.
- An older client ignores the added key; the field is `omitempty`, so a false
  value changes no existing JSON or fingerprint.
- A newer daemon's unknown state reads active in Go and the client catalog,
  and is not terminal; the owner's existing state map still decides what it
  can do next.

## Acceptance

- Every registered machine validates, and the shared fixture matches its
  class, terminality and resting facts in both Go and TypeScript.
- The job, delegate, session, turn, thread-status, task, watch, goal and
  host-operation predicates match their old results for known and unknown
  states.
- A delegate with `needsAttention` set produces a `SessionDelegate` and a
  `JobActivityDelegate` carrying it; an open row reads attention and a
  terminal row reads by outcome.
- `job_list`'s status schema and its handler accept the same declared set.
- `evener doctor lifecycle` returns the registry with no selector and rejects
  one.
- Generated output is current, TypeScript typechecks, and the touched tests
  and lint gates pass.

## Whole-journey test plan

- Declare and validate every machine; the shared classification fixture matches
  the registry and the generated client catalog.
- Job terminality and rollup match the old sets; delegate outcome rollup and
  run terminality match the old mappings; delegate fold edges match the fold's
  guards.
- Session, turn, thread-status, task, watch, goal and host-operation predicates
  match their old results, including unknown states.
- Both activity producers carry `needsAttention` on their delegate rows, the
  dot reads attention, and a terminal row reads by outcome.
- The job-status filter and the `job_list` schema accept both vocabularies.
- An unknown state classifies as active in Go and in the client catalog.
- `evener doctor lifecycle` returns the machines with no selector and rejects a
  stray selector.
- Generated output is current (`make lint-generated`); TypeScript typechecks and
  the touched tests pass; Biome passes.

## Evidence to date

- `go build ./...` passes; `go vet` on touched packages passes.
- `go test` passes for `appwire`, `jobstore`, `delegatestore`, `goal`, `task`,
  `hostops`, `internal/appwirets`, `agent/doctor`, `cmd/evener-doctor`, plus
  targeted agent and hub host-operation tests.
- `npm run typecheck` passes; targeted vitest passes for lifecycle, activity,
  delegate-details, session-activity-store, transcript-body and subagent rows.
- `make lint-gofmt`, `lint-naming`, `lint-internal`, `lint-package-imports`,
  `lint-generated` and `lint-biome` pass. Touched-package `golangci-lint` reports
  0 issues.
- `go test ./agent/...` hit the agent package's 10-minute timeout under load;
  a 20-minute full-package run hit the timeout again under the same load.
  Every `agent/*` subpackage and the targeted agent tests passed, and
  `TestMemoryEvalVerifierBehavior` passed alone in 249s.

## Open decision for Jesse

The spec is retroactive. Approve it, or name what changes: the registered
machine set, the generated client catalog, the `needsAttention` wire addition,
or the doctor command. After approval: PAR on the spec, PAR on the implementation,
`simplify-code`, then `shepherd-pr`.
