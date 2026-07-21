import type { CadenceState } from "../cadence";
import { requireClass } from "../internal/requireClass";
import styles from "./statusdot.module.css";

export interface StatusDotProps {
  state: CadenceState;
}

// Mirrors Cadence's own state -> family -> label mapping (see
// src/widgets/cadence/index.tsx). Not shared code: Cadence keeps its
// mapping private (unexported), and its directory is out of scope for this
// stream, so StatusDot - a plain standalone status dot built for contexts
// that don't want Cadence's full activity trace - carries its own copy.
type Family = "alive" | "attention" | "danger" | "neutral";

const STATE_FAMILY: Record<CadenceState, Family> = {
  working: "alive",
  "needs-you": "attention",
  failed: "danger",
  idle: "neutral",
  ended: "neutral",
};

const STATE_LABEL: Record<CadenceState, string> = {
  idle: "Idle",
  working: "Working",
  "needs-you": "Needs you",
  failed: "Failed",
  ended: "Ended",
};

const FAMILY_CLASS: Record<Family, string> = {
  alive: requireClass(styles.alive, "statusdot.module.css", "alive"),
  attention: requireClass(styles.attention, "statusdot.module.css", "attention"),
  danger: requireClass(styles.danger, "statusdot.module.css", "danger"),
  neutral: requireClass(styles.neutral, "statusdot.module.css", "neutral"),
};

const BASE_CLASS = {
  dot: requireClass(styles.dot, "statusdot.module.css", "dot"),
};

/** A standalone session-state indicator: just the dot, no activity trace.
 * Use Cadence instead where the trailing ~60s trace is wanted; StatusDot is
 * for tighter contexts (tree rows, compact lists) that only have room for
 * the dot. Passive - no interaction, no focus ring - but carries its own
 * accessible name since (unlike Cadence's dot) nothing else labels it. */
export function StatusDot({ state }: StatusDotProps) {
  const family = STATE_FAMILY[state];
  return <span role="img" aria-label={STATE_LABEL[state]} className={`${BASE_CLASS.dot} ${FAMILY_CLASS[family]}`} />;
}
