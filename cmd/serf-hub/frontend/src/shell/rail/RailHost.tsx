// RailHost is the thin host AppShell mounts in place of <Rail/> at both sites
// (the desktop flex sibling and StackHost's railSlot). The sidebar is ALWAYS
// docked on desktop — there is no collapsed mode, no ☰ chip, no overlay
// drawer, and no sidebarMode preference (removed 2026-07-24 at Jesse's
// direction: "remove collapsed mode entirely"). Mobile renders the same
// full-chrome Rail inside StackHost's TreeDrawer sheet.
//
// What's left for RailHost to own is the reveal seam: the palette's /project
// command (railController) hands it a session ref, and it threads that to the
// mounted Rail so it can expand the project section and scroll the row into
// view.
import { type JSX, useCallback, useEffect, useState } from "react";
import { Rail } from "./Rail";
import { setRailRevealHandler } from "./railController";

export function RailHost(_props: { railSlot?: never } = {}): JSX.Element {
  const [revealTarget, setRevealTarget] = useState<string | null>(null);
  const clearReveal = useCallback(() => setRevealTarget(null), []);

  // Reveal seam (railController /project, PIN-A). The rail is always mounted
  // (docked on desktop; inside the mobile TreeDrawer, which stays mounted with
  // the sheet), so a reveal only needs to hand over the ref.
  useEffect(() => {
    setRailRevealHandler((ref) => setRevealTarget(ref));
    return () => setRailRevealHandler(null);
  }, []);

  return <Rail revealTarget={revealTarget} onRevealConsumed={clearReveal} />;
}
