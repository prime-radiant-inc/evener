import { type ReactNode, useEffect, useRef } from "react";
import { Sheet } from "../../widgets";
import { NavigationChromeProvider } from "../navigationChrome";
import { RailNavigationActions } from "../rail/RailNavigationActions";
import { useWorkspaceStore } from "../workspace";
import { IPhoneSessionsToolbar } from "./IPhoneSessionsToolbar";
import styles from "./MobilePanel.module.css";

export interface MobilePanelProps {
  rail: ReactNode;
  open: boolean;
  onClose: () => void;
  onOpen?: () => void;
  toolbarEdge?: "left" | "right" | "system" | null;
}

export function MobilePanel({ rail, open, onClose, onOpen, toolbarEdge }: MobilePanelProps) {
  const panelAnchor = useRef<HTMLSpanElement>(null);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);

  const prevFocusedIdRef = useRef(focusedPaneId);
  const prevOpenRef = useRef(open);
  useEffect(() => {
    if (open && prevOpenRef.current !== open) prevFocusedIdRef.current = focusedPaneId;
    prevOpenRef.current = open;
    if (open && prevFocusedIdRef.current !== focusedPaneId) onClose();
    prevFocusedIdRef.current = focusedPaneId;
  }, [focusedPaneId, open, onClose]);

  return (
    <NavigationChromeProvider value={onClose}>
      <Sheet
        side="left"
        open={open}
        onClose={onClose}
        title="Sessions"
        headerActions={<RailNavigationActions />}
        size="wide"
        panelClassName={[styles.singleScrollPanel, toolbarEdge && styles[toolbarEdge]].filter(Boolean).join(" ")}
        bodyClassName={styles.flushBody}
      >
        <span ref={panelAnchor} hidden />
        {rail}
      </Sheet>
      {toolbarEdge === "system" && (open || onOpen) && (
        <IPhoneSessionsToolbar
          key={open ? "list" : "launcher"}
          mode={open ? "list" : "launcher"}
          panelAnchor={panelAnchor}
          onToggle={open ? onClose : (onOpen ?? onClose)}
        />
      )}
    </NavigationChromeProvider>
  );
}
