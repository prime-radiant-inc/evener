import { zoomPaneCoversConversation } from "../panes/zoom/cascadeCoverage";
import { type OpenPaneRecord, sourcePaneSessionRef, workspaceStore } from "../shell/workspace";

// A projection of mounted Comic readers, not an ancestry index or a read owner.
// Exact pane records fence reused IDs. Columns share a pane, but each reader
// replaces and retires only its own publication on collapse or unmount.
type Coverage = { rootRef: string; children: ReadonlySet<string> };
const coverage = new WeakMap<OpenPaneRecord, Map<object, Coverage>>();
// Phone navigation unmounts the reader. Only the document opened while that
// reader was mounted retains its proof, never a later request from a stale ID.
const documents = new WeakMap<OpenPaneRecord, { origin: OpenPaneRecord; rootRef: string; session: string }>();

export function captureComicDocumentCoverage(
  origin: OpenPaneRecord | undefined,
  session: string,
): (document: OpenPaneRecord) => void {
  const mounted = origin && mountedCoverage(origin, session);
  const proof = origin && mounted ? { origin, rootRef: mounted.rootRef, session } : undefined;
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
  return !!proof && proof.origin === origin && proof.session === session && hostsReader(origin, proof.rootRef);
}

export function publishComicPaneCoverage(
  pane: OpenPaneRecord,
  rootRef: string,
  children: Iterable<string>,
  reader: object = pane,
): () => void {
  const proof = { rootRef, children: new Set(children) };
  let readers = coverage.get(pane);
  if (!readers) {
    readers = new Map();
    coverage.set(pane, readers);
  }
  readers.set(reader, proof);
  return () => {
    if (readers.get(reader) === proof) readers.delete(reader);
  };
}

function hostsReader(pane: OpenPaneRecord, rootRef: string): boolean {
  return (
    workspaceStore.getState().panes.includes(pane) &&
    (pane.type === "sessionZoom"
      ? zoomPaneCoversConversation(pane, rootRef)
      : (pane.type === "session" || pane.type === "transcript") && sourcePaneSessionRef(pane) === rootRef)
  );
}

function mountedCoverage(pane: OpenPaneRecord, session: string): Coverage | undefined {
  const readers = coverage.get(pane);
  if (!readers) return;
  return [...readers.values()].find((proof) => proof.children.has(session) && hostsReader(pane, proof.rootRef));
}

export function comicPaneCoversConversation(pane: OpenPaneRecord, session: string): boolean {
  return mountedCoverage(pane, session) !== undefined;
}
