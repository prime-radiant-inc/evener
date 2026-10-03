import type { CadenceState } from "../widgets";
import { requireClass } from "../widgets/internal/requireClass";
import styles from "./SessionStatusIndicator.module.css";

const CLASS = {
  statusDot: requireClass(styles.statusDot, "SessionStatusIndicator.module.css", "statusDot"),
  statusSpinner: requireClass(styles.statusSpinner, "SessionStatusIndicator.module.css", "statusSpinner"),
};

// Session-list signal families also apply to each cascade scope's runtime.
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

// Quiet sessions have no glyph, so work and attention remain easy to scan.
export const SIGNAL_STATES: ReadonlySet<CadenceState> = new Set<CadenceState>(["working", "needs-you", "failed"]);

export const CADENCE_LABEL: Record<CadenceState, string> = {
  working: "Running",
  "needs-you": "Needs you",
  failed: "Broken",
  ended: "Ended",
  idle: "Idle",
};

export function SessionStatusIndicator({
  state,
  testIdPrefix = "session",
}: {
  state: CadenceState;
  testIdPrefix?: string;
}) {
  if (!SIGNAL_STATES.has(state)) return null;
  return (
    <span
      role="img"
      aria-label={CADENCE_LABEL[state]}
      data-testid={`${testIdPrefix}-status-${state === "working" ? "spinner" : "dot"}`}
      data-status={state === "working" ? undefined : state}
      className={state === "working" ? CLASS.statusSpinner : CLASS.statusDot}
    />
  );
}
