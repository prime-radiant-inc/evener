# Evener mobile design

**Direction approved; implementation in progress · 5 September 2026**

Use the [delivery plan](../../superpowers/plans/2026-09-06-native-mobile-delivery.md)
for sequencing, parallel ownership and completion gates, and the
[native mobile backlog](backlog.md) to track issues and new feedback.
Integration and lifecycle reliability precede whole-workflow visual acceptance;
MOB-001 remains a requirement throughout, not a stream of isolated spacing fixes.
MOB-011 records Jesse's composer correction: full-width text, Submit on the
controls row. It supersedes the earlier draft/submit placement in the studies.

Fluency and beauty are requirements of the mobile app, alongside capability and reliability. The current React Native prototype proves some workflows; it does not establish the visual direction. Jesse’s assessment of its chunky rows is the starting point for this work.

Product scope and behavior come from the current web UI, server contracts, and Jesse’s explicit requests. The old mobile UI is not a feature reference. Reusing its protocol or state code requires checking that code against these sources. Research references inform presentation and interaction; they do not add Evener capabilities. Screen studies must distinguish existing behavior from proposals and link their behavior to current sources. The [parity audit](web-parity-audit.md) records corrections and unresolved differences.

Read the [philosophy](philosophy.md), [style guide](style-guide.md), and [annotated lookbook](lookbook.html). The [research ledger](sources.md) separates published evidence from our design judgments. The [workflow studies](workflow-studies.md) define realistic content and full-product scenarios for the next design step. This is a proposed direction, not an approved design or a claim that the app already meets it.

## Roadmap and review gates

1. **Research and foundations — this draft.** Establish principles, reference imagery, component rules, and success criteria. Review the direction with Jesse before changing the app’s visual design.
2. **Screen studies.** Use the same realistic content for the hub/session list, a conversation, a running session needing attention, and a keyboard-open composer. Show light and dark appearances on iOS and Android. Include long titles, long code, errors, and large text—not just flattering empty states. Review these before building the components.
3. **Interaction prototypes.** Demonstrate opening and returning to a session, keyboard movement, following a streaming response, reviewing activity, switching hubs, and handling an uncertain send. Native transitions, gesture cancellation, focus, and scroll restoration are part of the design. Review recordings on both platforms.
4. **Implement in useful slices.** First session discovery and conversation reading; then composing, steering, queued messages and stopping; then approvals, questions, attachments and changes; then remaining session management and hub capabilities. Each slice must meet the visual and interaction criteria below before expanding scope.
5. **Continuous quality review.** Maintain reference screenshots and short recordings with stable, realistic fixtures. Review accessibility, performance, content stress cases, and platform differences alongside every substantial workflow. Voice and barge-in remain outside v1.

The capability inventory remains in `docs/superpowers/specs/2026-09-05-native-mobile-coverage.md`. Design work does not reduce that scope. The prototype’s transcript disclosure experiment is checkpointed at `3e24bf336` so it can be evaluated without treating it as the target aesthetic.

## Acceptance criteria

- A session is identifiable at a glance; its primary action and any required decision have a clear visual priority.
- Reading a conversation feels continuous. Ordinary text and routine activity do not become a stack of competing cards.
- Navigation, selection, keyboard movement and interrupted gestures feel native on each OS. Returning preserves reading position and draft context.
- Multiple hubs remain distinguishable wherever actions could affect the wrong session. Color is supplementary to names.
- Large text, screen readers, reduced motion, light/dark appearance and narrow screens are designed states.
- Long responses, tool bursts and fast streaming remain responsive. Measure on representative devices before claiming smoothness; simulator success alone is insufficient.
- Every shipped slice has both-platform manual evidence. A passing unit suite does not establish visual quality.

## Open design judgments

[Interruption notice evidence](interruption-notices.md) records the first
typed-notice implementation from the presentation study, including both
native platforms and the remaining disclosure-state and visual gaps.

[Presentation study 02](presentation-study.html) explores the whole-screen
hierarchy using the actual isolated-session markers and interruption notice.
It includes light/dark and illustrated keyboard states, expandable notices,
and insertion-only command selection. This is a proposed presentation, not
implemented native behavior. It maps session lists, tool output, decisions,
failures and administration to distinct visual treatments.

The proposed direction is quiet typography and restrained chrome, with stronger emphasis for actions that need attention. Screen studies must establish the exact conversation treatment, session-row density, and primary navigation. Tokens in the guide are starting values to test, not reasons to force a poor layout.

## Direct agent-product research

The [Codex/ChatGPT and Claude Code visual comparisons](agent-mobile-lookbook.html) and [community evidence](agent-mobile-research.md) extend the initial research with actual agent screens and dated public feedback. The lookbook server is bound to all IPv4 interfaces on port 8766 at Jesse’s request; use `http://m5.local:8766/lookbook.html` on the local network. It serves this design directory.

## Screen studies and first native visual slice

Jesse approved the researched direction and delegated routine choices while in meetings. [Screen studies](screen-studies.html) show the proposed session list, reading, decision and keyboard-open compositions. The [implementation plan](../../superpowers/plans/2026-09-05-native-mobile-visual-foundations.md) applies the existing native list/conversation hierarchy first; illustrated search, rich Markdown and approval controls do not claim implemented capability.

[Native visual slice evidence](visual-slice-evidence.md) records the tested build, both-platform keyboard screenshots, real-hub cross-device actions and the remaining accessibility and persistence defects.

[Draft persistence evidence](draft-persistence-evidence.md) records cold-launch preservation, interrupted delivery without replay, explicit recovery, and removal of local test-hub drafts on both platforms.
