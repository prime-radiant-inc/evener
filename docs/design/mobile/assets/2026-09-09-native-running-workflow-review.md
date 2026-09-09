# Native running workflow review

Method: independent, read-only review of the current native iPhone specimen using the named PNG captures, their AX JSON, the iPhone UX contract, the 2026-09-08 panel, and the Impeccable critique guidance. No simulator or detector rerun was used.

## Verdict

The specimen supports the core workflow. Running work keeps the draft visible and exposes `1 queued`, `Goal · active`, `Steer`, `Stop`, and `Queue`. The queue sheet states ordering and makes cancellation versus promotion explicit. The question sheet identifies the requested decision, shows the selected recommendation and its alternative, reports `1 of 1 answered`, and exposes an enabled `Send answers` action; the keyboard-open capture shows that action reachable after one scroll. The approval sheet names the blocked tool, sandbox consequence, and one-action scope with `Deny` and `Allow once`. The recovered state shows `Connected`, an enabled `Send`, and the preserved draft.

The only proven workflow defect is recovery action clarity at the composer. It is a bounded P2 issue, already identified by the design panel, and should be the smallest next native batch.

## Proven issue

### P2 — Footer `Connection` is a navigation proxy, not the recovery action

In both recovery captures, the footer presents an enabled `Connection` control while the actual `Reconnect` action is in the timeline header, offscreen above the current reading position. The header capture confirms that `Reconnect` is the actionable control. The current source in `mobile-native/src/screens.tsx` defines `ConnectionStatus` with that header action (around lines 147–163), renders it in the list header (around lines 2066–2069), and implements the footer branch (around lines 2444–2464) by dismissing the keyboard and scrolling to offset zero. Thus a person who taps the visible footer control must find and interpret a second control before recovery can begin. This is a concrete extra navigation step and can move the person away from the reading anchor.

Smallest useful batch:

1. Give the footer a concise state-specific label and action that matches its consequence, with reconnecting/disconnected state distinct from uncertain delivery.
2. Keep the detailed header notice available, but preserve the reader anchor when opening and returning.
3. Retain the draft and do not add automatic resend; `Check delivery` must remain a separate safe action for uncertain delivery.

## What the evidence does not support as a defect

- The running queue, goal, and composer controls are simultaneously readable in the supplied state. The queue and question flows are separate reachable states in the supplied run record; the queue was drained after the question flow. No action collision is proven by these captures.
- The question `Send answers` action is reachable with the keyboard open and was successfully submitted in the supplied run record. The selected recommendation and its consequence are understandable.
- The approval copy clearly explains the blocked action and the one-action permission consequence. The light approval capture is globally dimmed like a transition frame, so it is not valid evidence for persistent light-mode contrast; the dark stable frame is readable.
- The model value is visually shortened to `stud...` in the busy composer, while AX exposes the full model label. This is a minor recognition observation, not a blocking workflow finding in this review.

## Evidence limits

Screenshots and AX establish composition, labels, enabled state, and reachability in the captured states. They cannot establish tap latency, physical-device/network performance, provider request counts, persistence across process termination, arbitrary rotation/backgrounding, dynamic type behavior, or all reachable combinations. The supplied recovery run record reports draft preservation and zero extra provider requests; those are runtime claims rather than facts provable from the images alone. The globally dimmed light captures should not be used to diagnose a persistent contrast failure without a stable capture.
