// PaneTab is the custom dockview tab component wired as DockviewReact's
// defaultTabComponent (DockHost.tsx): it renders dockview's own default tab
// chrome unchanged (title, drag handling, the native (x) close button - see
// DockviewDefaultTab) and, for a session pane only, adds a live StatusDot
// beside the title when that session needs attention. A tab otherwise carries
// only its title - nothing on it says a session is working, needs a human,
// or failed, so those states were invisible until the tab happened to be
// focused (the pane's own header carries the same signal, but a background
// tab's doesn't).
//
// Reads status the SAME way Session.tsx's own header does: the live
// ThreadModel in stores/threads.ts, mapped through sessionStatus.ts's
// cadenceStateFor - the one canonical wire-status -> CadenceState
// mapping, not a second copy of it. Subscribing via useThreadsStore (not a
// one-time snapshot) is what makes the dot re-render live as a session's
// status changes, exactly like the pane's own Cadence dot.
import type { IDockviewPanelHeaderProps } from "dockview-core";
import { DockviewDefaultTab } from "dockview-react";
import { useThreadsStore } from "../stores/threads";
import { StatusDot } from "../widgets";
import styles from "./PaneTab.module.css";
import { cadenceStateFor, SIGNAL_STATES } from "./sessionStatus";
import type { PanePanelParams } from "./workspace";

// A session pane's params carry its thread ref (SessionPaneParams, panes/
// session/Session.tsx) - undefined for any other pane type, or a
// malformed/missing ref, either of which means "nothing to show a dot for".
function sessionRef(params: PanePanelParams): string | undefined {
  if (params.paneType !== "session") return undefined;
  const ref = (params.paneParams as { ref?: unknown } | undefined)?.ref;
  return typeof ref === "string" ? ref : undefined;
}

export function PaneTab(props: IDockviewPanelHeaderProps<PanePanelParams>) {
  const ref = sessionRef(props.params);
  // A ref with no tracked ThreadModel (never hydrated, or not a session
  // pane at all) reads as undefined here, same as an untracked ref
  // anywhere else in this file's neighbours (DockHost.tsx's own
  // threadName lookups).
  const statusType = useThreadsStore((s) => (ref === undefined ? undefined : s.threads.get(ref)?.status.type));
  const state = statusType === undefined ? undefined : cadenceStateFor(statusType);
  return (
    // data-session-tab is the hold-hints overlay's anchor (shell/holdhints):
    // the session-cycling chip positions itself over the first session tab.
    <span className={styles.tab} data-session-tab={ref !== undefined ? "" : undefined}>
      <DockviewDefaultTab {...props} />
      {state !== undefined && SIGNAL_STATES.has(state) && (
        <span className={styles.dot}>
          <StatusDot state={state} />
        </span>
      )}
    </span>
  );
}
