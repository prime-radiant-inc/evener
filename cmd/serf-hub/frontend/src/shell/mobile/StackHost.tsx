import { Suspense, useEffect } from "react";
import { paneFor } from "../paneRegistry";
import { useWorkspaceStore, workspaceStore, type OpenPaneRecord } from "../workspace";
import styles from "./StackHost.module.css";

// Renders exactly one pane, full-screen: whichever OpenPaneRecord
// useWorkspaceStore currently reports as focused. Uses the SAME registry
// (paneFor) and the SAME PaneProps contract every pane component already
// implements for DockHost - a pane never knows which host is showing it
// (Global Constraints: "panes never ask 'am I mobile?'").
function StackedPane({ pane }: { pane: OpenPaneRecord }) {
  const Component = paneFor(pane.type).component;
  return (
    <Suspense fallback={null}>
      <Component params={pane.params} paneId={pane.id} focused />
    </Suspense>
  );
}

export function StackHost() {
  const panes = useWorkspaceStore((s) => s.panes);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const focusedPane = panes.find((p) => p.id === focusedPaneId) ?? null;

  // Backstop, same reasoning as DockHost's own onReady fallback: a blank
  // stack with no chrome of its own to open a new pane from is a dead end.
  // In the real app AppShell's routing glue already opens SOME pane for
  // every resolvable route before either host ever mounts (see
  // AppShell.tsx's dockHostHasMountedRef comment), so this fires only for
  // a pane that's closed out from under focus, an HMR remount, or this
  // component under test standalone (no AppShell wrapping it).
  useEffect(() => {
    if (!focusedPane) workspaceStore.getState().openPane("welcome");
  }, [focusedPane]);

  return (
    <div className={styles.host}>
      <div className={styles.body}>
        {focusedPane && (
          // key forces a fresh mount on every distinct pane id, including
          // switching BACK to a previously-seen one - without it, two
          // panes of the SAME registered type (same Component reference)
          // would just re-render in place instead of remounting, breaking
          // the unmount-not-hide contract every pane is designed around
          // (see StackedPane's own comment). Two different pane TYPES
          // already remount naturally (React unmounts on element type
          // change), but same-type-different-params (e.g. two session
          // panes) would not, without this.
          <StackedPane key={focusedPane.id} pane={focusedPane} />
        )}
      </div>
    </div>
  );
}
