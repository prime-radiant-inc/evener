# Path picker redesign

**Status:** approved (Jesse, 2026-07-24)

One `PathField` widget replaces `widgets/pathpicker` and `panes/spawn/DirField`, and fills in the five surfaces that render a bare text box for a path today. `serf/dirs/complete` is renamed `serf/paths/complete` and learns to list files, which is what unblocks those surfaces.

This is the same treatment the model picker got (`2026-07-24-model-picker-redesign-design.md`): a field-shaped trigger, a list that is already expanded when the panel opens, a pre-filled input whose first keystroke replaces the value, no Cancel button, and no dismissal on page scroll. Unlike that round, this one changes the wire.

## 1. Why

Five separate implementations of "enter a path" exist:

| Surface | Today | Defects |
| --- | --- | --- |
| `widgets/pathpicker` | inline (non-portaled) popup; `Cancel` + `Use this folder` footer | clips inside scrollable ancestors; two-step browse-then-commit; closed until you type; dirs only |
| `panes/spawn/DirField` | bespoke Input + folder `IconButton` + `Popover`; recents; `../`; `Cancel` | page scroll dismisses it (default `closeOnScroll`); duplicates PathPicker's browse logic; `Cancel` footer |
| `AdvancedOptions` `path` kind | plain `Input` | no browse at all |
| `launchShared/fields.tsx` `path` kind | plain `Input` | no browse at all |
| `pathList` add rows (spawn + settings) | plain-text add field | no browse at all |

The three "no browse at all" rows are documented scope cuts from waves 7/8, and the documented reason is the wire: `completeDirs` (`cmd/serf-hub/internal/fspaths/app_paths.go:53`) hard-`continue`s on `!entry.IsDir()`, so no listing RPC can serve a `file` or `outputFile` field. The schema has five such fields — `systemPromptFile`, `systemPromptAppendFile` (`file`), `traceFile`, `cpuProfile`, `exportATIFPath` (`outputFile`) — plus one `file`-kind list, `mcpConfigs`.

## 2. Wire changes

### 2.1 Rename

`serf/dirs/complete` becomes **`serf/paths/complete`**. No alias, no deprecation shim: nothing outside this repo speaks appwire.

| File | Change |
| --- | --- |
| `appwire/types.go` | `MethodSerfDirsComplete` → `MethodSerfPathsComplete`; `DirsCompleteParams`/`DirsCompleteResponse` → `PathsCompleteParams`/`PathsCompleteResponse` |
| `appwire/client.go:403` | `(*Client).DirsComplete` → `PathsComplete` |
| `appwire/protocol.go:112` | catalog row; description becomes "Path autocompletion for a prefix." |
| `cmd/serf-hub/app_rpc.go:659` | handler registration |
| `cmd/serf-hub/internal/fspaths/app_paths.go` | `CompleteDirs`/`completeDirs` → `CompletePaths`/`completePaths` |
| `cmd/serf-hub/web_appwire_fuzz_test.go:238` | method-list entry |
| `appwire/cov_rhub_appwire_test.go:179` | client round-trip case |
| `docs/appwire-protocol.md`, `cmd/serf-hub/frontend/src/protocol/types.gen.ts` | regenerated via `go generate ./appwire` |

`SanitizeDirPrefix` (`cmd/serf-hub/internal/fspaths/paths.go:134`) keeps its name: it sanitizes a *prefix*, and the traversal rule it enforces is unchanged by this work.

### 2.2 New parameter

```go
type PathsCompleteParams struct {
	Prefix       string `json:"prefix"`
	Limit        int    `json:"limit,omitempty"`
	IncludeFiles bool   `json:"includeFiles,omitempty"`
}
```

Default `false`, so every existing caller keeps today's dirs-only behavior byte-for-byte. The `entry.IsDir()` filter becomes:

```go
if !entry.IsDir() && !params.IncludeFiles {
	continue
}
```

Everything else in `completePaths` is untouched: the `~` expansion, `SanitizeDirPrefix`, the trailing-slash-means-list-children branch, `directoryMatchScore` fuzzy ranking, the `Limit` cap, and the dotfile rule (`strings.HasPrefix(name, ".") && !strings.HasPrefix(filter, ".")`) — which now applies to files identically, so `.env` appears only once a leading dot has been typed.

### 2.3 Distinguishing files from directories

`PathsCompleteResponse.Data` stays `[]string` of absolute paths. When and only when `IncludeFiles` is true, **directory entries come back with a trailing `/`**:

```json
{"data": ["/etc/ssl/", "/etc/hosts", "/etc/passwd"]}
```

Dirs-only responses (`IncludeFiles` false) are unsuffixed, exactly as today — no existing caller or test sees a change.

Rationale: the trailing slash carries the single bit the client needs, is already the prefix protocol's own "list this directory's children" form (so a clicked directory path feeds straight back into the next `Prefix` with no reconstruction), and avoids changing the response type for every existing caller. The alternative — `[]PathEntry{Path string; IsDir bool}` — is cleaner in isolation but reshapes the response for callers that don't need it.

### 2.4 Go tests

New/updated in `cmd/serf-hub/internal/fspaths/paths_test.go`:

- dirs-only mode still excludes files, and returns directories unsuffixed
- `IncludeFiles: true` returns both files and directories
- in `IncludeFiles` mode, directories carry a trailing separator and files do not
- the dotfile rule holds for files: `.env` hidden unless the filter starts with `.`
- `Limit` still caps the combined file+dir result

`TestGeneratedFileCurrent` proves `types.gen.ts` and `docs/appwire-protocol.md` are regenerated.

## 3. `widgets/pathfield`

Replaces `widgets/pathpicker/` and `panes/spawn/DirField.tsx`, both of which are deleted along with their CSS modules and tests. Two files, mirroring the model picker's split:

- **`pathRows.ts`** — pure, DOM-free row builder plus `pickableRows`. Unit-tested directly.
- **`index.tsx`** — `PathField` (trigger + `Popover`) and `PathFieldPanel` (input + always-expanded listbox).

### 3.1 Props

```ts
export interface PathFieldProps {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  /** Decides whether files are listed and what a row click means. Default "dir". */
  kind?: "dir" | "file" | "outputFile";
  /** Injected — the widget stays wire-free, like PathPicker and DirField before it.
   *  includeFiles is derived from `kind`, never passed by the caller. */
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  /** Recent project directories. Only the spawn working-directory field passes
   *  this; a skills-directory field has no meaningful "recents". */
  listRecents?: () => Promise<string[]>;
  placeholder?: string;
  disabled?: boolean;
}
```

### 3.2 Closed state

The trigger is a form control: `display:flex`, `box-sizing:border-box`, `width:100%`, `height:32px`, one `1px solid var(--edge)` border, the value as plain monospace text (ellipsised), a `▾` chevron, and `(default)` in `--ink-low` when `value` is `""`. The whole field opens the panel — the separate folder `IconButton` is gone.

This is the same box `modelCatalog.module.css`'s `.trigger` draws. The shared declarations move into one internal CSS module that both widgets compose, rather than being copied.

`Popover` is used with `closeOnScroll={false}` (the panel's own list scrolls, and a page scroll behind it must not dismiss mid-interaction), `autoFocus={false}` (the panel input owns focus and its selection), and `stretchTrigger` (the trigger fills its field slot, lining up with sibling `Input`/`Select` rows). Because `autoFocus` is off, `FocusScope` does not restore focus, so `PathField` refocuses the trigger itself on close — the same contract `ModelCatalog` documents.

### 3.3 Open state

The panel is an ARIA 1.2 combobox-with-listbox-popup: `role="combobox"` on the input, a `role="listbox"` sibling that is always shown, and `aria-activedescendant` tracking the highlighted row. Real DOM focus never leaves the input.

The input is pre-filled with `value` and **fully selected** on mount, so the first keystroke replaces it wholesale. As in the model picker, a `typed: string | null` state distinguishes "hasn't typed yet" (show `value`, list unfiltered) from "is typing" (typed text is both the input value and the filter), including when typing is cleared back to `""`.

Row kinds, in list order:

1. **Recent projects group** — only when `listRecents` is supplied and returns entries, and only until the first keystroke (see 3.5). Each row shows the basename plus the full path as dim meta. Clicking one **commits and closes**.
2. **Current-directory group header** — the directory being listed. This *is* the "you are here" affordance; a directory row never carries a ✓.
3. **`../` parent row** — present unless the current directory is `/` or `""`. Descends to the parent.
4. **Directory rows** — folder icon, basename.
5. **File rows** — only when `kind` is `file` or `outputFile`. Document icon, basename. On a `dir`-kind field files are not listed at all, so every row in the list is a legal answer.

On a `file`/`outputFile` field the row whose basename matches `basename(value)` carries a ✓ and starts as the active descendant.

### 3.4 What a click means

Locked decision (Jesse, 2026-07-24): **the value tracks the browse position.** There is no commit button and no Cancel.

- **Directory row** → write the directory into `value` via `onChange` *and* list its children. The field always equals where you are, so nothing needs committing.
- **File row** → `onChange` and close. A file has no children, so there is no ambiguity.
- **Recent row** → `onChange` and close.
- **`../`** → same as a directory row.

Consequence, accepted: browsing mutates the field. Open the panel, descend twice, press Escape, and the field holds the last directory you looked at rather than what you started with. Escape and click-away behave identically (both simply close), which is the model picker's contract too.

Where the panel opens:

- `kind: "dir"` — lists the children of `value` (falling back to the last-working-directory global on the spawn field, then `""`, which the hub resolves to `$HOME`).
- `kind: "file" | "outputFile"` — lists the children of `dirname(value)` with `includeFiles: true`.

`outputFile` behaves exactly like `file`. The file being named may not exist yet, so typing is the expected path and existing files are pickable references; whether the parent directory is writable stays `serf/path/validate`'s job at submit time.

### 3.5 Typing

Typing filters by the last path component and re-lists when the directory part changes, keeping today's behavior: a 150 ms debounce, and a monotonic request id so a completion response older than the newest request is dropped (a stale response never overwrites fresher entries). Every keystroke calls `onChange` — the field is a plain controlled input.

The first keystroke permanently drops the Recent projects group for that panel's lifetime, as `DirField` does today.

### 3.6 Keyboard

`ArrowDown`/`ArrowUp`/`Home`/`End` move `aria-activedescendant` over pickable rows without moving DOM focus. `Enter` on an active row does exactly what clicking it does (descend for a directory, commit for a file or recent). `Enter` with nothing active commits the typed literal path — `DirField`'s current behavior, which matters for `outputFile` fields naming a file that does not exist. `Escape` closes; `Popover`'s own handler already calls `preventDefault`/`stopPropagation`, so the panel needs no Escape handler of its own.

### 3.7 Last-working-directory global

`spawnDefaults`'s `setGlobalLastWorkingDir` currently fires on commit. Since browsing now writes the value continuously, it would fire on every browse step; instead it stamps once, on panel **close**.

## 4. Call sites

Seven conversions:

| Site | Today | After |
| --- | --- | --- |
| `panes/spawn/Spawn.tsx:417` working directory | `DirField` | `PathField kind="dir"` + `listRecents` |
| `panes/spawn/AdvancedOptions.tsx` `path` kind (default branch) | plain `Input` | `PathField`, `kind` from `option.pathKind` |
| `panes/spawn/AdvancedOptions.tsx` `pathList` (`ListControl`) | plain-text add row | `PathField` via `CollectionEditor`'s `renderAddField` |
| `panes/settings/sections/launchShared/fields.tsx` `path` kind | plain `Input` | `PathField`, `kind` from `option.pathKind` |
| `panes/settings/sections/launchShared/collectionFields.tsx` `PathListField` | plain-text add row | `PathField` via `renderAddField` |
| `panes/settings/sections/dirListSetting.tsx` `PathListEditor` | `PathPicker` | `PathField`; new `kind` prop so MCP config files get `kind="file"` |
| `panes/settings/sections/marketplacesPlugins/MarketplacesSection.tsx:213` | `PathPicker` | `PathField kind="dir"` |

`PathListEditor` is instantiated twice — `DirListSetting` (`skillsDirs`/`pluginDirs`, dir) and `mcp.tsx` (`mcpConfigs`, file) — so it grows a `kind` prop it forwards. Both `pathList` add rows follow the `ModelListField` pattern: `renderAddField` renders the picker plus `CollectionEditor`'s own submit `Button`, and the picker's portaled panel sits outside the add `<form>`, so Enter inside the panel picks rather than submitting.

Server-side validation is unchanged everywhere: `serf/path/validate` still gates every `pathList` add and every scalar `path` field at submit time.

`stores/extensions.ts`'s `listDirChildren(path)` becomes `completePaths(prefix, includeFiles)` — the trailing-slash normalization moves into the widget, which needs to control the prefix directly for typed-prefix filtering.

Deletions and doc corrections:

- `widgets/pathpicker/` (index, module, test) and `panes/spawn/DirField.tsx` (+ module, + test)
- `dev/gallery-sections/pathpicker.tsx` → a `pathfield` section covering all three kinds
- the wave-7/8 scope-cut comments at the top of `launchShared/fields.tsx` (lines 13–24) and `panes/settings/sections/mcp.tsx` (lines 11–18), both of which document a limitation this removes
- `widgets/formrow/index.tsx:16`'s comment naming `PathPicker`

Not in scope: **`widgets/combobox` will have zero production consumers** once `PathPicker` is gone (the model picker moved off it in the previous round; only the gallery imports it now). Deleting it is a separate decision, flagged here rather than taken.

## 5. Error handling

`complete` rejections degrade to an empty list, never a thrown error, matching both widgets today: the widget has no RPC knowledge, and a permissions failure or transient blip must not crash a form. An empty result renders one status line, "Nothing here.", for every kind — today's two variants ("No subdirectories." in `PathPicker`, "No directories here." in `DirField`) collapse into it, since with files included the list is no longer directory-specific. `listRecents` rejecting degrades silently to no Recent group (an older hub without the RPC). The hub itself already returns an empty result rather than an error for an unreadable or unsanitizable prefix, so a bad path and an empty directory are indistinguishable to the widget by design.

## 6. Testing

**`pathRows.test.ts`** — pure row building: group/parent/dir/file row shapes; `../` suppressed at `/`; trailing-slash entries classified as directories and unsuffixed ones as files; files absent for `kind: "dir"`; the ✓ lands on the row matching `basename(value)` for file kinds; `pickableRows` skips group headers.

**`pathfield.test.tsx`** (jsdom) — input pre-filled and fully selected on open (`selectionStart`/`selectionEnd`); a directory click writes the value *and* re-lists; a file click commits and closes; `Enter` with nothing active commits the typed literal; a page scroll does **not** dismiss; focus returns to the trigger on close; the Recent group disappears on the first keystroke; a stale completion resolving after a newer one is dropped; a rejected `complete` renders the empty state rather than throwing.

**Call-site tests** — updated at all seven sites: `Spawn.test.tsx`, `AdvancedOptions.test.tsx`, `fields.test.tsx` (the "path kind renders as a free-text input" test is inverted — it asserted the limitation being removed), `collectionFields.test.tsx`, `dirListSetting`/`mcp` tests, `MarketplacesSection` tests, `extensions.test.ts` (three `serf/dirs/complete` cases renamed and extended for `includeFiles`).

**Gates** — `make test-web` (tsc → vitest → biome), `make build-web`, `make test` for the Go side, `make lint`. Then live verification against a real hub across every converted surface, in both themes and at 390 px, the way the model-picker round was verified — fixtures did not catch three of that round's real defects.

## 7. Constraints

- Design tokens only. `src/styles/tokens.css` is the sole color source; `token-contract.test.ts` fails on any hex/rgb literal elsewhere, comments included.
- `requireClass(styles.x, "<file>.module.css", "x")` module-scope `CLASS` tables; never `styles.x` in render.
- TypeScript strict with `noUncheckedIndexedAccess`.
- No backward-compatibility shims (the wire rename is a clean break).
- TDD per change; commit frequently; pre-commit hooks always run.
