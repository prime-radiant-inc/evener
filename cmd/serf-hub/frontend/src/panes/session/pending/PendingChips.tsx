// Optimistic send/steer/drain chips beside the composer (wave 8 T4 fills; T1
// mounts this stub in Session.tsx so the seam exists once and T4 never touches
// the Session chokepoint). T4 reads usePendingTurnEntries(sessionRef) for
// methods send|steer|drain (the "queue" method is already chipped by
// QueueStrip) and renders one compact chip per pending entry, reconciled and
// reaped entirely by the EXISTING 10s pendingTurnsStore logic - it adds NO new
// store state. Until then this renders nothing.
import type { JSX } from "react";

// Returns null while empty - both here (the T1 stub) and, honestly, in T4's
// real fill: with no pending send/steer/drain entries there is nothing to
// render beside the composer, so the component's true type is JSX.Element |
// null, not the seam block's shorthand JSX.Element.
export function PendingChips(_props: { sessionRef: string }): JSX.Element | null {
  return null;
}
