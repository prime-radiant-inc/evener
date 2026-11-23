import type { ReactNode } from "react";
import { CloseIcon } from "../dialog/CloseIcon";
import { requireClass } from "../internal/requireClass";
import styles from "./chip.module.css";

export type ChipTone = "neutral" | "attention" | "alive" | "danger";

export interface ChipProps {
  children: ReactNode;
  tone?: ChipTone;
  onRemove?: () => void;
  /** What the chip marks, rendered as `data-kind` so callers and tests can
   * tell one chip from another without reading its label. */
  kind?: string;
  /** Render the label in the mono face, for machine text such as tool names. */
  mono?: boolean;
}

const BASE_CLASS = {
  chip: requireClass(styles.chip, "chip.module.css", "chip"),
  mono: requireClass(styles.mono, "chip.module.css", "mono"),
  remove: requireClass(styles.remove, "chip.module.css", "remove"),
};

const TONE_CLASS: Record<ChipTone, string> = {
  neutral: requireClass(styles.neutral, "chip.module.css", "neutral"),
  attention: requireClass(styles.attention, "chip.module.css", "attention"),
  alive: requireClass(styles.alive, "chip.module.css", "alive"),
  danger: requireClass(styles.danger, "chip.module.css", "danger"),
};

// children is arbitrary ReactNode (the locked API's exact shape), not
// guaranteed to be a string, so it can only be safely folded into the
// remove button's accessible name in the one case where it unambiguously
// IS text - everything else (an element, a fragment, a number on its own)
// falls back to a bare "Remove" rather than guessing at a label.
function removeLabelFor(children: ReactNode): string {
  return typeof children === "string" ? `Remove ${children}` : "Remove";
}

/** A small labeled pill, optionally removable. tone maps to the four
 * semantic families (chip is pre-allowlisted in token-contract.test.ts). */
export function Chip({ children, tone = "neutral", onRemove, kind, mono = false }: ChipProps) {
  const monoClass = mono ? ` ${BASE_CLASS.mono}` : "";
  return (
    <span className={`${BASE_CLASS.chip} ${TONE_CLASS[tone]}${monoClass}`} data-kind={kind}>
      {children}
      {onRemove !== undefined && (
        <button type="button" className={BASE_CLASS.remove} aria-label={removeLabelFor(children)} onClick={onRemove}>
          <CloseIcon size={10} />
        </button>
      )}
    </span>
  );
}
