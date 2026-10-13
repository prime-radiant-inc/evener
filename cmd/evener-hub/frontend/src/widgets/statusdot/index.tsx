import type { DelegateDotFields, DelegateDotState } from "@evener/appwire-client";
import { delegateDotState } from "@evener/appwire-client";
import type { CadenceState } from "../cadence";
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

/** The one session-status vocabulary: StatusDot's accessible names, reused
 * by any text that names a session's state (a hover card's status line). */
export const STATUS_LABEL: Record<CadenceState, string> = {
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
  const family = STATE_FAMILY[state];
  const working = state === "working" ? ` ${BASE_CLASS.working}` : "";
  const attrs = {
    "data-state": state,
    "data-shape": state === "failed" ? "diamond" : "dot",
    className: `${BASE_CLASS.dot} ${FAMILY_CLASS[family]}${working}`,
  };
  if (decorative) {
    return <span aria-hidden="true" {...attrs} />;
  }
  return <span role="img" aria-label={STATUS_LABEL[state]} {...attrs} />;
}

// A subagent row's dot: the main sessions' status-dot vocabulary - a working
// row spins, a failure is a diamond - with one state the session vocabulary
// doesn't have: a running row gone quiet dims instead of disappearing,
// because a quiet run is still open work.
const SUBAGENT_DOT: Record<
  DelegateDotState,
  { family: Family; label: string; shape: "dot" | "diamond"; working: boolean } | null
> = {
  // The shared states speak the session vocabulary's own words and hues, so a
  // session-status ruling reaches the subagent dot with no second table to
  // update; quiet - the one state the session set does not have - is ours.
  fresh: { family: STATE_FAMILY.working, label: STATUS_LABEL.working, shape: "dot", working: true },
  quiet: { family: "neutral", label: "Quiet", shape: "dot", working: false },
  attention: { family: STATE_FAMILY["needs-you"], label: STATUS_LABEL["needs-you"], shape: "dot", working: false },
  failed: { family: STATE_FAMILY.failed, label: STATUS_LABEL.failed, shape: "diamond", working: false },
  // A settled row shows no dot, exactly as the main sessions' idle and
  // ended scopes show no glyph.
  idle: null,
};

/** A subagent row's leading dot, from the shared quiet-encoding rule: the
 * alive dot spins while the row is fresh, dims once its run goes quiet, reads
 * amber for attention and a red diamond for failure, and disappears when the
 * row settles. `labeled` makes the dot name its state where the row does not
 * already carry the word somewhere a screen reader meets it. */
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
  const dot = SUBAGENT_DOT[state];
  if (!dot) return null;
  const working = dot.working ? ` ${BASE_CLASS.working}` : "";
  return (
    <span
      role="img"
      aria-label={dot.label}
      aria-hidden={labeled ? undefined : "true"}
      data-state={state}
      data-shape={dot.shape}
      className={`${BASE_CLASS.dot} ${FAMILY_CLASS[dot.family]}${working}`}
    />
  );
}
