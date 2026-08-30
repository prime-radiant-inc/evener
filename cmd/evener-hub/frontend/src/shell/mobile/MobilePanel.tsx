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
  // Tracks the previous `open` so the closed->open transition can re-baseline
  // prevFocusedIdRef (see the effect below).
  const prevOpenRef = useRef(open);

  // Auto-close on navigation: when focusedPaneId changes while the panel
  // is open, close it (same pattern as TreeDrawer's old effect). On a
  // closed->open transition, re-baseline prevFocusedIdRef to the CURRENT
  // focusedPaneId first: a focus change that happened while the panel was
  // closed (or was batched into the same commit as the open, which React
  // never exposes as an intermediate closed state) must NOT be misread as a
  // navigation-while-open. The owner (StackHost) opens this panel over a
  // welcome pane that its own backstop just focused in that exact batched
  // beat; without the re-baseline, the panel opens already "stale" and
  // closes itself one effect tick later. A navigation that genuinely
  // happens AFTER the panel is already open still closes it (the task-4
  // contract), because prevFocusedIdRef then names the pane that was
  // focused at open time, not whatever landed batched with the open.
  useEffect(() => {
    if (open && prevOpenRef.current !== open) prevFocusedIdRef.current = focusedPaneId;
    prevOpenRef.current = open;
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
