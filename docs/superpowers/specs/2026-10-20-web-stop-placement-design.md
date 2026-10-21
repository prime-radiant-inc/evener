# Native-style Stop placement on the web

## Intent and scope

Jesse chose the native composer's Stop placement for **every web width**.
Keep a running turn easy to stop without adding a second composer action.
Document review is excluded because another session owns that work.

## Behavior

- These placement rules apply when the ordinary composer is displayed.
  Preserve the pending-question surface, which hides the ordinary input card
  and its menu. Returning from that surface restores the current draft's slot.
- When the existing session controls permit Stop and the draft is empty,
  Stop occupies Send's slot. The composer renders one of these buttons,
  never both.
- Any sendable draft restores Send: text, images (including images being
  prepared), selected skills or selected commands. Whitespace alone remains
  empty, matching the web composer's existing content rule.
- Stop remains in the session menu whenever the existing session controls
  permit it, including while a draft occupies the composer, at every width.
- Keep Steer's existing placement: inline on wide composers, in the session
  menu on narrow composers. Keep its draft and queued-message behavior.
- Keep the current Stop handler, capability checks, pending gates and error
  recovery. A refused or failed Stop preserves the draft and leaves healthy
  actions usable. Nothing here changes cancellation or durable-input ownership.
- Idle, finished and otherwise non-stoppable sessions retain Send and its
  existing eligibility and recovery behavior. Empty submission never stops a
  turn, whether submitted by Enter, a keyboard chord or the form.
- Keep the existing widgets, accessible control names and Send focus handoff.
  Distinct button identities prevent a press already in progress on Send from
  finishing on a replacement Stop button. The swap must not steal editor focus.
- When Send changes to Stop, show Stop immediately but disable that inline
  button for 500 ms, so an accidental double-tap cannot interrupt. Jesse requested
  the brief delay; 500 ms is the implementation choice. A Stop already displayed
  when the composer opens needs no delay. The session menu's Stop keeps its
  existing pending gates and remains available during the inline delay.
- The delay ends automatically and must not outlive the slot or session it
  belongs to. Existing pending gates can keep Stop disabled beyond the delay.

## Implementation boundary

Change the web composer and its menu offerings, their behavior tests, affected
browser guards and examples, and owning web/product documentation. Use the
existing `hasContent`, `controlsFor` and interrupt handler rather than a new
state machine. Do not change native code, the protocol, runtime cancellation,
document review, widget APIs, Send routing or the narrow Steer breakpoint.

## Verification

1. Add failing tests through the real composer, editor and thread store with
   the network fake at the AppWire boundary. Cover empty, whitespace, text,
   image-only, skill-only and command-only drafts at narrow and wide widths.
2. Exercise typing and clearing, adding and removing images, and active-to-idle
   transitions. Check real control identity, eligibility and menu reachability,
   not label phrasing. Enter and leave the pending-question surface with a
   preserved draft and verify the restored slot.
3. Exercise Stop from the composer and the menu, with a held request and a
   rejection. Assert one interrupt per action, pending gating, draft retention
   and a usable retry. Assert empty form/keyboard submission sends no interrupt.
4. Preserve existing queue, steer, live-capability, recovery and focus assertions
   when adapting tests that previously expected two simultaneous buttons.
5. Run the affected real-browser guard at narrow and wide widths, including
   measured composer content boxes of 399 and 400px and a narrow pane within a
   desktop viewport. State expected controls independently of the production
   breakpoint. Inspect both themes for overflow and keyboard access. Use trusted
   pointer input to verify that a press spanning Send's replacement does not
   interrupt. Double-tap Send across a fast successful submission and assert one
   submission and zero interrupts. After the 500 ms delay, a deliberate Stop must
   remain usable. Cover delay boundaries and timer cancellation with fake-clock
   tests, without sleeps or a longer-than-needed request timeout.
6. Run targeted frontend tests, typecheck and touched-file formatting locally;
   let CI run the full gates before merge.
