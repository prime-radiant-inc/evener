// RailHost is the sidebarMode-aware host AppShell mounts in place of <Rail/> at
// both sites (the desktop flex sibling and StackHost's railSlot). It keeps the
// same mount contract as <Rail/> and owns the sidebar's VISIBILITY:
//   pane  -> inline, always expanded
//   auto  -> inline at/above 1200px; below it, collapsed to the ☰ chip
//   rail  -> always collapsed to the ☰ chip
// (Semantics fixed by the shipped Wave-7 help copy, theme.tsx:103-104.)
// "Collapsed" hides the rail entirely, leaving a top-left ☰ chip that opens it
// as an overlay drawer. ⌘B cycles rail -> pane -> auto. Mobile is unaffected
// ("Desktop only"): RailHost renders the plain <Rail/> inside StackHost's tree
// drawer, with no mode logic.
import { type JSX, useCallback, useEffect, useRef, useState } from "react";
import { prefsStore, type SidebarModePref } from "../../stores/prefs";
import { useTreeStore } from "../../stores/tree";
import { Badge, Sheet } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useIsMobile } from "../useIsMobile";
import { useWorkspaceStore } from "../workspace";
import { Rail } from "./Rail";
import styles from "./Rail.module.css";
import { setRailRevealHandler } from "./railController";
import { useSidebarMode } from "./useSidebarMode";

const CLASS = {
  chipBar: requireClass(styles.chipBar, "Rail.module.css", "chipBar"),
  chip: requireClass(styles.chip, "Rail.module.css", "chip"),
};

// ⌘B cycles collapsed -> pane -> auto (wrapping auto -> collapsed), matching the
// shipped help copy's "cycles collapsed → pane → auto".
const NEXT_MODE: Record<SidebarModePref, SidebarModePref> = {
  rail: "pane",
  pane: "auto",
  auto: "rail",
};

// Ctrl+B is the macOS emacs-style "move cursor back one character" binding
// native text fields honor while focused; without this guard, the ⌘B
// listener below would hijack it mid-typing (it accepts ctrl as well as
// meta for the cross-platform chord). No shared "is this target editable"
// predicate exists elsewhere in the codebase yet (checked the other global
// keydown listeners - AppShell.tsx's ⌘K/Ctrl-K has no such guard, since ⌘K
// has no native text-editing meaning to collide with) - kept local since
// RailHost is its only caller today.
function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.isContentEditable ||
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.tagName === "SELECT"
  );
}

export function RailHost(_props: { railSlot?: never } = {}): JSX.Element {
  const isMobile = useIsMobile();
  const { collapsed } = useSidebarMode();
  const needsYou = useTreeStore((s) => s.tree?.attentionSummary.needsYou ?? 0);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [revealTarget, setRevealTarget] = useState<string | null>(null);
  const clearReveal = useCallback(() => setRevealTarget(null), []);
  const prevFocusedRef = useRef(focusedPaneId);

  const hostCollapsed = !isMobile && collapsed;

  // ⌘B cycles the sidebar mode (desktop only - the modes are "Desktop only").
  // PIN-D: ⌘B is T5's; ⌘K (palette) is T1/T3's - separate, disjoint listeners.
  useEffect(() => {
    if (isMobile) return undefined;
    function onKeyDown(event: KeyboardEvent): void {
      if (isEditableTarget(event.target)) return;
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === "b") {
        event.preventDefault();
        const current = prefsStore.getState().sidebarMode;
        prefsStore.getState().setSidebarMode(NEXT_MODE[current]);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [isMobile]);

  // Reveal seam (railController /project, PIN-A). Reveal-first when collapsed:
  // open the overlay drawer so a <Rail/> is mounted to expand+scroll, then hand
  // it the ref via revealTarget.
  useEffect(() => {
    setRailRevealHandler((ref) => {
      if (hostCollapsed) setDrawerOpen(true);
      setRevealTarget(ref);
    });
    return () => setRailRevealHandler(null);
  }, [hostCollapsed]);

  // Close the overlay drawer once a session is opened from it (focus changed),
  // mirroring the mobile TreeDrawer's own auto-close. A /project reveal doesn't
  // openPane, so it leaves the drawer open on the revealed row.
  useEffect(() => {
    if (prevFocusedRef.current !== focusedPaneId) {
      setDrawerOpen(false);
      prevFocusedRef.current = focusedPaneId;
    }
  }, [focusedPaneId]);

  if (isMobile) {
    return <Rail revealTarget={revealTarget} onRevealConsumed={clearReveal} />;
  }

  if (collapsed) {
    const label = needsYou > 0 ? `Show sidebar (${needsYou} need attention)` : "Show sidebar";
    return (
      <>
        <div className={CLASS.chipBar}>
          <button type="button" className={CLASS.chip} aria-label={label} onClick={() => setDrawerOpen(true)}>
            <span aria-hidden="true">{"☰"}</span>
            {needsYou > 0 && <Badge count={needsYou} tone="attention" />}
          </button>
        </div>
        <Sheet side="left" open={drawerOpen} onClose={() => setDrawerOpen(false)} title="Sessions">
          <Rail revealTarget={revealTarget} onRevealConsumed={clearReveal} />
        </Sheet>
      </>
    );
  }

  // Inline and expanded (pane, or auto at/above 1200px). onHide collapses the
  // sidebar by moving to rail mode (which this same component then renders as
  // the ☰ chip).
  return (
    <Rail
      onHide={() => prefsStore.getState().setSidebarMode("rail")}
      revealTarget={revealTarget}
      onRevealConsumed={clearReveal}
    />
  );
}
