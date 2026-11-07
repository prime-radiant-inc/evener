// Whether a zoom pane's cascade hosts a conversation as one of its embedded
// scopes. Shared by the document opener (a cascade column's filename open
// binds the hosting pane as the document's origin) and AppShell's route
// reconciliation (a conversation inside a cascade is the route's companion).

import type { OpenPaneRecord } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import { deriveCascadePath, parseZoomParams, type SessionZoomParams } from "./intent";

// The canonical column enumeration: every conversation the authoritative
// cascade-ancestry derivation renders as one of this cascade's columns,
// resolved exactly as the rendered path resolves it. An ancestor supplied
// only by the activity context's authoritative chain - absent from the
// leaf, the source, and the recorded edge segment - is a column here.
// Every seat/selection consumer enumerates through this one derivation;
// none keeps a parallel column list.
export function zoomParamsColumnRefs(params: SessionZoomParams): string[] {
  // The client's latest activity context refines canonical ancestry;
  // without a client or a snapshot the params' own edge chain still names
  // every column's requested ref.
  const client = connectionStore.getState().client;
  const context = client ? (sessionActivitySnapshot(client, params.ref, "session")?.context ?? null) : null;
  return [...new Set(deriveCascadePath(params, context).scopes.map((scope) => scope.requestedRef))];
}

export function zoomParamsCoverConversation(params: SessionZoomParams, session: string): boolean {
  return zoomParamsColumnRefs(params).includes(session);
}

export function zoomPaneCoversConversation(pane: OpenPaneRecord, session: string): boolean {
  if (pane.type !== "sessionZoom") return false;
  const params = parseZoomParams(pane.params);
  return params !== null && zoomParamsCoverConversation(params, session);
}
