# Wave 8 — T4b (location cluster — triage #7) — adversarial review

**Range:** `2e2878e3c..7cbd4b5bb` (impl `abeabadef` + report `7cbd4b5bb`)
**Verdict:** **APPROVED**
**Reviewer stance:** claims verified against code (legacy template, Go wire types + handlers, T6's landed
single-pane wiring), gates re-run from scratch, two mutation nets re-executed live.

## Gate summary (re-run by reviewer, AND-chain, tsc first, from `cmd/serf-hub/frontend`)

`npx tsc --noEmit` **exit 0** · `npx vitest run` (bare) **exit 0 — 240 files / 3451 tests passed** ·
`npm run lint` (biome ci) **exit 0** (663 files) · `npm run build` **exit 0**, `git restore dist/PLACEHOLDER`
clean, `git status --porcelain` empty. Delta vs baseline 239/3439 = **+1 file, +12 tests**, reconciled
exactly (see P6).

## Probe outcomes

- **P0 pins (standing): MET.** No chokepoint (`routing.ts`/`AppShell.tsx`/`Session.tsx`/`paneRegistry`/
  `widgets/index.ts`/`index.html`), no `reducer.ts`, no `protocol/model.ts`, no `tokens.css`, no Go touched
  (`git diff --name-only 2e2878e3c 7cbd4b5bb` = 5 files under `panes/session/chrome/**` + the SDD report).
  Design-system binds satisfied (P5). Gates honest-exit-code, count-up (P6). Wire truth confirmed against the
  Go handlers, not a fixture guess.
- **P1 parity fidelity: VERIFIED.** Legacy `templates/partials/input_strip.html:6-10` = an outer
  `.status-location` span holding three `{{if}}`-guarded keyed parts (branch/worktree/cwd), each a
  `status-key` + `status-value` under `title="<key> <value>"`, placed before the context item (line 12).
  `LocationCluster.tsx` mirrors it faithfully: outer `cluster` span, conditionally-pushed keyed parts
  (branch/project/cwd), each `key`+`value` span under `title={`${key} ${value}`}`, rendered in `StatusRow`
  immediately before the context gauge. Structure, order, tooltip form, and location-then-context placement
  all match; only the 3rd label differs (→ P3).
- **P2 honest absence: VERIFIED (2 nets re-run live).** `locationParts` truthy-guards each field, so an
  absent value AND an **empty-string cwd** are both dropped (`if (model.cwd)` drops `""`, the case the probe
  called out); `parts.length === 0 → return null` blanks the whole cluster. Re-ran mutation 2 (render empty
  cwd unconditionally) → "omits cwd when empty" + "renders nothing when no fact is known" both bit (2 failed);
  re-ran mutation 3 (reorder cwd-first) → the order test bit (1 failed). Restored to zero net diff each time.
- **P3 label divergence: CONFIRMED honest (wire-truth).** `appwire/types.go:196-203` documents `ProjectPath`
  as a hub-resolved canonical project root "intentionally separate from CWD: a linked worktree may have a
  different working directory while still belonging to the same canonical project." The wire `Thread` carries
  **no worktree path** — `Path = filepath.Base(cwd)` (`app_threadread.go:222`, a display basename),
  `ProjectPath = project.CanonicalPath` (`app_threadlist.go:99`); the legacy "worktree" came from
  `worktreeLabel(pe.Meta.WorktreePath)` (`web_format.go:210`, `web_workspace.go:509/544`), a persistence-meta
  field never projected onto `appwire.Thread`. So "project" is the honest label for the field the wire
  actually provides, "worktree" would be a lie, and no worktree fact exists on the wire to label honestly.
- **P4 thread-document-mode divergence: REAL divergence, correctly deferred (Minor).** `/thread/{ref}`
  resolves to the **session pane** (`routing.ts:63-64`), which mounts `SessionChrome → StatusRow →
  LocationCluster`. Single-pane mode strips only *shell* chrome — `RailHost` (`AppShell.tsx:245`) and, via the
  `[data-single-pane]` marker, dockview's tab strip + the mobile drawer trigger (`singlePane/global.css`) —
  never the in-pane footer chrome. **StatusRow does render there.** So the legacy hide (`input_strip.html:5`
  `{{if not .ThreadDocumentMode}}`, which wrapped only the location cluster) is **not moot**: the new surface
  shows the cluster where the legacy hid it. The implementer disclosed this precisely and deferred it; the
  deferral is correct — gating would require either a chokepoint / `SessionChrome`-contract change (out of
  T4b's manifest) or coupling `LocationCluster` to global `window.location` (an anti-pattern), and the plan
  already resolved single-pane = the session pane with full chrome (§Ambiguities #1), flagging the floor-§2
  "met vs diverged" rows for M9. Auth-gated app ⇒ no disclosure escalation. Recorded for the close sweep.
- **P5 design system: VERIFIED.** `locationcluster.module.css` is tokens-only — `--space-1/2`, `--ink-low`,
  `--ink-mid`, `--font-mono`; the token-contract chromatic-literal guard passes it, and it reaches for no
  `--attention/--alive/--danger` (correct color-is-attention: location is neutral context → ink ramp only).
  `max-width: 24ch` is a content-sizing literal, consistent with sibling precedent (`statusrow.module.css`
  `width: 64px`), not a color/spacing-token violation. All four classes go through `requireClass`; `styles.`
  appears only in code, never a comment (comment-blind guard safe). Ellipsized paths stay accessible: CSS
  `text-overflow` clips only the visual render — the full value lives in the DOM text node — and the part's
  `title="<key> <value>"` supplies the full text on hover. Keys lower-case matches the legacy `status-key`
  parity floor (and `cwd` is a fixed initialism); no sentence-case regression.
- **P6 gates + manifest: VERIFIED.** Gates above. Manifest: 5 impl files all under `panes/session/chrome/**`
  (+ the SDD report); no chokepoint/reducer/model/tokens/Go. Delta reconciled exactly: LocationCluster.test.tsx
  (7, the +1 file) + StatusRow.test.tsx (+2) + requireclass-contract's per-consumer test for LocationCluster.tsx
  (+1) + token-contract's chromatic-literal + attention-allowlist tests for locationcluster.module.css (+2) =
  **+12**. The dynamic-scan guards grew with the new consumer + new CSS module exactly as the probe anticipated.

## TDD RED + report accuracy

RED is credible: `LocationCluster.test.tsx` imports `./LocationCluster`, so before the component existed the
whole file errored at collection ("component absent"). The report is accurate on every substantive claim —
gate counts exact, field mappings match the Go wire + generated TS (`Thread.cwd: string`,
`projectPath?: string`), the four mutation nets real (two re-verified), honest-absence and the P3/P4
divergences correctly characterized and consciously flagged. `showCost` (triage #8) deferral is consistent
with the parent T4 report (no honest thread-level cost crosses the wire; `StatusRow.tsx:8-16`) and is outside
T4b's stated "cwd, git branch, project path" scope.

## Findings

**Critical:** none.
**Important:** none.

**Minor**
1. **Line-citation drift (docs only).** The report cites `appwire/types.go:186-188` and
   `web_workspace.go:496,531`, and the shipped `LocationCluster.tsx:13` comment cites
   `appwire/types.go:186-191`; the actual locations in this tree are `types.go:196-203` and the
   `worktreeLabel` call sites `web_workspace.go:509/544` (def `web_format.go:210`). Every referenced
   symbol/function/doc is real and the content is represented accurately — only the line numbers are ~10 off.
   The one that lives in shipped code is the component comment; a fixup is trivial and non-blocking.
2. **`ThreadDocumentMode` hide not replicated on `/thread/{ref}` (P4).** Real divergence (StatusRow renders in
   single-pane mode), already disclosed by the implementer and correctly deferred to the close sweep / M9 —
   recorded here so the ledger tracks it. Not a defect in T4b's work.
