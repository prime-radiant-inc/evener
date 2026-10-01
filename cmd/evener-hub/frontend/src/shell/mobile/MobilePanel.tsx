import { type ReactNode, useEffect, useRef } from "react";
import { Sheet } from "../../widgets";
import { useActivitySidebarStore } from "../activitybar/activitySidebarStore";
import { useWorkspaceStore } from "../workspace";
import styles from "./MobilePanel.module.css";

export interface MobilePanelProps {
  rail: ReactNode;
  open: boolean;
  onClose: () => void;
}

export function MobilePanel({ rail, open, onClose }: MobilePanelProps) {
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const activityOpen = useActivitySidebarStore((s) => s.open);

  const prevFocusedIdRef = useRef(focusedPaneId);
  const prevOpenRef = useRef(open);
  useEffect(() => {
    if (open && prevOpenRef.current !== open) prevFocusedIdRef.current = focusedPaneId;
    prevOpenRef.current = open;
    if (open && (activityOpen || prevFocusedIdRef.current !== focusedPaneId)) onClose();
    prevFocusedIdRef.current = focusedPaneId;
  }, [activityOpen, focusedPaneId, open, onClose]);

  return (
    <Sheet
      side="left"
      open={open}
      onClose={onClose}
      title="Sessions"
      size="wide"
      panelClassName={styles.singleScrollPanel}
      bodyClassName={styles.flushBody}
    >
      {rail}
    </Sheet>
  );
}
