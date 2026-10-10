# Session status follows the parent’s own failure

## Scope and intent

A top-level session’s status describes its own failure and current work.
Historical failed subagents remain visible without making a healthy parent look
broken. Apply this rule to the web sidebar and the phone’s Board session rows.

Jesse requested: “the red dot should only be if the top-level session is broken.
and if a subagent is running and there is no question, we should show the spinning
status.” He approved “Web and phone” and “Keep the phone’s working indicator”.

## Behavior

- Only the parent’s own `errored` state produces its Broken/Failed status. A
  nonzero failed-subagent count never produces that status.
- A live parent with running subagents shows Running on the web and Working on
  the phone, including when other subagents have failed. This also applies when
  the parent itself is quiet or has a nonblocking warning.
- Pending questions and approvals keep their existing needs-you presentation
  ahead of running-subagent work. A warning carrying a pending question or
  approval remains an attention state.
- On the phone, a new nonblocking Warning alert waits while live children run.
  A retained warning alerts when the last child settles; a warning that clears
  first never alerts. Warnings carrying a pending question or approval retain
  immediate alert eligibility. Existing first-read and offline rules still apply.
- The parent’s own failure, restart-required state, and existing unavailable,
  ended and unloaded presentations remain unchanged. Only live compact summaries
  can contribute running-subagent work.
- Otherwise, each client keeps its current parent-state and quiet/completion
  presentation.
- Keep the web spinner. Keep the phone’s existing Working pulse meter in Live
  and static Working mark in other Board views. Change classification, not the
  visual language.

## Authority, preservation and recovery

Read the authoritative compact subagent summary already delivered by navigation.
Reuse the shared navigation selectors where appropriate. Do not reconstruct
counts from loaded descendant rows or introduce polling, subscriptions, new wire
fields, runtime state changes or backward compatibility.

The status change leaves failed-subagent counts, outcome records, Agents/Subagents
views and their navigation intact. A failed delegate is still inspectable.
Question resolution restores the working presentation while subagents run.
When the last running subagent settles, normal parent-state presentation resumes,
even if historical failures remain. Refresh and reconnect use the same rules.

## Change boundary

The web projection lives in
`cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx`; the phone classification
lives in `mobile-native/src/board/attention.ts`. Shared navigation selectors live
in `appwire-client/typescript/state/navigation/selectors.ts`. Keep changes small
and preserve unrelated classifiers and descendant-row behavior.

Update `docs/product/session-activity.md` and the affected rows in
`docs/product/subsystems.md` with the verified client rule and unchanged authority.
No daemon, hub aggregation, session lifecycle, TUI or phone-search redesign is in
scope. Deployment requires separate authorization.

The phone’s `mobile-native/src/alerts/alertEvents.ts` consumes Board bands. Pin
the approved delayed-warning policy without changing alert detection, combined
banners, Needs you counts or Next navigation.

## Test plan and acceptance

Start with failing regressions against the current implementation. Cover own
failure, failed-only descendants, mixed running/failed descendants, quiet parents,
nonblocking warnings, pending questions, approvals, restart-required and unavailable
sessions. Assert the actual accessible status and retained failed counts on both
clients, including the phone’s unchanged Working visuals.

Exercise navigation delivery through the real shared store and rendered session
list, rather than testing classification alone. Follow running work with a retained
failed child, a pending question, question resolution, last-child settlement, and
refresh/reconnect. Verify attention and working transitions, normal quiet/completion
presentation, and preserved access to failed-child details. Keep deterministic
transport fixtures at the external boundary; do not replace the selectors, store
or renderers under test.

Exercise the real Board-to-alert feed for warning onset during child work,
retained-warning settlement, clearing before settlement and warning with a
pending question or approval. Include hub Needs you membership and duplicate reads.

Run affected tests, `make test-web`, `make test-native`, and relevant browser guards
after reading their runners. Use CI for the full repository gates. Report any
physical-phone or live-runtime checks that were not performed.
