# Transcript chat bubbles — user and agent message treatment

Date: 2026-07-30
Status: approved (brainstorming session, mockups in `.superpowers/brainstorm/83487-1785389342/content/`)
Supersedes: the slack-lean no-bubble decision in `docs/web-ui/specs/2026-07-29-transcript-slack-lean-messages.md` for the message BODY only; that spec's speaker header, avatar column, and gutter geometry stand.

## Intent

The transcript reads as a document; the user wants it to feel a little more
like chat. Direction B from the brainstorm: keep the left-aligned avatar
column (the tool rows and gutter depend on it) and put every user and agent
message in a bubble. Within B: both speakers bubbled (B1), and every agent
fragment gets its own bubble (per-fragment, iMessage-style — not one bubble
per exchange, not bubbles around tool runs).

## Decisions

1. **Every message is a bubble.** User messages and agent messages, exchange
   openers and continuations alike. The bubble hugs its content
   (`inline-block`-style, max-width ~92% of the content column).

2. **User bubble = accent wash; agent bubble = neutral ink wash.** Both are
   `color-mix(in oklab, …)` over theme tokens — no hardcoded hex, so the dark
   theme stays honest. The agent fill is deliberately NOT `--surface-2`: in
   the light theme `--surface-1` and `--surface-2` measure identical to the
   pane (recorded in toolcallitem.module.css's hover note), so a surface
   token would be invisible exactly where the transcript is densest.

3. **Tail geometry on openers only.** An exchange-opening bubble (the one
   with the avatar and speaker header) takes a 4px corner toward the avatar
   and 14px elsewhere — the chat "tail". Continuation fragments are fully
   rounded (14px) and carry no header and no avatar; the existing TurnBlock
   gutter aligns them to the same content edge, so no spacer element is
   needed.

4. **Headers are unchanged.** "You · time" above the user bubble;
   "Agent · model · time" above exchange-opening agent bubbles only. The fork
   action stays in the user header.

5. **Tool rows stay outside bubbles, untouched.** Per-fragment bubbles mean
   the transcript's interleaving (prose, tool row, prose) keeps its current
   structure; only the prose fragments change shape.

6. **Code blocks inside a bubble flip to the pane surface + hairline.**
   Inside a bubble, surfaced blocks (code) use `--surface-1` with a 1px
   `--edge` border instead of the default fill, fixing grey-on-grey in both
   themes.

7. **Streaming keeps the same shape.** Live text (StreamingText) renders
   inside the same bubble wrapper as settled markdown, so a sub-second
   stream never changes shape on settle — the same contract the speaker
   header already honors (AgentMessageItem renders its header in both
   branches).

8. **The 32px exchange margin stays killed.** The speaker header and the
   bubble now mark the exchange boundary; extra vertical space above "You"
   messages read as dead air (removed in this branch's first commit).

## Non-goals

- No right-alignment of user messages (direction A, rejected: it fights the
  left-aligned tool rows).
- No bubbles around tool rows or whole runs (rejected: boxes the transcript's
  working surface).
- No changes to tool rows, think blocks, notifications, or steering items
  beyond SteeringItem's reuse of UserMessageView (which inherits the bubble
  for free).

## Testing

- DOM: the bubble wrapper is present in BOTH the live and settled agent
  branches; continuation fragments render a bubble with no header and no
  avatar; the user bubble renders with and without header actions.
- CSS: bubble fills are color-mix over tokens (no hardcoded hex); the
  continuation radius is uniform; the code-in-bubble override exists and
  references `--surface-1` and `--edge`.
- Regression: existing ToolRow, TurnBlock, and steering tests stay green.
