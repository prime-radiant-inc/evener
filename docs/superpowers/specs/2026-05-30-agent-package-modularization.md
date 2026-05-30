# Agent package modularization — leaf-harvest + seam-driven core split

Date: 2026-05-30 · Ticket: PRI-1940 · Follows: PRI-1938 (Spec 0 same-package splits)

## Problem

The `agent` package is a god-package: 70 source files, every central type (`Session`,
`ExecutionEnvironment`, `ToolRegistry`, `RegisteredTool`, events, `ProviderProfile`,
`Turn`) referenced almost everywhere. PRI-1938 split the 5,200-line `session.go` into
focused files but left everything in one package. The question: can we draw real package
boundaries to namespace/modularize it?

## What the dependency analysis says

A `go/types` symbol-reference analysis (every edge = "file A uses a package-level
symbol/method/type defined in file B"; tool in `/tmp/agentdep`, rendered map in
`2026-05-30-agent-package-dependency-map.{dot,svg,png}`) is decisive:

- **13 of 14 concern-clusters form a single strongly-connected component** with `session`.
  No package boundary along any concern line is cycle-free.
- The knot is held shut by **four direct back-cycles into `session`**: `tools⇄session`
  (handlers close over `*Session`), `context⇄session` (strategies take `*Session`),
  `subagents⇄session` (subagent needs its parent), `sessiondata⇄session` (snapshot/turns/
  fork reference `Session`). Everything else joins the SCC transitively through those four.
- Only **`events`** sits cleanly below `session` at the cluster level.

This is the import-cycle trap the mux improvement spec's adversarial reviews warned about,
and it confirms Spec 0's same-package-first call was correct, not a cop-out.

**The hope is at the file level:** ~37 of 70 files reference *zero* `session` symbols. The
clusters are entangled only because a *few* files per cluster touch `session`. Individual
clean files can be extracted today.

## Strategy — two tiers

**Tier 1 — leaf-harvest (now).** Extract the cleanest *pure leaf* files (`outdeg=0` — they
reference no other `agent` file at all, so they are guaranteed cycle-free) into
`agent/internal/*` lower packages. Extract files **individually**, never whole
concern-clusters (each cluster has ≥1 file that touches `session`). This shrinks the
god-package and establishes the `agent/internal/*` pattern with zero cycle risk.

**Tier 2 — seam-driven core split (deferred).** The entangled core can only be split into
packages after the four back-cycles are cut with **interfaces** — which is exactly the mux
Specs 1–4 seam work (lifecycle controller breaks `subagents⇄session`; a tool-policy/registry
interface breaks `tools⇄session`; the event broker — `events` is already the lone leaf;
runtime services). Deep package boundaries fall out of that work as a *consequence*. Chasing
them before the seams exist is the cycle fight to avoid. **Out of scope for this ticket.**

## Tier 1 extraction procedure (per leaf file F → package `agent/internal/<pkg>`)

This is a behavior-preserving move across a package boundary; the existing `agent` suite is
the regression harness.

**Selection pre-req (critical):** an `agent/internal/*` package is importable only under
`agent/`. So a candidate's used symbols must have **zero references outside the `agent`
package** (`grep` `cmd/ server/ llm/ internal/ cmdutil/`). A symbol consumed externally is
public API — it cannot move to `internal/` (it would break external imports). It may move
to a non-internal `agent/<pkg>` later, but that relocates public API and is out of this batch.

For each selected extraction:

1. `mkdir -p agent/internal/<pkg>`; create `agent/internal/<pkg>/<file>.go` with F's full
   content, changing the header `package agent` → `package <pkg>`. Run `goimports` on it.
2. **Export any top-level symbol that F defines AND the rest of `agent` references** but is
   currently unexported (capitalize it; rename its uses). First-batch files are chosen to
   already export their used symbols, so this step is a no-op for them.
3. Delete the original `agent/<file>.go`.
4. `go build ./agent` now reports `undefined: X` at each site in `agent` that used a moved
   symbol unqualified. For each: add `import "primeradiant.com/serf/agent/internal/<pkg>"`
   to that file and qualify `X` → `<pkg>.X`. (Only **type names, function names, and
   package-level vars/consts** need qualifying — struct *field* and *method* accesses on a
   value do not.) If the package name collides with a local variable (e.g. a `timings` var),
   alias the import. Loop `goimports -w` + `go build ./agent` until clean.
5. `go build ./...` and `go test ./agent` green; `gofmt -l` clean.
6. Commit (files by name): `refactor(agent): extract <file> into agent/internal/<pkg>`.

Extractions touch shared `agent` files (the qualification sites), so they run **sequentially**
(each commits before the next starts) — no parallel file races.

## Candidate backlog (churn-ranked; churn = core references into the file)

Pure leaves (`outdeg=0`, guaranteed cycle-free), cheapest first. Note churn overstates real
cost — it counts field/method accesses that need no qualification.

| File | → package | churn | needs export? | notes |
| --- | --- | --- | --- | --- |
| `prompt_paths.go` | `promptpath` | 2 | no | `GlobalPromptsDir`, `ProjectPromptsDir` |
| `workspace.go` | `workspace` | 2 | no | `WorkspaceInfo`, `ScanWorkspace` (+ internal helpers); 334 lines, self-contained |
| `installation_id.go` | `installid` | 4 | **yes** (2 syms) | `loadOrCreateInstallationID`, the metadata-key const |
| `round_timings.go` | `roundtiming` | 46* | no | one type `RoundTimings`; *real cost ~3 type sites; alias import (local `timings` var) |
| `builtin_skills.go` | (skills pkg) | 1 | check | built-in skill data |
| `turns.go` | (turns pkg) | 66* | check | `Turn`/`TurnKind` — foundational, high real churn |
| `env.go` | `execenv` | 50* | check | `ExecutionEnvironment` — central abstraction, high churn |
| `events.go` | `agent/events` | 251* | no | **the foundational one** — `EventKind` + all event-data types; the lone clean leaf-cluster; biggest payoff, biggest sweep |

`prompt_assets.go` is **excluded**: its `//go:embed` directive couples it to its source
directory, so it cannot move without relocating the embedded prompt tree.

## First batch (this ticket, via the `agent-leaf-harvest` workflow)

The three cleanest pure leaves with **zero external references** (confirmed):

1. `agent/internal/workspace` ← `workspace.go` — `WorkspaceInfo`, `ScanWorkspace` already exported, 0 external refs.
2. `agent/internal/promptpath` ← `prompt_paths.go` — `GlobalPromptsDir`, `ProjectPromptsDir` already exported, 0 external refs.
3. `agent/internal/installid` ← `installation_id.go` — `loadOrCreateInstallationID` + the metadata-key const are *unexported* (so internal-only); they get **exported** on the move (demonstrates the export path), 0 external refs.

> **`round_timings.go` was dropped from the first batch:** `RoundTimings` is consumed
> externally by `server/appwire_projection.go` through the event system, so it is public API
> and cannot move to `internal/`. Deferred (a non-internal `agent/<pkg>` relocation, later).

Executed as a workflow: one subagent per extraction (sequential, build+`go test ./agent`
gated, committed), then a holistic review reconstructing each move to confirm it is a pure
relocation (no logic change) and the qualification is complete.

## Non-goals

- No concern-level package reshuffle (the cycle trap).
- No core split before the Tier-2 seams exist.
- No widening of the *public* API — all new packages are `agent/internal/*` (module-private).
- No behavior change — extractions are pure relocations gated by the existing suite.

## Risks

- **Qualification misses** → caught hard by `go build` (`undefined`).
- **Naming collisions** (package name vs local var) → aliased imports; caught by build.
- **Export widening** (Tier-1 files needing export) → confined to `internal/`; deferred files only.
- **The deferred high-churn foundational extractions** (`events`, `turns`, `env`) are large
  mechanical sweeps best done with a build-error-driven loop and their own review, in later batches.
