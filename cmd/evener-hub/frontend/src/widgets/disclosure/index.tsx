import type { ReactNode } from "react";
import { Chevron } from "../chevron";
import { requireClass } from "../internal/requireClass";
import styles from "./disclosure.module.css";
import { isDisclosureOpen, toggleDisclosure } from "./disclosureStore";

const CLASS = {
  details: requireClass(styles.details, "disclosure.module.css", "details"),
  summary: requireClass(styles.summary, "disclosure.module.css", "summary"),
  chevron: requireClass(styles.chevron, "disclosure.module.css", "chevron"),
  body: requireClass(styles.body, "disclosure.module.css", "body"),
};

export interface DisclosureProps {
  /** Stable key; open/closed state survives remount because it lives in the
   * disclosureStore under this id, not in component-local useState. */
  id: string;
  /** The always-visible summary row content. */
  summary: ReactNode;
  /** The collapsible body. */
  children: ReactNode;
  /** Fallback used only when the store has no entry for this id (default false). */
  defaultOpen?: boolean;
  "data-testid"?: string;
}

// Disclosure is the store-backed, rotating-chevron disclosure primitive
// (yt2q, §4.1). It mirrors ToolCallItem.tsx's controlled-<details> behavior
// (preventDefault on the native summary, drive `open` from state) but sources
// open/closed state from disclosureStore keyed by `id`, so the expansion
// survives the VirtualList/dockview remount that would reset a local useState.
export function Disclosure({ id, summary, children, defaultOpen = false, ...rest }: DisclosureProps) {
  const open = isDisclosureOpen(id, defaultOpen);
  return (
    <details className={CLASS.details} open={open} data-testid={rest["data-testid"]}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; see ToolCallItem.tsx */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(id, defaultOpen);
        }}
      >
        <span className={CLASS.chevron} aria-hidden="true" data-open={open ? "true" : "false"}>
          <Chevron />
        </span>
        {summary}
      </summary>
      {open && <div className={CLASS.body}>{children}</div>}
    </details>
  );
}
