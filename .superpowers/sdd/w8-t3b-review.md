# Wave 8 — T3b adversarial review (open-beside producers)

**Reviewer verdict: FIX_ROUND** (one blocking gate failure; the feature itself is correct and well-tested).
**Branch:** `w8-t3b` @ HEAD `7b234cadb` (worktree `webui-w8-t3b`).
**Scope reviewed:** round 1 (`52107aac2..ab9b3597a`, subagent "Open transcript" producer) + round 3
(`7f649fb66..7b234cadb`, file/image card producers). Absorb merge `7f649fb66` (controller sessionRef
wiring + DECISION-B cwd hydration) treated as landed base, not T3b's work.

---

## Gate summary (measured, from `cmd/serf-hub/frontend`)

`npx tsc --noEmit` EXIT 0 → `npx vitest run` **242 files / 3463 tests all pass** (matches both reports
exactly) → **`npm run lint` (Biome ci) EXIT 1 — FAIL, 1 error** → `npm run build` EXIT 0 (+ `dist/PLACEHOLDER`
restored). Three of four gates green; the required Biome-ci close gate is RED at HEAD. Both round reports
claim "Biome ci exit 0" — inaccurate.

---

## Probe outcomes

- **P0 (PIN-A adjudication) — RATIFY the direct `openBeside` call.** (a) Byte-identical: `openDocBeside(p)`
  IS `openBeside({type:"doc", params:p})` (openDoc.ts:27-29); fileOpenBeside builds
  `openBeside({type:"doc", params:{session,path,kind}})` with the same DocParams shape — no param-shape
  difference. (b) VERIFIED by experiment: re-adding the `openDocBeside` value-import crashes **exactly 32**
  DockHost/StackHost tests (`filenameOf(undefined)`→`.split` at docFile.ts:12 / DocPane.tsx:123, the real
  doc pane clobbering the `{ref}`-only fixture). AppShell.tsx:32 registers "doc" at boot (locked by
  paneRestore.test.ts), so openDoc.ts's eager `import "./index"` is now redundant for production. Full
  recommendation below.
- **P1 (D1 closure) — PASS.** Only `read_file`/`edit_file`/`write_file` set `openBesidePath`;
  `apply_patch` (multi-target) + `grep`/`list_dir`/`glob` (dir/pattern) opt out — exactly floor §3.7's
  list. The `openBesidePath?()` hook is optional and touched no other descriptor's behavior.
- **P2 (cwd gate) — PASS.** `cwdRelative` mirrors legacy renderer.js:2204-2210 for absolute paths
  (prefix-strip, out-of-cwd→undefined, cwd-itself→undefined) and adds a `..`-gated already-relative branch
  (beyond-parity, safe). Mutation-verified: removing the out-of-cwd gate kills 4 tests.
- **P3 (image half) — PASS.** `png|jpe?g|gif|webp` exactly mirrors Go `supportedOutputImageMedia`
  (output_images.go:158); SVG excluded there, so `.svg`→kind:file is both safe (source shown as escaped
  text, no scriptable render) and functional (kind:image would 404). ImageGallery/outputImages untouched.
  DECISION-C divergence is accurate (minor phrasing nit, M2).
- **P4 (affordance UX) — PASS.** `e.stopPropagation()` keeps row toggle intact (mutation-verified: 1 test
  kills it); accessible name "Open beside" from button text; `variant="quiet" size="sm"` valid, register
  matches the subagent button.
- **P5 (round 1 transcript) — PASS.** `openTranscript`→`openBeside({type:"transcript",params:{ref}})`;
  desktop splits beside focused, mobile plain-opens; guarded by `transcriptRef`. Tests + mutation nets
  credible.
- **P6 (gates + manifest) — MIXED.** Manifest clean: both commits confined to
  `panes/session/transcript/**` + the report; no chokepoints/reducer/model/tokens/Go/siblings. Counts
  match measured run. **But Biome ci fails (see F1).**

---

## Findings

### Important

**F1 — Biome-ci close gate is RED at HEAD (blocks merge).** `npm run lint` (= `biome ci src`) exits **1**
with one error: `src/panes/session/transcript/tools/subagentModule.test.tsx:9` — "Sort these imports".
Round 1 inserted `import type { DockviewApi } from "dockview-core";` at line 12, in the middle of the
relative-import block; Biome's `organizeImports` wants that package import grouped with the other package
imports (the base at `52107aac2` was lint-clean, and paneRestore.test.ts places its own `dockview-core`
import correctly at the top). `biome.json` is unchanged this wave, so this WOULD have failed at round-1
commit time too — the reports' "Biome ci exit 0" (both rounds) is inaccurate. The plan makes
`npm run lint` EXIT 0 a required per-task-and-close gate, so this must be fixed before merge.
**Fix (mechanical, 1 line moved):** hoist the `dockview-core` type import above the relative imports (or
run Biome's safe organize-imports fix) and re-run `biome ci src` to confirm exit 0. tsc/vitest/build are
unaffected.

### Minor

**M2 — DECISION-C phrasing imprecise (doc-only).** The report says outputImages have "no cwd path on the
wire." True for sha/`data:` images, but shell-path/written-file outputImages DO carry `Path` +
`/doc/image?session=&path=` URL on the wire (app_rpc_test.go:960) — it's `reducer.ts:97` (`outputImagesToStrings`)
that flattens them to a bare string. The operational conclusion (post-reducer `ItemModel.outputImages`
can't feed `openDocBeside` without a reducer change; keep ImageGallery) is correct; only the wording
overstates. No code change needed.

**M3 — relative-arg acceptance is a conscious beyond-parity divergence (ledger note).** Legacy `cwdRelative`
withheld the affordance for already-relative args (they never matched the absolute prefix); the new one
accepts them (`..`-gated). Safe (server `ResolveInRoot` 403s escapes; client gates `..`) and justified
(serf agents pass relative paths), but it is a divergence from the cited legacy behavior — record it in
T8's ledger rather than presenting it as pure parity.

---

## P0 recommendation (PIN-A: producer routes through `openDocBeside` vs the direct `openBeside`)

**Ratify the direct `paneActions.openBeside({type:"doc", params})` call as pin-spirit-met.** The PaneRef is
provably byte-identical to what `openDocBeside` builds, so the pin's intent — "open-beside producers route
through the `openBeside` seam to open a doc pane beside" — is fully met; only the literal helper name
differs. The divergence is forced by a verified constraint (the value-import pulls openDoc.ts's eager
`import "./index"`, clobbering the DockHost/StackHost `{ref}`-only doc fixtures → exactly 32 red tests),
and it adds no new module-load side effect because `paneActions` is already eager in the transcript tree
(subagentModule) and the doc pane is already registered at boot by AppShell.tsx:32 (locked by
paneRestore.test.ts, which the producer now depends on for registration — a safe, protected dependency).

A controller reconciliation to restore the pin's letter IS viable: because AppShell.tsx:32 now makes
openDoc.ts's eager `import "./index"` **redundant**, the controller/T5 could drop that eager import from
openDoc.ts and switch the producer back to `openDocBeside`. **Tradeoff:** that edits an off-limits file
(openDoc.ts, T5's manifest) and removes a defensive self-registration for any future non-AppShell entry
path — a real blast-radius increase for a purely cosmetic letter-match. I recommend **Option 1 (ratify
the direct call, in-manifest, functionally identical)**, and note the redundant openDoc.ts eager import as
an optional, non-blocking T5/controller cleanup — not a T3b task and not a gate on this merge.
