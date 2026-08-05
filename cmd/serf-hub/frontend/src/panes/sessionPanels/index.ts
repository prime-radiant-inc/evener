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

const pane = (kind: SessionPanelKind) =>
  lazy(() =>
    import("./SessionPanelPane").then(({ SessionPanelPane }) => ({
      default: (props: PaneProps<SessionPanelParams>) => createElement(SessionPanelPane, { ...props, kind }),
    })),
  );

const panelTitle = (prefix: string) => (params: SessionPanelParams, ctx: PaneTitleCtx) =>
  `${prefix} · ${ctx.threadName?.(params.ref) ?? params.ref}`;

registerPane<SessionPanelParams>({
  id: "sessionTasks",
  title: panelTitle("Tasks"),
  component: pane("tasks"),
});

registerPane<SessionPanelParams>({
  id: "sessionActivity",
  title: panelTitle("Activity"),
  component: pane("activity"),
});

registerPane<SessionPanelParams>({
  id: "sessionDetails",
  title: panelTitle("Details"),
  component: pane("details"),
});
