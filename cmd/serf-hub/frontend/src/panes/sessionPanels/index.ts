// Eagerly registers the session panel pane types while keeping their shared
// host in a lazy chunk. The three descriptors deliberately share one host;
// the wrappers supply the selected panel kind without putting it in params.
import { createElement, lazy } from "react";
import type { PaneProps, PaneTitleCtx } from "../../shell/paneRegistry";
import { registerPane } from "../../shell/paneRegistry";

export interface SessionPanelParams {
  ref: string;
}

export type SessionPanelKind = "tasks" | "activity" | "details";

/** The single title grammar shared by the pane registry and PaneScaffold. */
export function sessionPanelTitle(kind: SessionPanelKind, ref: string, name?: string): string {
  const label = kind === "tasks" ? "Tasks" : kind === "activity" ? "Activity" : "Details";
  return `${label} · ${name || ref}`;
}

const pane = (kind: SessionPanelKind) =>
  lazy(() =>
    import("./SessionPanelPane").then(({ SessionPanelPane }) => ({
      default: (props: PaneProps<SessionPanelParams>) => createElement(SessionPanelPane, { ...props, kind }),
    })),
  );

const panelTitle = (kind: SessionPanelKind) => (params: SessionPanelParams, ctx: PaneTitleCtx) =>
  sessionPanelTitle(kind, params.ref, ctx.threadName?.(params.ref));

registerPane<SessionPanelParams>({
  id: "sessionTasks",
  title: panelTitle("tasks"),
  component: pane("tasks"),
});

registerPane<SessionPanelParams>({
  id: "sessionActivity",
  title: panelTitle("activity"),
  component: pane("activity"),
});

registerPane<SessionPanelParams>({
  id: "sessionDetails",
  title: panelTitle("details"),
  component: pane("details"),
});
