import { findEntityIds } from "@evener/appwire-client";
import { type ReactNode, type ReactPortal, type RefObject, useLayoutEffect, useState } from "react";
import { createPortal } from "react-dom";
import { MERMAID_DIAGRAM_ATTR } from "../../../widgets/mermaid/markers";
import { EntityRef } from "./EntityRef";
import { segmentEntityIds } from "./entitySegments";

const ENTITY_SKIP_SELECTOR = `code, pre, script, style, a, [data-entity-host], [${MERMAID_DIAGRAM_ATTR}]`;
const entityEpochs = new WeakMap<HTMLElement, object>();

/** Enhances only plain text owned by a rendered prose root. The original text
 * nodes stay in the tree as empty anchors so cleanup can restore React's DOM
 * exactly before the next enhancement pass. */
export function enhanceEntityText(root: HTMLElement): { portals: ReactNode[]; cleanup(): void } {
  const epoch = {};
  entityEpochs.set(root, epoch);
  const mounts: ReactPortal[] = [];
  const cleanups: Array<() => void> = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  while (walker.nextNode()) {
    const node = walker.currentNode as Text;
    const parent = node.parentElement;
    if (!parent || parent.closest(ENTITY_SKIP_SELECTOR)) continue;
    if (findEntityIds(node.data).length > 0) nodes.push(node);
  }

  for (const node of nodes) {
    const original = node.data;
    const fragment = document.createDocumentFragment();
    let cursor = 0;
    for (const match of findEntityIds(original)) {
      fragment.append(original.slice(cursor, match.start));
      const host = document.createElement("span");
      host.setAttribute("data-entity-host", "");
      fragment.append(host);
      mounts.push(createPortal(<EntityRef id={match.id} />, host, `${mounts.length}:${match.id}`));
      cursor = match.end;
    }
    fragment.append(original.slice(cursor));

    const inserted = [...fragment.childNodes];
    node.before(fragment);
    node.data = "";
    cleanups.push(() => {
      if (entityEpochs.get(root) !== epoch || !root.contains(node)) return;
      for (const insertedNode of inserted) insertedNode.parentNode?.removeChild(insertedNode);
      if (node.data === "") node.data = original;
    });
  }

  return {
    portals: mounts,
    cleanup() {
      for (const cleanup of cleanups.reverse()) cleanup();
    },
  };
}

/** Other prose callers retain the entity-only lifetime. AgentMarkdown owns the
 * coordinated file-first/entity-second lifetime instead. */
export function useEntityTextEnhancement(rootRef: RefObject<HTMLElement | null>, deps: unknown[]) {
  const [portals, setPortals] = useState<ReactNode[]>([]);

  useLayoutEffect(
    () => {
      const root = rootRef.current;
      if (!root) return;
      const result = enhanceEntityText(root);
      setPortals(result.portals);
      return result.cleanup;
    },
    // Callers know which render inputs replace the DOM owned by their root.
    // biome-ignore lint/correctness/useExhaustiveDependencies: the caller intentionally owns this hook's dependency tuple
    deps,
  );

  return portals;
}

export function EntityText({ text }: { text: string }) {
  const out: ReactNode[] = [];
  // The segment's own offset in `text`, accumulated as we walk: it is the
  // segment's identity for React's keys (segments are derived
  // deterministically from `text`, and offsets never repeat).
  let offset = 0;
  for (const segment of segmentEntityIds(text)) {
    if (segment.kind === "entity") {
      out.push(<EntityRef key={`${offset}:${segment.id}`} id={segment.id} />);
      offset += segment.id.length;
    } else {
      out.push(segment.text);
      offset += segment.text.length;
    }
  }
  return <>{out}</>;
}
