// The desktop workspace host: dockview-react renders every open pane as a
// dockview panel, wired to useWorkspaceStore (shell/workspace.ts) as the
// single source of truth for which panes are open/focused - see that
// file's own header comment for why the store, not dockview, owns that
// list. This component is the ONE place that talks to the real dockview
// library; everything else in the app touches panes only through the
// registry/store's own React-shaped surface.

import type { DockviewApi } from "dockview-core";
import { DockviewReact, type DockviewReadyEvent, type IDockviewPanelProps } from "dockview-react";
import { Suspense, useEffect, useRef, useState } from "react";
import "dockview-react/dist/styles/dockview.css";
import "./dockview-theme.css";
import { threadsStore, useThreadsStore } from "../stores/threads";
import styles from "./DockHost.module.css";
import { PopoutHeaderAction } from "./PopoutHeaderAction";
import { type PaneTitleCtx, paneFor } from "./paneRegistry";
import {
  type OpenPaneRecord,
  type PanePanelParams,
  registerDockviewApi,
  useWorkspaceStore,
  workspaceStore,
} from "./workspace";

// .v2, not .v1: a layout saved before the one-pane-per-main-group rule can
// hold several panes in the main group, which the rule says is not a valid
// layout. Bumping the key means such a value is simply never read again -
// cheaper and more honest than migration code for a shape it was never tested
// against (Jesse: "do not care about current local storage").
const LAYOUT_STORAGE_KEY = "serf.workspace.layout.v2";
// Coalesces a whole user gesture (a drag-resize fires onDidLayoutChange
// many times a second; dockview's own doc comment on that event says as
// much: "may be worth debouncing outputs") into one localStorage write,
// A timer is fine for the SAVE side; only a test asserting the persisted
// payload needs fake timers, never a bare sleep.
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

// Serializes the live layout and writes it. Skips a null layout outright:
// layoutJSON() returns null with no dockview api registered, and persisting
// that would replace a perfectly good saved layout with the literal "null" -
// which readStoredLayout hands back as a defined value, sending the next boot
// down restoreLayout's structural-failure path (an empty workspace) rather
// than restoring anything.
function saveLayout(): void {
  try {
    const json = workspaceStore.getState().layoutJSON();
    if (json === null) return;
    localStorage.setItem(LAYOUT_STORAGE_KEY, JSON.stringify(json));
  } catch {
    // Best-effort: a full quota (or Safari private-mode) must never be
    // fatal to the workspace itself, only to persistence across reloads.
  }
}

// Turns a pane's logical slot (workspace.ts's one placement rule) into the
// dockview addPanel `position` that realizes it.
//
//   main      -> to the LEFT of the secondary group when one exists, so the
//                main slot stays the top-left one it has always been (the case
//                after the main pane is closed and welcome relaunches, or after
//                a real pane displaces welcome). With no secondary group there
//                is nothing to anchor to and no position is needed: dockview
//                creates the one group itself.
//   secondary -> stack into the existing secondary group when there is one
//                ("within"), otherwise split to the RIGHT of the main pane,
//                which is what creates that group in the first place. This is
//                what makes a third pane join the second's group rather than
//                making a third column ("generally open in a group to the
//                right", singular).
function positionFor(
  api: DockviewApi,
  pane: OpenPaneRecord,
): { position: { referencePanel: string; direction: "left" | "right" | "within" } } | Record<string, never> {
  const openIds = new Set(api.panels.map((p) => p.id));
  const others = workspaceStore.getState().panes.filter((p) => p.id !== pane.id && openIds.has(p.id));
  const secondary = others.find((p) => p.slot === "secondary");
  if (pane.slot === "main") {
    return secondary ? { position: { referencePanel: secondary.id, direction: "left" } } : {};
  }
  if (secondary) return { position: { referencePanel: secondary.id, direction: "within" } };
  const main = others.find((p) => p.slot === "main");
  return main ? { position: { referencePanel: main.id, direction: "right" } } : {};
}

// The main slot is never left empty: with no main pane, welcome goes there.
// The one predicate both callers share - the boot sequence (nothing routed and
// nothing restored, or a saved layout that was itself empty) and the
// per-close relaunch (the user closed the single main pane). openPane's own
// placement rule is what actually puts it in the main slot: the slot is empty,
// so the next pane opened takes it.
function ensureMainPane(): void {
  const ws = workspaceStore.getState();
  if (ws.mainPane() === null) ws.openPane("welcome");
}

// The MAIN group's header never shows (Jesse: "I despise having the tab bar
// on the 'main' section of the screen") - it holds exactly one pane by rule,
// so a tab there would only ever label something already unambiguous. Every
// OTHER group's header always shows, whatever its pane count. dockview
// supports this natively - each group's `header.hidden` is its own documented
// flag, and it round-trips through toJSON/fromJSON as `hideHeader` - so this
// needs no CSS of its own. Identity is not lost with the main tab gone: every
// pane draws its own PaneScaffold header, carrying the same title a tab would.
//
// THE MAIN PANE IS REPLACEABLE, NOT CLOSEABLE - deliberate (Jesse, round 3).
// Every per-group header affordance dockview offers (the native tab (x), the
// "Pop out" right-header action) lives inside the container this hides, so the
// main pane has none of them. That is the intended design, not an oversight to
// route around: you change what the main slot shows by opening something else
// into it (openPane's placement rule displaces a welcome placeholder; anything
// else stacks to the right), never by closing it. Do not add a close button
// here, and do not un-hide this header to get one back.
// DockHost.test.tsx pins both halves ("the main pane offers no way to close
// it") so this can't be quietly undone as a "missing button" bug fix.
//
// The rule used to be "hidden whenever a group holds <=1 panes" - symmetric,
// but wrong: it hid the SECONDARY group's header too the moment it held
// exactly one pane, taking the native tab (x) - the only close affordance
// that group's lone pane ever had - down with it. Nothing else closes it: no
// pane's own PaneScaffold draws a close action, and the main-group rule above
// bars adding one there by design (that leaves the secondary group as the one
// place a close control could live). The result was a one-way door (kata
// 65zj): open a file "beside", or a subagent's transcript, and there was no
// way back short of a reload or dragging the splitter to zero. The rule below
// is keyed on GROUP IDENTITY (is this the main group) instead of a pane
// count, which is the only thing that was ever supposed to determine it -
// the main group's one-pane-ness is a permanent invariant of its slot, not a
// transient count secondary happens to share sometimes.
function syncGroupHeaders(api: DockviewApi): void {
  const mainPaneId = workspaceStore.getState().mainPane()?.id;
  for (const group of api.groups) {
    const hidden = group.panels.some((p) => p.id === mainPaneId);
    if (group.model.header.hidden !== hidden) group.model.header.hidden = hidden;
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
        saveTimer = undefined;
        saveLayout();
      }, LAYOUT_SAVE_DEBOUNCE_MS);
    });

    return () => {
      // A pending save is FLUSHED, not dropped. The debounce exists to
      // coalesce a gesture's many onDidLayoutChange bursts into one write
      // (see LAYOUT_SAVE_DEBOUNCE_MS) - "fewer writes", never "maybe no
      // write at all". Cancelling the timer without writing loses whatever
      // the user last did in the 400ms before DockHost came down: this
      // component unmounts on any route the shell doesn't resolve to a pane
      // (AppShell's NotFound branch), so a splitter drag immediately
      // followed by such a navigation silently forgot the new geometry.
      //
      // Flushing SYNCHRONOUSLY here is what keeps the original hazard this
      // cleanup was written for - a stray timer firing against a torn-down
      // api after teardown - fixed: the write happens now, while the api is
      // still registered (registerDockviewApi(null) is below, deliberately
      // after), and the timer that could have fired later is gone either
      // way. React destroys a parent's effects before its children's, so
      // DockviewReact has not disposed its grid yet at this point;
      // saveLayout's own null guard covers the case regardless.
      if (saveTimer !== undefined) {
        clearTimeout(saveTimer);
        saveLayout();
      }
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
          ...positionFor(api, pane),
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

    // Every add/remove above can change a group's pane count, so the tab-bar
    // rule is re-applied here rather than at each mutation site - one pass over
    // the current groups, idempotent, no bookkeeping.
    syncGroupHeaders(api);

    // The main slot never sits empty: closing the one main pane relaunches
    // welcome there rather than promoting something from the right-hand group
    // (Jesse's call). ensureMainPane is the same predicate the boot fallback
    // uses, so "the main slot is empty, put welcome in it" exists once.
    ensureMainPane();
  }, [api, panes]);

  // Focus reconciliation: pushes workspace.ts's focusedPaneId into dockview
  // whenever it doesn't already match (a fresh pane's own default-active
  // behavior on addPanel usually makes this a no-op for a brand new pane;
  // it's what actually moves focus for an existing one, e.g. a tree-rail
  // click in a later wave). `panes` is a dependency because a just-opened
  // pane's dockview panel may not exist until the structural effect above
  // creates it - same commit, declared first, so it already will have by
  // the time this one re-runs. Deliberately trigger-only: never read
  // inside this effect's own body.
  // biome-ignore lint/correctness/useExhaustiveDependencies: panes is a deliberate trigger-only dep for same-commit ordering, see above
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

    // MERGE, not suppress: whatever AppShell's routing glue already opened
    // during render (a deep link, or welcome for "/" - see its own comment
    // on why it opens the initial route's pane during render, before this
    // handler ever runs) is captured by TYPE+PARAMS - not id - before
    // attempting a restore, then re-opened via the normal openPane() path
    // AFTER it, on top of whatever the saved layout restored, focused.
    // Capturing type+params rather than id is deliberate on two counts:
    // (1) restoreLayout() replaces `panes` wholesale (dockview's fromJSON
    // has no concept of "merge with what's already there"), so the routed
    // pane's pre-restore id is gone from the store the instant restore
    // runs regardless; (2) re-deriving it via openPane() afterward reuses
    // that function's own same-params dedup for free - if the restored
    // layout already contains the identical session, this focuses THAT
    // panel instead of ever opening a second tab for it.
    //
    // Both the restore attempt and the routed re-open are unconditional
    // (not gated on "panes is currently empty" the way the previous,
    // provisional fix was) - restoreLayout() and openPane() are each
    // already idempotent/no-op-safe on their own terms (nothing stored ->
    // skipped outright below; nothing routed -> an empty loop), so there is
    // no "already non-empty" case left to specially suppress.
    //
    // Failure-mode floor, preserved exactly: restoreLayout()'s own
    // structural-validation failure (corrupt localStorage, or a restored
    // panel referencing an unregistered pane type) clears the store back to
    // empty (see workspace.ts) BEFORE the routed re-open loop runs - so a
    // corrupt saved layout still leaves the routed pane as the ONLY thing
    // that ends up open, the same "deep link wins alone" guarantee the
    // pre-merge implementation always provided.
    //
    // NOTE for whoever wires AppShell's routing glue to this store: React
    // runs child effects before parent effects within one commit, so THIS
    // handler (called from DockviewReact's own mount effect, a descendant
    // of AppShell) fires before any plain useEffect in AppShell ever could
    // - a deep-linked route opened via a normal AppShell useEffect would
    // race this exact sequence and land ALONGSIDE a spurious welcome tab,
    // not merged into place. AppShell must open its initial route's pane
    // during render (e.g. a useRef-guarded call in the component body, or
    // a useState lazy initializer) so it runs before ANY effect in the
    // tree - openPane's own same-params/singleton dedup makes that call
    // safe to repeat if StrictMode (or a future refactor) invokes it more
    // than once.
    // "welcome" is excluded here (kata eve5): it is the generic fallback for
    // "/" and any route that doesn't resolve to a real deep link, never a
    // deep link itself - and openPane's own placement rule guarantees no
    // saved layout ever CONTAINS one (it is displaced from the main slot
    // the instant any real pane opens, and nothing else ever restores one).
    // Re-opening it here therefore always resolves to "genuinely new" -
    // openPane's own final branch focuses that unconditionally, which is
    // correct for an actual deep link (see the routed-pane tests) but wrong
    // for the plain "/" fallback: it forced a fresh, focused welcome pane
    // into the secondary group on every ordinary reload, stealing focus off
    // whatever the saved layout had actually restored. ensureMainPane()
    // below remains the sole path that opens welcome for real - it only
    // fires when the main slot is genuinely empty (nothing routed AND
    // nothing restored, or a saved-but-empty layout), which is the one case
    // this exclusion must still cover.
    const routed = workspaceStore
      .getState()
      .panes.filter((p) => p.type !== "welcome")
      .map((p) => ({ type: p.type, params: p.params }));

    const stored = readStoredLayout();
    if (stored !== undefined) {
      workspaceStore.getState().restoreLayout(stored);
    }
    // keepExistingFocus: the restored layout's own active tab wins over the
    // address bar for a pane the layout ALREADY contains. On an ordinary
    // reload the URL still names whichever pane the user first deep-linked
    // to, which is usually not the tab they were last on - re-opening it
    // focused made every reload snap focus back to that first pane, silently
    // discarding the active tab dockview had faithfully persisted (verified
    // live: the saved leaf's `activeView` was correct both before and after
    // the reload; only the in-page focus was wrong). A deep link to a pane
    // the saved layout does NOT contain still creates a pane, and openPane
    // still focuses that - a genuinely new deep link is exactly the case
    // that should win.
    for (const pane of routed) {
      workspaceStore.getState().openPane(pane.type, pane.params, { keepExistingFocus: true });
    }

    // Backstop: a blank main slot with no chrome of its own to open a new pane
    // from is a dead end - covers both "nothing was ever routed AND nothing
    // was saved" and "a restore succeeded but the saved layout was itself
    // already empty" (every tab closed before the last save). The same
    // predicate the reconciliation effect applies per-close.
    ensureMainPane();

    setApi(event.api);
  }

  return (
    <div className={styles.host}>
      <DockviewReact
        components={COMPONENTS}
        onReady={handleReady}
        className="dockview-theme-serf"
        rightHeaderActionsComponent={PopoutHeaderAction}
      />
    </div>
  );
}
