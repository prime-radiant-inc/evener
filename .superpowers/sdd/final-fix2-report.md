# Final Fix 2 Report

## Result

DONE

## Changes

- Mobile ask mode now gives its bottommost footer fixed spacing plus
  `env(safe-area-inset-bottom)`. The normal composer remains unchanged and does
  not receive a second bottom inset.
- Ask option label IDs now use the question key and structural suffixes:
  `regular-<display index>`, `alternative-free`, and `alternative-decide`.
  Display labels and submitted answer payloads are unchanged.

## RED Evidence

- `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-mobile-css.js`
  exited 1 with `FAIL: the bottommost mobile ask footer must combine fixed
  spacing with env(safe-area-inset-bottom)`.
- `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node cmd/serf-hub/jstest/test-ask-card.js`
  exited 1 because colliding labels produced duplicate IDs and both alternative
  editor references resolved to the wrong label elements.

## GREEN Evidence

- Ask-card, ask-compose, ask-submit, mobile-css, and renderer viewport-dock
  targeted scripts passed.
- `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh cmd/serf-hub/jstest/run-all.sh`
  passed the complete JSDOM suite.
- `go test ./cmd/serf-hub/...` passed all Hub packages.
- `git diff --check` passed.

## Commit

This report is part of the single fix commit. Its immutable hash is returned in
the final task result (`git rev-parse HEAD` after commit); a commit cannot embed
its own hash because doing so changes that hash.

## Self-review

- The safe-area addition is scoped to the mobile `.ask-footer`; existing CSS
  contracts continue to prohibit bottom safe-area padding on `.workspace-input`
  and `.input-controls`, avoiding double insetting in normal composer mode.
- IDs no longer depend on model/user display text. The stable sorted display
  index preserves uniqueness for duplicate labels without changing label order,
  selection logic, or submitted values.
- The DOM regression checks both uniqueness and exact `aria-labelledby` target
  identity for the free and decide editors.

## Concerns

None.
