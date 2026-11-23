import type { DelegateDotFields, DelegateDotState } from "@evener/appwire-client";
import { delegateDotState } from "@evener/appwire-client";
import type { CadenceState } from "../cadence";
import { STATE_LABELS, type StatusFamily, stateFamily } from "../cadence";
import { requireClass } from "../internal/requireClass";
import styles from "./statusdot.module.css";

export interface StatusDotProps {
  state: CadenceState;
  /** Render the glyph as a decoration with no role or accessible name when the
   * caller's own adjacent text already names the status (the compact ancestor
   * spine's exact runtime label). The dot, its geometry and its motion are
   * unchanged; only the extra accessible name goes away. Default false keeps
   * the standalone name. */
  decorative?: boolean;
}

const FAMILY_CLASS: Record<StatusFamily, string> = {
  alive: requireClass(styles.alive, "statusdot.module.css", "alive"),
  attention: requireClass(styles.attention, "statusdot.module.css", "attention"),
  danger: requireClass(styles.danger, "statusdot.module.css", "danger"),
  neutral: requireClass(styles.neutral, "statusdot.module.css", "neutral"),
};

const BASE_CLASS = {
  dot: requireClass(styles.dot, "statusdot.module.css", "dot"),
  working: requireClass(styles.working, "statusdot.module.css", "working"),
};

/** A standalone session-state indicator: just the dot, no activity trace.
 * The working state turns as a spinner (a static dot under reduced motion)
 * so an active agent always reads as active. Failed is a diamond so it never
 * depends on hue alone to differ from needs-you.
 * Use Cadence instead where the trailing ~60s trace is wanted; StatusDot is
 * for tighter contexts (tree rows, compact lists) that only have room for
 * the dot. Passive - no interaction, no focus ring - but carries its own
 * accessible name since (unlike Cadence's dot) nothing else labels it.
 * A caller that already names the status (the compact ancestor spine's exact
 * runtime label) passes `decorative`, which drops the role and the name so the
 * glyph is not announced a second time. */
export function StatusDot({ state, decorative = false }: StatusDotProps) {
  const family = stateFamily(state);
  const working = state === "working" ? ` ${BASE_CLASS.working}` : "";
  const attrs = {
    "data-state": state,
    "data-shape": state === "failed" ? "diamond" : "dot",
    className: `${BASE_CLASS.dot} ${FAMILY_CLASS[family]}${working}`,
  };
  if (decorative) {
    return <span aria-hidden="true" {...attrs} />;
  }
  return <span role="img" aria-label={STATE_LABELS[state]} {...attrs} />;
}

// A subagent row's dot: the main sessions' status-dot vocabulary - a working
// row spins, a failure is a diamond. The shared quiet state dims on native,
// but the web keeps an open run's working ring until terminal settlement
// (#4521 superseded): a >20s tool call, reasoning turn or child wait is still
// active work, never a dead grey dot.
const SUBAGENT_DOT: Record<
  Exclude<DelegateDotState, "quiet">,
  { family: StatusFamily; label: string; shape: "dot" | "diamond"; working: boolean } | null
> = {
  // The shared states speak the session vocabulary's own words and hues, so a
  // session-status ruling reaches the subagent dot with no second table to
  // update. Quiet is normalized to fresh before lookup.
  fresh: { family: stateFamily("working"), label: STATE_LABELS.working, shape: "dot", working: true },
  attention: { family: stateFamily("needs-you"), label: STATE_LABELS["needs-you"], shape: "dot", working: false },
  failed: { family: stateFamily("failed"), label: STATE_LABELS.failed, shape: "diamond", working: false },
  // A settled row shows no dot, exactly as the main sessions' idle and
  // ended scopes show no glyph.
  idle: null,
};

/** A subagent row's leading dot, from the shared quiet-encoding rule: the
 * alive dot keeps spinning while the row is open, reads amber for attention
 * and a red diamond for failure, and disappears when the row settles.
 * `labeled` makes the dot name its state where the row does not already carry
 * the word somewhere a screen reader meets it. */
export function SubagentDot({
  delegate,
  now,
  labeled = false,
}: {
  delegate: DelegateDotFields;
  now: number;
  labeled?: boolean;
}) {
  const state = delegateDotState(delegate, now);
  // The web never dims an open run: normalize the shared quiet state to fresh
  // so the dot's attributes, class and spinner stay the working ones.
  const effectiveState = state === "quiet" ? "fresh" : state;
  const dot = SUBAGENT_DOT[effectiveState];
  if (!dot) return null;
  const working = dot.working ? ` ${BASE_CLASS.working}` : "";
  return (
    <span
      role="img"
      aria-label={dot.label}
      aria-hidden={labeled ? undefined : "true"}
      data-state={effectiveState}
      data-shape={dot.shape}
      className={`${BASE_CLASS.dot} ${FAMILY_CLASS[dot.family]}${working}`}
    />
  );
}
