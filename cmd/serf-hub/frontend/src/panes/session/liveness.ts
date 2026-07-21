// Shared "what time is it, and what does this wire status mean for
// Cadence" helpers. Hoisted out of Session.tsx (their original home) so
// transcript/tools/watchedChild.tsx - the subagent module's per-row watched-
// child indicator - can use the SAME logic instead of carrying its own
// verbatim copy (file-ownership-forced during the parallel wave-4 streams).
// A direct `import ... from "../../Session"` from watchedChild.tsx would
// create a cycle (Session.tsx -> "./transcript/tools" -> subagentModule.tsx
// -> watchedChild.tsx -> back to Session.tsx), so this lives in its own
// leaf module instead - it imports nothing from panes/session/ itself.
import { useEffect, useState } from "react";
import type { CadenceState } from "../../widgets";

// Same interval as the legacy renderer's own liveness tick
// (cmd/serf-hub/assets/renderer.js LIVENESS_TICK_MS=3000) - fine-grained
// enough that Cadence's tick decay visibly advances promptly, coarse enough
// to be a non-issue re-rendering cost-wise.
export const NOW_TICK_MS = 3_000;

// cadenceStateForStatus maps the WIRE ThreadStatus.type vocabulary
// (appwire/types.go's constants: idle/active/awaiting/warning/closed/
// notLoaded/systemError - ThreadModel.status.type carries this straight
// through, see reducer.ts) onto Cadence's four-family state space.
// Deliberately a SEPARATE function from shell/rail/RailRow.tsx's own
// cadenceStateFor: that one consumes hubcore.NormalizeState's ALREADY-
// remapped output (closed->ended, systemError->errored folded in) from the
// REST /api/tree snapshot, not the raw wire vocabulary a live ThreadModel
// carries - collapsing the raw wire vocabulary straight to CadenceState in
// one hop here mirrors NormalizeState's own remapping without making
// either caller depend on shell/rail's module for it.
export function cadenceStateForStatus(type: string): CadenceState {
  switch (type) {
    case "systemError":
      return "failed";
    case "awaiting":
    case "warning":
      return "needs-you";
    case "active":
      return "working";
    case "closed":
      return "ended";
    default: // "idle", "notLoaded", "", and any future/unknown value
      return "idle";
  }
}

// useNowTick is the one thing that owns a live decay clock for Cadence: it
// is a pure prop-driven render (widgets/cadence's own doc comment - "no
// timers, no Date.now()"), so something above it has to own the ticks that
// make its trace visibly decay even between live frames. Transient by
// design - unmounting drops the interval and a remount just starts a fresh
// one from the current instant, which is exactly right for a pure "what
// time is it" signal.
export function useNowTick(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}
