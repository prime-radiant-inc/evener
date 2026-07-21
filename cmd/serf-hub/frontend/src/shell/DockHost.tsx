// The desktop workspace host: dockview-react renders every open pane as a
// dockview panel, wired to useWorkspaceStore (shell/workspace.ts) as the
// single source of truth for which panes are open/focused - see that
// file's own header comment for why the store, not dockview, owns that
// list. This component is the ONE place that talks to the real dockview
// library; everything else in the app touches panes only through the
// registry/store's own React-shaped surface.
import { Suspense, useEffect, useRef, useState } from "react";
import { DockviewReact, type DockviewReadyEvent, type IDockviewPanelProps } from "dockview-react";
import type { DockviewApi } from "dockview-core";
import "dockview-react/dist/styles/dockview.css";
import "./dockview-theme.css";
import { paneFor, type PaneTitleCtx } from "./paneRegistry";
import { registerDockviewApi, useWorkspaceStore, workspaceStore, type PanePanelParams } from "./workspace";
import { threadsStore, useThreadsStore } from "../stores/threads";
import styles from "./DockHost.module.css";

const LAYOUT_STORAGE_KEY = "serf.workspace.layout.v1";
// Coalesces a whole user gesture (a drag-resize fires onDidLayoutChange
// many times a second; dockview's own doc comment on that event says as
// much: "may be worth debouncing outputs") into one localStorage write,
// same idiom as widgets/combobox's onQuery debounce (see that widget's own
// comment) - a timer is fine for the SAVE side; only a test asserting the
// persisted payload needs fake timers, never a bare sleep.
const LAYOUT_SAVE_DEBOUNCE_MS = 400;

// Every open pane renders through this ONE dockview "component" key - the
// registry (paneFor) is what actually decides which real component and
// props a given pane's params dispatch to, so DockHost never needs a
// dockview component entry per pane type. `focused` tracks the panel's own
// isActive, kept live via onDidActiveChange rather than derived from
// useWorkspaceStore's focusedPaneId - both are kept in sync (see the focus-
// reconciliation effect below) but reading dockview's own truth here avoids
// a render-order dependency between this component and DockHost's effects.
//
// UNMOUNT, NOT HIDE: dockview unmounts a panel's whole React tree when it
// isn't the active tab in its group - confirmed via a live probe (see this
// wave's task report), not just CSS-hidden. Any pane's own component-local
// state (an in-progress draft, scroll position, anything not lifted into a
// store) is lost the instant its tab loses focus, and the component
// remounts from scratch when it regains it. Every real pane implementation
// (wave 4's transcript view, most directly) must be designed remount-safe:
// durable state belongs in a store keyed by the pane's own params (e.g.
// threads.ts, refcounted per ref - see that file's own header comment),
// never component-local useState for anything that needs to survive a tab
// switch - a remount re-subscribes cleanly through the SAME refcount
// mechanism that already handles multiple panes sharing one ref.
function PaneHost({ api, params }: IDockviewPanelProps<PanePanelParams>) {
  const [focused, setFocused] = useState(api.isActive);
  useEffect(() => {
    const disposable = api.onDidActiveChange((e) => setFocused(e.isActive));
    return () => disposable.dispose();
  }, [api]);

  const Component = paneFor(params.paneType).component;
  return (
    <Suspense fallback={null}>
      <Component params={params.paneParams} paneId={api.id} focused={focused} />
    </Suspense>
  );
}

const PANE_COMPONENT_KEY = "pane";
// Cast at the one point a specifically-typed panel component enters
// dockview's own loosely-typed (Parameters = {[key: string]: any}) catalog -
// every panel this app ever creates goes through addPanel() below with a
// params object shaped exactly like PanePanelParams, so the cast reflects a
// real, enforced invariant rather than papering over a type mismatch.
const COMPONENTS = { [PANE_COMPONENT_KEY]: PaneHost as React.FunctionComponent<IDockviewPanelProps> };

function readStoredLayout(): unknown {
  try {
    const raw = localStorage.getItem(LAYOUT_STORAGE_KEY);
    return raw === null ? undefined : JSON.parse(raw);
  } catch {
    return undefined; // malformed JSON, or localStorage itself unavailable
  }
}

function persistLayout(json: unknown): void {
  try {
    localStorage.setItem(LAYOUT_STORAGE_KEY, JSON.stringify(json));
  } catch {
    // Best-effort: a full quota (or Safari private-mode) must never be
    // fatal to the workspace itself, only to persistence across reloads.
  }
}

export function DockHost() {
  const [api, setApi] = useState<DockviewApi | null>(null);
  const panes = useWorkspaceStore((s) => s.panes);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const threads = useThreadsStore((s) => s.threads);
  // Tracks, per paneId, the last params reference actually pushed into
  // dockview (via addPanel at creation or updateParameters on a change) -
  // params identity only changes in workspace.ts when the value actually
  // differs (see its own sameParams guard), so a reference-equality check
  // here is enough to skip a no-op updateParameters() call on every
  // unrelated re-render without needing a second deep-equal pass.
  const pushedParamsRef = useRef(new Map<string, unknown>());

  // Native-interaction wiring: mirrors dockview-native interactions
  // (closing a tab via its own (x), clicking a different tab) back into the
  // store so it stays the single source of truth regardless of which side
  // initiated a change. The restore-or-fallback boot sequence deliberately
  // does NOT live here (see handleReady below for why).
  useEffect(() => {
    if (!api) return;

    const removeSub = api.onDidRemovePanel((panel) => {
      workspaceStore.getState().closePane(panel.id);
    });
    // origin distinguishes a user clicking a tab from a change THIS
    // component itself just drove via panel.api.setActive() (the focus-
    // reconciliation effect below) - mirroring 'api'-origin events back in
    // unconditionally would let an intermediate step of a multi-panel
    // reconciliation (e.g. two panes opened in the same batch) briefly
    // overwrite the real target before the effect finishes. See this
    // task's report for the live dockview probe that found this.
    const activeSub = api.onDidActivePanelChange((e) => {
      if (e.origin === "user" && e.panel) {
        workspaceStore.getState().focusPane(e.panel.id);
      }
    });

    let saveTimer: ReturnType<typeof setTimeout> | undefined;
    const layoutSub = api.onDidLayoutChange(() => {
      clearTimeout(saveTimer);
      saveTimer = setTimeout(() => {
        persistLayout(workspaceStore.getState().layoutJSON());
      }, LAYOUT_SAVE_DEBOUNCE_MS);
    });

    return () => {
      clearTimeout(saveTimer);
      removeSub.dispose();
      activeSub.dispose();
      layoutSub.dispose();
      registerDockviewApi(null);
    };
  }, [api]);

  // Structural reconciliation: adds a dockview panel for every pane
  // workspace.ts doesn't have one for yet, pushes updated params into an
  // existing panel's when they change (e.g. a singleton pane reopened with
  // different params), and removes any dockview panel workspace.ts no
  // longer lists - idempotent both ways, so a panel removed by dockview
  // itself (native tab close, already mirrored into the store above) is
  // simply absent from BOTH sides by the time this runs again, never
  // double-removed. The initial title uses a one-time, non-reactive
  // threadsStore snapshot (not the `threads` reactive value below) so this
  // effect's own dependency array doesn't re-run the whole reconciliation
  // on every live thread update - the title-sync effect owns keeping an
  // already-open pane's title current.
  useEffect(() => {
    if (!api) return;
    const currentIds = new Set(api.panels.map((p) => p.id));
    const bootTitleCtx: PaneTitleCtx = { threadName: (ref) => threadsStore.getState().threads.get(ref)?.name };

    for (const pane of panes) {
      const panelParams: PanePanelParams = { paneType: pane.type, paneParams: pane.params };
      if (!currentIds.has(pane.id)) {
        api.addPanel({
          id: pane.id,
          component: PANE_COMPONENT_KEY,
          title: paneFor(pane.type).title(pane.params, bootTitleCtx),
          params: panelParams,
          ...(pane.beside ? { position: { referencePanel: pane.beside, direction: "right" as const } } : {}),
        });
        pushedParamsRef.current.set(pane.id, pane.params);
      } else if (pushedParamsRef.current.get(pane.id) !== pane.params) {
        api.getPanel(pane.id)?.api.updateParameters(panelParams);
        pushedParamsRef.current.set(pane.id, pane.params);
      }
    }

    const desiredIds = new Set(panes.map((p) => p.id));
    for (const panel of api.panels) {
      if (!desiredIds.has(panel.id)) {
        api.removePanel(panel);
        pushedParamsRef.current.delete(panel.id);
      }
    }
  }, [api, panes]);

  // Focus reconciliation: pushes workspace.ts's focusedPaneId into dockview
  // whenever it doesn't already match (a fresh pane's own default-active
  // behavior on addPanel usually makes this a no-op for a brand new pane;
  // it's what actually moves focus for an existing one, e.g. a tree-rail
  // click in a later wave). `panes` is a dependency because a just-opened
  // pane's dockview panel may not exist until the structural effect above
  // creates it - same commit, declared first, so it already will have by
  // the time this one re-runs.
  useEffect(() => {
    if (!api || !focusedPaneId) return;
    if (api.activePanel?.id !== focusedPaneId) {
      api.getPanel(focusedPaneId)?.api.setActive();
    }
  }, [api, focusedPaneId, panes]);

  // Title sync: live-updates every open pane's dockview tab whenever its
  // computed title changes - the ONLY place PaneTitleCtx's threadName is
  // wired to a REACTIVE threads subscription, so a session pane's tab
  // tracks a rename without needing a page reload.
  useEffect(() => {
    if (!api) return;
    const ctx: PaneTitleCtx = { threadName: (ref) => threads.get(ref)?.name };
    for (const pane of panes) {
      const panel = api.getPanel(pane.id);
      if (!panel) continue; // not created yet on this pass - the structural effect (same commit) already gave it the right initial title
      const title = paneFor(pane.type).title(pane.params, ctx);
      if (panel.title !== title) panel.setTitle(title);
    }
  }, [api, panes, threads]);

  // Registers the api and runs the restore-or-fallback boot sequence
  // BEFORE exposing `api` to this component's own state (setApi, last) -
  // deliberately not inside a [api]-keyed effect. The structural
  // reconciliation effect above trusts `panes` and api.panels to agree on
  // the very first render where it sees a non-null api; restoreLayout()
  // mutates api.panels directly (fromJSON, bypassing addPanel/removePanel
  // entirely - the only way to restore split/group geometry) and workspace
  // state separately, and those two writes cannot land in the same React
  // commit. Doing both here, synchronously, before setApi() triggers the
  // first render that hands `api` to the reconciliation effect, means
  // `panes` is already caught up by the time that effect ever runs - no
  // render observes dockview's restored panels and a still-empty `panes`
  // together, which would otherwise read as "these panels are stale" and
  // remove everything restoreLayout() just created. Caught via a live
  // save-then-restore round-trip test, not spotted by inspection - see
  // this task's report.
  function handleReady(event: DockviewReadyEvent): void {
    registerDockviewApi(event.api);

    // Restore-or-fallback runs ONLY into an EMPTY workspace - a routed pane
    // (AppShell's routing glue, most directly - see its own comment on why
    // it opens the initial route's pane during render, before this handler
    // ever runs) always wins outright over a stale saved layout, rather
    // than the layout replacing it. Both the restore attempt AND the
    // welcome fallback are gated by the SAME check: restoreLayout() itself
    // mutates dockview's panels unconditionally once called (fromJSON has
    // no concept of "only if nothing else is here"), so calling it at all
    // when something has already been routed in would wipe out that routed
    // pane and replace it with whatever the layout last had - exactly the
    // bug a live reproduction caught (a returning user with a saved layout
    // following a deep link saw their OLD tabs, not the link). The nested
    // re-check after restoreLayout() covers the OTHER empty case: a
    // "successfully" restored but empty layout (every tab was closed
    // before the last save), which still needs the welcome fallback since
    // a blank dockview with no chrome of its own to open a new pane from
    // is a dead end.
    //
    // This is a deliberately simple fix, not the target behavior: the
    // controller has ruled that a future pass (Task 7, its own test cycle)
    // should MERGE instead - restore the saved layout as the base, then
    // open the routed pane inside it, focused - rather than suppressing
    // restore entirely whenever anything is already routed. Today's fix
    // trades that richer behavior for straightforward correctness now.
    //
    // NOTE for whoever wires AppShell's routing glue to this store: React
    // runs child effects before parent effects within one commit, so THIS
    // handler (called from DockviewReact's own mount effect, a descendant
    // of AppShell) fires before any plain useEffect in AppShell ever could
    // - a deep-linked route opened via a normal AppShell useEffect would
    // race this exact fallback and land ALONGSIDE a spurious welcome tab,
    // not instead of it. AppShell must open its initial route's pane
    // during render (e.g. a useRef-guarded call in the component body, or
    // a useState lazy initializer) so it runs before ANY effect in the
    // tree - openPane's own same-params/singleton dedup makes that call
    // safe to repeat if StrictMode (or a future refactor) invokes it more
    // than once.
    if (workspaceStore.getState().panes.length === 0) {
      const stored = readStoredLayout();
      if (stored !== undefined) {
        workspaceStore.getState().restoreLayout(stored);
      }
      if (workspaceStore.getState().panes.length === 0) {
        workspaceStore.getState().openPane("welcome");
      }
    }

    setApi(event.api);
  }

  return (
    <div className={styles.host}>
      <DockviewReact components={COMPONENTS} onReady={handleReady} className="dockview-theme-serf" />
    </div>
  );
}
