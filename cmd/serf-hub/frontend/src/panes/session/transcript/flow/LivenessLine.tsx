// LivenessLine is the honest, quiet liveness indicator for the transcript
// pane: "Quiet ~30s" rolling to "May be stalled - no updates for 3m 5s",
// driven purely by describeLiveness (see liveness.ts). Pure/prop-driven,
// same contract as widgets/cadence's own Cadence ("no timers, no
// Date.now()") - `now` is Session.tsx's own useNowTick value, already
// plumbed there for Cadence, so this never starts a second clock. Renders
// nothing while level is "none" (fresh/inactive), and deliberately carries
// no animation of its own - Cadence's trace already conveys activity; this
// line's entire job is to say something honest when that activity stops.

import { requireClass } from "../../../../widgets/internal/requireClass";
import { describeLiveness } from "./liveness";
import styles from "./livenessline.module.css";

export interface LivenessLineProps {
  /** ThreadModel.lastFrameAt - epoch ms of the most recent live frame. */
  lastFrameAt: number;
  /** Epoch-ms "current" instant; caller-owned clock, same as Cadence's own `now`. */
  now: number;
  /** Only "active" threads show a liveness line at all (matches the legacy gate). */
  active: boolean;
}

const CLASS = {
  line: requireClass(styles.line, "livenessline.module.css", "line"),
  stalled: requireClass(styles.stalled, "livenessline.module.css", "stalled"),
};

export function LivenessLine({ lastFrameAt, now, active }: LivenessLineProps) {
  const { level, text } = describeLiveness(now - lastFrameAt, active);
  if (level === "none" || text === null) return null;

  return (
    <div data-testid="liveness-line" className={level === "stalled" ? `${CLASS.line} ${CLASS.stalled}` : CLASS.line}>
      {text}
    </div>
  );
}
