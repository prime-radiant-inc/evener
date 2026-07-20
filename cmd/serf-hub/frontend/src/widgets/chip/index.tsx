import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./chip.module.css";

export type ChipTone = "neutral" | "attention" | "alive" | "danger";

export interface ChipProps {
  children: ReactNode;
  tone?: ChipTone;
  onRemove?: () => void;
}

const BASE_CLASS = {
  chip: requireClass(styles.chip, "chip.module.css", "chip"),
  remove: requireClass(styles.remove, "chip.module.css", "remove"),
};

const TONE_CLASS: Record<ChipTone, string> = {
  neutral: requireClass(styles.neutral, "chip.module.css", "neutral"),
  attention: requireClass(styles.attention, "chip.module.css", "attention"),
  alive: requireClass(styles.alive, "chip.module.css", "alive"),
  danger: requireClass(styles.danger, "chip.module.css", "danger"),
};

function RemoveIcon() {
  return (
    <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
      <path d="M2 2 L10 10 M10 2 L2 10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

/** A small labeled pill, optionally removable. tone maps to the four
 * semantic families (chip is pre-allowlisted in token-contract.test.ts). */
export function Chip({ children, tone = "neutral", onRemove }: ChipProps) {
  return (
    <span className={`${BASE_CLASS.chip} ${TONE_CLASS[tone]}`}>
      {children}
      {onRemove !== undefined && (
        <button type="button" className={BASE_CLASS.remove} aria-label="Remove" onClick={onRemove}>
          <RemoveIcon />
        </button>
      )}
    </span>
  );
}
