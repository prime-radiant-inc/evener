import type { HTMLAttributes, ReactNode } from "react";
import { CloseIcon } from "../dialog/CloseIcon";
import { requireClass } from "../internal/requireClass";
import styles from "./chip.module.css";

export type ChipTone = "neutral" | "attention" | "alive" | "danger" | "accent";
export type ChipVariant = "solid" | "quiet";

export interface ChipProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  children: ReactNode;
  tone?: ChipTone;
  /** "quiet" drops the fill and tone ink for a state marker inside a row. */
  variant?: ChipVariant;
  onRemove?: () => void;
  /** The remove control's accessible name. Omitted, it derives from a plain
   * string child as `Remove <text>` and falls back to `Remove`. */
  removeLabel?: string;
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
  accent: requireClass(styles.accent, "chip.module.css", "accent"),
};

// children is arbitrary ReactNode (the locked API's exact shape), not
// guaranteed to be a string, so it can only be safely folded into the
// remove button's accessible name in the one case where it unambiguously
// IS text - everything else (an element, a fragment, a number on its own)
// falls back to a bare "Remove" rather than guessing at a label.
function removeLabelFor(children: ReactNode): string {
  return typeof children === "string" ? `Remove ${children}` : "Remove";
}

/** A small labeled pill, optionally removable. tone maps to the semantic
 * families (chip is pre-allowlisted in token-contract.test.ts). */
export function Chip(props: ChipProps) {
  const {
    children,
    tone = "neutral",
    variant = "solid",
    onRemove,
    removeLabel,
    kind,
    mono = false,
    className,
    ...rest
  } = props;
  const classes = `${BASE_CLASS.chip} ${TONE_CLASS[tone]}${mono ? ` ${BASE_CLASS.mono}` : ""}${className ? ` ${className}` : ""}`;
  return (
    <span {...rest} className={classes} data-kind={kind} data-variant={variant}>
      {children}
      {onRemove !== undefined && (
        <button
          type="button"
          className={BASE_CLASS.remove}
          aria-label={removeLabel ?? removeLabelFor(children)}
          onClick={onRemove}
        >
          <CloseIcon size={10} />
        </button>
      )}
    </span>
  );
}
