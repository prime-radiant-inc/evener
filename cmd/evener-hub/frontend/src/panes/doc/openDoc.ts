// The one document opener used by file/image producers. It binds the reference
// to the source session, retains the exact source pane for Back, and routes the
// deduplicated document through the real openBeside action.
import { bindFilePath, type FileReference, isImagePath } from "@evener/appwire-client/docContent";
import { captureComicDocumentCoverage, comicPaneCoversConversation } from "../../comic/comicPaneCoverage";
import * as paneActions from "../../shell/paneActions";
import {
  documentPaneState,
  type OpenPaneRecord,
  presentedPanes,
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
  /** Which root `path` is relative to, for a document the session's working
   * directory does not contain. Absent means the working directory, the only
   * root a document pane knew before retained artifacts were linkable. The
   * params outlive the pane's in-memory reference (a restored workspace
   * rebuilds it from params alone), so `path` and `root` together are what
   * let the pane read the same file again after a reload. */
  root?: "artifacts";
  /** The exact retained file this pane opened, as an absolute path. `path` is
   * relative to `root`, and a stable session ref can outlive the session it
   * named (Clear moves the ref to a new session), so a restored pane needs
   * the concrete target: binding `path` against the replacement session's
   * directory would quietly show a different file of the same name. The pane
   * refuses instead when the target is no longer inside the directory the
   * ref now names. */
  artifactTarget?: string;
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
  if (pane.type === "sessionZoom")
    return zoomPaneCoversConversation(pane, session) || comicPaneCoversConversation(pane, session);
  if (pane.type !== "session" && pane.type !== "transcript") return false;
  return sourcePaneSessionRef(pane) === session || comicPaneCoversConversation(pane, session);
}

function isRequestedDocument(pane: OpenPaneRecord, params: DocParams): boolean {
  if (pane.type !== "doc") return false;
  const candidate = pane.params as Partial<DocParams>;
  return (
    candidate.session === params.session &&
    candidate.path === params.path &&
    candidate.kind === params.kind &&
    (candidate.root ?? undefined) === (params.root ?? undefined) &&
    // The target is part of what a document is, not decoration: two reports
    // with the same name in different sessions are different documents, and
    // matching them would let one pane overwrite the other's saved target.
    (candidate.artifactTarget ?? undefined) === (params.artifactTarget ?? undefined)
  );
}

export function openDocBeside(request: DocumentOpenRequest): void {
  const path = request.reference.provenance === "absolute" ? request.reference.readTarget : request.reference.path;
  const bound = bindFilePath(path, request.reference.cwd);
  // The recorded reference must keep saying which root it came from. DocPane
  // rebinds against the session cwd once the thread's cwd differs from an
  // artifacts dir, so a reference that lost the marker would be re-recorded
  // pointing into the working tree.
  const reference = bound && request.reference.root === "artifacts" ? { ...bound, root: "artifacts" as const } : bound;
  if (!reference) return;
  const artifacts = request.reference.root === "artifacts";
  const params: DocParams = {
    session: request.session,
    // Both roots store the path relative to their own root, so a document
    // records the same path the reference does and the shell's companion
    // check compares like with like.
    path: reference.path,
    ...(artifacts ? { root: "artifacts" as const, artifactTarget: reference.readTarget } : {}),
    kind: isImagePath(reference.path) ? "image" : "file",
  };
  const workspace = workspaceStore.getState();
  const presented = presentedPanes(workspace.panes);
  const requestedOrigin = presented.find((pane) => pane.id === request.sourcePaneId);
  const origin =
    (requestedOrigin && paneOwnsSession(requestedOrigin, request.session) ? requestedOrigin : undefined) ??
    presented.find((pane) => pane.type === "session" && paneOwnsSession(pane, request.session)) ??
    presented.find((pane) => pane.type === "transcript" && paneOwnsSession(pane, request.session));
  const existingDocument = presented.find((pane) => isRequestedDocument(pane, params));
  const retainComicCoverage = captureComicDocumentCoverage(origin, request.session);
  if (existingDocument) {
    const previous = documentPaneState(existingDocument);
    retainComicCoverage(existingDocument);
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
  const document = presentedPanes(workspaceStore.getState().panes).find((pane) => isRequestedDocument(pane, params));
  if (!document) return;
  retainComicCoverage(document);
  recordDocumentPaneState(document, {
    reference,
    origin,
    reopen: 0,
  });
}
