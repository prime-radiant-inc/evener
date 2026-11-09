// The status bar: a session pane's glance surface. Its working location sits
// on the left and its activity counters sit on the right. A chip click first
// focuses the owning pane, then opens the shared activity sidebar on the
// matching tab. deriveScope combines navigation placement with the shared
// session activity summary; no activity state lives here.

import type { ReactNode } from "react";
import { requireClass } from "../../widgets/internal/requireClass";
import { ActivityChips } from "./ActivityChips";
import styles from "./statusbar.module.css";

const CLASS = {
  bar: requireClass(styles.bar, "statusbar.module.css", "bar"),
  leading: requireClass(styles.leading, "statusbar.module.css", "leading"),
};

export interface StatusBarProps {
  sessionRef: string;
  paneId: string;
  leading: ReactNode;
}

export function StatusBar({ sessionRef, paneId, leading }: StatusBarProps) {
  return (
    <div className={CLASS.bar} data-testid="statusbar">
      <div className={CLASS.leading}>{leading}</div>
      <ActivityChips sessionRef={sessionRef} paneId={paneId} />
    </div>
  );
}
