import { useEffect } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { EmptyState, PaneScaffold } from "../../widgets";
import { BackToParentAction } from "../backToParentAction";
import { ActivityPanel } from "../session/chrome/ActivityPanel";
import { DetailsPanel } from "../session/chrome/DetailsPanel";
import { TasksPanel } from "../session/chrome/TasksPanel";
import type { SessionPanelKind, SessionPanelParams } from "./index";

export interface SessionPanelPaneProps extends PaneProps<SessionPanelParams> {
  kind: SessionPanelKind;
}

function isSessionPanelParams(value: SessionPanelParams): value is SessionPanelParams {
  return typeof value?.ref === "string" && value.ref.length > 0;
}

export function SessionPanelPane({ params, paneId, focused: _focused, kind }: SessionPanelPaneProps) {
  if (!isSessionPanelParams(params)) {
    throw new Error("SessionPanelPane: params.ref must be a non-empty string");
  }
  const { ref } = params;
  const model = useThreadsStore((state) => state.threads.get(ref));

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

  if (!model) {
    return (
      <PaneScaffold title={ref} actions={<BackToParentAction parentRef={ref} />}>
        <EmptyState title="Loading session panel…" />
      </PaneScaffold>
    );
  }

  const body =
    kind === "tasks" ? (
      <TasksPanel sessionRef={ref} model={model} hideTrigger />
    ) : kind === "activity" ? (
      <ActivityPanel sessionRef={ref} model={model} now={Date.now()} hideTrigger />
    ) : (
      <DetailsPanel model={model} now={Date.now()} hideTrigger />
    );

  const title = kind === "tasks" ? "Tasks" : kind === "activity" ? "Activity" : "Details";
  return (
    <PaneScaffold title={title} actions={<BackToParentAction parentRef={ref} />}>
      <div data-pane-id={paneId}>{body}</div>
    </PaneScaffold>
  );
}
