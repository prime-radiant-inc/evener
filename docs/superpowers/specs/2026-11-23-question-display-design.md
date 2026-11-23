# Question display cleanup

## Intent

Make `ask_user` questions easier to scan and answer on desktop and phone browsers. Use the competitor reference's clear hierarchy within Evener's existing editorial design system. Jesse selected the neutral-frame HTML mockup and does not want the large orange attention wrapper.

## Goals

- Give the question text the strongest visual hierarchy.
- Put each option label and its explanation on separate lines, with the radio or checkbox aligned to the label.
- Keep the selected answer obvious through a quiet accent wash, border, and checked control.
- Separate the question, choices, optional note, and submission footer with spacing rather than nested surfaces.
- Use the same question and option hierarchy for read-only questions in transcript history.

## Non-goals

- No changes to answering, notes, free text, multi-select, question tabs, automatic advancement, submission, recovery, or keyboard behavior.
- No native iOS layout changes.
- No new widgets, widget props, fonts, colors, or one-click submission.

## Constraints

- Use the existing `Card`, type, palette, spacing, radius, focus, and touch-target tokens.
- Preserve semantic grouping, accessible names, focus handling, and 44px coarse-pointer targets.
- Keep long model-authored questions, labels, and details wrapping without horizontal overflow.
- Keep the existing live announcements and pending-question recovery behavior unchanged.

## Approaches

1. **Borderless:** place the question directly in the transcript with a small attention dot. This is the quietest option, but it provides less separation between an actionable question and surrounding conversation.
2. **Neutral frame, selected:** use the same hierarchy inside a thin neutral `Card` boundary. This keeps the task visually contained without a large amber surface.
3. **Retain the attention envelope and clean only its contents:** smaller code churn, but Jesse rejected the large orange wrapper.

## Selected design

Each live question batch uses a neutral outlined `Card`. A small `Question` label with an amber attention dot sits inside the frame, followed by the model-provided header and the question in semibold prose type. The amber container tint and edge are removed; amber remains only in the small attention cue.

Options become aligned rows with two columns: the native radio or checkbox, then a text stack. The label and optional `Recommended` marker form the first line. The detail sits below in smaller secondary text. The selected row keeps the existing accent wash and edge. `Something else…`, the shared note/free-text field, answered count, and primary action retain their current behavior.

The separate numeric prefix on every question card goes away. Multi-question tabs keep their existing numbers and answered states. This explicitly supersedes the current visual test requiring a number on a single-question card; the replacement check covers the header/question structure and preserved tab numbering rather than the removed decoration.

Read-only transcript questions use the same strong question text and stacked option details. They do not gain the live attention cue or answering controls.

Phone browsers use the same neutral frame and hierarchy, with tighter inline padding, full wrapping, 16px editable-field text, and the existing coarse-pointer target floor. The footer remains usable at narrow widths.

## Implementation surface

- `AskDock.tsx` and `askdock.module.css`: neutral batch frame and compact attention cue.
- `AskQuestionCard.tsx` and `askquestioncard.module.css`: question hierarchy and two-line option rows.
- `askUser.tsx` and `askuser.module.css`: matching read-only hierarchy.
- Existing focused tests and browser layout fixtures: update only the visual assertions superseded above; preserve all interaction and recovery checks.
- `docs/web-ui/design-system.md`: replace the obsolete amber-`AskDock` description with the neutral-frame contract.

## Verification

Run the focused AskDock, AskQuestionCard, read-only ask-user, and relevant layout tests. Check desktop and phone-width rendering in both themes, including a selected option, long wrapped content, free text, and multi-select. Run Biome on touched files, the frontend typecheck, `make test-web`, and `make test-web-browser` on a Chrome-capable host.
