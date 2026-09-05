# Native mobile visual foundations implementation plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task-by-task. Jesse delegated routine choices and requested uninterrupted progress while in meetings.

**Goal:** Apply the researched visual hierarchy to the existing native session list and conversation, with both-platform evidence.

**Architecture:** Retain native navigation and the shared AppWire services/stores. Adjust presentation in the existing small components; do not replace connection or mutation semantics. Keep screen studies separate from shipped capability.

**Tech Stack:** Expo 57, React Native 0.86.3, React Navigation native stack, TypeScript.

**Spec:** docs/design/mobile/philosophy.md, style-guide.md and screen-studies.html.

## Global constraints

- Voice and barge-in remain outside v1.
- Native platform behavior, selectable text and large text remain supported.
- Keep capability-gated actions and unknown-delivery safeguards intact.
- Preserve existing uncommitted Apple signing edits.
- No new dependency or protocol change for the visual slice.

### Task 1: Screen composition

- [x] Rebase on current main, preserve signing edits and retain a pre-rebase branch.
- [x] Create docs/design/mobile/screen-studies.html: session list, reading, decision and keyboard-open composition; light/dark and platform treatments.
- [x] Inspect at narrow widths and larger text; correct clipping and misleading controls.
- [x] Link the study from the lookbook and record that fictional controls are not implemented capabilities.

### Task 2: Native visual hierarchy

Files: mobile-native/src/ui.tsx, screens.tsx, TimelineItem.tsx, App.tsx.
Interfaces: retain useColors(), Action props, TimelineItem({item}), existing route and store contracts.

- [x] Establish semantic palette and consistent native typography; make Action support a primary treatment while preserving accessibility state.
- [x] Replace bordered session cards with aligned rows: title then one supporting context line; retain needs-you status and disclose roster limit.
- [x] Render assistant content directly on the canvas, user content on a restrained inset surface, and failures/questions with meaningful emphasis. Keep activity disclosure complete.
- [x] Unify composer input and actions, choose Send or Steer as primary according to capabilities, keep Queue and Stop reachable. Bound growing input; retain uncertain-send recovery.
- [x] Apply palette to native stack navigation; preserve native back gestures.
- [x] Run native typecheck and existing behavior tests. Styling is verified through rendering, not tests that assert style literals.
- [x] Commit this independently reviewable slice.

### Task 3: Runtime evidence

- [x] Export both platforms, build/install the native apps on available simulators.
- [x] Use the isolated playground to inspect list, disclosure, keyboard-open long draft and capability-gated send/steer/queue/stop on both platforms.
- [x] Inspect light/dark and large text; record actual evidence and remaining limitations in docs/design/mobile.
- [x] Run focused hub roster/protocol checks after rebase and review the final diff before committing evidence.

## Execution record

Completed the scoped visual implementation and runtime inspection; evidence and open defects are in [visual-slice-evidence.md](../../design/mobile/visual-slice-evidence.md). Completed inspection does not mean every accessibility case passed: live iOS text-size changes remain defective and Android activity recreation exposes the existing draft-persistence gap. Full native functionality remains active work.

Ruling: keep Latest always reachable rather than infer bottom proximity from scroll events alone; content can grow while the reader is stationary. Normal status and detailed recovery scroll, with recovery access retained next to the composer.
