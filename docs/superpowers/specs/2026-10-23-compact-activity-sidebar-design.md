# Compact activity sidebar

Status: approved by Jesse on 2026-10-23. The clarifications below preserve the selected visual direction and record the independent design review's implementation constraints.

## Intent and size

Make the browser activity sidebar's Agents, Jobs, Watches, and Tasks views easier to scan without spending a second line repeating a settled status. Jesse chose the Compact treatment in an interactive comparison over the Roomier treatment.

This is a small frontend presentation change. It uses the existing activity data, components, groups, and actions. No backend or protocol work is needed.

## Goals

- Give every activity item a readable title and a relative timestamp on its first line.
- Use one shared visual grammar across the four tabs: leading status, flexible title, trailing time.
- Keep live progress, failures, and watch conditions visible without repeating “Completed” on every settled agent or job.
- Preserve truthful timestamps and existing activity behavior at desktop and narrow web widths.

## Non-goals

- No native app changes, new activity types, grouping or ordering changes, pagination changes, or new navigation.
- No changes to activity ownership, subscriptions, collection reads, recovery, or the standalone Activity tree.
- No change to the standalone Tasks Sheet or pane's existing appearance. The sidebar's shared task body needs an explicit compact presentation; it must not silently restyle the standalone surface.
- No new widget, public widget API, settings toggle, or alternate density preference.
- No sample-data values or invented liveness from the mockup.

## Approaches

1. **Strict one-line ledger.** Put every title, status, and time on one line. This is densest, but it loses useful live intent and makes failures and watch conditions harder to understand.
2. **Compact rows with contextual detail, chosen.** Successfully settled agents and jobs get one line; tasks retain their existing compact history treatment. A second line appears for live work, a failure, cancelled/stopped or another non-success outcome, or watch conditions and delivery context. This matches the mockup Jesse selected and keeps important exceptions legible.
3. **Roomier two-line rows.** Give every item a status/timing line. This is easier to read in isolation but repeats settled status and required scrolling in the comparison; Jesse did not choose it.

## Row grammar

The first line is a single horizontal unit: the existing status glyph or task check, a title that ellipsizes when necessary, and a non-shrinking relative `<time>`. The timestamp never displaces or overlaps the title. It uses the existing caption type and neutral readable ink, with tabular numbers.

- **Successfully settled Agents and Jobs:** one line. Remove the repeated duration/status meta from the collapsed row. A successful agent does not gain a green status wash or dot.
- **Non-success outcomes:** keep the established status shape and explicit plain-language failure, cancellation, stopped, or other outcome meta. Do not reduce a non-success terminal row to an indistinguishable title or rely on hue alone.
- **Live Agents and Jobs:** retain the current live glyph behavior and useful secondary text. A running agent's own stated intent remains the why-line, with the existing quiet-dot rule; shell jobs keep their job glyph. Status and attention behavior does not change.
- **Tasks:** retain `TaskCheck`, cancelled-task strike-through, non-struck completed titles, and the latest note on unfinished tasks when present. Settled rows stay one line. Only the sidebar's task presentation changes.
- **Watches:** retain the second line from `watchMeta`, including trigger conditions, delivery counts, next-fire information where applicable, and armed/ended state. These facts justify the extra line.
- **Missing context:** omit an empty secondary line rather than render a placeholder or empty track.

Agents and Jobs keep their current drill-in/open actions. Watches and Tasks keep their disclosures. The mockup's generic “select to expand” behavior does not replace those real actions. Existing full titles, details, transcript output, and report content remain available through their current paths.

Compose the title and time through the existing `ListRow` and `Disclosure` content slots, sharing a small activity-row presentation helper between the sidebar and task body. A real-browser probe confirmed that a long title and timestamp share the first line with the current flex layout. Do not style widget internals, add raw controls, or extend the widget API. If implementation proves the existing slots cannot preserve this geometry, stop and ask Jesse before changing a widget.

## Timestamp rules

Use the shared `relativeTime` and `absoluteTime` formatters. The visible stamp is relative; its `<time>` retains the source ISO value and its accessible tooltip names the meaning and absolute time. An unfinished task's stamp means last updated, not last run. Omit the stamp when no authoritative source time exists; never substitute the current time. When choosing between two real timestamps, compare their parsed instants but render the original source string. Missing or invalid optional timestamps are absent, not zero dates or raw prose pretending to be relative time.

| View and state | Source timestamp | Meaning exposed with the time |
| --- | --- | --- |
| Running Agent | The newer of `latestActivityAt` and `runStartedAt`, matching the existing delegate quiet anchor | Latest activity, or run started when there is no activity yet |
| Settled Agent | `runEndedAt` | Run ended |
| Running Job | `lastOutputAt`, falling back to `startedAt` | Latest output, or started |
| Settled Job | `endedAt` | Ended |
| Open or running Task | `updatedAt` | Last updated |
| Done or cancelled Task | `completedAt`, falling back to `updatedAt` for legacy rows | Completed, cancelled, or last updated as applicable |
| Watch with deliveries | Last entry in `deliveryTimes`; when delivery evidence is absent, fall back to `createdAt` | Last delivered, or armed/created as applicable |
| Watch without deliveries | `createdAt` | Armed when currently armed, otherwise created |

Missing optional fields use only the stated fallback. A missing `completedAt` on an old settled task must not hide the row. A retained armed Watch can have a historical delivery count without retained delivery instants; in that case its creation time must keep its armed meaning, never imply a fresh delivery. Existing expanded details keep their fuller timestamps.

One clock owned by the visible sidebar refreshes relative labels every 30 seconds, matching the existing watch-tab cadence. It supplies the same `now` to first-line timestamps, Watch conditions, and expanded Watch details. Remove the Watch tab's separate interval; retain each running Agent's existing quiet-threshold timeout because that is a state transition, not a display clock. The clock changes display only: it adds no activity reads, subscriptions, collection demand, or lifecycle work, and it is cleaned up when the sidebar closes or unmounts. Existing watch countdown behavior remains intact.

## Constraints and preserved behavior

- Reuse the design tokens, `ListRow`, `Disclosure`, `SubagentDot`, `ToolIcon`, `TaskCheck`, and existing status/formatting helpers.
- Keep status words accessible after visual meta is shortened. Every successful, failed, cancelled, and live Agent or Job exposes its resolved status through the interactive row's accessible name or description; a nested `title` alone is insufficient. Successful settled rows keep their one-line appearance and no-dot success treatment. Do not make color or a glyph the only indication of failure.
- Keep keyboard operation, focus, touch target sizing, disclosures, retention, scroll boundaries, loading, empty, stale, and failure states.
- Keep current group membership and ordering even where the illustrative mockup differs.
- Do not infer ownership or state from navigation rows. The existing typed activity reads remain authoritative.
- Apply the compact layout throughout the browser activity sidebar, including its narrow web layout; this does not authorize native changes.

## Verification

Write behavior tests before changing the row implementation. Tests must not use visible copy as their oracle.

- Drive Agent, Job, Watch, and Task rows with fixed source timestamps and assert the selected `<time dateTime>` and meaning for live, settled, missing, legacy, and never-delivered cases.
- Prove successful settled Agents and Jobs have no secondary status line, every settled outcome remains accessible, live Agent intent remains, and Watch condition/delivery context remains.
- Prove titles and times coexist without overlap at desktop and narrow web widths, including long titles, and touch sizing does not regress.
- With a fixed clock, prove first-line timestamps and expanded Watch details advance together every 30 seconds, only one interval exists across tab switches, it stops on close/unmount, and ticks cause no additional transport calls.
- Exercise existing row actions, disclosures, keyboard navigation, paging, refresh, and retained-view behavior.
- Confirm the standalone Tasks Sheet keeps its existing presentation and the Activity tree and native sources are untouched.
- Run targeted frontend tests, `npm run typecheck`, Biome on touched files, and targeted real-browser geometry checks. Check both themes visually. CI owns `make test-web`, `make test-web-browser`, and the full suite on this shared workstation.

Update the browser activity ownership description in the product guide and subsystem map in the implementation change if the durable presentation contract changes.
