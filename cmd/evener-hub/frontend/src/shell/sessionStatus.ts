import type { CadenceState } from "../widgets";

// Navigation summaries carry NormalizeState's values, not raw ThreadStatus.type.
export function cadenceStateFor(wireState: string): CadenceState {
  switch (wireState) {
    case "errored":
      return "failed";
    case "awaiting":
    case "warning":
    case "restartRequired":
      return "needs-you";
    case "active":
      return "working";
    case "ended":
      return "ended";
    default: // "idle", "notLoaded", "", and any future/unknown value
      return "idle";
  }
}

// The states worth spending a StatusDot on in compact session chrome (rail
// rows, pane tabs, ancestor spines): a session is working, needs a human, or
// failed. Quiet sessions have no glyph, so work and attention stay easy to scan.
export const SIGNAL_STATES: ReadonlySet<CadenceState> = new Set<CadenceState>(["working", "needs-you", "failed"]);
