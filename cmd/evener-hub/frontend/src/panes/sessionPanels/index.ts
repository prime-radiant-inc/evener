// Eagerly registers the session panel pane types while keeping their shared
// host in a lazy chunk. The descriptors deliberately share one host;
// the wrappers supply the selected panel kind without putting it in params.
import { createElement, lazy } from "react";
import type { PaneProps, PaneTitleCtx } from "../../shell/paneRegistry";
import { registerPane } from "../../shell/paneRegistry";

export interface SessionPanelParams {
  ref: string;
}

export type SessionPanelKind = "tasks" | "details";

/** The single title grammar shared by the pane registry and PaneScaffold. */
export function sessionPanelTitle(kind: SessionPanelKind, ref: string, name?: string): string {
  const label = kind === "tasks" ? "Tasks" : "Details";
  return `${label} · ${name || ref}`;
}

/** The workspace pane type each panel kind opens (SessionMenu, rail rows). */
export function sessionPanelPaneType(kind: SessionPanelKind): "sessionTasks" | "sessionDetails" {
  return kind === "tasks" ? "sessionTasks" : "sessionDetails";
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
  id: "sessionDetails",
  title: panelTitle("details"),
  component: pane("details"),
});
