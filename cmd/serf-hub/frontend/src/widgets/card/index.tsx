import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./card.module.css";

export interface CardProps {
  children: ReactNode;
}

const BASE_CLASS = {
  card: requireClass(styles.card, "card.module.css", "card"),
};

/** A raised surface for grouping related content — its 1px ring comes from
 * --shadow-card, not a border. Passive container - no interaction, no focus
 * ring. */
export function Card({ children }: CardProps) {
  return <div className={BASE_CLASS.card}>{children}</div>;
}
