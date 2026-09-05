// RailHost is the host AppShell mounts in place of <Rail/> at both sites (the
// desktop flex sibling and StackHost's railSlot). Desktop visibility is one
// persisted BOOLEAN — hidden or docked. There is no mode system: the old
// tri-state sidebarMode (auto/pane/rail) with its overlay-drawer "Collapsed"
// mode was removed 2026-07-24 at Jesse's direction; hiding now just removes
// the docked rail and leaves a top-left ☰ chip that docks it right back.
// ⌘B (or Ctrl+B outside editable fields) toggles the same boolean; the ☰
// button in the rail header hides it - the same glyph both directions, so
// one icon owns the sidebar toggle. Mobile is untouched: StackHost's
// TreeDrawer sheet hosts the same full-chrome Rail, and the hidden pref is
// desktop-only (the drawer is mobile's own show/hide).
//
// RailHost also owns the reveal seam: the palette's /project command
// (railController) hands it a session ref; if the rail is hidden, revealing
// docks it first so there's a mounted Rail to expand + scroll.
import { type JSX, useCallback, useEffect, useState } from "react";
import { ACTIONS } from "../../keybindings/actions";
import { keybindingsRegistry } from "../../keybindings/registry";
import { selectNeedsYouCount } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { prefsStore, usePrefsStore } from "../../stores/prefs";
import { Badge } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { installKeybindings } from "../installKeybindings";
import { useIsMobile } from "../useIsMobile";
import { Rail } from "./Rail";
import styles from "./RailHost.module.css";
import { setRailRevealHandler } from "./railController";

const CLASS = {
  chipBar: requireClass(styles.chipBar, "RailHost.module.css", "chipBar"),
  chip: requireClass(styles.chip, "RailHost.module.css", "chip"),
};

export function RailHost(_props: { railSlot?: never } = {}): JSX.Element {
  const isMobile = useIsMobile();
  const hidden = usePrefsStore((s) => s.sidebarHidden);
  const sidebarWidth = usePrefsStore((s) => s.sidebarWidth);
  const navigation = useNavigationStore();
  const needsYou = selectNeedsYouCount(navigation);
  const [revealTarget, setRevealTarget] = useState<string | null>(null);
  const clearReveal = useCallback(() => setRevealTarget(null), []);

  // ⌘B toggles the sidebar (desktop only — mobile's drawer is its own
  // show/hide, and with no action registered the binding is inert there).
  // PIN-D: ⌘B is the rail's; ⌘K (palette) is AppShell's. The chord itself is
  // the keybindings dispatcher's (keybindings/defaults.ts's rail.toggle
  // binding carries this listener's old policy verbatim: suppressed from
  // editable targets so Ctrl+B keeps its emacs "cursor back" meaning in
  // native text fields, and NO defaultPrevented check -
  // ignoreIfDefaultPrevented: false).
  useEffect(() => {
    if (isMobile) return undefined;
    installKeybindings();
    return keybindingsRegistry.getState().registerAction(ACTIONS.railToggle, () => {
      const prefs = prefsStore.getState();
      prefs.setSidebarHidden(!prefs.sidebarHidden);
    });
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

  // Width/handle are the desktop docked rail's alone: mobile's Rail fills
  // TreeDrawer's sheet. Because the width lives in the pref rather than in this
  // component, hiding and re-showing the rail (or a reload) restores exactly
  // the dragged width.
  return (
    <Rail
      onHide={isMobile ? undefined : () => prefsStore.getState().setSidebarHidden(true)}
      width={isMobile ? undefined : sidebarWidth}
      onWidthChange={isMobile ? undefined : (width) => prefsStore.getState().setSidebarWidth(width)}
      revealTarget={revealTarget}
      onRevealConsumed={clearReveal}
      scrollOwner={isMobile ? "parent" : "rail"}
    />
  );
}
