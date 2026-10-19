# Plugin Warning Ownership and Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking. Jesse requires a dedicated Sol 6.1 coordinator in its own worktree for this fix.

**Goal:** report an unresolved selected-plugin failure once, with useful advice, while keeping recovery and filesystem diagnostics out of ordinary conversation detail.

**Architecture:** one session-owned episode tracker arbitrates duplicate source failures across skill discovery and component loading. Existing warning fields carry three typed codes. The shared TypeScript warning policy governs native and web visibility.

**Tech Stack:** Go events/diagnostics, AppWire projection, shared TypeScript warnings and real native/web consumers.

**Spec:** [Plugin session preservation design](../specs/2026-10-16-plugin-session-preservation-design.md), shared constraints and Fix 3.

## Global Constraints

- Continue using healthy plugins and ordinary conversation when one source fails.
- Tests assert typed state, identities, counts and side effects, never prose.
- Default tests use local fixtures and scripted external boundaries, never live providers or ambient home configuration.
- Run targeted red/green checks locally. Full tests and lint gates belong in CI.
- Use existing client widgets. This work introduces no new widget or widget prop.
- Do not blanket-hide `skill_diagnostic`, uncoded warnings, or independent component failures.

## Review Focus

1. Two producers observe the same absent source: one episode, one actionable notice. Task 1.
2. Recovery followed by a later failure: allow a new episode without permanent suppression. Task 1.
3. Hook/MCP failure with readable skills: preserve the independent component failure. Task 1.
4. Overlay and direct idle warnings at each detail level: shared policy agrees across clients, including detail changes after arrival. Task 2.
5. Unknown warning codes and unrelated skill diagnostics: keep existing conservative visibility. Task 2.

---

## Ownership and execution

Use coordinator `codex-jesse-at-pr/gpt-6.1-sol` with `isolation="worktree"`, based
on the completed recovery and native-history fixes. Own the paths below. Changes
to `mobile/src/state/conversation.ts` are limited to transient warning metadata
and shared visibility policy; do not redo overlay/history reconciliation here.
Avoid new protocol fields, widgets, transports or a filesystem polling service.

### Task 1: consolidate source-failure episodes at the producer

**Files:**
- Create: `agent/plugin_diagnostics.go`, `agent/plugin_diagnostics_test.go`.
- Modify: `agent/session_init.go`, `agent/session_events.go`, `agent/session_skill_source.go`, `agent/session.go` as required for the owner field.
- Modify: `agent/events/payloads.go`, `agent/diagnostic/diagnostic.go` and focused tests.
- Inspect: `agent/plugin/skill_sources.go`, `agent/plugin/plugin.go` for structured error ownership, preserving independent component failures.

**Interfaces, new:**

```go
// In agent/events/payloads.go.
const WarningCodePluginUnavailable = "plugin_unavailable"
const WarningCodePluginDiagnostic = "plugin_diagnostic"
const WarningCodePluginRecovered = "plugin_recovered"

// In agent/diagnostic/diagnostic.go, recognized by normalizeSource and defaultForSource.
const SourcePlugin Source = "plugin"

// In agent/plugin_diagnostics.go. SourceID is canonical root identity,
// Component is "source" or the stable component/relative-source identity.
type pluginFailureKey struct { SourceID, Component string }
type pluginFailureEpisodes struct { active map[pluginFailureKey]bool }
func (e *pluginFailureEpisodes) Observe(key pluginFailureKey, failed bool) bool
```

`Observe(key, true)` returns true only at the start of a failure episode.
`Observe(key, false)` clears that episode after confirmed recovery and returns
false. A missing physical root uses a stable cleaned absolute identity until it
can be canonicalized; all producers consume the same selection-resolution root
mapping, rather than independently assigning identities.

Use existing `events.WarningData` (`Code`, `Source`, `Message`, `Title`, `Hint`,
`PluginName`). On a new unresolved-source episode, queue one `plugin_unavailable`
summary and one `plugin_diagnostic` technical detail. Use safe plugin identity in
the summary, never the raw OS error. On successful recovery, consume the prior
plan's `SelectedDirRecovery` once and emit `plugin_recovered` with the same source
owner. Genuine independent skill/collision/component errors retain their owners.

- [ ] Add `TestPluginWarningsCoalesceMissingSource`, `TestPluginWarningsRecoverThenRefail`, and `TestPluginWarningsKeepIndependentComponents`. Drive the real initializer/loaders with local broken and healthy plugins; capture emitted typed warnings. Assert:

```go
if counts[events.WarningCodePluginUnavailable] != 1 { t.Fatal(counts) }
if counts[events.WarningCodePluginDiagnostic] != 1 { t.Fatal(counts) }
// Reobserving one failure must not increment either count.
// Successful source validation clears only its key; a later failure increments both to 2.
```

  A broken hook with a readable skill must still emit its own component warning.
  Use structured diagnostic classification to verify plugin-specific advice;
  never assert a particular phrase. An opaque test path sentinel may prove raw
  filesystem data reaches only the technical record, not the actionable summary.
- [ ] Run `go test ./agent -run '^TestPluginWarnings' -count=1`; observe both original producers emitting before adding the tracker. Run diagnostic-specific tests before changing normalization.
- [ ] Introduce the owner and codes. Feed source discovery, full component loading and fresh invocation through it. Suppress duplicate source failures by key, not by text. Classify plugin diagnostics before generic daemon advice. Use explicit title/hint fields to preserve useful plugin-specific recovery advice. Clear an episode only when the relevant source/component actually succeeds, not merely when another loader runs.
- [ ] Rerun the focused agent tests plus `go test ./agent/diagnostic ./agent/events -count=1`. Verify healthy skills remain callable and successful recovery produces no unavailable warning.
- [ ] Stage named touched files, inspect the staged diff, commit `fix(agent): own plugin failure notices per episode`, then run specification and quality review.

### Task 2: enforce the shared detail policy with real producer evidence

**Files:**
- Modify: `appwire-client/typescript/warnings.ts`, root exports in `appwire-client/typescript/index.ts`, tests in `warnings.test.ts` and `transcriptProjector.test.ts`.
- Modify/test: transient warning handling in `mobile/src/state/conversation.ts` and `mobile/src/state/conversation.test.ts`.
- Test consumers: `mobile-native/src/projectedRows.test.ts`, `mobile-native/src/TimelineItem.test.tsx`, and `cmd/evener-hub/frontend/src/panes/session/transcript/messages/WarningItem.test.tsx`.
- Create: `agent/plugin_warning_wire_test.go` and `agent/testdata/pluginwarningwire/README.md` with a recorded real-producer fixture beside it.
- Update: `docs/product/history-content.md`, `docs/product/subsystems.md` S03/S10/S17–S19/S21.

**Interfaces:**
- Define/export `WarningCodePluginUnavailable`, `WarningCodePluginDiagnostic`, and `WarningCodePluginRecovered` in TypeScript with the exact values from Task 1. Bind their machine-token values to the Go constants in the existing warning contract test style.
- Add only `plugin_diagnostic` and `plugin_recovered` to the existing informational-code policy. `plugin_unavailable` remains actionable at all levels.
- Export `isInformationalWarningCode(code: unknown): boolean` from `warnings.ts` and the package root. Have the existing `isInformationalWarning(item: ItemModel)` call it, preserving one classification list. Native transient warnings retain the folded `code` alongside their row/anchor and use that predicate with existing `informationalNoticesVisible` at projection time. Do not discard a hidden notice on arrival or mistake detail filtering for retention eviction; it must appear when the reader selects full detail while it remains in the retained window.
- Keep the current warning shape and projector API. Follow `agent/testdata/summaryfailurewire/README.md` for real producer → AppWire → shared decoder → client evidence, not a hand-authored warning that bypasses production mapping.

- [ ] Produce a deterministic wire fixture from the real missing-source/recovery scenarios in Task 1. Add detail-level tests at conversation, tool-call and full configurations. Assert notice counts by warning code/severity and rendered role, not prose: unavailable = 1 at every level; technical/recovered = 0 at ordinary levels and 1 each when present at full. Unknown codes and unrelated skill diagnostics retain their baseline behavior.
- [ ] Add `idle coded warnings follow shared detail policy` in the store tests. Send direct `warning` frames with no active turn for all three codes, unknown codes and uncoded warnings. Switch ordinary → full → ordinary, refresh, and apply retention pressure. Assert visibility, stable identity, arrival anchoring, bounded eviction and no promotion into recorded history. This is an additional consumer-boundary test: the inspected plugin producer emits overlay notices, so do not describe a direct frame as evidence of that producer route.
- [ ] Run from `mobile-native`:

```bash
node node_modules/vitest/vitest.mjs run --root .. --config mobile-native/vitest.config.mts --configLoader runner --no-cache --maxWorkers 2 --exclude '**/.worktrees/**' appwire-client/typescript/warnings.test.ts appwire-client/typescript/transcriptProjector.test.ts mobile/src/state/conversation.test.ts mobile-native/src/projectedRows.test.ts mobile-native/src/TimelineItem.test.tsx
```

  Observe quiet codes failing the ordinary-detail assertions before changing the policy.
- [ ] Change the shared code classification, reuse existing renderers and update the evergreen docs. If a client bypasses shared policy, correct that existing consumer without adding a second classification list.
- [ ] Rerun the targeted tests, `go test ./agent -run '^TestPluginWarningWire' -count=1`, and `(cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/WarningItem.test.tsx)`. Inspect dependency/preflight scripts first; use a real local dependency install, never `npm ci` through a symlink. Run `(cd mobile-native && npm run check)` and `(cd cmd/evener-hub/frontend && npm run typecheck)`. Format touched shared files from the frontend's pinned Biome directory, and native files from native's pinned install. Full native/web gates run in CI.
- [ ] Commit exact touched paths as `fix(clients): show plugin technical notices only at full detail`. Run task reviews and whole-fix review; report producer-to-renderer evidence and any device qualification not run.

## Completion

One failure has one short ordinary-detail notice. Recovery and raw filesystem
errors stay at full detail. At full detail the summary and technical diagnostic
are intentionally distinct rows. The native refresh fix separately guarantees
that refreshing those rows cannot multiply them.
