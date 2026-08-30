import { type ChangeEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { WelcomeContent } from "../../panes/welcome/WelcomeContent";
import { Input, Sheet } from "../../widgets";
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
  const [search, setSearch] = useState("");

  useEffect(() => {
    if (open && prevOpenRef.current !== open) prevFocusedIdRef.current = focusedPaneId;
    prevOpenRef.current = open;
    if (open && prevFocusedIdRef.current !== focusedPaneId) onClose();
    prevFocusedIdRef.current = focusedPaneId;
  }, [focusedPaneId, open, onClose]);

  function handleSearchChange(e: ChangeEvent<HTMLInputElement>) {
    setSearch(e.target.value);
  }

  return (
    <Sheet side="left" open={open} onClose={onClose} title="Sessions" size="wide">
      {nothingFocused && <WelcomeContent showHints />}
      <div className={styles.searchWrap}>
        <Input
          type="search"
          value={search}
          onChange={handleSearchChange}
          placeholder="Search sessions"
          aria-label="Search sessions"
        />
      </div>
      {rail}
    </Sheet>
  );
}
