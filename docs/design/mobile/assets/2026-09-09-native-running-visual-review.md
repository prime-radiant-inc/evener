# Independent visual and hierarchy review: native iPhone running specimen

Date: 2026-09-09  
Source under review: native source `b9990e5c1`  
Evidence root: `/var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-native-running-obase0wi`

## Verdict

The specimen has a coherent native reading surface. The header, readable transcript, restrained user-message surface, full-width composer, and native sheets form a recognizable iPhone interaction model. Dark mode is especially calm and legible. Questions and approvals are understandable decisions with reachable primary actions. The remaining hierarchy problem is recovery: the person sees a generic bottom action while the explanatory state is elsewhere in a long transcript, and the recovery-header capture shows that action overlaid on the header rather than attached to the current decision.

This is a visual and interaction-fluency review of the supplied captures. It does not certify iPad, accessibility, physical-device behavior, performance, or states that were not captured.

## Prioritized findings

### P1 — Recovery action and explanation are separated

Evidence: `study-recovery-light.png`, `study-recovery-dark.png`, `study-recovery-header-dark.png`; AX captures show `Connection` at the composer while the reconnecting status and detailed context are hundreds of points above the current position. The header capture places `Reconnect` at the top while the draft remains at the bottom.

A returning person can see that something is wrong, but must infer what “Connection” means and scroll away from the draft to learn whether the next safe action is reconnecting, checking delivery, or reviewing an error. The current arrangement preserves the draft and avoids automatic replay, which is correct, but the safe next action is not local to the person’s current task.

Smallest bounded fix: keep the existing detailed header notice, but make the local action avoid timeline scrolling, but replace the generic composer label with a compact state-specific action group adjacent to the composer: `Reconnect` for a disconnected hub, `Check delivery` for an unconfirmed send, and `Review error` for a failed operation. Add one short subordinate status line beside that action when space allows. Do not add automatic resend or a second permanent toolbar. Verify that tapping the local action preserves the draft and reader anchor.

### P2 — Running composer has a clear primary action but weak secondary grouping

Evidence: `study-running-light.png`, `study-running-keyboard-light.png`, `study-running-keyboard-dark.png`; AX bounds show 44-point controls, with `Steer` at 77 points, `Stop` at 50, and `Queue` on a second wrapped row. The queue and goal summaries precede the input, while model, reasoning, and vision controls are compressed into a single footer row; the model value is visibly truncated to `stud…` in the keyboard captures.

The blue `Steer` action reads as primary and the writing field remains full width. However, the wrapped `Queue` action and compressed setting values make the secondary actions feel like a continuation of the primary row rather than a deliberate hierarchy. This matters most while the keyboard is open, when vertical space is scarce.

Smallest bounded fix: preserve the current full-width composer and native target sizes, but give the primary action row an explicit visual grouping and move queue depth into the quiet summary row or a clearly secondary trailing action. Keep full accessible labels and expose the complete selected model value in a sheet or tooltip on demand rather than expanding the footer. Confirm the real running-plus-queue-plus-goal combination before changing layout; do not apply this to the separate question state.

### P2 — Long approval paths compete with the decision

Evidence: `study-approval-sheet-dark.png` and AX bounds. The exact blocked path occupies four lines before the `Sandbox · restricted` explanation and the `Deny`/`Allow once` actions. The action pair remains visible and correctly sized, and the request is clearly attributed to Evener rather than the agent.

The safety content is honest, but the path is a poor first-read target on a phone. It increases scan cost and pushes the actual decision toward the lower half of the sheet.

Smallest bounded fix: show the final path component plus a concise parent-directory summary in the primary layout, with the exact absolute path available through a native disclosure or copy action. Keep the tool name, blocked/permission semantics, and one-action scope visible before the decision buttons. This preserves auditability without making the path the visual headline.

### P3 — Decision-sheet keyboard capture is a non-defect

Evidence: `study-question-sheet-light.png`, `study-question-sheet-dark.png`, `study-question-keyboard-scroll-dark.png`; AX bounds show a 44-point `Send answers` button reachable after one scroll with the keyboard open. The selected recommendation, alternative, note field, answered count, and submission action are understandable.

The selected option is partially clipped after the deliberate scroll beneath the pinned header. This is normal scroll behavior and is not a fix target; the action remains reachable and the capture proves submission reachability.

No fix is recommended. The selected option is partially clipped only after deliberate scrolling beneath the pinned header; the action remains reachable and submitted answers were observed separately.

## Appearance and native sizing

The stable dark captures use the intended dark canvas and surface tones with strong text separation. The idle reader is sparse without losing hierarchy; the user message, transcript heading, code block, and composer are distinct without decorative cards around ordinary prose. Light running captures show the same geometry and readable hierarchy.

The global gray/dim treatment in `study-running-keyboard-light.png` and `study-approval-sheet-light.png` is treated as an appearance/native transition capture, not a confirmed contrast defect. It should be rechecked only with a stable appearance capture. The approval dark capture is the reliable contrast reference for that sheet.

Header back and overflow controls, composer controls, queue controls, question submission, and approval actions meet the supplied 44-point native target evidence. The question and approval sheets use a clear title, originating session identity, explicit action labels, and native spacing. The queue sheet presents ordering and the consequence of steering clearly.

## State and evidence boundaries

The running keyboard captures show queued work plus an active goal and a draft. The question captures show a separate question-plus-goal flow; I do not combine these into one asserted state. The supplied study records that queued content auto-drained, so the queue sheet is treated as its own observed state.

The approval write was observed and its exact path is retained in the specimen evidence. The fault study preserved an ordinary unsent draft; no unconfirmed submission or lost acknowledgement was observed. The recovery action then jumped to the transcript header; that behavior is the basis for the P1 finding.

No claims are made about iPad, dedicated accessibility work, physical network performance, or simulator behavior beyond the supplied captures and AX records.


## Factual corrections

The fault study contains an ordinary unsent draft and no unconfirmed submission. Provider request count remained 7, which proves no unsolicited send in that study; it does not prove replay protection for a genuinely lost acknowledgement. Recovery did preserve the draft in the supplied captures, but the visual evidence does not establish the lost-acknowledgement branch.

The recovery recommendation is specifically to keep the local action near the composer without jumping the timeline to the header. The existing header detail can remain available, but the local action must preserve the reader anchor so the person can act without context loss.
