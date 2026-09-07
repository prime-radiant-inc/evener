import { type ReactNode, useEffect, useRef } from "react";
import { WelcomeContent } from "../../panes/welcome/WelcomeContent";
import { Sheet } from "../../widgets";
import { useWorkspaceStore } from "../workspace";
import styles from "./MobilePanel.module.css";

export interface MobilePanelProps {
  rail: ReactNode;
  open: boolean;
  onClose: () => void;
}

export function MobilePanel({ rail, open, onClose }: MobilePanelProps) {
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const panes = useWorkspaceStore((s) => s.panes);
  const focusedPane = panes.find((p) => p.id === focusedPaneId) ?? null;
  const nothingFocused = focusedPaneId === null || focusedPane?.type === "welcome";

  const prevFocusedIdRef = useRef(focusedPaneId);
  const prevOpenRef = useRef(open);
  useEffect(() => {
    if (open && prevOpenRef.current !== open) prevFocusedIdRef.current = focusedPaneId;
    prevOpenRef.current = open;
    if (open && prevFocusedIdRef.current !== focusedPaneId) onClose();
    prevFocusedIdRef.current = focusedPaneId;
  }, [focusedPaneId, open, onClose]);

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
      {nothingFocused && (
        <div className={styles.drawerWelcome}>
          <WelcomeContent showResume={false} showHints={false} />
        </div>
      )}
      {rail}
    </Sheet>
  );
}
