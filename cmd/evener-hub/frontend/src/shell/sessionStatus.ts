import { attentionNeedsYou, attentionState, normalizeWireState } from "@evener/appwire-client";
import type { CadenceState } from "../widgets";

// Cadence's family for a wire state: the raw ThreadStatus.type or a navigation
// summary's NormalizeState value (attentionState reads both).
export function cadenceStateFor(wireState: string): CadenceState {
  const attention = attentionState(wireState);
  if (attentionNeedsYou(attention)) return "needs-you";
  switch (attention) {
    case "failed":
      return "failed";
    case "working":
      return "working";
    case "shutDown":
      // notLoaded has no session to end: it rests like idle.
      return normalizeWireState(wireState) === "ended" ? "ended" : "idle";
    default:
      return "idle";
  }
}

// The states worth spending a StatusDot on in compact session chrome (rail
// rows, pane tabs, ancestor spines): a session is working, needs a human, or
// failed. Quiet sessions have no glyph, so work and attention stay easy to scan.
export const SIGNAL_STATES: ReadonlySet<CadenceState> = new Set<CadenceState>(["working", "needs-you", "failed"]);
