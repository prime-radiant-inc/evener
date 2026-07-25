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

// openBeside opens `pane` in the secondary group, to the right of the main
// pane. It is now a plain openPane() call: placement is workspace.ts's own
// single rule (the main slot holds one pane, everything else stacks to its
// right), so "beside" is what EVERY open does on desktop and this function no
// longer needs to ask for it. Kept as a named entry point because its callers
// (the session's file/image tool cards, subagent "open transcript" rows) mean
// something specific by it - "open this next to what I'm reading, don't
// replace it" - and that intent is worth naming even when the mechanism is now
// the default.
//
// Deduping is the store's own job: openPane already focuses an identical
// already-open pane (same type + deep-equal params) instead of opening a
// duplicate (floor §3.2). Mobile needs no special case either - StackHost has
// no groups at all and simply shows the focused pane full-screen.
export function openBeside(pane: PaneRef): void {
  workspaceStore.getState().openPane(pane.type, pane.params);
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
    // the theme copy to that window's load. This is the ONE-TIME open-time
    // copy; the module-level MutationObserver below (see inheritOpenerTheme's
    // own following comment) is what keeps it in sync for as long as the
    // popout stays open afterward.
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

// A live theme flip (Settings -> Theme, or the palette's "Switch theme") re-
// syncs every popout dockview currently has open, keeping each one's theme
// live for as long as it stays open (not just at the moment popOutPane's own
// onDidOpen above ran) - the cross-document gap parity floor §3.8 flags. The
// opener's data-theme attribute, not the prefs store, is the signal watched:
// prefs.ts's setTheme sets it directly, but so does its OS-driven "system"
// re-apply (handleSystemSchemeChange), which never touches the prefs store's
// own state - data-theme is the one thing common to both, and it is already
// inheritOpenerTheme's own source of truth. A MutationObserver on it needs no
// separate registry of open popout windows of this module's own:
// api.getPopouts() (component.api.d.ts) is dockview's own live enumeration -
// the exact channel popOutPane's onDidOpen callback above already receives
// one popout window through - so re-sync just walks that same list instead
// of inventing a second one. A popout that later closes drops out of
// getPopouts() on its own; there is nothing here to tear down. Installed
// unconditionally at module load (never lazily): unlike prefs.ts's
// matchMedia-gated listener, MutationObserver needs no environment feature-
// detection, and this module is already guaranteed loaded (by openDoc.ts /
// DockHost's PopoutHeaderAction) before any popout could exist to react to
// in the first place.
new MutationObserver(() => {
  const api = getDockviewApi();
  if (!api) return;
  for (const popout of api.getPopouts()) {
    inheritOpenerTheme(document.documentElement, popout.window.document.documentElement);
  }
}).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
