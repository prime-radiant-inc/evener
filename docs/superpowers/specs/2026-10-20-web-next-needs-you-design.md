# Web Next needs you

## Purpose and scope

Make the existing web attention workflow usable with a thumb, without opening the session drawer. Bring over native's visible Next control and held-press session list while keeping web navigation, attention ordering, and the user's work intact.

This design covers Next needs you only. Open-at-first-unread-reply follows separately, at every web width and off by default. Document review belongs to another session. Deployment, native changes, a new attention classifier, and a general navigation redesign are out of scope.

This is a design for approval, not a statement that the feature is implemented.

## Jesse's decisions

- Offer the control at every web width: phone, tablet, and desktop.
- Use floating placement above the composer, rather than reserving a footer row.
- The adjacent More control should not be a down arrow.
- Preserve existing reading behavior when returning to an already-open conversation.

The preview uses a horizontal ellipsis for More. That specific icon is the author's recommendation in response to Jesse's correction.

## User-visible behavior

### Placement and visibility

The focused conversation gets one compact floating Next control above the trailing edge of its composer. It names the next session and includes a separate horizontal-ellipsis More button. Both are built from existing widgets and tokens. Long titles truncate visually but remain available to assistive technology.

The controls have usable touch targets on phone and tablet. They stay out of Send and Stop's hit areas, including while a draft is present and while the viewport or keyboard changes size. Preserve the existing Stop placement and double-tap delay unchanged.

The control floats over the reading area, as selected. Provide enough transcript-end clearance for the final content to be scrolled fully above it. Coordinate its position with the current-conversation New Content control rather than overlapping or repurposing that control. New Content stays a way to navigate within the current transcript; Next stays a way to change sessions.

Show the control only for the actual focused conversation. A cascade's focused leaf may participate, but parent columns and other visible panes must not duplicate the affordance. Do not use viewport width or a desktop-only header/footer as the availability gate.

Hide it only while an IME composition is in progress, while a question or approval needs action in the focused conversation, and while transcript Find or another blocking modal owns interaction. Editor focus never hides it, and neither does a saved draft: a session open autofocuses its composer, so hiding on focus would take the control away before the reader could use it. Return it automatically when the suppressing condition clears. Jesse's later product ruling replaced the original editing rule.

### Next and More

- Clicking Next opens the next eligible session other than the current one.
- Clicking More opens an accessible list of other eligible sessions. Holding Next opens the same list.
- Pointer movement, release before the hold threshold, cancellation, loss of the relevant view, and unmount cancel the hold. A completed hold must not also activate Next on release.
- More is independently keyboard-accessible. The list supports normal keyboard activation, Escape and outside dismissal. Dismissal returns focus sensibly without moving the transcript or focusing the editor.
- List rows name the session and show the existing attention reason where available. They do not invent a second status or priority model.
- Recheck a selected session against current attention data when activating it. Do not navigate a stale list selection or let a late request take focus after the user has moved on.
- Hide the control if the complete known result contains no other eligible session. If more data exists, keep a way to request the next page instead of treating the loaded subset as the entire queue.

The hold threshold, movement tolerance, exact spacing and icon sizing are left to the builder, using existing platform and widget conventions. The preview's values are not a product contract.

### Ordering, paging, and recovery

Keep web's existing server order and cycle semantics: advance after the focused session, wrap at the end, and begin at the head when the focused session is absent. Exclude self-navigation from the new shared action. Do not copy native's recently-alerted reordering, automatic full-queue loading, or first-eight iOS action-sheet limit.

Use the shared navigation store's `needs_you` section as the sole membership, ordering and paging owner. Show loaded results without claiming that a loaded count is the total. If there are additional pages, More offers the existing next-page demand path; Next requests at most the next required bounded page before resolving its action. Do not eagerly fill the whole queue.

Keyboard navigation, Next, and list activation share one action owner and validity checks. They do not each create their own retry loop or subscriptions. A failed page must not strand the visible control until a new hub generation: later deliberate activation may retry the demand through the existing store, while reconnect/invalidation retains its existing automatic recovery. Distinguish a failed read from a valid empty result. Preserve healthy loaded choices when a later page is unavailable.

Deferred navigation belongs to the exact initiating action, focused reader and accepted navigation generation. Leaving and returning before a response resolves still cancels the old intent. Opening a modal, choosing a different target, changing the relevant session identity, or disposing the source also cancels it. Recovered data may update the list, but must not cause unsolicited navigation.

### Navigation and preservation

Use normal web history: each distinct Next or explicit list selection is one history step. Browser Back and Forward traverse those steps. Do not introduce native's first-push/subsequent-replace queue compression.

Changing the active session must preserve the source's structured draft, staged images, in-flight attachment processing, retained transcript position, and independent panes. Back must restore the exact retained source reader and composer when they still exist. Text reconstructed from storage is not equivalent to preserved attachment bytes or source lifetime.

Attention Back/Forward preserves the draft and staged attachment work owned by the exact existing reader being restored. Do not add live cross-view draft synchronization; keep ordinary draft/cache behavior unchanged, and do not promise permanently independent same-session drafts. This does not change ordinary navigation or promise separate persisted drafts per reader.

For a secondary pane or cascade leaf, remember the exact originating reader and placement, not just the URL's top-level session. An already-open target is reused, not duplicated. The affordance must not unexpectedly create a new visible split or close unrelated panes merely to preserve history. Existing explicit Close remains an intentional disposal boundary; returning to a closed session uses normal fresh-open behavior. This change does not promise restoration of unsaved binary attachments across browser reload or process loss.

This preservation work is part of delivering the control, not a separate cleanup. Keep it limited to attention navigation and its Back/Forward reconciliation. Ordinary navigation behavior outside that path is not silently broadened.

## Current source and integration boundaries

Relevant existing owners:

- `cmd/evener-hub/frontend/src/shell/AppShell.tsx`: attention shortcut, bounded page demand, route-to-pane reconciliation.
- `cmd/evener-hub/frontend/src/shell/rail/needsYouCycle.ts`: current web cycle order.
- `appwire-client/typescript/state/navigation/`: shared section data, page-demand deduplication, invalidation and reconnect recovery.
- `cmd/evener-hub/frontend/src/panes/session/ConversationBody.tsx`: conversation footer/composer integration across widths.
- `cmd/evener-hub/frontend/src/panes/session/Session.tsx`: pane identity and retained source/read-view binding.
- `cmd/evener-hub/frontend/src/shell/workspace.ts`, `paneLifetime.ts`, `routing.ts`, `mobile/StackHost.tsx`, and `DockHost.tsx`: placement, history and source lifetime.

The existing attention opener eventually calls `workspace.replacePrimary`. For a different primary, that function replaces the entire pane list. Removed records are disposed by `paneLifetime`. Wiring the new control directly to that path would violate the preservation requirement.

`openPane`, `focusPane` and `promotePane` retain existing records, but promotion demotes the prior primary into a secondary slot. A naive open/promote sequence can therefore add visible panes, and a subsequent route reconciliation can still replace the records. The implementation plan must resolve both placement and lifetime together. It must identify how attention history retains the source without creating a visible-pane backlog, and how Back/Forward restores an originating secondary/cascade reader. Do not claim that merely calling `openPane` before navigation solves this.

Keep the workspace and retained source/read-view lifetime as the authorities. Avoid a second composer, transcript, or attention store. A narrowly scoped attention-history binding may reference exact retained records; it must not duplicate their contents. If satisfying hidden retention requires changing the general workspace lifecycle or slot model, bring that architectural choice back before implementation rather than hiding a general redesign inside this feature.

### Hidden-retention decision

Jesse selected hidden retention after source inspection found no existing cross-host presentation state that could preserve the source without exposing another pane. Add an attention-only parked presentation under workspace ownership. The parked conversation keeps its exact pane lifetime, composer and reader, but neither host presents it as another pane or tab. Runtime browser-history bindings refer to those records; they do not copy drafts, attachments or transcript state. Prove this groundwork before adding the floating control.

Back and Forward restore only the affected presentation and exact reader. An already-presented destination is focused without rearranging it. Ordinary navigation and explicit Close retain their existing behavior. Close invalidates retained bindings; reload uses normal fresh-open/layout recovery and does not restore binary attachment state. Discarded Forward history releases parked records no longer referenced by any remaining attention entry. A host change must preserve the logical binding without replaying an entire workspace layout over independent panes.

Use existing Button/IconButton native-event forwarding for pointer and keyboard handling, and an existing controlled Popover or Sheet for the list. A controlled Popover can use its existing `closeOnScroll={false}` option so list scrolling does not dismiss it. Reposition/dismiss when its anchor becomes unavailable. The uncontrolled Menu trigger cannot implement held opening by itself. No new widgets or widget props without Jesse's approval.

## Proof required

Follow `docs/developing-evener/testing.md`. Tests assert behavior, identity, focus, state and geometry, not exact human-readable copy. Use deterministic scripted input at the provider boundary and real application code below it. No mocked end-to-end journeys.

1. **Shared action:** server ordering, current-session exclusion, wrap, cold data, page boundaries, valid empty results, failed reads, explicit retry, reconnect/invalidation recovery, and loaded-count versus total handling.
2. **Interaction:** tap, keyboard, More list, hold, movement/cancel/unmount, post-hold click suppression, stale selection, focus restoration, and leaving-then-returning while a request is pending.
3. **Visibility:** only the focused reader at every width; IME composition, question, approval, Find and modal suppression; automatic return afterward; a focused composer and a saved draft stay visible.
4. **Preservation:** a real Next → Next → Back → Forward journey retains structured draft and image bytes, pending attachment work, away-reading position and independent panes. Include existing targets, secondary/cascade origins, explicit source closure, and phone/desktop host swaps.
5. **Geometry:** 320, 390, 700, 899, 900, 1024 and 1400 CSS-pixel widths; long titles, list bounds, viewport/keyboard resize, unchanged Send/Stop hit targets, interaction with the New Content control, and reachable transcript-end content beneath the floating controls.
6. **Producer/recovery journey:** use the existing production-browser harness with real daemon/hub/SPA and only a scripted LLM. Include later-page targets, real reconnect after multiple pages, and no late navigation after explicit user movement.

Inspected entrypoints include AppShell/needsYouCycle tests, workspace/sessionPlacement/StackHost tests, retained Session/composer/read-view tests, and the existing shell, overflow, transcript-scroll and cascade browser guards. Existing passing tests are not evidence of this unimplemented feature. Add cases to the appropriate owners instead of creating an unrelated parallel harness.

Run targeted checks and the required focused browser journey locally. Full gates run in CI. Update the product navigation/session-experience responsibilities and linked preservation contracts in the same implementation change. Do not alter document-review behavior or deploy the hub.

## Delivery and remaining approval

1. Approve this behavior description and settle any consequential placement/lifetime choice the builder check exposes.
2. Write the implementation plan, including the smallest source-preserving navigation seam.
3. Implement and verify Next needs you before starting the separate unread-opening design.

No unread target semantics are decided here. That later design must distinguish real reading progress from the existing last-visit divider and settle what counts as the beginning of a reply and how failed/interrupted partial replies are treated.
