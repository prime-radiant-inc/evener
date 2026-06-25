# Task 1 Report: Hover/focus reveal for tool timing metadata

## Status
DONE_WITH_CONCERNS

## Summary
Implemented CSS-only hover/focus reveal behavior for transcript tool timing metadata.

- `cmd/serf-hub/assets/style.css`
  - `.tool-call .tool-meta` now remains in the DOM but is visually hidden by default with `opacity: 0` and `visibility: hidden`.
  - `.tool-call:hover .tool-meta` and `.tool-call:focus-within .tool-meta` reveal timing metadata with `opacity: 1` and `visibility: visible`.
  - Preserved existing layout/typography declarations and added the required transition.
- `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`
  - Added the required CSS contract assertions for hidden-by-default, hover reveal, and focus-within reveal behavior.
  - Updated `ruleContains` so the test helper recognizes selectors in comma-separated selector lists. This was necessary because the required CSS implementation uses a combined hover/focus selector list.

## TDD Evidence

### Failing test before implementation
Command:

```bash
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Output:

```text
FAIL: tool timing metadata should be visually hidden by default
FAIL: tool timing metadata should reveal on row hover
FAIL: tool timing metadata should reveal on keyboard focus within the row
[exit 1]
```

### Passing CSS contract test after implementation
Command:

```bash
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Output:

```text
PASS: pane compact and full-border sidebar resize CSS contracts
[exit 0]
```

### Hub package tests
Command:

```bash
go test ./cmd/serf-hub -count=1
```

Output:

```text
ok  	primeradiant.com/serf/cmd/serf-hub	4.861s
[exit 0]
```

## Diff Scope
Reviewed with:

```bash
git diff -- cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Changed files:

```text
cmd/serf-hub/assets/style.css                    | 16 +++++++++++++-
cmd/serf-hub/jstest/test-pane-and-sidebar-css.js | 28 ++++++++++++++++++++----
2 files changed, 39 insertions(+), 5 deletions(-)
```

The implementation commit stages only:

- `cmd/serf-hub/assets/style.css`
- `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`

## Concerns
The `superpowers:test-driven-development` skill could not be invoked through a native skill-loading tool because `use_skill` is unavailable in this session. I followed the required TDD workflow from the task brief directly: write failing test, observe failure, implement, observe pass, run package tests, review diff.

I made one small test-helper change beyond inserting the exact assertions: `ruleContains` now supports comma-separated selector lists. Without that, the required verbatim combined CSS selector rule would not satisfy the hover assertion even though the CSS behavior was correct.

---

# Final-review Important finding fix: accessible hidden tool timing metadata

## Status
DONE

## Summary
Fixed the final-review accessibility finding by changing transcript tool timing metadata from `visibility: hidden` hiding to opacity-only visual hiding. The metadata remains in the DOM and is not hidden with `visibility: hidden`, so it remains available to assistive technology while staying visually quiet until row hover or `:focus-within`.

Changed files:

- `cmd/serf-hub/assets/style.css`
  - `.tool-call .tool-meta` now uses `opacity: 0` with `transition: opacity var(--motion-fast)`.
  - Removed `visibility: hidden` from the default state.
  - Removed `visibility: visible` from reveal state.
  - Hover/focus reveal remains CSS-only through `.tool-call:hover .tool-meta` and `.tool-call:focus-within .tool-meta` with `opacity: 1`.
- `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`
  - Updated CSS contract to require default `opacity: 0`.
  - Updated CSS contract to assert the default rule does not contain `visibility: hidden`.
  - Updated hover/focus reveal assertions to require `opacity: 1` without requiring visibility toggles.
- `docs/superpowers/specs/2026-06-25-hover-only-turn-timing-metadata-design.md`
  - Updated design/accessibility text to require opacity-only visual hiding and avoid hiding that removes metadata from assistive technology.
- `docs/superpowers/plans/2026-06-25-hover-only-turn-timing-metadata.md`
  - Updated implementation plan and deterministic CSS contract examples to remove the `visibility:hidden`/assistive-technology contradiction.

## TDD Evidence

### Failing test before implementation
Command:

```bash
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Output:

```text
FAIL: tool timing metadata should be visually hidden by default without visibility:hidden
[exit 1]
```

### Passing CSS contract test after implementation
Command:

```bash
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Output:

```text
PASS: pane compact and full-border sidebar resize CSS contracts
[exit 0]
```

### Hub package tests
Command:

```bash
go test ./cmd/serf-hub -count=1
```

Output:

```text
ok  	primeradiant.com/serf/cmd/serf-hub	4.620s
[exit 0]
```

## Additional checks

Command:

```bash
rg -n "visibility: hidden|visibility:hidden|assistive technology|opacity" docs/superpowers -g '*.md'
```

Relevant output confirmed the hover-only timing metadata spec/plan now describe `opacity: 0`, explicitly prohibit `visibility: hidden`, and state metadata remains available to assistive technology.

Command:

```bash
rg -n "tool-meta|visibility" cmd/serf-hub/assets/style.css
```

Relevant output confirmed the `.tool-call .tool-meta` rules no longer include `visibility` declarations.

## Concerns
None.
