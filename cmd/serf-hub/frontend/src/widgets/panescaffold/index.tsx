import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./panescaffold.module.css";

export interface PaneScaffoldProps {
  title: string;
  cadence?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  children: ReactNode;
}

const CLASS = {
  pane: requireClass(styles.pane, "panescaffold.module.css", "pane"),
  header: requireClass(styles.header, "panescaffold.module.css", "header"),
  title: requireClass(styles.title, "panescaffold.module.css", "title"),
  cadenceSlot: requireClass(styles.cadenceSlot, "panescaffold.module.css", "cadenceSlot"),
  actions: requireClass(styles.actions, "panescaffold.module.css", "actions"),
  body: requireClass(styles.body, "panescaffold.module.css", "body"),
  footer: requireClass(styles.footer, "panescaffold.module.css", "footer"),
};

/**
 * The standard pane chrome every pane type in the app uses: a header row
 * (truncating title + optional cadence slot + optional actions cluster), a
 * scrollable body, and an optional footer. Deliberately boring - this is
 * the most-copied layout primitive in the app, so every pane looks and
 * behaves the same way.
 */
export function PaneScaffold({ title, cadence, actions, footer, children }: PaneScaffoldProps) {
  return (
    <div className={CLASS.pane}>
      <div className={CLASS.header}>
        <h2 className={CLASS.title}>{title}</h2>
        {cadence !== undefined && (
          <div className={CLASS.cadenceSlot} data-testid="pane-cadence-slot">
            {cadence}
          </div>
        )}
        {actions !== undefined && (
          <div className={CLASS.actions} data-testid="pane-actions">
            {actions}
          </div>
        )}
      </div>
      <div className={CLASS.body}>{children}</div>
      {footer !== undefined && (
        <div className={CLASS.footer} data-testid="pane-footer">
          {footer}
        </div>
      )}
    </div>
  );
}
