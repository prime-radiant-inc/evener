// The one document opener used by file/image producers. It binds the reference
// to the source session, retains the exact source pane for Back, and routes the
// deduplicated document through the real openBeside action. openDoc.test.ts's
// namespace import (not a named one) is deliberate: it lets the test spy
// openBeside through the module object, the reliable vitest seam for asserting
// this delegation.
import type { DestinationProvenance } from "@evener/appwire-client";
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

// The opening provenance a doc pane carries when its producers captured one:
// the two browser destination shapes only. A restored or native destination is
// not a serialized opening origin and is never stored here.
export type DocOpeningOrigin = Extract<
  DestinationProvenance,
  { kind: "browser-origin" } | { kind: "browser-readonly-session" }
>;

export interface DocParams {
  session: string;
  path: string;
  // The persisted doc kind vocabulary: the current file/image kinds plus the
  // legacy "text" kind older panels still carry (and a restored layout must
  // preserve, not rewrite). DocPane renders anything but an image as a file.
  kind: "file" | "image" | "text";
  // The scope that opened this document, captured at the real opening action.
  // Absent for the approved older persisted shape (and when no provider supplied
  // one), which stays readable and session-bound.
  origin?: DocOpeningOrigin;
}

export interface DocumentOpenRequest {
  readonly session: string;
  readonly reference: FileReference;
  readonly sourcePaneId: string;
  // The browsing scope that opened this document, captured by the producer
  // inside the opening pane through the doc-opening context. Absent when no
  // provider supplied one; the document then stays origin-less.
  readonly origin?: DocOpeningOrigin;
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

// Captured origins are fresh objects on every open, so refresh decisions
// compare them by value: two captures of the same scope are the same origin.
function sameOpeningOrigin(a: DocOpeningOrigin | undefined, b: DocOpeningOrigin | undefined): boolean {
  if (a === undefined || b === undefined) return a === b;
  if (a.kind === "browser-origin") {
    return b.kind === "browser-origin" && a.paneId === b.paneId && a.compositionOwnerId === b.compositionOwnerId;
  }
  return b.kind === "browser-readonly-session" && a.sourcePaneId === b.sourcePaneId;
}

export function openDocBeside(request: DocumentOpenRequest): void {
  const path = request.reference.provenance === "absolute" ? request.reference.readTarget : request.reference.path;
  const reference = bindFilePath(path, request.reference.cwd);
  if (!reference) return;
  const params: DocParams = {
    session: request.session,
    path: reference.path,
    kind: isImagePath(reference.path) ? "image" : "file",
    ...(request.origin === undefined ? {} : { origin: request.origin }),
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
    // The serialized origin must follow the live capture: a note authored
    // after this reuse captures params.origin as its destination, so leaving
    // the first composer's identity here would strand every later annotation
    // on a composer that no longer exists. An originless capture replaces it
    // too: a surface without composition authority must leave new annotations
    // PENDING, not deliver them into the first opener's composer. Equal
    // provenance (both captured or both absent) keeps the record untouched.
    // Destinations already captured by prepared or saved operations keep
    // their own payloads. The assignment keeps the pane record's identity,
    // like promotePane's slot swap, so the workspace's per-record state stays
    // keyed to it; the publish below carries the change to consumers.
    const capturedOrigin = request.origin;
    if (!sameOpeningOrigin((existingDocument.params as Partial<DocParams>).origin, capturedOrigin)) {
      existingDocument.params = params;
    }
    // Publish a changed binding before the reused pane is focused, so
    // consumers never observe the newly focused document with its old source.
    recordDocumentPaneState(existingDocument, {
      reference,
      origin,
      reopen: previous === undefined ? 0 : previous.reopen + 1,
    });
    // The document is already open, so focus it here rather than re-asking
    // openPane: that dedupe compares FULL params, and the opening origin
    // churns with every composer remount, so the re-ask would read the reused
    // pane as a different document and open a duplicate beside it. A
    // document's identity is its session/path/kind (isRequestedDocument),
    // never its opening origin.
    if (origin && origin.type !== "sessionZoom") workspace.promotePane(origin.id);
    workspace.focusPane(existingDocument.id);
    return;
  }
  // A cascade origin keeps its secondary slot: promoting the inspection into
  // main would break "main is the routed session" for the origin
  // conversation and let the next primary navigation replace the whole
  // cascade. The document opens beside the zoom either way; conversation
  // origins still take main so the document lands beside its source.
  if (origin && origin.type !== "sessionZoom") workspace.promotePane(origin.id);
  paneActions.openBeside({ type: "doc", params });
  const document = workspaceStore.getState().panes.find((pane) => isRequestedDocument(pane, params));
  if (!document) return;
  recordDocumentPaneState(document, {
    reference,
    origin,
    reopen: 0,
  });
}
