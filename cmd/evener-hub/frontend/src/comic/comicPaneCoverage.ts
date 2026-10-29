import { type OpenPaneRecord, sourcePaneSessionRef, workspaceStore } from "../shell/workspace";

// A projection of mounted Comic readers, not an ancestry index or a read owner.
// Exact pane records fence reused IDs. The committing reader replaces its proof
// and its layout-effect cleanup retires it on collapse, replacement or unmount.
const coverage = new WeakMap<OpenPaneRecord, { rootRef: string; children: ReadonlySet<string> }>();
// Phone navigation unmounts the reader. Only the document opened while that
// reader was mounted retains its proof, never a later request from a stale ID.
const documents = new WeakMap<OpenPaneRecord, { origin: OpenPaneRecord; rootRef: string; session: string }>();

export function captureComicDocumentCoverage(
  origin: OpenPaneRecord | undefined,
  session: string,
): (document: OpenPaneRecord) => void {
  const mounted = origin && coverage.get(origin);
  const proof =
    origin && mounted && comicPaneCoversConversation(origin, session)
      ? { origin, rootRef: mounted.rootRef, session }
      : undefined;
  return (document) => {
    documents.delete(document);
    if (proof) documents.set(document, proof);
  };
}

export function comicDocumentCoversConversation(
  document: OpenPaneRecord,
  origin: OpenPaneRecord,
  session: string,
): boolean {
  const proof = documents.get(document);
  return (
    !!proof &&
    proof.origin === origin &&
    proof.session === session &&
    sourcePaneSessionRef(origin) === proof.rootRef &&
    workspaceStore.getState().panes.includes(origin)
  );
}

export function publishComicPaneCoverage(
  pane: OpenPaneRecord,
  rootRef: string,
  children: Iterable<string>,
): () => void {
  const proof = { rootRef, children: new Set(children) };
  coverage.set(pane, proof);
  return () => {
    if (coverage.get(pane) === proof) coverage.delete(pane);
  };
}

export function comicPaneCoversConversation(pane: OpenPaneRecord, session: string): boolean {
  const proof = coverage.get(pane);
  return (
    !!proof &&
    (pane.type === "session" || pane.type === "transcript") &&
    workspaceStore.getState().panes.includes(pane) &&
    sourcePaneSessionRef(pane) === proof.rootRef &&
    proof.children.has(session)
  );
}
