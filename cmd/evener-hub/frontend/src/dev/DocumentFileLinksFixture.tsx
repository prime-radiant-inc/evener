import { AppwireClient } from "@evener/appwire-client";
import { lazy, StrictMode } from "react";
import DocPane from "../panes/doc/DocPane";
import type { DocParams } from "../panes/doc/openDoc";
import Session, { type SessionPaneParams } from "../panes/session/Session";
import "../panes/doc/openDoc";
import { AppShell } from "../shell/AppShell";
import { type PaneProps, registerPane } from "../shell/paneRegistry";
import { workspaceStore } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import "../panes/welcome";

function SourcePane(props: PaneProps<SessionPaneParams>) {
  return (
    <div data-file-links-source={props.paneId} style={{ height: "100%" }}>
      <Session {...props} />
    </div>
  );
}
function DocumentPane(props: PaneProps<DocParams>) {
  return (
    <div data-file-links-document={props.paneId} style={{ height: "100%" }}>
      <DocPane {...props} />
    </div>
  );
}
registerPane<SessionPaneParams>({
  id: "session",
  title: (params) => params.ref,
  component: lazy(async () => ({ default: SourcePane })),
});
registerPane<DocParams>({
  id: "doc",
  title: (params) => params.path,
  component: lazy(async () => ({ default: DocumentPane })),
});

export const fileLinksClient = new AppwireClient({
  url: `${location.origin.replace(/^http/, "ws")}/rpc`,
  clientInfo: { name: "document-file-links-guard", version: "fixture" },
});
connectionStore.getState().connect(fileLinksClient);
export async function startFileLinksFixture(ref: string) {
  await fileLinksClient.connect();
  workspaceStore.getState().openPane("session", { ref });
}
export function DocumentFileLinksFixture() {
  return (
    <StrictMode>
      <AppShell client={fileLinksClient} />
    </StrictMode>
  );
}
