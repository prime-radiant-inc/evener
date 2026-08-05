import { useEffect } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { EmptyState, PaneScaffold } from "../../widgets";
import { BackToParentAction } from "../backToParentAction";
import { ActivityPanelBody } from "../session/chrome/ActivityPanel";
import { DetailsPanelBody } from "../session/chrome/DetailsPanel";
import { TasksPanelBody } from "../session/chrome/TasksPanel";
import { NOW_TICK_MS, useNowTick } from "../session/liveness";
import type { SessionPanelKind, SessionPanelParams } from "./index";

export interface SessionPanelPaneProps extends PaneProps<SessionPanelParams> {
  kind: SessionPanelKind;
}

function isSessionPanelParams(value: SessionPanelParams): value is SessionPanelParams {
  return typeof value?.ref === "string" && value.ref.length > 0;
}

function panelTitle(kind: SessionPanelKind, refOrName: string): string {
  const label = kind === "tasks" ? "Tasks" : kind === "activity" ? "Activity" : "Details";
  return `${label} · ${refOrName}`;
}

export function SessionPanelPane({ params, paneId, focused: _focused, kind }: SessionPanelPaneProps) {
  if (!isSessionPanelParams(params)) {
    throw new Error("SessionPanelPane: params.ref must be a non-empty string");
  }
  const { ref } = params;
  const model = useThreadsStore((state) => state.threads.get(ref));
  const now = useNowTick(NOW_TICK_MS);

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

  const title = panelTitle(kind, model?.name || ref);

  if (!model) {
    return (
      <PaneScaffold title={title} actions={<BackToParentAction parentRef={ref} />}>
        <EmptyState title="Loading session panel…" />
      </PaneScaffold>
    );
  }

  const body =
    kind === "tasks" ? (
      <TasksPanelBody sessionRef={ref} model={model} />
    ) : kind === "activity" ? (
      <ActivityPanelBody sessionRef={ref} model={model} />
    ) : (
      <DetailsPanelBody sessionRef={ref} model={model} now={now} />
    );

  return (
    <PaneScaffold title={title} actions={<BackToParentAction parentRef={ref} />}>
      <div data-pane-id={paneId}>{body}</div>
    </PaneScaffold>
  );
}
