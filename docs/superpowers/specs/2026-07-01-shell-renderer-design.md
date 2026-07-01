# Shell and Tool Renderer Design

**Date:** 2026-07-01
**Scope:** `cmd/serf-hub` web transcript renderer (`assets/renderer.js`, `assets/renderer-tools.js`, `assets/style.css`, renderer JSDOM tests)
**Status:** Approved design; implementation plan pending.

## Goal

Make shell/bash tool calls readable and intentional while improving the expandable-tool affordance across all visible tools. The shell renderer should feel like a first-class command transcript, not a generic tool row with a clipped command. The broader tool renderer should use standardized components so shell does not become a one-off widget.

The redesign must keep Serf's existing transcript tone: quiet annotation tier, low chrome, clear hierarchy, and honest output states.

## Problems to solve

Current shell/tool rendering has several readability and interaction issues:

- Shell rows are labeled as `shell`/`exec_command`, which reads like an internal tool name rather than a prompt.
- The disclosure caret is hard to notice and hard to click.
- The disclosure is visually separated from the command/action it expands.
- Tool metadata is hover-revealed and competes awkwardly with the disclosure.
- Expanding a shell row shows output, but not a complete, nicely rendered command transcript.
- Tool bodies use related but ad hoc structures; shell improvements should not further fragment the renderer.

## Design principles

1. **The action leads.** The left side of a row answers "what happened." Metadata stays secondary.
2. **Disclosure belongs to the action.** The chevron is inline at the end of the thing it expands, not in a separate column.
3. **Subtle is not invisible.** The affordance should be quiet but always discoverable, with a reliable hit target and keyboard focus state.
4. **Shell reads as shell.** Shell-like rows use `$` and expanded bodies render a command transcript.
5. **Standardize the component contract.** Every visible expandable tool uses the same row/body mechanics; individual tools only specialize body content.
6. **Keep honest output semantics.** Client-collapsed output, server-truncated output, binary output, and empty output remain distinct.

## Approved row anatomy

Every visible tool row uses a shared header contract.

Without an agent purpose:

```text
$ git status --short  ▸                         10:23:53 AM · 1ms
```

With an agent purpose:

```text
Verify repository status before merging.        10:23:53 AM · 1ms
$ git status --short  ▸
```

Behavior:

- The primary action stays left.
- If the tool has an agent purpose, the purpose remains the prominent sans line.
- If the tool has a demoted command/detail line, the inline disclosure appears at the end of that command/detail text.
- If there is no purpose, the inline disclosure appears at the end of the main tool action text.
- Time/runtime metadata stays right-aligned and readable.
- The disclosure control is subtle but visible: muted chevron, larger clickable area, hover/focus styling, keyboard activation.
- Rows with no meaningful expandable body do not render a disclosure.
- Failed/error rows still surface attention through existing error status treatment.

## Shared component structure

The implementation should converge visible tool rows toward this shared structure:

```text
.tool-call
  .tool-main          // primary action row: purpose or verb/target/result
  .tool-command       // secondary mono row when purpose exists
  .tool-disclosure    // inline chevron button at end of action text
  .tool-meta          // right-aligned time/runtime metadata
  .tool-body          // standardized expandable body container
```

The exact DOM can preserve compatibility with existing class names where useful, but the conceptual contract should be explicit in code and tests.

Shared disclosure behavior:

- One button class/attribute path for all expandable tools.
- `aria-expanded` is kept in sync with `data-expanded`.
- `aria-label` distinguishes expand/collapse.
- Enter and Space toggle the disclosure.
- No disclosure is rendered after finalization if the body is empty or hidden.
- The hit target is larger than the glyph without creating a visible heavy button.

Shared body behavior:

- One standardized body container owns margins, left rule/rail, spacing, collapse behavior, and focus/accessibility styling.
- Body variants specialize content, not outer mechanics.
- Existing output helpers (`setExpandableOutput`, `setExpandableDiff`) remain the source of truth for line folding and truncation honesty.

## Body variants

### Terminal body (`.tool-body--terminal`)

Used by `shell`, `exec_command`, and `run_shell_command`.

Expanded shape:

```text
╭─ shell
│ $ git status --short
│  M cmd/serf-hub/assets/renderer-tools.js
│  M cmd/serf-hub/assets/style.css
╰─ exit 0 · 1ms
```

Requirements:

- The row identity is `$`, not `shell` or `exec_command`.
- The full command is repeated inside the expanded body, untruncated, after a `$` prompt.
- Output uses `pre-wrap` so long lines remain readable in panes and on mobile.
- Empty successful output may still expand when the command itself is useful; the body can show the command and quiet footer.
- Nonzero exits or tool errors open by default and emphasize the footer/status rather than tinting the whole block.
- Long output preserves the existing "first 5 lines + expand N more" behavior inside the terminal body.
- Server-truncated or binary output uses the existing dropped-output note semantics.

### Preview body (`.tool-body--preview`)

Used by read/search/list/job/fetch-like renderers:

- `read_file`
- `grep_files`, `grep`, `grep_search`
- `list_dir`, `list_directory`, `glob`
- `web_fetch`, `web_search`
- `job_read_output`
- `delegate_send`, `job_send_message`
- `job_list`
- `use_skill` when it has activation text

Requirements:

- Reuse the standardized body frame.
- Preserve existing output preview and expandable-output behavior.
- Preserve argument previews where existing tools intentionally show them, such as grep/list details.
- Keep source-truncation and binary-output messaging honest.

### Diff body (`.tool-body--diff`)

Used by:

- `edit_file`
- `write_file`
- `apply_patch`

Requirements:

- Reuse the standardized body frame.
- Preserve diff-specific coloring and per-line classes.
- Keep diff bodies collapsed or expanded according to each renderer's existing default.
- `apply_patch` continues to preview the patch content rather than the command stdout.

### List/structured bodies (`.tool-body--list`)

Used where a renderer has real structured rows rather than raw pre text.

Requirements:

- Reuse the shared body spacing and collapse mechanics.
- Do not force structured content into terminal/pre styling.

## Coverage across tool categories

### Cheap clustered tools

Current cheap-cluster behavior remains:

- `read_file`, `grep_files`, `list_dir`, and `glob` can group into `.tool-call-cluster`.
- `job_stop` stays a cheap row.
- A completed multi-tool cheap cluster can collapse to a summary.

Changes:

- Cluster summary disclosure should use the same subtle disclosure language.
- Individual rows inside an expanded cluster use the shared row/header contract.
- Compact-pane behavior remains intact.

### Preview/output tools

`job_read_output`, `delegate_send` / `job_send_message`, `job_list`, `web_fetch`, `web_search`, and `use_skill` adopt the shared row/body mechanics without changing their result semantics.

### Diff tools

Diff tools keep their diff-specific rendering and default expansion choices but move into the shared disclosure/body contract.

### Shell tools

Shell-like tools are the only tools with terminal body treatment and `$` row identity.

### Suppressed or aggregated tools

These retain existing special behavior and do not become normal tool cards:

- `communicate` renders assistant output.
- `task_list` renders task update cards/system lines.
- `delegate` contributes to the subagent module and removes its own row after spawn.

## Accessibility and interaction

- Disclosure buttons are real `<button>` elements.
- `aria-expanded` and `aria-label` update on toggle.
- Keyboard activation uses Enter and Space.
- Focus-visible styles are clear but not loud.
- The button's clickable area is larger than the chevron glyph.
- The disclosure is visible without hover, but hover/focus increases contrast.
- Metadata remains readable; it should not be hover-only if it is part of the row's timing context.

## Mobile and compact panes

- Shell output uses `pre-wrap`, not horizontal scrolling as the default.
- Long commands in collapsed rows may still truncate or wrap according to existing hierarchy, but the expanded terminal body always shows the full command.
- The shared row should avoid margin-induced horizontal overflow. Existing mobile safeguards around `.tool-call.has-purpose .tool-command` should be preserved or replaced with equivalent padding-safe layout.
- In compact panes, metadata may be reduced if needed, but the action and disclosure remain discoverable.

## Implementation notes

Likely files:

- `cmd/serf-hub/assets/renderer.js`
  - Tool row construction, disclosure placement, metadata placement, empty-body cleanup.
- `cmd/serf-hub/assets/renderer-tools.js`
  - Shell renderer `$` identity and terminal body creation.
  - Body variant classes for preview/diff/terminal/list.
- `cmd/serf-hub/assets/renderer-panels.js`
  - Existing delegated expand/collapse behavior; may need class/ARIA updates.
- `cmd/serf-hub/assets/style.css`
  - Shared row anatomy, inline disclosure, standardized body containers, terminal body styling, mobile adjustments.
- `cmd/serf-hub/jstest/test-tool-renderers.js`
  - Main renderer contract tests.
- Existing disclosure/mobile/pane tests may need updates if they assert the old right-column caret or hover-only metadata.

## Testing strategy

Add or update deterministic JSDOM/CSS tests for:

1. Shell collapsed row uses `$`, not `shell`.
2. Disclosure appears inline with the action/command text for expandable rows.
3. Disclosure is keyboard accessible and updates `aria-expanded`.
4. Metadata remains right-side timing context and does not displace the action.
5. Expanding shell shows the full untruncated `$ command`.
6. Shell output uses pre-wrapped output styling.
7. Shell footer shows exit/runtime, with nonzero exit emphasized.
8. Read/grep/list/job preview bodies still render through standardized body containers.
9. Diff bodies still render diff classes and patch previews correctly.
10. No disclosure appears for rows whose finalized body is empty or intentionally hidden.
11. Cheap clusters still group, summarize, and expand correctly.
12. `communicate`, `task_list`, and `delegate` special suppression/aggregation behavior remains unchanged.

Run at minimum:

```sh
cd cmd/serf-hub && ./jstest/run-all.sh
```

If Go server/template behavior changes, also run the relevant Go tests under `cmd/serf-hub`.

## Non-goals

- Do not redesign the whole transcript visual system.
- Do not add copy buttons in the first pass unless implementation reveals they are necessary for the component contract.
- Do not change backend event shapes.
- Do not change tool execution semantics or output capture.
- Do not turn `communicate`, `task_list`, or `delegate` into ordinary visible tool cards.

## Open implementation decisions

These should be resolved in the implementation plan, not by changing the product direction:

- Whether to introduce new wrapper elements (`.tool-main`) immediately or adapt existing `.tool-intent` / `.tool-command` classes with minimal DOM churn.
- Whether terminal body rails use box-drawing characters literally or achieve the same feel with CSS borders. CSS is likely more robust, but the visual intent is a compact Serf terminal transcript.
- How much metadata remains visible in very narrow mobile panes.
