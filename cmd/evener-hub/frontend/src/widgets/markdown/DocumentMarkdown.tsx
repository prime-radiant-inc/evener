import { type ReactElement, useLayoutEffect, useMemo, useRef } from "react";
import { MermaidDiagram } from "../mermaid";
import {
  type BoundDocumentMap,
  bindDocumentMap,
  type DocumentRenderIdentity,
  renderDocument,
} from "./documentSourceMap";
import { MARKDOWN_CLASS } from "./rendering";

export function DocumentMarkdown(props: {
  source: string;
  identity: DocumentRenderIdentity;
  onMap(map: BoundDocumentMap | null): void;
}): ReactElement {
  const { source, identity, onMap } = props;
  const { displayedSnapshotId, mappingId } = identity;
  const rendered = useMemo(
    () => renderDocument(source, { displayedSnapshotId, mappingId }),
    [source, displayedSnapshotId, mappingId],
  );
  const root = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!root.current) return;
    const map = bindDocumentMap(root.current, rendered.map);
    onMap(map);
    return () => {
      map.dispose();
      onMap(null);
    };
  }, [rendered, onMap]);
  // Keep React from resetting innerHTML after the binder has unwrapped its
  // verified markers. A changed source/identity builds fresh content and maps.
  const content = useMemo(
    () =>
      rendered.segments.map((segment) =>
        segment.kind === "mermaid" ? (
          <div key={segment.key} data-document-diagram-run={segment.runId}>
            <MermaidDiagram source={segment.text} />
          </div>
        ) : (
          // biome-ignore lint/security/noDangerouslySetInnerHtml: shared escaped renderer and unchanged DOMPurify policy in renderDocument
          <div key={segment.key} className={MARKDOWN_CLASS.root} dangerouslySetInnerHTML={{ __html: segment.html }} />
        ),
      ),
    [rendered],
  );
  return <div ref={root}>{content}</div>;
}
