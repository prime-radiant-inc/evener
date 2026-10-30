# Issue #4362: user evidence in bounded transcript ranges

## Status and scope

Frozen for independent PAR review before implementation. The issue request authorizes choosing the elision rule from the current code and delivering it without a further interactive approval step. After implementation review exposed non-monotone subset-packing failures, the parent explicitly directed the floor amendment below: stop optimizing subsets and guarantee complete evidence or an exact omission address.

Issue: https://github.com/prime-radiant-inc/evener/issues/4362.
Base: `origin/main` at `48a6703dc790cd665bb574c6a411afe6ea0796d2`, merged into the isolated `issue-4362-range-user-turns` lane before this specification.

Change only markdown `read_transcript` range selection/elision, its tests, and this specification. The owning tool contract is `docs/tools/transcripts.md`, with history ownership in product subsystem S10. The task's explicit file boundary overrides the general instruction to update other product documents.

## Problem and evidence

The issue records a memory-librarian read of range `27-40` that omitted user turn 30 and led the reader to infer that the human partner had said nothing in the range. The linked eval session returns HTTP 403, and no matching eval report is present in the tracked repository. Deterministic fixtures, not an invented replay of that session, establish the regression.

`agent/transcript_render.go` currently selects a contiguous window. `budgetedEnd` drops newest turns for `start:N` and `N-M`; `budgetedStart` drops oldest turns for defaults and `last:N`. Neither consults turn kind. The counts include turns outside the requested window: `turns_rendered + elided_turns == turns_total` for the session, not merely for the request. Tool-result bodies are already condensed before this selection.

## Required behavior

A markdown range response must not silently hide a `USER_INPUT` entry in the effective requested range. This includes default and tail-anchored ranges, entry removal, and partial removal by a preview or final cap. A user turn outside the requested range is not pulled into it. Complete user evidence means the existing markdown text projection, not a new rendering of multimodal attachments.

Preserve the current result exactly when current selection and size bounds do not threaten a requested user turn. In particular, assistant-only ranges, fitting ranges, range grammar/fallback, public turn numbering, tool-result pairing, and expansion addressing keep their existing behavior.

When the current budget selection would omit a requested user turn, use user-prioritized selection:

1. Remove non-user turns before any user turn. Existing tool-result condensation remains the first reduction of tool output. Whole assistant/tool turns are the next material to shed; this change does not invent assistant-body summaries.
2. Reserve exact omission accounting, then consider requested user turns once in the existing anchor order. Render a complete user body when that candidate response fits the existing conversation budget, including the exact addresses of all still-omitted users. Otherwise leave its exact omission entry and continue to later users. Do not remove a retained user to fit assistant/tool content. Do not revisit earlier decisions to maximize packing or find a lexicographically preferred subset: omission overhead makes that a separate, non-monotone optimization problem. A single oversized user does not stop consideration of short users.
3. Preserve real turn numbers and chronological order. Mark every gap, not a fictitious contiguous span. Results pair by call ID within the selected entries, never with an entry that has been removed.
4. Keep the existing conversation, hard-cap and registry limits and their units: conversation selection measures bytes, the content hard cap measures runes, and the encoded envelope cap measures bytes. Omission notices count toward the prioritized response's conversation budget. Do not increase any limit.

`USER_INPUT` is the existing semantic user kind. Steering, automatic environment/memory context, summaries, and assistant messages are not reclassified as human user turns in this change.

## When even the user turns cannot fit

Before any bulky content, name exactly the requested user turns whose complete bodies were omitted. Use their universal numeric addresses, compacting only consecutive addresses if useful. Each named turn is recoverable through `expand_turn:N`; a narrower `range` remains another recovery route. Do not present an incomplete user body as complete evidence.

If the exact all-user omission accounting alone cannot fit, return an explicit range-too-large error that asks for a narrower range, rather than search for a fitting subset. Never silently truncate the list of omitted user turns. If a final hard/encoded-envelope cap would remove protected user evidence or its omission accounting, likewise return an explicit narrowing error rather than a misleading successful response. This is an exceptional recovery path, not a larger budget or new wire field.

The registered markdown handoff must also prevent generic registry clipping under a configured smaller character or line limit. Use the existing complete-output delivery contract so that such a limit produces a typed delivery error, not successful partial JSON. Default-limit responses remain unchanged; no registry implementation or configuration changes belong in this task.

Explicit `expand_turn` remains exact byte-paged evidence. Its pinned assistant/result span may be supplemental to the budgeted range, as today; prioritize or name the requested user evidence independently of that supplemental span. Do not change exact expansion bytes, page sizes, encodings, or continuation fields.

## Output contract

- Keep the existing JSON envelope and metadata field names. No new wire/storage fields.
- `meta.range` still describes the effective requested range.
- `meta.turns_total` remains the session's public entry count.
- `meta.turns_rendered` counts selected in-range entries, including tool-result entries folded into their shown calls. It does not count whole omitted user turns or supplemental out-of-range expansion spans.
- `meta.elided_turns = meta.turns_total - meta.turns_rendered`, preserving existing session-wide accounting.
- `meta.truncated` is true whenever budget elision occurs, including omitted user bodies.
- Content names every in-range omitted user address explicitly. Entries merely outside the requested range are not described as budget-elided users.
- For sparse selection, the window announcement reports a selected count and requested window, not an unbroken first-to-last span. Gap notices identify their real omitted spans and recovery ranges.
- With no user at risk, preserve existing selection, counts, markers, and announcements.

## Non-goals

No transcript writer, storage, schema, RPC or tool-definition changes. No raw JSONL or outline-format policy changes. No optimal subset packing or promise that every globally feasible user combination will be found. No new search operation, model-behavior eval, provider request, budget increase, new GitHub labels, or unrelated cleanup. File unrelated findings separately.

## Verification

Use opaque fixture sentinels for user data and structured turn selections/counts for assertions. Do not pin fragments of human-readable production copy. A complete-output golden may cover the new bounded response and recovery notices.

Required red/green regressions:

- A requested `27-40` range with a short user turn at 30 and enough assistant/tool output before it to make the existing front-anchored algorithm drop it. Assert the user data renders after the fix and the turn/count contract stays honest.
- Equivalent risk for `start:N`, `last:N`, and default selection, without including users outside the effective range.
- Multiple user turns whose bodies collectively exceed the budget, including an individually oversized turn beside short users. Assert full retained user data and the exact omitted addresses, budget compliance, deterministic one-pass anchor order, and recovery via exact expansion. Exercise the non-monotone `[23500,14000,1]` permutation in both directions without requiring optimal subset packing; every omitted user still needs its exact address.
- Gap/result pairing across omitted entries and truthful sparse-window announcements.
- No-user-at-risk cases match the existing renderer; existing range, truncation, pin and expansion tests pass. Replace an affected contiguous-span oracle only with a stricter sparse-aware oracle that independently checks selected addresses, ordering, count and gaps. Keep all existing cap, determinism and accounting checks.
- Exact omission accounting that cannot fit yields a narrowing error rather than a successful incomplete list, including hundreds of compact users beside a large user. Final envelope bounding cannot silently remove protected evidence, even when another turn quotes the same text. Unicode and escape-heavy user fixtures verify the existing measurement units. Registered-tool tests with lowered character and line limits require a typed complete-delivery failure rather than clipped evidence.

Run focused Go packages for `agent/transcript`, `agent/internal/tool`, and transcript/range tests in `agent`, then `make build`. Inspect make recipes and any reached setup scripts before reporting what ran. Do not run the full local suite; CI is authoritative.

Freeze this file for two independent competing PAR reviewers. Resolve evidenced findings before implementation, run PAR on the immutable implementation diff, run simplify-code, commit, run branch roborev, push and shepherd the PR. Refresh and merge current main again before the final push if it moved. Merge only after the required current-head checks are green and roborev findings are low or conclusively refuted.
