# Caller delivery's two keys — direction note

**Status:** Direction note only — NOT a reviewed spec. Queued context for the
recursion era, when watch semantics become more load-bearing. · **Date:**
2026-06-13 · **Branch:** `job-control-spec`

## The observation

"Notify me when this fires" is expressible two ways that read identically but
are different machinery:

1. **Omit `send`** — the implicit caller notification endpoint: delivery is a
   `<job-notification event="watch">` block.
2. **`send.to="caller"`** — an explicit caller-alias watch-send: delivery is a
   rendered watch-send frame (`event="watch_send"`, `delivery_id`, `trigger`,
   coalescing state machine, durable pending/delivered settle).

Per contract (`docs/job-control.md` ~`:541`) these are DISTINCT watch keys that
coexist on the same target. The shape was flagged twice from live use: the
caller-notification-delivery card's sharp edges call it "a contract ambiguity
surfaced while writing this card", and an end-user's field notes (2026-06-12)
tripped over the adjacent key-replacement semantics.

## Why it matters more under recursion

Drive-down delivery (recursion spec v3 §3) multiplies watch/notification
traffic across depths. Two near-identical caller-delivery flavors mean: models
choose one at random, budgets/coalescing apply differently per flavor, and
debugging "why did I get two differently-shaped wakes for one fire" gets worse
at depth.

## Candidate directions (pick at spec time, not before)

- Collapse: make `send.to="caller"` an alias FOR the implicit endpoint (one
  key, one delivery shape) — needs a story for `include_frame`/`delivery_id`
  consumers.
- Keep both, but the tool description names the difference at the decision
  point (cheap, the 2026-06-12 pattern for surfacing key semantics).
- Keep both, contract gains a "which one do I want" table.

## Trigger

Reopen when recursion's coordinator-pattern e2e cards are authored: if card
authors or live models confuse the flavors again, that is the trigger named.
No implementation before then.
