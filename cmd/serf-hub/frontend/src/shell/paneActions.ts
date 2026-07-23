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
// dockview's addPopoutGroup does window.open on its default same-origin
// `/popout.html`, waits for that window's `load`, then moves the group's DOM
// into its document.body and clones this window's stylesheets in (dockview-core
// 7.0.2 popoutWindow.js:82,128,135-136 + dom.js addStyles). Two constraints
// shape the wiring:
//   - the popoutUrl MUST be same-origin http(s): dockview's
//     assertSameOriginPopoutUrl (popoutWindow.js:19, enforced :83) rejects
//     about:blank / data: / blob:, so the target has to be a real served page.
//   - a bare SPA fallback at /popout.html would return index.html and boot a
//     SECOND full app instance inside the popout window.
// The hub serves a minimal blank same-origin shell at /popout.html for exactly
// this (cmd/serf-hub/webnext.go servePopoutShell); dockview clones the app's
// styles into it, so the shell itself carries no CSS or JS. The one caller is
// DockHost's PopoutHeaderAction - a per-group "Pop out" header action.
export function popOutPane(paneId: string): void {
  const api = getDockviewApi();
  if (!api) return;
  const panel = api.getPanel(paneId);
  if (!panel) return;
  void api.addPopoutGroup(panel, {
    // dockview calls onDidOpen with the popout window BEFORE it navigates to
    // /popout.html (popoutWindow.js:119 vs the load handler at :128), so defer
    // the theme copy to that window's load. Open-time inheritance only: a
    // theme change while the popout stays open does not re-sync (the
    // cross-document gap parity floor §3.8:673-679 notes).
    onDidOpen: ({ window: popoutWindow }) => {
      popoutWindow.addEventListener("load", () => {
        inheritOpenerTheme(document.documentElement, popoutWindow.document.documentElement);
      });
    },
  });
}

// inheritOpenerTheme copies the opener document root's data-theme attribute onto
// a popout document root. dockview clones the opener's stylesheets into the
// popout (dom.js addStyles) but not this attribute, which tokens.css keys its
// light overrides off - so without it a light-theme opener renders a dark
// popout. A dark opener carries no data-theme at all (the :root default), so
// there is nothing to copy and the popout keeps that same dark default.
export function inheritOpenerTheme(openerRoot: Element, popoutRoot: Element): void {
  const theme = openerRoot.getAttribute("data-theme");
  if (theme !== null) popoutRoot.setAttribute("data-theme", theme);
}
