// Imperative pane actions: open-beside and popout (wave 8). T1 ships these as
// compiling no-op stubs against their locked signatures so the six streams can
// import them behind a stable seam; wave-8 T6 fills the bodies (this file is in
// T6's manifest) and never re-touches a chokepoint to do it.
//
// Producers (T3's session file/image tool cards via openDocBeside, subagent
// "open transcript" rows via openBeside) code against these stubs; their
// reviewers read T6's landed bodies before merge (PIN-A). A stub MUST stay a
// safe no-op - never throw - so a producer that calls it mid-wave degrades to
// "nothing opens yet", not a crash.
import type { PaneTypeId } from "./paneRegistry";

// A logical pane to open: its registered type plus that type's own params bag
// (shape validated by the owning pane, same erased-params contract as
// workspace.ts's OpenPaneRecord).
export interface PaneRef {
  type: PaneTypeId;
  params: unknown;
}

// openBeside opens `pane` in a split group beside the focused group (dockview
// addPanel with position {direction:"right"}), deduping an identical
// already-open pane by a (type + params-key) signature and focusing it instead
// (floor §3.2). On the mobile StackHost (useIsMobile) it degrades to a full
// navigate (no splits). T6 fills; no-op until then.
export function openBeside(_pane: PaneRef): void {}

// popOutPane promotes an open panel to a dockview-native popout window (floor
// cross-cutting #10 - a new capability, no legacy port). T6 fills; no-op until
// then.
export function popOutPane(_paneId: string): void {}
