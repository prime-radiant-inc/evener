# Round 2: proposal

Design-only. No production code changed.

## What the four reviews did

All four reviewers read all three round-1 mockups plus the real rail and activity
source. Vision was unavailable to every one of them, so **nobody has eyeballed
the rendered mockups** — the reviews are source-and-code critiques. That is the
one real limitation of this round.

| Lens | 1st | 2nd | 3rd | Deciding finding |
| --- | --- | --- | --- | --- |
| Triage / information design | B | A | C | The rail's one-line rule; a bare count is inventory, not attention |
| Interaction / a11y / edge cases | B | A | C | C's cards are keyboard-unreachable; glyphs aren't in the app fonts |
| Visual craft / design-system | A | B | C | C's band, cards, ticks and progress bar violate stated rules |
| Feasibility / data model | A | B | C | C's countdown and progress bar have no backing data |

C is rejected by every lens. A and B split, and round 2 is that split resolved.

## What round 2 keeps

- **The count on the summary line**, as asked, in neutral ink, placed before the
  branch so ellipsis never eats it.
- **Inline watch rows in the fold-out**, in the rail's existing row grammar:
  drawn clock, the watch's note as the title, cadence and state on the second
  line.
- **A dedicated Watches group in the Activity panel** with an expandable detail
  strip: the note as a sentence, then `source / cadence / armed / last fire /
  fires`, then the delivery log.
- **Honest states**: `armed` for conditions, `needs a look` for runaway drops,
  `Ended watches (N)` folded in the panel.

## What round 2 drops, and why

| Dropped | Reason |
| --- | --- |
| The `◷` text glyph | No codepoint in the app's font `unicode-range` covers it; the rail names rows from content, so a screen reader would read the glyph |
| `next in 4m`, cadence ticks, progress track, `↻30m ×6h` | No next-fire instant, no previous-fire instant, no TTL/duration field exists in the runtime |
| The "Armed" card band and 2-column grid | `design-system.md` bans nested boxes and tinted bands; cards also ellipsize a prose note to nonsense |
| A third rail fold for ended watches | Three collapsed disclosures per session is rail noise; ended watches belong in the panel |
| Watches above running work, unconditionally | An armed watch is not attention; only the abnormal case is promoted |

## The two proposals

**Primary — `mockups/round-2-primary.html`.** Running work keeps the top of the
Activity panel; watches get their own group beneath it; only abnormal watches are
promoted to a "Needs a look" group at the top. Reads the panel as a triage
surface.

**Variant — `mockups/round-2-variant.html`.** Watches lead the panel, ordered by
severity then name, with running work below. Honours the literal ask most
directly and is the cheaper build: a parallel payload rendered as its own
section, so the strict activity parser, the `ActivityRow` union, the tree switch,
and the row detail component are all untouched.

Both share every decision in the "keeps" list above, and both include the rail
edge states: an idle session whose only pending work is one watch (the coverage
hole), the abnormal count, the many-watches cap, and the zero case.

## Data plan, verified

**Already there:** `jobManager.liveWatchSummaries()` → `{id, source, condition,
deliveries, createdAt}` for the session's visible watches; `recentWatchEntry` →
`{id, source, condition, deliveries, end_reason, ended_at}` for ended ones;
cadence and note already inside `watchConditionSummary`'s prose.

**Cheap plumbing:** project that to the hub (`DetailedStatus` →
`EvenerDiagnostics` → `NavigationSessionSummary`), and split `note` and cadence
into structured fields rather than parsing the condition prose. Delivery
timestamps exist (`WatchSendState.CreatedAt`) but `DeliveryView` omits them; add
the field for the "last fire" line.

**Must be defined before building:** "active watch" means in the live registry
and visible to this session, excluding fired-pending-end entries; a receiver
watch counts for its receiver, not its owner, or a subtree rollup double-counts.

## Open questions for Jesse

1. **Coverage hole:** may an idle session whose only pending work is a watch grow
   a second line? Round 2 says yes (otherwise the count is invisible exactly when
   a watch is the only thing happening), but that amends the "quiet row is one
   line" rule. The alternative is to accept that the count only ever appears on
   already-busy sessions.
2. **Primary or variant** — running work first, or watches first?
3. **Bare count vs named state.** The ask was "# of active watches". Round 2 shows
   `3 watches` normally and `1 watch needs a look` when one is abnormal. Willing
   to always show the bare count, or should the abnormal case replace it?
4. **Scope of the plumbing.** This is a real backend change (hub projection +
   structured watch fields), not a frontend-only tweak. Confirm that's in scope
   before implementation starts.

## Files

- `mockups/direction-a.html`, `direction-b.html`, `direction-c.html` — round 1
- `mockups/round-2-primary.html`, `round-2-variant.html` — round 2
- `mockups/mockup.css` — shared styles, token values copied from `tokens.css`
- `screenshots/*.png` — dark renders of each
- `reviews/synthesis.md` — the four reviews, consolidated and independently checked
- `README.md` — reconnaissance and round-1 rationale
