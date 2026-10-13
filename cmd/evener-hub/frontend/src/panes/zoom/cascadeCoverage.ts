// Whether a zoom pane's cascade hosts a conversation as one of its embedded
// scopes. Shared by the document opener (a cascade column's filename open
// binds the hosting pane as the document's origin) and AppShell's route
// reconciliation (a conversation inside a cascade is the route's companion).

import type { OpenPaneRecord } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import { deriveCascadePath, parseZoomParams, type SessionZoomParams } from "./intent";

export function zoomParamsCoverConversation(params: SessionZoomParams, session: string): boolean {
  // The client's latest activity context refines canonical ancestry;
  // without a client or a snapshot the params' own edge chain still names
  // every column's requested ref.
  const client = connectionStore.getState().client;
  const context = client ? (sessionActivitySnapshot(client, params.ref, "session")?.context ?? null) : null;
  return deriveCascadePath(params, context).scopes.some((scope) => scope.requestedRef === session);
}

export function zoomPaneCoversConversation(pane: OpenPaneRecord, session: string): boolean {
  if (pane.type !== "sessionZoom") return false;
  const params = parseZoomParams(pane.params);
  return params !== null && zoomParamsCoverConversation(params, session);
}
