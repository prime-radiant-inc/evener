# E2E re-run addendum — post-punch-list matrix on `5469dd63`

**Date:** 2026-06-12 (evening run) · **Base:** `5469dd63` (six commits past the
14/14 run at `0c22499d` recorded in `2026-06-12-e2e-coverage-report.md`) ·
**Model:** `openai/gpt-5.5` (OAuth `openai` instance) · **Runner:** sonnet
subagent per `docs/agentic-testing.md`, sequential, hermetic workdirs ·
**Ledger:** `2026-06-12-e2e-rerun-ledger.md` (verbatim, including the
orchestrator-driven card-1 retry).

## Why this run existed

Cards re-run because six commits landed after the green matrix: `every:1`
reads-as-unset, live `output_bytes`, hooks `CLAUDE_EFFORT` strip, nested-read
test deflake, finalize enqueue-before-closeDone, and the docs/goal note. Three
of those change surfaces the cards exercise; the finalize-ordering fix sits on
the notification path every card crosses.

## Result

**14/14 green.** 11 PASS outright. The four non-PASS verdicts in the runner's
first pass were each root-caused from primary artifacts (transcripts, card
texts, source, git history) — none was a regression from the six commits:

| Card | First verdict | Root cause | Disposition |
|---|---|---|---|
| 1 shell-lifecycle | FAIL | Runner's spawn prompt repositioned the card's command quoting; spawned model and serf both faithful (output byte-identical to the mangled command's semantics) | Orchestrator retry with verbatim quoting: **PASS**, all arms |
| 4 notification-wake | PARTIAL | Card letter (`job_read_output` required) predates terminal-notification result excerpts (`75c11569`); model surfaced exact content from the excerpt — designed, optimal | Card amended `d4bc036b` |
| 6 delegate-result-schema | PARTIAL | Arm (b)'s capture-time invalid signature is masked **by serf's own call-time args-schema validation** (`agent/internal/tool/registry.go` rejects the invalid call; child retried valid). The prior coverage report attributed this masking to provider strict enforcement — **correction: the registry gate is the layer that fired in this run**; provider enforcement is a second potential layer above it. Capture-time triplet remains unit-covered (`agent/job_delegate_test.go`) | Card sharp-edges amended `d4bc036b` |
| 8 / 10 watch cards | PASS-WITH-NOTE | `fired`/`replaced_existing` serialized absent-when-false, contradicting contract §7.1 ("fired=false on none") and the install example; three unit tests pinned the omission shape against the contract. Separately, card 8 asserted no between-tool-rounds delivery, contradicting the contract's Delivery modes boundary list | Product fixed + pins corrected + card 8 amended: `5c376c95` |

## Found-and-fixed during this campaign tail

- `fired` / `replaced_existing` now serialize explicitly (contract-true shape),
  with the always-present rule stated beside the contract example (`5c376c95`).

## Punch items harvested (not fixed here)

1. The terminal-notification template says "Use `job_read_output` to inspect
   output." while carrying a complete result excerpt for small outputs —
   for fully-excerpted results the instruction nudges a wasted tool call
   (gpt-5.5 ignored it; weaker models may not). Candidate: condition that
   sentence on excerpt truncation. Tracked on PRI-2206.

## Coverage notes

Same not-live-coverable surface as the prior report, with one correction
folded above (card 6 masking-layer attribution). The card-1 quoting failure
mode is a runner-procedure hazard, not product surface: when driving cards by
prompt, command text must be transmitted verbatim — the gotcha is now noted in
the retry section of the ledger.
