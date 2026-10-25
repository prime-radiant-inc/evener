# Comic Chat amendment: thoughts and “Meanwhile…” sub-chats

## Status and scope

Jesse approved this amendment to
`docs/superpowers/specs/2026-10-25-comic-chat-client-design.md`, including its
interaction details. It adds thought bubbles, a distinct identity for every
subagent and expandable “Meanwhile…” strips. Connection-and-routing Task 1 remains
unchanged. Affected implementation awaits independent review and a revised plan.

This amendment replaces the design's exclusion of reasoning and its child-link-only
presentation. General job/watch inspection, logs and settings remain in the regular
UI. It adds no new transport, history store, mutation owner or automatic agent work.
The font, mobile, original-art and no-deployment constraints remain unchanged.
Human questions and sandbox approvals retain Evener's interactive-root-only rules.
This client adds no child-to-human interaction mechanism or permission change.

## Approaches

- **Expandable “Meanwhile…” strips, selected by Jesse.** Child conversations unfold
  within the parent's comic, with a distinct cast identity. This keeps parallel work
  legible in context and requires independent child reader lifetimes and anchors.
- **Separate spin-off pages as the primary view, declined.** Simpler nested layout,
  but following each child would take the reader away from the parent. Opening a
  long child conversation on its own comic page remains an optional action.

The details below were Bot's implementation proposals, accepted with this amendment.

## Thought bubbles

- Render available thinking/reasoning content as cloud-shaped thought balloons with
  a trail of small circles to the thinking character. Speech keeps its pointed tail.
  Thought labels remain available to screen readers and the plain transcript view.
- Display only the content already provided by the canonical transcript. Never ask
  a model to supply thoughts, fill gaps, infer intent or paraphrase reasoning.
  Content-free or redacted entries remain content-free or redacted under the shared
  projection rules. Do not reveal unavailable content through a preview or tooltip.
- Keep thinking in source order among dialogue, images and other visible entries.
  Preserve its shared item identity through streaming, reset and settlement. Use
  the SDK's reasoning-paragraph assembly, preserving each paragraph's characters
  and separating those paragraphs consistently with the existing transcript.
- Available thought text is expanded by default in the comic. Long thoughts use
  measured continuation panels, with the same literal text, grapheme, font and
  reading-position guarantees as speech. Empty thinking has no fabricated text;
  an in-progress content-free entry can show its honest activity state.
- The main assistant's thoughts belong to its character. A child's thoughts belong
  to that child's character. Parent instructions and harness notices keep their
  actual source and remain attributed captions, never human speech or child thought.

## A stable cast

- Give each owned delegate a stable presentation identity from its qualified owner
  ref and stable delegate ID. Use the authoritative child ref to open its transcript;
  never derive it from a name, array position, parsed prose or a navigation row.
- A persona combines an original Microsoft character, a nameplate and a stable
  identifier badge. Use several original characters for the supporting cast. Their
  art may repeat when the cast exceeds the finite asset set, but the complete visible
  identity must remain distinguishable, including unnamed or identically named
  delegates and delegates on different hosts. Do not rely on color alone.
- Casting is deterministic, independent of fetch order, page position, status and
  run generation. Refresh, reconnect, more pages and a resumed delegate retain its
  character and identity. Names are display data, not routing keys. A full qualified
  identity remains accessible when a short badge needs disambiguation.
- Use that identity consistently in the strip heading, its child dialogue/thoughts
  and attributed parent-side updates or reports. A child opened on its own comic
  page keeps the same persona when ownership is proven by canonical context. Do not
  guess ownership for an unresolved direct link; keep it readable with truthful
  attribution and adopt the proven persona when the shared owner resolves it.
- The existing shared activity context carries the stored delegate name for the
  addressed child and proven delegate ancestors. Use these names for direct-page
  and parent-instruction attribution without acquiring parent activity or paging a
  roster for labels. Names remain bounded display data; existing ancestor titles
  and qualified routing identities retain their meanings. Unnamed or unresolved
  identities keep truthful attribution without an invented name.
- A child's parent instructions are attributed to its actual parent. They are not
  relabeled as something Jesse said. Poses never imply success or inferred emotion.

## “Meanwhile…” strips

- A collapsed strip shows the child's character, name/identifier and authoritative
  activity state. Expanding it reveals the actual child transcript, using the same
  literal dialogue, thought, image and caption projection as the parent. A report
  preview or invented summary is not a substitute for that conversation.
- Place the strip at the first loaded canonical delegation event that proves its
  owner/delegate identity. Keep at most one expandable strip per owned delegate in
  that parent. Later notifications remain attributed source-order entries and can
  link to that strip; they do not create duplicate child readers.
- If canonical activity proves a child but its causal event is outside loaded
  history, keep the child reachable in a clearly separate “Meanwhile…” group near
  the conversation controls. Do not invent a chronological insertion point. When
  history brings the event into view, preserve expansion and reader state as its
  existing strip gains that position. The group uses the activity owner's paging
  and incomplete/unavailable states, never an unbounded roster fetch.
- Inline panels use the parent's vertical reading flow. At 760px and below, each
  child panel is full width, with a clear strip heading/border rather than repeated
  indentation that squeezes grandchildren. Keep rendering bounded as the loaded
  parent and child conversations grow.
- A child's own children use the same rule and open only on explicit expansion.
  No eager recursive tree or transcript fetch. Repeated or cyclic identities cannot
  recurse indefinitely. A parent can show several deliberately opened strips.
- The expanded child is a reader, without a second inline composer. Its explicit
  “Open conversation” action selects the child at `/comic/s/:ref`, where the normal
  comic controls follow the existing selected-session admission rules. Questions
  and sandbox approvals are exercised on interactive root conversations. Child
  strips show authoritative activity state and an available full-page route;
  model-facing delegate attention is not a human question or approval. Expanding
  a strip never changes the parent composer, draft, route or action target.
- Collapse returns focus to the strip control and releases that child's owned read
  demand, without cancelling another reader. Keep lightweight expansion/anchor state
  above virtual row mounts, so scrolling a row out of view does not count as the
  user collapsing it. Reopening reuses the shared history rather than a second cache.

## Ownership, recovery and reading position

`src/stores/sessionActivity.ts`, the shared `buildEntityView`/`findEntityView`
projection and canonical activity context own delegate evidence. Share their
subscriptions with transcript consumers and request explicit session scope, not an
inferred navigation subtree. Acquire only the activity collections actually needed.
`src/panes/session/transcript/useEntityView.ts` is the existing integration reference.
All frontend paths here are relative to `cmd/evener-hub/frontend/`.

The chosen entity snapshot owns its status and run generation. Never combine a
resumed run with a previous run's report or infer completion from a receipt. Recorded
older updates/reports remain historical transcript entries with their attribution.
When identity is missing or ambiguous, keep the source caption but do not open an
arbitrary child or borrow a same-named local session for an unavailable remote one.

Each explicitly expanded child acquires its own foreground transcript claim and
committed shared pager reader. Its activity binding follows that lifetime. Release
owned demand on collapse, parent/session retirement or client replacement, and fence
late responses. Another consumer of the same child must retain its claim and retry.
Opening or expanding remains a read and must not start or resume a session.

Preserve loaded child panels through disconnect and recover through existing owners.
A failed or missing child does not hide healthy siblings or disable the parent.
Preserve typed errors and existing automatic retry rather than inventing new recovery.

Reading anchors identify the complete session ref, source entry and character offset,
including the expanded-child path when needed. Expansion, collapse, streaming,
older-page prepend, font settlement and reflow preserve an away reader's position.
New output in a child does not pull the parent away from what the person is reading.
Explicit latest-follow applies to the reader's chosen target, not whichever child
most recently emitted. Persist only small view/anchor state, not transcript bodies.
Refresh restores the expanded path needed for a saved child anchor using bounded
shared paging. Back from a full-page child restores the parent reading location.
Plain/comic view switches retain those identities, positions and drafts.

## Additional proof obligations

1. Feed independent canonical thinking fixtures through live, reset and settled
   projection. Reconstruct available source paragraphs exactly; confirm thought
   attribution and source order. Content-free/redacted cases reveal no hidden text.
2. Use two same-named delegates, unnamed delegates, different hosts, a resumed run
   and more delegates than character assets. Assert distinct complete identities
   and stable casting across reordering, paging, reconnect and direct child opening.
3. Exercise real producer fixtures in `agent/testdata/subagentwire` through the shared
   entity adapter and comic projection. Pin parent instruction, mid-work update,
   terminal report, resumed generation and ambiguous/missing identity cases.
4. Verify collapsed strips do not fetch child transcripts, expansion loads the
   correct qualified ref and two consumers share ownership. Collapse/retire during
   a delayed read must not cancel a sibling reader or publish into a new selection.
   Confirm no start/resume mutations from these reads and no parent draft changes.
5. Page both activity and child history. Introduce a previously unloaded causal
   event and retain the existing strip's identity, expansion and reading position.
   Refresh/reconnect after multiple pages with a visible later-page child message.
6. Extend the real hub/daemon browser journey with scripted-provider delegation,
   thoughts and at least one nested child. Exercise real child status, expansion,
   collapse, full-page child navigation and Back. Assert durable producer data,
   correct identities and no duplicate readers or transcript content. Prove human
   questions and sandbox approvals separately on interactive root conversations;
   do not fabricate pending child state or loosen the runtime's root-only gates.
7. At 1360, 760, 600, 390 and 320px, verify full-width mobile child panels, thought
   balloon geometry, no horizontal overflow, keyboard expansion/focus, plain-view
   parity, bounded DOM and stable away anchors during child streaming and reflow.

Update the owning Comic Chat and session-activity guides and subsystem map when this
behavior is implemented and verified. This amendment alone proves no live behavior.
