// The one document opener used by file/image producers. It binds the reference
// to the source session, retains the exact source pane for Back, and routes the
// deduplicated document through the real openBeside action.
import { bindFilePath, type FileReference, isImagePath } from "@evener/appwire-client/docContent";
import * as paneActions from "../../shell/paneActions";
import {
  documentPaneState,
  type OpenPaneRecord,
  recordDocumentPaneState,
  sourcePaneSessionRef,
  workspaceStore,
} from "../../shell/workspace";
import { zoomPaneCoversConversation } from "../zoom/cascadeCoverage";
// Importing the opener registers the "doc" pane type (side effect of ./index):
// every producer that can open a doc pane already imports this module, so the
// pane is guaranteed registered before openDocBeside routes it through dockview.
import "./index";

export interface DocParams {
  session: string;
  path: string;
  kind: "file" | "image";
}

export interface DocumentOpenRequest {
  readonly session: string;
  readonly reference: FileReference;
  readonly sourcePaneId: string;
}

function paneOwnsSession(pane: OpenPaneRecord, session: string): boolean {
  // An embedded cascade column has no pane of its own: the zoom pane
  // hosting the conversation owns the request when its cascade covers the
  // session.
  if (pane.type === "sessionZoom") return zoomPaneCoversConversation(pane, session);
  if (pane.type !== "session" && pane.type !== "transcript") return false;
  return sourcePaneSessionRef(pane) === session;
}

function isRequestedDocument(pane: OpenPaneRecord, params: DocParams): boolean {
  if (pane.type !== "doc") return false;
  const candidate = pane.params as Partial<DocParams>;
  return candidate.session === params.session && candidate.path === params.path && candidate.kind === params.kind;
}

export function openDocBeside(request: DocumentOpenRequest): void {
  const path = request.reference.provenance === "absolute" ? request.reference.readTarget : request.reference.path;
  const reference = bindFilePath(path, request.reference.cwd);
  if (!reference) return;
  const params: DocParams = {
    session: request.session,
    path: reference.path,
    kind: isImagePath(reference.path) ? "image" : "file",
  };
  const workspace = workspaceStore.getState();
  const requestedOrigin = workspace.panes.find((pane) => pane.id === request.sourcePaneId);
  const origin =
    (requestedOrigin && paneOwnsSession(requestedOrigin, request.session) ? requestedOrigin : undefined) ??
    workspace.panes.find((pane) => pane.type === "session" && paneOwnsSession(pane, request.session)) ??
    workspace.panes.find((pane) => pane.type === "transcript" && paneOwnsSession(pane, request.session));
  const existingDocument = workspace.panes.find((pane) => isRequestedDocument(pane, params));
  if (existingDocument) {
    const previous = documentPaneState(existingDocument);
    // Publish a changed binding before openBeside focuses the reused pane, so
    // consumers never observe the newly focused document with its old source.
    recordDocumentPaneState(existingDocument, {
      reference,
      origin,
      reopen: previous === undefined ? 0 : previous.reopen + 1,
    });
  }
  // A cascade origin keeps its secondary slot: promoting the inspection into
  // main would break "main is the routed session" for the origin
  // conversation and let the next primary navigation replace the whole
  // cascade. The document opens beside the zoom either way; conversation
  // origins still take main so the document lands beside its source.
  if (origin && origin.type !== "sessionZoom") workspace.promotePane(origin.id);
  paneActions.openBeside({ type: "doc", params });
  if (existingDocument) return;
  const document = workspaceStore.getState().panes.find((pane) => isRequestedDocument(pane, params));
  if (!document) return;
  recordDocumentPaneState(document, {
    reference,
    origin,
    reopen: 0,
  });
}
