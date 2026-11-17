// Shared "what time is it, and what does this wire status mean for
// Cadence" helpers. Hoisted out of Session.tsx so the session pane and its
// transcript descendants can share the same logic without importing the
// pane back into this leaf module.
import { createContext, useContext, useState } from "react";
import { usePaneDocument } from "../../shell/paneDocument";
import { useVisibleInterval } from "../../shell/useVisibleInterval";

// The session clock every relative-age display shares. A timestamp that
// rewrites itself every few seconds reads as churn, so the floor is 5s:
// coarse enough that the reader sees a label settle, fine enough that ages
// still feel live. Cadence's decay is derived from each frame's wall-clock
// age, so a slower tick only delays when a step appears, never how many
// occur.
export const NOW_TICK_MS = 5_000;

// Work time is a different display granularity, so it owns a different
// clock: formatWorkDuration shows whole seconds below a minute, and a 5s
// clock would make that counter skip four values at a time ("14s", then
// "19s"). This drives only the readouts that count seconds.
export const WORK_TICK_MS = 1_000;

// Session owns the live clock; transcript descendants only consume it. The
// fixed default keeps standalone renders deterministic and timer-free.
export const SessionNowContext = createContext(Date.now());

export function useSessionNow(): number {
  return useContext(SessionNowContext);
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
  const ownerDocument = usePaneDocument();
  // It is a rendering clock: nothing on screen is being read while the tab is
  // hidden, and the resume tick is what keeps the first visible frame's ages
  // honest. It watches the document the enclosing pane lives in, so a session
  // moved to a popout keeps ticking when the opener's tab hides.
  useVisibleInterval(() => setNow(Date.now()), intervalMs, true, ownerDocument);
  return now;
}
