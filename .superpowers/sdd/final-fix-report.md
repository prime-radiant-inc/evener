# Final Fix Report

## Scope

Fixed the ask response alternatives so each radio label contains only its radio
and visible label content. The conditional free-text and leaning editors are
siblings of those labels in wrapping alternative rows, with explicit
`aria-labelledby` associations. Narrow-layout CSS permits the rows and editors
to shrink or wrap without overflowing a 320px viewport while preserving the
44px mobile tap-target floor.

## RED Evidence

Before production changes:

```text
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-ask-card.js
FAIL: free option label contains exactly one labelable descendant, its radio
FAIL: free editor is a sibling of its radio label in a wrapping alternative row
FAIL: free editor is explicitly labelled by its alternative option text
FAIL: decide option label contains exactly one labelable descendant, its radio
FAIL: decide editor is a sibling of its radio label in a wrapping alternative row
FAIL: decide editor is explicitly labelled by its alternative option text

node test-mobile-css.js
FAIL: mobile alternative rows wrap and cannot impose an intrinsic minimum width
FAIL: mobile alternative option labels stay within the narrow response dock
FAIL: mobile alternative editors shrink and wrap without overflowing a 320px viewport
```

## GREEN Evidence

The focused matrix passed:

```text
test-ask-card.js: PASS
test-ask-compose.js: PASS
test-ask-submit.js: PASS
test-mobile-css.js: PASS
test-renderer-viewport-dock.js: PASS
```

`NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` completed with
`jstest: all tests passed`.

## Self-review

- Native radio containment and click behavior are unchanged; only the text
  editor moved out of the label.
- Existing data attributes and resolution/focus selectors remain stable.
- Exact payload composition, toggle-off behavior, rehydration, notes, skip,
  settlement, and viewport docking are covered by the passing ask and dock suites.
- CSS retains the mobile `--tap-min: 44px` floor while removing editor intrinsic
  width pressure with `min-width: 0`, wrapping, and responsive flex sizing.

## Commit

This report is part of the single final-fix commit. A Git commit cannot contain
its own hash without changing that hash; the resolved hash is returned with the
completion status.

## Concerns

None.
