# Wave 8 — T4b (location cluster — triage #7, unblocked) — report

**Status:** DONE
**Branch:** `w8-t4b` off wave tip `2e2878e3c`
**Commit range:** `2e2878e3c..abeabadef` (1 implementation commit)
**Manifest touched:** `panes/session/chrome/**` only. No chokepoint, reducer/model, tokens.css, or
sibling edit. 5 files (3 new: `LocationCluster.tsx`, `LocationCluster.test.tsx`,
`locationcluster.module.css`; 2 modified: `StatusRow.tsx`, `StatusRow.test.tsx`).

**Final gate (HEAD):** `tsc --noEmit` clean · `vitest run` **240** files / **3451** passed (baseline
**239** / **3439**; +1 file, +12 tests) · `biome ci` exit 0 · `build` exit 0, `dist/PLACEHOLDER`
restored, tree clean. Each gate AND-chained from `cmd/serf-hub/frontend`, `tsc` first.

## What was built

Restored the legacy input-strip **status-location cluster** onto the new `ThreadModel` location
fields the controller wiring commit added (`6c2e51b1e`). New `LocationCluster` component rendered by
`StatusRow` after the work-time clock and before the context gauge (mirroring the legacy
location-then-context order).

**Wire receipts (fields consumed, all snapshot-only, no live push):**
- `model.cwd: string` ← `Thread.cwd` (reducer `hydrateThread` `reducer.ts:269`).
- `model.gitBranch?: string` ← `Thread.gitInfo.branch` (`reducer.ts:270`).
- `model.projectPath?: string` ← `Thread.projectPath` (`reducer.ts:271`).
- Field doc: `model.ts:128-134` ("cwd is always present; gitBranch/projectPath only when known").

**Parity floor + layout (`cmd/serf-hub/templates/partials/input_strip.html:6-10`):** the legacy
`.status-location` cluster renders three parts — **branch / worktree / cwd** — each a `status-key`
label + `status-value` + a `title="<key> <value>"` tooltip, each guarded by `{{if .X}}` (omitted when
empty). Reproduced as **branch / project / cwd** (order preserved), each a keyed part with the same
`"<key> <value>"` title tooltip.

**Honest label divergence (conscious, for close sweep):** the legacy third slot is **worktree**
(`worktreeLabel(WorktreePath)`, `web_workspace.go:496,531`). The new snapshot does NOT carry the
worktree path; it carries `projectPath` = the hub-resolved **canonical project root**, which
`appwire/types.go:186-188` documents as *deliberately separate from cwd* ("a linked worktree may have
a different working directory while still belonging to the same canonical project"). So the third part
is labeled **"project"**, not "worktree" — the honest name for the field the wire actually provides
(and the exact framing the t4b dispatch used: "cwd, git branch, project path"). Mislabeling
`projectPath` as "worktree" would be dishonest.

**Honest absence:** each part is emitted only when its value is non-empty — `gitBranch`/`projectPath`
omitted when absent, and an empty `cwd` (a pathless external thread, per the `projectPath` doc's
"presentation-only" note) is dropped too. No placeholder dashes. The whole cluster returns `null` when
no fact is known.

**Design-system:** tokens-only CSS (`--space-*`, `--ink-low`/`--ink-mid`, `--font-mono`; no chromatic
literal — location is neutral context, never an attention state); every class via `requireClass`;
values ellipsized (`max-width` + `text-overflow`) with the full path in the `title` tooltip;
accessible structure — real inline `branch`/`project`/`cwd` text labels precede each value (not an
interactive control, so no accessible-name-on-control requirement applies).

## RED / mutation proofs

RED: `LocationCluster.test.tsx` failed at import (component absent) before the first write.
Mutation-verified nets (reintroduce defect → net bites → restore to zero net diff):
1. **Honest absence (branch):** rendering `gitBranch` unconditionally ⇒ the "omits branch when absent" test bites.
2. **Honest absence (cwd) + all-empty:** rendering an empty `cwd` ⇒ the "omits cwd when empty" and "renders nothing when no fact is known" tests bite.
3. **Order (branch→project→cwd):** reordering to cwd-first ⇒ the order test bites.
4. **StatusRow integration:** removing `<LocationCluster model={model}/>` from the row ⇒ the "surfaces the session's location facts" composition test bites.
All restored to zero net diff; final suite green.

## Concerns

- **triage #8 (`showCost`) remains consciously deferred — out of this t4b scope, and still no wire
  data.** The location cluster now exists as a potential telemetry home, but there is still no honest
  thread-level cost on the wire (only per-`Turn.cost`; summing loaded turns under-counts —
  `StatusRow.tsx:8-16`). Nothing to consume; unchanged from the T4 report.
- **`ThreadDocumentMode` hide not replicated (conscious divergence).** The legacy hides the location
  cluster in thread-document mode (`input_strip.html:5 {{if not .ThreadDocumentMode}}`). The new
  single-pane `/thread/{ref}` mode is T6's surface and the `StatusRow` receives no mode signal, so the
  cluster renders whenever the fields are present. Gating it on single-pane would need a mode prop the
  chrome does not currently take (a Session/SessionChrome wiring change outside T4's manifest) —
  flagged for the close sweep, not built.
