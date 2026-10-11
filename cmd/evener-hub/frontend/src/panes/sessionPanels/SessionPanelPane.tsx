import type { ThreadModel } from "@evener/appwire-client";
import type { PaneProps } from "../../shell/paneRegistry";
import { useThreadModel } from "../../stores/useThreadModel";
import { EmptyState, PaneScaffold } from "../../widgets";
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

function DetailsPaneBody({ model }: { sessionRef: string; model: ThreadModel }) {
  const now = useNowTick(NOW_TICK_MS);
  return <DetailsPanelBody model={model} now={now} />;
}

export function SessionPanelPane({ params, paneId, focused, kind }: SessionPanelPaneProps) {
  if (!isSessionPanelParams(params)) {
    throw new Error("SessionPanelPane: params.ref must be a non-empty string");
  }
  const { ref } = params;
  const model = useThreadModel(ref);

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
    ) : (
      <DetailsPaneBody sessionRef={ref} model={model} />
    );

  return (
    <PaneScaffold title={title} paneId={paneId} focused={focused} scaffoldMarker={`session-panel:${kind}:${ref}`}>
      <div data-pane-id={paneId}>{body}</div>
    </PaneScaffold>
  );
}
