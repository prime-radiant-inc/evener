// RailHost is the sidebarMode-aware host AppShell mounts in place of <Rail/>
// at both sites (the desktop flex sibling and StackHost's railSlot). It keeps
// the exact same mount contract as <Rail/>.
//
// T1 ships a pass-through to <Rail/>; T5 fills the desktop mode logic
// (auto responsive at 1200px, pane always-expanded, rail/"Collapsed" behind a
// ☰ overlay drawer, ⌘B cycling rail -> pane -> auto) while still rendering the
// plain <Rail/> on mobile.
import { Rail } from "./Rail";

export function RailHost() {
  return <Rail />;
}
