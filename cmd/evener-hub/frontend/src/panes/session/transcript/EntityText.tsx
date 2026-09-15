import { findEntityIds } from "@evener/appwire-client";
import { type ReactNode, type ReactPortal, type RefObject, useLayoutEffect, useState } from "react";
import { createPortal } from "react-dom";
import { EntityRef } from "./EntityRef";

const ENTITY_SKIP_SELECTOR = "code, pre, script, style, a, [data-entity-host]";

/** Enhances only plain text owned by a rendered prose root. The original text
 * nodes stay in the tree as empty anchors so cleanup can restore React's DOM
 * exactly before the next enhancement pass. */
export function useEntityTextEnhancement(rootRef: RefObject<HTMLElement | null>, deps: unknown[]) {
  const [portals, setPortals] = useState<ReactPortal[]>([]);

  useLayoutEffect(
    () => {
      const root = rootRef.current;
      if (!root) return;

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
          for (const insertedNode of inserted) insertedNode.parentNode?.removeChild(insertedNode);
          node.data = original;
        });
      }

      setPortals(mounts);
      return () => {
        for (const cleanup of cleanups) cleanup();
      };
    },
    // Callers know which render inputs replace the DOM owned by their root.
    // biome-ignore lint/correctness/useExhaustiveDependencies: the caller intentionally owns this hook's dependency tuple
    deps,
  );

  return portals;
}

export function EntityText({ text }: { text: string }) {
  const matches = findEntityIds(text);
  const out: ReactNode[] = [];
  let cursor = 0;
  for (const match of matches) {
    out.push(text.slice(cursor, match.start));
    out.push(<EntityRef key={`${match.start}:${match.id}`} id={match.id} />);
    cursor = match.end;
  }
  out.push(text.slice(cursor));
  return <>{out}</>;
}
