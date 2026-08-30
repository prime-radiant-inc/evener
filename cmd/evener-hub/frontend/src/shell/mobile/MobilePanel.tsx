import { type ReactNode, useEffect, useRef } from "react";
import { WelcomeContent } from "../../panes/welcome/WelcomeContent";
import { Sheet } from "../../widgets";
import { useWorkspaceStore } from "../workspace";

const PEEK_HEIGHT = 360;

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

  // Auto-close on navigation: when focusedPaneId changes while the panel
  // is open, close it (same pattern as TreeDrawer's old effect).
  useEffect(() => {
    if (open && prevFocusedIdRef.current !== focusedPaneId) onClose();
    prevFocusedIdRef.current = focusedPaneId;
  }, [focusedPaneId, open, onClose]);

  return (
    <Sheet
      side="bottom"
      open={open}
      onClose={onClose}
      title="Sessions"
      expandable={{ peekHeight: PEEK_HEIGHT, fullScreenFirst: true }}
    >
      {rail}
      {nothingFocused && <WelcomeContent showHints />}
    </Sheet>
  );
}
