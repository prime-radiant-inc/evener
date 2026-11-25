import type { CSSProperties, ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./meter.module.css";

export type MeterTone = "neutral" | "attention" | "alive" | "danger";
export type MeterVariant = "bar" | "ring";

export interface MeterProps {
  /** role=meter requires an accessible name; required (not optional) so a
   * Meter can't ship without one. Rendered as aria-label. */
  label: string;
  value: number;
  max: number;
  tone?: MeterTone;
  variant?: MeterVariant;
}

const BASE_CLASS = {
  track: requireClass(styles.track, "meter.module.css", "track"),
  fill: requireClass(styles.fill, "meter.module.css", "fill"),
  ring: requireClass(styles.ring, "meter.module.css", "ring"),
  ringSvg: requireClass(styles.ringSvg, "meter.module.css", "ringSvg"),
  ringTrack: requireClass(styles.ringTrack, "meter.module.css", "ringTrack"),
  ringFill: requireClass(styles.ringFill, "meter.module.css", "ringFill"),
  ringFillEmpty: requireClass(styles.ringFillEmpty, "meter.module.css", "ringFillEmpty"),
};

const TONE_CLASS: Record<MeterTone, string> = {
  neutral: requireClass(styles.fillNeutral, "meter.module.css", "fillNeutral"),
  attention: requireClass(styles.fillAttention, "meter.module.css", "fillAttention"),
  alive: requireClass(styles.fillAlive, "meter.module.css", "fillAlive"),
  danger: requireClass(styles.fillDanger, "meter.module.css", "fillDanger"),
};

const RING_RADIUS = 5.7;
const RING_STROKE_WIDTH = 2.6;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;
const RING_DASH_ARRAY = RING_CIRCUMFERENCE.toFixed(3);

/** A passive usage readout (role=meter, not a control) - no interaction, no
 * focus ring. The default "bar" variant sets its fill width via a style
 * custom property (--fill) rather than an inline style rule, per Direction;
 * "ring" renders a 14px circular SVG arc starting at 12 o'clock. */
export function Meter({ label, value, max, tone = "neutral", variant = "bar" }: MeterProps) {
  const safeMax = Number.isFinite(max) && max > 0 ? max : 0;
  const safeValue = Number.isFinite(value) ? value : 0;
  const clamped = Math.min(safeMax, Math.max(0, safeValue));
  const percent = safeMax > 0 ? (clamped / safeMax) * 100 : 0;

  let content: ReactNode;
  if (variant === "ring") {
    const isEmpty = percent <= 0;
    const rawOffset = RING_CIRCUMFERENCE * (1 - percent / 100);
    const dashOffset = rawOffset <= 0.0005 ? "0" : rawOffset.toFixed(3);
    const fillClass = [BASE_CLASS.ringFill, TONE_CLASS[tone], isEmpty && BASE_CLASS.ringFillEmpty]
      .filter(Boolean)
      .join(" ");

    content = (
      <svg
        className={BASE_CLASS.ringSvg}
        viewBox="0 0 16 16"
        width={14}
        height={14}
        aria-hidden="true"
        focusable="false"
      >
        <circle className={BASE_CLASS.ringTrack} cx={8} cy={8} r={RING_RADIUS} strokeWidth={RING_STROKE_WIDTH} />
        <circle
          data-testid="meter-fill"
          className={fillClass}
          cx={8}
          cy={8}
          r={RING_RADIUS}
          strokeWidth={RING_STROKE_WIDTH}
          strokeDasharray={RING_DASH_ARRAY}
          strokeDashoffset={dashOffset}
          transform="rotate(-90 8 8)"
        />
      </svg>
    );
  } else {
    const fillStyle = { "--fill": `${percent}%` } as CSSProperties;
    content = <div data-testid="meter-fill" className={`${BASE_CLASS.fill} ${TONE_CLASS[tone]}`} style={fillStyle} />;
  }

  return (
    // Native <meter> can't be restyled cross-browser (its fill/track are
    // closed to CSS) - this div+role IS the deliberate themed-gauge escape
    // hatch (see this file's top doc comment for the widget's own intent).
    // biome-ignore lint/a11y/useSemanticElements: div+role is deliberate, see above
    <div
      className={variant === "ring" ? BASE_CLASS.ring : BASE_CLASS.track}
      data-variant={variant}
      role="meter"
      aria-label={label}
      aria-valuenow={clamped}
      aria-valuemin={0}
      aria-valuemax={safeMax}
    >
      {content}
    </div>
  );
}
