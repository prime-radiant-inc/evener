import type { CSSProperties } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./meter.module.css";

export type MeterTone = "neutral" | "attention" | "alive" | "danger";

export interface MeterProps {
  value: number;
  max: number;
  tone?: MeterTone;
}

const BASE_CLASS = {
  track: requireClass(styles.track, "meter.module.css", "track"),
  fill: requireClass(styles.fill, "meter.module.css", "fill"),
};

const TONE_CLASS: Record<MeterTone, string> = {
  neutral: requireClass(styles.neutral, "meter.module.css", "neutral"),
  attention: requireClass(styles.attention, "meter.module.css", "attention"),
  alive: requireClass(styles.alive, "meter.module.css", "alive"),
  danger: requireClass(styles.danger, "meter.module.css", "danger"),
};

/** A horizontal fill readout - disk/token/quota usage. Passive (role=meter,
 * not a control) - no interaction, no focus ring. The fill's width is the
 * one genuinely dynamic value in this batch, so it's set via a style
 * custom property (--fill) rather than an inline style rule, per Direction. */
export function Meter({ value, max, tone = "neutral" }: MeterProps) {
  const clamped = Math.min(max, Math.max(0, value));
  const percent = max > 0 ? (clamped / max) * 100 : 0;
  const fillStyle = { "--fill": `${percent}%` } as CSSProperties;

  return (
    <div className={BASE_CLASS.track} role="meter" aria-valuenow={clamped} aria-valuemin={0} aria-valuemax={max}>
      <div data-testid="meter-fill" className={`${BASE_CLASS.fill} ${TONE_CLASS[tone]}`} style={fillStyle} />
    </div>
  );
}
