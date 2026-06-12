# Job-Control E2E Coverage Report — MATRIX GREEN (14/14)

Date: 2026-06-12. Branch `job-control-spec`; code under test `0c22499d`; cards at `5d0af135`. Live model `openai/gpt-5.5` (OAuth instance), hub-served per `docs/agentic-testing.md`. Evidence: the per-card ledger, committed verbatim at `docs/superpowers/research/2026-06-12-e2e-ledger.md` (477 lines, session IDs and verbatim tool results throughout). Three executor runs: run 1 (cards 1-4; died at a usage limit), run 3 (cards 5-14 + card-1 re-run), closeout (amended cards 9+12 re-run).

## The matrix

| # | Card | Verdict | Notes |
|---|------|---------|-------|
| 1 | job-shell-lifecycle | **PASS** (re-run, all 6 arms) | Run-1 PARTIAL: two arms blocked by the strict-mode bug; fixed `0c22499d`, re-verified natively |
| 2 | job-read-output-blocking-grep | **PASS** | Mid-stream match, entry check, timeout arm, non-consuming reads |
| 3 | job-list-and-recovery | **PASS** | Filters, ordering, recovery re-orientation; delegate background native |
| 4 | job-stop-and-children | **PASS** | Stop confirm, retention after stop, include_children over nested |
| 5 | job-notification-semantics | **PASS** | pending/delivered lifecycle, ephemerality, shell+delegate flavors |
| 6 | job-notification-wake | **PASS** | Idle wake via server notify; terminal notification format |
| 7 | job-watch-output-match-catchup | **PASS** | Level-trigger attach scan; terminal catch-up both arms; events-on-terminal still `target_terminal` |
| 8 | job-watch-caller-notification-delivery | **PASS** | Render-by-key tokens; coalescing 3 fires → 1 latest frame; notify+send flavors coexist |
| 9 | job-watch-caller-send-no-deadlock | **PASS** (amended re-run 9R) | Both deadly configs rejected at create; tool-heavy turn completed with all TOOL_RESULTS persisted (the incident's lost artifact); observer received frames ×5 with matching delivery_ids |
| 10 | job-watch-sidecar-observer | **PASS** | Frame → session-keyed grant read (incl. after resume) → caller comment, delivery_id chained end to end; watch_send pending→delivered lifecycle |
| 11 | job-delegate-result-schema | **PASS** | Valid arm + inheritance-across-resume live; invalid arm inconclusive-by-design (provider strict enforcement masked it — serf's validator is the unit-tested backstop beneath) |
| 12 | job-send-message-surface | **PASS** (amended re-run 12R) | Live steer landed (`action:"sent"`, marker in child transcript pre-communicate); resume/`on_finished:"fail"`/foreground_timeout arms all green |
| 13 | job-nested-visibility | **PASS** | Hidden-by-default, cross-store read, routed stop, post-terminal retained read |
| 14 | job-restart-durability | **PASS** | kill -9, reconciliation `stopped/runtime_lost` with stable `terminal_generation`, pre-crash output readable, exactly-once notification, second-restart dedupe |

## Findings

- **One product bug, found → fixed → re-verified within the loop:** OpenAI's Responses path sends tools `strict=true`, forcing every parameter onto every call; the presence-based `background`+`block_timeout_ms` rejection made background jobs uncreatable on that wire. Fixed in `0c22499d` (zero reads as unset, all three arms, red-first tests, contract rows); card-1 re-run green natively. Three pre-live review layers (two /par rounds, a Haiku comprehension gate) validated intent-level behavior and structurally could not see this wire-level failure — the live loop is the only layer that could.
- **Two card defects, amended `5d0af135`:** card 9 pinned its watch arguments (a driver model freestyled `every:1`; validation correctly rejected it) and dropped a deleted parameter from its prompts; card 12's arm (a) now accepts both legal outcomes of its inherent steer-vs-finish race with delivery as the invariant.
- **Defense-in-depth observed live (card 12R):** a driver model hit the positive-timeout rejection and self-corrected from the error text alone — the teaching-error design functioning.

## Not live-coverable (documented, with the covering layer)

Per the card-authoring coverage analysis: shell approval flows (no approval policy in this harness config); `stop_unconfirmed`/`supervision_lost` (need unconfirmable-kill races); retention-pruned reads (need aged state); capacity caps (unit territory); the delegate-side invalid-`result_schema` arm on strict providers (provider enforces above serf's validator; the validator is unit-tested, contract `:294`); library-mode no-notify degradation (cards require serve mode). F1/F2/F4 supervision features are unit-covered; their live exercise rides future runs.

## Punch list harvested from live behavior (queued, non-blocking)

1. Watch-frame `trigger` renders the internal event kind (`TOOL_CALL_END`) instead of the model-facing name (`assistant.tool`) — frames should speak the vocabulary agents know.
2. `every:1` with multiple kinds is rejected though it is semantically the default — treat as no-op; reserve the single-kind rule for `every>1`.
3. `job_list` shows `output_bytes: 0` for running jobs (populated at finish) — surface live retained bytes or document.
4. Per-fire observer economics: an idle observer is resumed per fire (coalescing applies only while busy) — correct per contract; worth one teaching sentence in the background-jobs section.

## Verdict

The job-control contract's live surface — shell and delegate jobs, reads, stops, lists, notifications, wakes, watches in all three trigger modes, caller and sidecar delivery, grants, structured results, message surface, nested visibility, and crash durability — is verified green on the shipped binaries, end to end, under the directed model. Goal condition: **e2e green with coverage matrix — met.**
