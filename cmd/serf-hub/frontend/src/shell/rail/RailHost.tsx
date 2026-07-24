// RailHost is the host AppShell mounts in place of <Rail/> at both sites (the
// desktop flex sibling and StackHost's railSlot). Desktop visibility is one
// persisted BOOLEAN — hidden or docked. There is no mode system: the old
// tri-state sidebarMode (auto/pane/rail) with its overlay-drawer "Collapsed"
// mode was removed 2026-07-24 at Jesse's direction; hiding now just removes
// the docked rail and leaves a top-left ☰ chip that docks it right back.
// ⌘B (or Ctrl+B outside editable fields) toggles the same boolean; the «
// button in the rail header hides it. Mobile is untouched: StackHost's
// TreeDrawer sheet hosts the same full-chrome Rail, and the hidden pref is
// desktop-only (the drawer is mobile's own show/hide).
//
// RailHost also owns the reveal seam: the palette's /project command
// (railController) hands it a session ref; if the rail is hidden, revealing
// docks it first so there's a mounted Rail to expand + scroll.
import { type JSX, useCallback, useEffect, useState } from "react";
import { prefsStore, usePrefsStore } from "../../stores/prefs";
import { useTreeStore } from "../../stores/tree";
import { Badge } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useIsMobile } from "../useIsMobile";
import { Rail } from "./Rail";
import styles from "./Rail.module.css";
import { setRailRevealHandler } from "./railController";

const CLASS = {
  chipBar: requireClass(styles.chipBar, "Rail.module.css", "chipBar"),
  chip: requireClass(styles.chip, "Rail.module.css", "chip"),
};

// Ctrl+B is the macOS emacs-style "move cursor back one character" binding
// native text fields honor while focused; without this guard, the ⌘B
// listener below would hijack it mid-typing (it accepts ctrl as well as
// meta for the cross-platform chord).
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
  const hidden = usePrefsStore((s) => s.sidebarHidden);
  const needsYou = useTreeStore((s) => s.tree?.attentionSummary.needsYou ?? 0);
  const [revealTarget, setRevealTarget] = useState<string | null>(null);
  const clearReveal = useCallback(() => setRevealTarget(null), []);

  // ⌘B toggles the sidebar (desktop only — mobile's drawer is its own
  // show/hide). PIN-D: ⌘B is the rail's; ⌘K (palette) is AppShell's.
  useEffect(() => {
    if (isMobile) return undefined;
    function onKeyDown(event: KeyboardEvent): void {
      if (isEditableTarget(event.target)) return;
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === "b") {
        event.preventDefault();
        const prefs = prefsStore.getState();
        prefs.setSidebarHidden(!prefs.sidebarHidden);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [isMobile]);

  // Reveal seam (railController /project, PIN-A). Reveal-first when hidden:
  // dock the rail so a <Rail/> is mounted to expand + scroll, then hand it
  // the ref.
  const hostHidden = !isMobile && hidden;
  useEffect(() => {
    setRailRevealHandler((ref) => {
      if (hostHidden) prefsStore.getState().setSidebarHidden(false);
      setRevealTarget(ref);
    });
    return () => setRailRevealHandler(null);
  }, [hostHidden]);

  if (hostHidden) {
    const label = needsYou > 0 ? `Show sidebar (${needsYou} need attention)` : "Show sidebar";
    return (
      <div className={CLASS.chipBar}>
        <button
          type="button"
          className={CLASS.chip}
          aria-label={label}
          onClick={() => prefsStore.getState().setSidebarHidden(false)}
        >
          <span aria-hidden="true">{"☰"}</span>
          {needsYou > 0 && <Badge count={needsYou} tone="attention" />}
        </button>
      </div>
    );
  }

  return (
    <Rail
      onHide={isMobile ? undefined : () => prefsStore.getState().setSidebarHidden(true)}
      revealTarget={revealTarget}
      onRevealConsumed={clearReveal}
    />
  );
}
