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
import { getDockviewApi, workspaceStore } from "./workspace";
// Registers the read-only "transcript" pane type as a side effect. Producers
// open it via the bare openBeside({type:"transcript", ...}) below (PIN-A), not
// through a dedicated opener the way the "doc" pane self-registers via
// openDocBeside - so paneActions is the one module guaranteed to be loaded
// before any transcript open, and it pulls the registration in. The heavy pane
// component stays a lazy chunk (see panes/transcript/index.ts); only the tiny
// registration is eager here.
import "../panes/transcript";

// A logical pane to open: its registered type plus that type's own params bag
// (shape validated by the owning pane, same erased-params contract as
// workspace.ts's OpenPaneRecord).
export interface PaneRef {
  type: PaneTypeId;
  params: unknown;
}

// openBeside opens `pane` in a split group beside the currently-focused pane
// (workspace.ts records the `beside` hint; DockHost turns it into dockview's
// addPanel position {referencePanel, direction:"right"}). Deduping is the
// store's own job: openPane already focuses an identical already-open pane
// (same type + deep-equal params) instead of opening a duplicate, so re-opening
// a pane beside just focuses it (floor §3.2). The mobile StackHost registers no
// dockview api (getDockviewApi() === null), which is the "no split capability"
// signal: there openBeside degrades to a plain open, and the one pane simply
// becomes the focused, full-screen screen the stack shows (its own "navigate"
// equivalent - a doc/transcript pane has no URL to navigate to).
export function openBeside(pane: PaneRef): void {
  const ws = workspaceStore.getState();
  // Split only when a dockview host is present AND something is focused to
  // split beside; otherwise open with no positioning hint (a fresh pane, or
  // the mobile full-screen degrade).
  const beside = getDockviewApi() !== null ? ws.focusedPaneId : null;
  ws.openPane(pane.type, pane.params, beside !== null ? { beside } : undefined);
}

// popOutPane promotes an open panel to a dockview-native popout window (floor
// cross-cutting #10 - a new capability, no legacy port). Dockview owns popout
// entirely; this is the thin imperative entry point. A no-op when there is no
// dockview host (mobile) or the id names no open panel, so a caller can invoke
// it optimistically without first checking either.
//
// DORMANT (investigated wave-8 fix round; a served popout shell is reserved for
// Jesse). dockview's addPopoutGroup does window.open(popoutUrl) - default
// same-origin `/popout.html` - waits for that window's `load`, then appends the
// group's DOM into its document.body (dockview-core 7.0.2 popoutWindow.js:
// open()). Two hard facts make a frontend-only fix impossible in this version:
//   1. serf-hub serves no /popout.html; its SPA fallback returns index.html, so
//      the default URL boots a SECOND full app instance in the popout window.
//   2. An `about:blank` (or data:/blob:) popoutUrl override is REJECTED by
//      dockview's own assertSameOriginPopoutUrl guard (popoutWindow.js:19,
//      enforced at :83): the popoutUrl MUST be same-origin http(s).
// The only working mechanism is a minimal same-origin http(s) blank shell the
// server serves (a small Go route) - reserved for Jesse, no route added here.
// Until then popout stays dormant: popOutPane has NO caller anywhere in the app
// (popoutDormant.test.ts locks that), so no affordance can open the broken
// window. The body is left intact so a future served-shell wiring is just a
// global `popoutUrl` dockview option, nothing more.
export function popOutPane(paneId: string): void {
  const api = getDockviewApi();
  if (!api) return;
  const panel = api.getPanel(paneId);
  if (!panel) return;
  void api.addPopoutGroup(panel);
}
