# Wave 8 — T3b (PIN-A producer wiring) report

**Status:** NEEDS_CONTEXT
**Branch:** `w8-t3b` (worktree `webui-w8-t3b`), base wave tip `52107aac2`
**Commit range:** `52107aac2..5a776fa1f` (1 commit — sub-task 3)
**Gate (that commit):** tsc clean → `237 files / 3401 tests` bare-vitest green (baseline `237 / 3400`, +1) → Biome ci exit 0 → build clean + `dist/PLACEHOLDER` restored.

Sub-task 3 (subagent "open transcript" → transcript pane) is **DONE and committed** (closes presweep
**D2**). Sub-tasks 1 & 2 (file/image tool cards → `openDocBeside`, presweep **D1**) are **BLOCKED on
off-limits files + one open design decision** — details and an actionable unblock path below.

## Sub-task 3 — subagent "open transcript" → read-only transcript pane (DONE, fixes D2)

`subagentModule.tsx`'s `openTranscript` changed from `workspaceStore.openPane("session", {ref})` (the
stale W4-era stand-in the D2 finding named) to **`openBeside({type:"transcript", params:{ref}})`** — the
DISTINCT read-only surface (plan §Ambiguities #1 / PIN-A: reachable via `openBeside`, never a URL),
opened against the subagent child's own `transcriptRef`. This is a clean **replace** (not
complement): PIN-A is explicit, the button is literally "Open transcript", and the code's own stale
comment already said "session stands in for a dedicated transcript pane type, which registers in
Wave 8" — so there is **no ambiguity** to STOP on. The old `workspaceStore` import is dropped.

Tests (both paths the coordinator asked for, mutation-nets stated):
- **Mobile / no dockview host** (`registerDockviewApi(null)`): the transcript pane opens as a plain
  full-screen open (`beside` undefined) and **no** session pane is created. Mutating the target back
  to `openPane("session")` fails both the type assertion and the no-session assertion.
- **Desktop host present** (`registerDockviewApi(fakeApi())` + a focused anchor pane): the transcript
  pane opens **split beside** the focused pane (`beside === anchorId`). Mutating the split hint (or
  dropping the `openBeside` call) fails the `beside` assertion.

`openBeside`'s own split/mobile branching is T6's tested surface (`shell/paneActions.test.ts`); these
tests verify the *producer* routes through it correctly on each path.

## Sub-tasks 1 & 2 — file/image tool cards → `openDocBeside` (NEEDS_CONTEXT, D1 stays open)

**Why not built:** the file/image tool-card renderers are descriptor bodies that receive
`ToolRenderProps = { item, live }` only (`toolRenderers.ts`). Building the floor §3.7 affordance needs
data that is **not reachable from my manifest**:

1. **Session ref** — needed for `openDocBeside({session, …})` / `/doc/file?session={id}`. It lives in
   `Session.tsx` and is threaded nowhere below it: `Session.tsx:182` renders
   `<TurnBlock turn={turnAt(index)} />` with **no `sessionRef`**, there is no session-ref React
   context (only `shell/clientContext.tsx` for the AppwireClient), the `ItemModel` carries no ref, and
   turn/item ids are per-thread-sequential so the ref cannot be recovered from them. `Session.tsx` is
   an explicit **chokepoint (off-limits)**. This is the **same one line** my merged T3 turn-failure
   end-cap already needs (`.superpowers/sdd/w8-t3-report.md` Concern 1): `Session.tsx:182` →
   `renderRow={(index) => <TurnBlock turn={turnAt(index)} sessionRef={ref} />}`.

2. **Session cwd** — floor §3.7 requires the file arg be made **relative to the session cwd**, and
   **out-of-cwd paths get no affordance at all** (legacy `cwdRelative`, `renderer.js:2201-2219`). The
   cwd is on the wire at **`Thread.cwd` (`types.gen.ts:771`)** but `hydrateThread` does **not** map it
   onto `ThreadModel` (grep-confirmed: no `cwd` in `model.ts`/`reducer.ts`), and `reducer.ts` is
   **off-limits**. So even with the ref, the client cannot relativize or gate out-of-cwd without a
   controller-landed reducer change.

3. **Image mechanism is an unresolved design decision (§3.8 / presweep D13).** The `openDocBeside`
   image path — `docImageURL(session, path)` → `/doc/image?session=&path=` — serves a **cwd file by
   relative path**. But a tool card's images are `ItemModel.outputImages`, which the reducer
   **flattens to plain src strings** (`img.url ?? img.path ?? img.name ?? img.source`,
   `reducer.ts:94`) — typically the sha-addressed `/s/{id}/images/{sha}` URL (or a `data:` URL) the
   legacy card opened directly (`renderer.js:2263-2274`), **not** a cwd-relative file path. The new
   seams have no "open an arbitrary image URL in a pane" path, so routing tool-card images through
   `openDocBeside(kind:"image")` does not fit the data. Floor §3.8 flags exactly this as an open
   decision ("extend the allowlist, or route these through `/doc/image`"). I did not invent a
   resolution.

**Controller decisions needed to unblock (then I complete these in my manifest):**

- **(A) Add the `Session.tsx:182` `sessionRef={ref}` line** (the chokepoint edit — already needed for
  turn-failure). Once blessed, I thread `sessionRef` through the parts I own —
  `TurnBlock → ItemRenderProps (types.ts) → ToolCallItem → ToolRenderProps → fsTools/editTools` — and
  wire the file-card affordance (`read_file`/`edit_file`/`write_file` only; `apply_patch`/`grep`/`ls`
  excluded per floor §3.7).
- **(B) Session cwd:** either land a reducer change mapping `Thread.cwd` → `ThreadModel.cwd` (so I can
  do the floor's client-side relativization + out-of-cwd gate), **or** rule that the affordance passes
  the raw path and relies on the server's `ResolveInRoot` 403-on-escape (a conscious divergence: an
  out-of-cwd file would show an affordance that opens a 403 pane instead of no affordance).
- **(C) Image open-beside mechanism (D13):** decide whether image cards route through
  `openDocBeside(kind:"image")` (requires a cwd-relative image path the model does not carry today) or
  some other same-origin-image-in-a-pane mechanism. This is a spec/controller call.

I stopped rather than build the threading speculatively: without (A) it would be another
built-but-unreachable affordance (the exact D1/D2 anti-pattern this task exists to fix), and (B)/(C)
touch reducer/spec territory that is controller-owned.

## Concerns

- **One chokepoint line unblocks the most:** decision (A) — `sessionRef={ref}` at `Session.tsx:182` —
  is required by BOTH this task's file cards AND the already-merged T3 turn-failure recovery. Landing
  it once lets me finish the file-card affordance (modulo the cwd/image decisions B/C).
- **D1 remains open** until (A)+(B) land; **D2 is closed** by the committed sub-task 3.

---

# Round 2 (blockers A/B/C resolved) — file/image cards DONE, D1 closed

**Status:** DONE
**Commit range (this round):** `5a776fa1f..38df503f9` (branch re-baselined at merge `7f649fb66`; this
round is the single commit `38df503f9`).
**Gate:** tsc clean → `242 files / 3463 tests` bare-vitest green (post-merge baseline `241 / 3444` +
sub-task-3's +1 → 3445; this round +18) → Biome ci exit 0 → build clean + `dist/PLACEHOLDER` restored.

The controller resolved all three blockers: (A) `Session.tsx:182` now passes `sessionRef={ref}` to
TurnBlock; (B) `ThreadModel.cwd` (always) / `gitBranch?` / `projectPath?` now hydrate from the wire;
(C) the image-mechanism adjudication. Both remaining sub-tasks are now built.

## Sub-task 1 — file tool cards → open beside (closes presweep D1)

`read_file`/`edit_file`/`write_file` cards now carry an "Open beside" control that opens the
referenced file in a read-only doc pane beside the session. Wiring, all in-manifest:
- `sessionRef` threaded from TurnBlock → `ItemRenderProps` → `ToolCallItem` → the new
  `FileOpenBesideButton` (the descriptor `body`/`ToolRenderProps` were left alone; the control lives
  in the row summary so it shows collapsed, and its click `stopPropagation`s so opening a file never
  toggles the row).
- Each single-file descriptor opts in via a new `openBesidePath?(item)` (returns `file_path ?? path`);
  multi-target `apply_patch` and directory/pattern `grep`/`list_dir`/`glob` opt out (floor §3.7).
- `fileOpenBeside.tsx` (new, pure + component): `cwdRelative` makes the arg relative to `ThreadModel.cwd`
  and **gates out-of-cwd → no affordance** (the legacy `cwdRelative`/`fileOpenBesideSpec` rule,
  `renderer.js:2201-2251`). It handles BOTH an absolute arg (strip the cwd prefix) and an
  already-relative arg (accepted unless it escapes via `..`) — the two shapes execenv's `resolve()`
  accepts (`local.go:1530-1533`), so the affordance isn't dead when the agent passes relative paths.
- `cwd` is read from the store by ref (`useThreadsStore`), snapshot-only and stable, so no re-renders
  from streaming.

## Sub-task 2 — image half (DECISION C, binding)

- **Built:** a file-path-bearing card whose path is an image file (e.g. `read_file` on `logo.png`)
  opens with **`kind:"image"`** (extension set mirrors what `/doc/image` serves — png/jpeg/gif/webp;
  SVG excluded → opens as a file), so the doc pane renders it via `docImageURL`.
- **Unbuildable-as-specced (divergence-ledger entry):** the floor §3.7 "Image cards" row —
  open-beside for a card's `item.outputImages` — cannot be built through `openDocBeside`. Per
  DECISION C, `ItemModel.outputImages` are flattened sha-URL/`data:` src strings
  (`outputImagesToStrings`, `reducer.ts:94`) with **no cwd path on the wire**, and `docImageURL`
  requires a real cwd-relative path. These images **keep the existing ImageGallery lightbox
  unchanged** (untouched by this stream). Recorded here for the wave report's divergence ledger.

## Deliberate divergence (flagged for the reviewer to ratify)

The affordance opens the doc pane via `paneActions.openBeside({type:"doc", params})` — which is
exactly `openDocBeside`'s body (`openDocBeside` IS `openBeside({type:"doc", params})`) — rather than
importing `openDocBeside`. Reason: `panes/doc/openDoc.ts` eagerly registers the doc pane (its own
`import "./index"`), and pulling that through `ToolCallItem` forces the doc-pane registration into the
entire transcript tree at module load. That clobbers the doc-pane **test fixtures** `DockHost.test`/
`StackHost.test` register (their fixture "doc" title is robust; the real doc title
`filenameOf(params.path)` crashes on their `{ref}`-only params) — 32 pre-existing shell tests went red
until I broke the eager import. The doc pane is already registered at app boot by `AppShell.tsx:32`
(the D3 fix), and `paneActions` is already eagerly loaded by this tree (subagentModule), so the direct
call adds no new module-load side effect while staying functionally identical to the seam. `openDoc`'s
`DocParams` type is imported type-only (erased, no side effect). `panes/doc/openDoc.ts`,
`DockHost.test`, and `StackHost.test` are all off-limits, so this in-manifest routing choice is the fix.

## Status of the presweep findings

- **D1 (doc pane had no producer): CLOSED** — file cards open text/markdown/binary files and image
  files (kind:image) beside; the outputImages sub-case is the recorded DECISION-C divergence.
- **D2 (transcript pane had no producer): CLOSED** in round 1 (subagent "open transcript").
