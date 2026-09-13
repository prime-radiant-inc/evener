import { useEffect } from "react";
import type { ThreadModel } from "../../protocol/model";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { selectSessionWatches } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { EmptyState, PaneScaffold } from "../../widgets";
import { ActivityPanelBody } from "../session/chrome/ActivityPanel";
import { DetailsPanelBody } from "../session/chrome/DetailsPanel";
import { TasksPanelBody } from "../session/chrome/TasksPanel";
import { NOW_TICK_MS, useNowTick } from "../session/liveness";
import { type SessionPanelKind, type SessionPanelParams, sessionPanelTitle } from "./index";

export interface SessionPanelPaneProps extends PaneProps<SessionPanelParams> {
  kind: SessionPanelKind;
}

function isSessionPanelParams(value: SessionPanelParams): value is SessionPanelParams {
  return typeof value?.ref === "string" && value.ref.length > 0;
}

function DetailsPaneBody({ sessionRef, model }: { sessionRef: string; model: ThreadModel }) {
  const now = useNowTick(NOW_TICK_MS);
  return <DetailsPanelBody sessionRef={sessionRef} model={model} now={now} />;
}

export function SessionPanelPane({ params, paneId, focused, kind }: SessionPanelPaneProps) {
  if (!isSessionPanelParams(params)) {
    throw new Error("SessionPanelPane: params.ref must be a non-empty string");
  }
  const { ref } = params;
  const model = useThreadsStore((state) => state.threads.get(ref));
  // The standalone Activity pane reads the same navigation-fed watches the
  // pane footer's chrome does. It deliberately installs no clock of its own:
  // watch detail falls back to the activity tree's own live tick, which
  // ActivityTree enables whenever the session carries watch rows - so a
  // watch-only, live-row-free tree still ticks (pinned by ActivityTree's
  // "a session whose only work is watches still runs the clock"). Do not drop
  // that watch-row condition: without it a watch-only tree would freeze. The
  // Details pane's NOW_TICK_MS clock stays Details-only.
  // Narrow subscription: selectSessionWatches keeps its result identity while
  // this session's watch content is unchanged, so the pane no longer re-renders
  // on every navigation update for any session.
  const watches = useNavigationStore((state) => selectSessionWatches(ref, state));

  useEffect(() => {
    let started = false;
    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore
        .getState()
        .ensureThread(ref)
        .catch(() => {});
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      if (started) threadsStore.getState().releaseThread(ref);
    };
  }, [ref]);

  const title = sessionPanelTitle(kind, ref, model?.name);

  if (!model) {
    return (
      <PaneScaffold title={title} paneId={paneId} focused={focused} scaffoldMarker={`session-panel:${kind}:${ref}`}>
        <EmptyState title="Loading session panel…" />
      </PaneScaffold>
    );
  }

  const body =
    kind === "tasks" ? (
      <TasksPanelBody sessionRef={ref} model={model} />
    ) : kind === "activity" ? (
      <ActivityPanelBody sessionRef={ref} model={model} watches={watches} />
    ) : (
      <DetailsPaneBody sessionRef={ref} model={model} />
    );

  return (
    <PaneScaffold title={title} paneId={paneId} focused={focused} scaffoldMarker={`session-panel:${kind}:${ref}`}>
      <div data-pane-id={paneId}>{body}</div>
    </PaneScaffold>
  );
}
