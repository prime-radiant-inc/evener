# Library demo-surface taste audit — proposed improvements (for panel vetting)

**Goal:** make the *demo surface* — the public APIs of the published libraries
`llm`, `agent`, `auth/openai` (+ `agent/events`) — the kind of Go Rob Pike would
hold up at Google I/O: minimal, orthogonal, no exported internals, great names.

**Status:** PROPOSAL. Each idea below is MINE; a panel of opus reviewers vets each
for legitimacy, correctness (esp. the SPI/deadness claims — see the trap note),
and whether a breaking change to a *published* library is worth it. Nothing is
executed until vetted; breaking-API changes get Jesse's go/no-go.

**Deadness/SPI trap (hard-won):** exports that *look* like internal helpers are
often the **provider SPI** — used cross-package by `llm/providers/*` (adapter
authors), not by app consumers. "Unexport it" is WRONG for those; the right move
is *relocate to an SPI-addressed sub-package*. Every "remove/unexport" idea must
be backed by a repo-wide usage check, not inference.

**Blast radius:** the only consumers are serf's own binaries. Session queue/steer
API changes touch 3 app files (`cmd/serf/serve.go`, `cmd/serf-hub/app_rpc.go`,
`cmd/serf-hub/web_session.go`) + agent internals. The llm SPI is used by
`llm/providers/*`. No external consumers are known (externally-consumable was
validated structurally, not by real dependents) — so breaking changes are cheap
to land in-repo, but still churn a surface meant to be stable.

---

## A. `llm` public surface (~45 funcs, ~55 types — flat namespace)

The package mixes three audiences in one flat namespace: **consumer API**
(`Client`, `Generate`/`StreamGenerate`, `Request`, `Response`, `Message`,
`ProviderAdapter`, errors, streaming), **provider-adapter SPI** (the toolkit an
adapter author needs), and **internal helpers**. The audience split is invisible.

- **A1 — Relocate the provider SPI to an addressed sub-package** (e.g.
  `llm/adapterkit` or `llm/providerkit`). Confirmed SPI (repo-wide usage in
  `llm/providers/*`): `AdapterTimeout`(10 files), `ApplyAdapterTimeout`(6),
  `ParseRetryAfter`(6), `StampEndpointURL`(6), `ParseSSE`(3), `IntFromAny`(5),
  `ExpandTilde`(4), `IsLocalPath`(4), `InferMimeTypeFromPath`(4), `DataURI`(3),
  `RewriteErrorProvider`(1), plus `AdapterTransport`, `ClientWithConnectTimeout`,
  `StampErrorBehaviorTag`, `WrapContextError`, the `New*Error` constructors,
  `RegisterEnvAdapterFactory`/`RegisterInstanceAdapterFactory`,
  `Retry`/`RetryPolicy`/`SleepFunc`. Leaves `llm` as the clean consumer surface.
  **BREAKING · LARGE · confidence medium.** Open question: full sub-package move
  vs. just a documented grouping + `doc.go` "for adapter authors" section.
- **A2 — Truly-internal helpers → `llm/internal/` or unexport.** Symbols used
  ONLY within `llm` (not providers, not consumers): `DefaultSleep` (0 provider
  uses; only `retry_util.go`/`stream_generate.go`). Per-symbol verification
  required. **non-breaking if internal-only · SMALL · confidence medium.**
- **A3 — `APILog*` family (8 symbols) → `llm/apilog` sub-package.** API
  request/response logging (`APILogger`, `APILogContext`, `APILogEntry`,
  `APILogRequest`, `APILogResponse`, `APIRawLogEntry`, `NewAPILogger`,
  `WithAPILogContext`) is a peripheral concern occupying 8 public symbols in the
  core namespace. **BREAKING · MEDIUM · confidence medium.**
- **A4 — Examine the two-axis error model** (`ErrorClass` × `ErrorKind`).
  `doc.go` defends it as deliberate orthogonal axes; likely justified — flag only
  to confirm it reads as exemplary, not over-engineered. **confidence low.**

## B. `agent` public surface (`Session` = 34 methods + schema types)

- **B1 — Collapse the three `*WithImages`/`*WithInput` method doublings.**
  `Steer`/`SteerWithImages`, `Enqueue`/`EnqueueWithImages`,
  `DrainAsSteer`/`DrainAsSteerWithInput` are the same operation twice. Collapse
  each to one method taking images (variadic or an `...Option`). Removes 3
  methods. **BREAKING (3 app files + tests) · confidence medium** — counter-view:
  the no-image overload is an ergonomic convenience; panel weighs.
- **B2 — Move strategy-SPI methods off the consumer surface.** `Emit` and
  `WithResponseSideEffects` exist for the `strategyHost` SPI (consumed in-package
  by `strategy_session_log.go`). They pollute the consumer `Session` surface.
  Assess whether they can be unexported / reached only via the `strategyHost`
  interface. **confidence low-medium** (may be needed for out-of-package plugin
  strategies — verify).
- **B3 — Audit free-function path helpers.** `CacheDir`, `RuntimeDir`,
  `RuntimeDirWithStateHome`, `SessionDisplayName` — consumer API or app-internal
  leaked into the library? Relocate if app-only. **confidence low** (needs usage
  check).

## C. Internal clarity (NON-breaking — safe to run freely)

- **C1 — Split `session_lifecycle.go` (1803 lines) into cohesive files**
  (turn-loop / teardown / tool-batch / queue-drain). Same-package move →
  behavior-preserving by construction (compiler-guaranteed). **SAFE · confidence
  high.**
- **C2 — Survey oversized files/functions** across the libraries; split the worst
  offenders. **SAFE · confidence medium** (pending survey).

## D. Demo material (the literal stage content)

- **D1 — Audit the runnable `Example`s + `doc.go` package docs** for `llm`,
  `agent`, `auth/openai`. Do they read like a confident "here's how you use this,"
  or are they checkbox examples that merely satisfy the docs-gate? Improve the
  weak ones. **SAFE · confidence medium.**

---

## Panel charge

For EACH idea: verdict ∈ {keep, kill, revise}; is it legitimate; is my reasoning
correct (verify SPI/usage claims against the real code with file:line); for
breaking ideas, is it worth churning a published surface; risk (low/med/high);
one-paragraph rationale. THEN: what significant demo-surface wart did I MISS?
Kill churn and matters-of-taste-not-worth-breaking; a KEPT idea must be a genuine
Pike-grade win defensible with evidence.
