// Shared "what time is it, and what does this wire status mean for
// Cadence" helpers. Hoisted out of Session.tsx so the session pane and its
// transcript descendants can share the same logic without importing the
// pane back into this leaf module.
import { createContext, useContext, useEffect, useState } from "react";
import type { CadenceState } from "../../widgets";

// Same interval as the legacy renderer's own liveness tick
// (cmd/evener-hub/assets/renderer.js LIVENESS_TICK_MS=3000) - fine-grained
// enough that Cadence's tick decay visibly advances promptly, coarse enough
// to be a non-issue re-rendering cost-wise.
export const NOW_TICK_MS = 3_000;

// Session owns the live clock; transcript descendants only consume it. The
// fixed default keeps standalone renders deterministic and timer-free.
export const SessionNowContext = createContext(Date.now());

export function useSessionNow(): number {
  return useContext(SessionNowContext);
}

// cadenceStateForStatus maps the WIRE ThreadStatus.type vocabulary
// (appwire/types.go's constants: idle/active/awaiting/warning/closed/
// notLoaded/systemError - ThreadModel.status.type carries this straight
// through, see reducer.ts) onto Cadence's four-family state space.
// Separate from shell/SessionStatusIndicator.tsx's cadenceStateFor, which
// consumes hubcore.NormalizeState's navigation summary (closed->ended,
// systemError->errored). A live ThreadModel carries raw wire values, so this
// mapper mirrors that normalization without depending on the shell renderer.
export function cadenceStateForStatus(type: string): CadenceState {
  switch (type) {
    case "systemError":
      return "failed";
    case "awaiting":
    case "warning":
    case "restartRequired":
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
