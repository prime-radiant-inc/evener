import { requireClass } from "../internal/requireClass";
import styles from "./badge.module.css";

export type BadgeTone = "neutral" | "attention" | "alive" | "danger";

export interface BadgeProps {
  count: number;
  tone?: BadgeTone;
}

const COUNT_CAP = 99;

const BASE_CLASS = {
  badge: requireClass(styles.badge, "badge.module.css", "badge"),
};

const TONE_CLASS: Record<BadgeTone, string> = {
  neutral: requireClass(styles.neutral, "badge.module.css", "neutral"),
  attention: requireClass(styles.attention, "badge.module.css", "attention"),
  alive: requireClass(styles.alive, "badge.module.css", "alive"),
  danger: requireClass(styles.danger, "badge.module.css", "danger"),
};

/** A small numeric count indicator. Passive - no interaction, no focus
 * ring. Caps its display at "99+" so a large count never stretches the
 * badge's footprint in a list or tab. */
export function Badge({ count, tone = "neutral" }: BadgeProps) {
  const display = count > COUNT_CAP ? `${COUNT_CAP}+` : String(count);
  return <span className={`${BASE_CLASS.badge} ${TONE_CLASS[tone]}`}>{display}</span>;
}
