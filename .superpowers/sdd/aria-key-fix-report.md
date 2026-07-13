# ARIA key collision fix report

## Root cause

`buildAskQuestionEl` derived header, question text, option label, and note label
IDs from a sanitized `item.key`. Distinct keys such as `call:a:0` and
`call_a:0` therefore produced identical IDs, causing the second question's
`aria-labelledby` references to resolve to elements in the first question.

## TDD evidence

Added `testQuestionIDsRemainUniqueAcrossCollidingKeys` to
`cmd/serf-hub/jstest/test-ask-card.js`. It renders two acknowledged calls whose
keys sanitize identically and verifies:

- every ID in the response dock is globally unique;
- every `aria-labelledby` token resolves inside its owning question; and
- free/decide editors reference their own alternative label.

Before the renderer change, the focused test failed on duplicate IDs and all
second-question ownership checks. After the change, it passed.

## Implementation

Question-local IDs and the radio group name now use the stable global display
number passed as `num`. `data-ask-key` remains unchanged and continues to own
state identity and focus restoration.

## Verification

- `test-ask-card.js`: pass
- `test-ask-compose.js`: pass
- `test-ask-submit.js`: pass
- `test-mobile-css.js`: pass
- `test-renderer-viewport-dock.js`: pass
- full `cmd/serf-hub/jstest/run-all.sh`: pass
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/... -count=1`: pass
