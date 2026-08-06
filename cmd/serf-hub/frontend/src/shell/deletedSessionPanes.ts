import { navigate, paneToURL, urlToPane } from "./routing";
import { workspaceStore } from "./workspace";

// n15j's safety contract for any delete that actually happened: "if the
// deleted session is open in the WebUI, navigate to a surviving
// workspace/session rather than leaving a dead route." Closing every pane
// still showing a session whose files are gone is the whole of what this
// layer owes - workspace.ts's own invariant (DockHost's relaunchWelcome)
// refills an emptied main slot from there, so this never picks a
// replacement. Shared by BOTH delete paths: one deleted session and a whole
// deleted project leave the workspace in the same dead-route state, so they
// clean it up the same way.
//
// Both endpoints report what they actually removed as bare thread ids
// (web_api_project_delete.go's result.Deleted carries target.ThreadID;
// web_api_session_delete.go ships the same shape for one target), and both
// only ever delete LOCAL sessions - so a bare id names the "local:<id>" ref
// a pane carries. An id that already carries a source prefix passes through
// unchanged, the same both-forms tolerance stores/tree.ts's sessionIDMatches
// applies to this very field.
export function closePanesForDeletedSessions(deletedIDs: string[]): void {
  const goneRefs = new Set(deletedIDs.map((id) => (id.includes(":") ? id : `local:${id}`)));
  const workspace = workspaceStore.getState();
  for (const pane of workspace.panes) {
    const paneRef = (pane.params as { ref?: unknown }).ref;
    if (typeof paneRef === "string" && goneRefs.has(paneRef)) workspace.closePane(pane.id);
  }
  leaveDeadRoute(goneRefs);
}

// Closing the pane is not enough for the pane the ADDRESS BAR names (kata
// 1hdc): AppShell re-applies the current route on every workspace change, and
// a URL naming a session re-opens a pane for it whether or not the tree still
// has that session - so closing the routed pane just makes the shell open it
// again, on "Loading transcript…" forever. Landing on welcome removes the dead
// route the re-application would keep acting on, and is where the emptied main
// slot already goes on its own (DockHost's relaunch-welcome invariant).
//
// Only a URL naming a ref we JUST deleted is rewritten. Every other route -
// another session, settings, anything - is left exactly where it is: deleting
// one session from the rail is not a reason to move the user off whatever they
// were looking at.
function leaveDeadRoute(goneRefs: ReadonlySet<string>): void {
  const route = urlToPane(window.location.pathname);
  if (route?.type !== "session") return;
  const routedRef = (route.params as { ref?: unknown }).ref;
  if (typeof routedRef !== "string" || !goneRefs.has(routedRef)) return;
  const welcome = paneToURL("welcome", {});
  if (welcome !== null) navigate(welcome);
}
