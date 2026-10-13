import type { HTMLAttributes, ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./card.module.css";

/** flat is the page-ground section; outlined is a bordered box on the first
 * surface step, for a block that sits inside a denser flow (a transcript
 * tool body, a spawn-form panel, a review summary) and needs its own edge. */
export type CardVariant = "flat" | "outlined";

/** attention is the "a human is needed right now" envelope (a pending ask
 * batch, a parked sandbox approval). It draws the outlined box in the
 * attention hue, so it ignores variant. */
export type CardTone = "neutral" | "attention";

export interface CardProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "className" | "style"> {
  children: ReactNode;
  variant?: CardVariant;
  tone?: CardTone;
}

const BASE_CLASS = {
  card: requireClass(styles.card, "card.module.css", "card"),
  outlined: requireClass(styles.outlined, "card.module.css", "outlined"),
  attention: requireClass(styles.attention, "card.module.css", "attention"),
};

function surfaceClass(variant: CardVariant, tone: CardTone): string | undefined {
  if (tone === "attention") return BASE_CLASS.attention;
  if (variant === "outlined") return BASE_CLASS.outlined;
  return undefined;
}

/** A surface for grouping related content. The flat card's 1px ring comes
 * from --shadow-card, not a border; outlined and attention draw a real one.
 * Passive container - no focus ring of its own. Native attributes (role,
 * aria-*, data-*, tabIndex, key handlers) spread onto the element BEFORE
 * className, so a loosely-typed spread cannot replace the card's classes
 * (Button's rest-spread rule). */
export function Card({ children, variant = "flat", tone = "neutral", ...rest }: CardProps) {
  const surface = surfaceClass(variant, tone);
  return (
    <div {...rest} className={surface ? `${BASE_CLASS.card} ${surface}` : BASE_CLASS.card}>
      {children}
    </div>
  );
}
