# Apple-native Evener design

## Intent and scope

Make the complete iPhone experience feel deliberately designed for iOS: clear hierarchy, calm content, coherent controls, and careful light/dark treatment. Jesse selected iPhone-first and delegated the visual decisions after declining mockups. Preserve capabilities and all data, recovery, draft, approval, and navigation contracts. iPad and Android delivery stay deferred.

## Direction: a quiet native instrument

The person checks ongoing work in varied light, often one-handed. Follow their system appearance. Use crisp neutral native surfaces, one blue interaction tint, and semantic amber/green/red only for attention, real work, and failure. Keep Evener's distinction between SF interface text, optional Source Serif reading text, and Menlo evidence. Shared identity means shared meaning and hierarchy, not copying the web's warm paper hex values onto every native surface.

Apple's navigation stacks, sheets, menus, gestures and materials remain the frame. No decorative glass, gradients, dashboard metrics, new navigation model, or replacement framework. A large title belongs to a primary list, never to every nested sheet. Grouped settings use one consistent row grammar. Headings carry their own importance instead of relying on small tracked capitals.

### First viewport

The Board should read as a prioritized list of work: clear section headings, aligned session titles, a quiet age and context line, and unmistakable attention marks. Shortcuts stay visually subordinate to the list. The conversation should read as a document with a precise, compact composer rather than a pile of cards. Hub settings should look like the same app at every depth.

### Interaction and motion

Preserve the existing signature: real activity meters, stable list ordering while the person interacts, native push/sheet transitions, and the compact composer opening in place without remounting its input. Selection feedback and pressed states are consistent. Reduce Motion and Reduce Transparency keep their existing owners. Nothing moves merely for decoration.

### Component rules

- Neutral foregrounds and surfaces distinguish page, grouped canvas, raised rows, and input wells in both themes. All normal text and placeholders retain at least 4.5:1 contrast.
- SF roles establish a clear hierarchy; interface copy is sentence case. Machine labels remain verbatim. Prose font preferences stay unchanged.
- Lists keep sparse hairlines, never border boxes around every row. Settings sections use inset groups and aligned labels/footers.
- Compact buttons still have real 44-point targets. Disabled actions remain legible and inert. No reliance on hit slop beyond a parent's bounds.
- Segmented choices use restrained selection and change to a readable wrapping/stacked layout when large text or available width makes equal-width single-line segments unsafe.
- Large text must not squeeze a session title between fixed metadata columns. Preserve title, state, age and actions with reflow.
- Settings summaries, values, editable fields, and destructive actions have consistent roles. Existing save, cancel, uncertainty, and reconnect behavior is untouched.

## Implementation boundaries

Shared native primitives and tokens carry most changes. Board, composer/context controls, and the older Hub administration layouts adopt them. Do not alter session reading-position behavior, server/protocol code, source-of-truth ownership, connection recovery, or user preferences. The web implementation already expresses the editorial hierarchy; update its platform guidance if needed rather than redesigning web without a concrete inconsistency.

## Verification

Use red/green tests for adaptive layout and interaction contracts, existing screen tests for preserved behavior, native typecheck/Biome, and the real iOS bundle/build. Inspect current-source iPhone simulator captures in light, dark and larger text, including Board, conversation, New session, Hub and representative settings. Fixture captures establish visual rendering only, not real-hub end-to-end acceptance. Run an independent finish review and code review. Record exactly which screens were inspected and any remaining limitations.

## Direction calibration

The pinned Apple-native brief governs the direction. Candidate references considered were iOS Mail's list hierarchy, Notes' reading canvas, Settings' grouped forms, Reminders' focused work list, Books' reader, Files' navigation and Shortcuts' precise controls. The concept calibration assigned the fourth candidate; its task-first list discipline fits the Board. Catalog alternatives were declined because catalog grids, coded album sleeves, bitmap specimens and transit diagrams obscure familiar iPhone actions. Their useful disciplines are retained: consistent alignment, prioritization, truthful state, and restrained semantic color. No catalog motif overrides Apple-native behavior.

Calibration record: direction seed `701a3594`, mode `operate`, approved pool
`c3b204a1eed6`, seven grounded candidates, assigned index four. The original
command result is retained in the design session's
`job:job_034iKD7qCVWGQfi6rNvDAN_hilS6G7Gy8Qq` output. The quality bar is the
first-party iPhone discipline above: work-first hierarchy, system navigation,
readable Dynamic Type, restrained surfaces and deliberate light/dark contrast.
Jesse declined mockups; there is no approved comp or separate catalog quality-bar
selection to claim.
