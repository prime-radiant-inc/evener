# Comic live refinements — 2026-10-31

Jesse's live-testing feedback on the merged Comic presentation (#5441), with his
 rulings recorded 2026-10-31 22:00 UTC. All six items ship in this lane.

## Scope (agreed)

1. **Pack panels by width.** A row holds as many panel-sized panels as the
   conversation width fits: `n = floor((width + 15px) / (26rem + 15px))`,
   clamped to at least 1. Panels stay at their 26rem bound; spare width stays
   gutter (existing ruling: never stretch a lone panel). Below ~847px (16px
   root) this is one column; at 761–847px the old shrinking 2-up pairing is
   gone, which the
   panel-size ruling prefers. Inserts keep their own 40rem rows; spotlight and
   continuation invariants are unchanged.
2. **Stack same-party runs.** Consecutive short single-prose messages from the
   same party (persona key, or the human persona) compose into ONE panel: one
   character, balloons stacked in source order. Every word, slice and reading
   anchor is preserved; each balloon keeps its own source range node. Long
   runs continue into a following panel of the same party. Different-party
   singletons pack side by side per rule 1. Captions without a persona do not
   stack.
3. **Meanwhile: keep expansion, remove the bottom group.** The expandable
   strip stays as shipped. The "Meanwhile… outside loaded history" bottom
   group is deleted. A strip with no loaded launch anchors at its earliest
   loaded folded update (rule 4) instead of the bottom; when its launch later
   loads, the strip moves to it (existing key-stable move). A strip with
   neither launch nor loaded traffic is not rendered.
4. **Fold delegate mid-work updates into their strip.** Delegate `update`
   notifications and `delegate_send` earlier-update results no longer render as
   caption panels; their strip header shows a live update count. Final replies
   and failures still render (and stack per rule 2). If activity data is
   unavailable, updates render as captions as today rather than disappearing.
5. **Reminders never render.** Daemon steering text wrapped in
   `<SYSTEM-REMINDER>` never becomes a comic panel: the wrapper is stripped
   first, notification cards inside still parse and render, and remaining
   reminder prose is dropped. Human-authored steering is unwrapped for
   display and still renders as Anna.
6. **Normal composer.** The conversation pane mounts the standard `Composer`
   (attach, in-card session menu "…", steer, queue) instead of the bespoke
   `ComicComposer`, which is deleted. The footer falls back to the standalone
   session menu only when there is no composer source.
7. **Thought bubble fix.** `.thought`'s offset box-shadow paints a duplicate
   balloon silhouette; remove it, keep the cloud shape and trail. Verified
   visually on the built UI.

## Non-goals

- No new link-panel type, no change to expansion, reading-position, coverage or
  document-opening contracts.
- No font, art, appearance, question or approval changes.
- No new widgets or widget props.

## Whole-journey verification

- Red-first unit coverage per rule in `comic/` (layout, rows, projection,
  header, body).
- `make test-web` gate + browser comic guard in CI; visual check of packing,
  stacking, strips and thought bubbles on the built UI.
- Docs stay evergreen in the same change: `docs/product/comic-chat.md`
  (packing, Meanwhile, reminders, composer), subsystems row if it changes.
