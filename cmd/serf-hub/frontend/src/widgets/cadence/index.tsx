import { requireClass } from "../internal/requireClass";
import styles from "./cadence.module.css";

export type CadenceState = "idle" | "working" | "needs-you" | "failed" | "ended";

export interface CadenceProps {
  state: CadenceState;
  /** Epoch-ms timestamp of each frame's arrival. Unsorted is fine. */
  frameTimes: number[];
  /** Epoch-ms "current" instant ticks decay relative to. Never read from
   * Date.now() internally — Cadence is a pure render of its props, so the
   * caller (which does own a clock) controls decay. */
  now: number;
}

const WINDOW_MS = 60_000; // "the last ~60s of frame arrivals"
const BUCKET_COUNT = 4;
const BUCKET_MS = WINDOW_MS / BUCKET_COUNT;
const VIEWBOX_WIDTH = 24;
const VIEWBOX_HEIGHT = 10;
const TICK_WIDTH = 1.2;

const STATE_LABELS: Record<CadenceState, string> = {
  idle: "Idle",
  working: "Working",
  "needs-you": "Needs you",
  failed: "Failed",
  ended: "Ended",
};

type Family = "alive" | "attention" | "danger" | "neutral";

const STATE_FAMILY: Record<CadenceState, Family> = {
  working: "alive",
  "needs-you": "attention", // dot AND trailing (freshest) ticks go amber
  failed: "danger",
  idle: "neutral", // "quiet" — ticks (if any) read as already-aged
  ended: "neutral",
};

const BASE_CLASS = {
  cadence: requireClass(styles.cadence, "cadence.module.css", "cadence"),
  dot: requireClass(styles.dot, "cadence.module.css", "dot"),
  trace: requireClass(styles.trace, "cadence.module.css", "trace"),
  tick: requireClass(styles.tick, "cadence.module.css", "tick"),
};

const FAMILY_CLASS: Record<Family, string> = {
  alive: requireClass(styles.alive, "cadence.module.css", "alive"),
  attention: requireClass(styles.attention, "cadence.module.css", "attention"),
  danger: requireClass(styles.danger, "cadence.module.css", "danger"),
  neutral: requireClass(styles.neutral, "cadence.module.css", "neutral"),
};

const BUCKET_CLASS: readonly [string, string, string, string] = [
  requireClass(styles.age0, "cadence.module.css", "age0"),
  requireClass(styles.age1, "cadence.module.css", "age1"),
  requireClass(styles.age2, "cadence.module.css", "age2"),
  requireClass(styles.age3, "cadence.module.css", "age3"),
];

function bucketOf(age: number): 0 | 1 | 2 | 3 {
  const bucket = Math.floor(age / BUCKET_MS);
  return Math.min(BUCKET_COUNT - 1, Math.max(0, bucket)) as 0 | 1 | 2 | 3;
}

interface Tick {
  key: string;
  x: number;
  bucket: 0 | 1 | 2 | 3;
}

function ticksFor(frameTimes: number[], now: number): Tick[] {
  return frameTimes
    .map((frameTime, i): Tick | null => {
      const age = now - frameTime;
      if (age < 0 || age > WINDOW_MS) return null; // outside the trace window
      return {
        key: `${frameTime}-${i}`,
        // oldest at the left edge, "now" at the right edge
        x: (VIEWBOX_WIDTH - TICK_WIDTH) * (1 - age / WINDOW_MS),
        bucket: bucketOf(age),
      };
    })
    .filter((tick): tick is Tick => tick !== null)
    // ascending x = oldest (smallest x) first, freshest (largest x) last,
    // so the freshest tick paints on top and a dense burst reads clearly
    .sort((a, b) => a.x - b.x);
}

/**
 * The signature widget: a state dot plus a 24x10px trace of the last ~60s
 * of frame arrivals as fading vertical ticks. Rendered everywhere a
 * session appears (tree row, pane header, mobile card).
 *
 * Pure: no timers, no Date.now(). It only ever changes what it shows when
 * React re-renders it with new props, so a stalled agent shows honest
 * decay instead of faked liveness.
 */
export function Cadence({ state, frameTimes, now }: CadenceProps) {
  const familyClass = FAMILY_CLASS[STATE_FAMILY[state]];
  const ticks = ticksFor(frameTimes, now);

  return (
    <span className={BASE_CLASS.cadence}>
      <span data-testid="cadence-dot" aria-hidden="true" className={`${BASE_CLASS.dot} ${familyClass}`} />
      <svg className={BASE_CLASS.trace} viewBox={`0 0 ${VIEWBOX_WIDTH} ${VIEWBOX_HEIGHT}`} role="img">
        <title>{STATE_LABELS[state]}</title>
        {ticks.map((tick) => (
          <rect
            key={tick.key}
            className={`${BASE_CLASS.tick} ${familyClass} ${BUCKET_CLASS[tick.bucket]}`}
            x={tick.x}
            y={0}
            width={TICK_WIDTH}
            height={VIEWBOX_HEIGHT}
          />
        ))}
      </svg>
    </span>
  );
}
