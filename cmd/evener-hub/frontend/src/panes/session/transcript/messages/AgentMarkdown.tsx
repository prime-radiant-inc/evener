import { isValidTranscriptRef } from "@evener/appwire-client";
import { type ReactNode, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { Markdown, OpenButton } from "../../../../widgets";
import { MERMAID_DIAGRAM_ATTR } from "../../../../widgets/mermaid/markers";
import { openDocBeside } from "../../../doc/openDoc";
import { enhanceEntityText } from "../EntityText";
import { enhanceFileReferences, resolveAnchorReference } from "./fileReferenceDOM";

// A bare relative href is the only shape an authored file reference can have
// before the session's cwd arrives; anything with a scheme, an absolute path
// or a fragment already names its own destination and must not be held.
function deferrableFileHref(href: string): boolean {
  const value = href.trim();
  return (
    value !== "" &&
    !value.startsWith("#") &&
    !value.startsWith("/") &&
    !value.startsWith("?") &&
    !/^[a-z][a-z0-9+.-]*:/i.test(value)
  );
}

/** Adds source-bound file actions to sanitized assistant prose without changing
 * the shared Markdown renderer's URL policy or admitting authored HTML. */
export function AgentMarkdown({ source, live = false }: { source: string; live?: boolean }) {
  const { thread, sourcePaneId } = useTranscriptRenderContext();
  const sessionRef = thread?.ref;
  const cwd = thread?.cwd;
  const artifactsDir = thread?.artifactsDir;
  const scratchDir = thread?.scratchDir;
  const root = useRef<HTMLDivElement>(null);
  const [portals, setPortals] = useState<ReactNode[]>([]);
  // A filename click that arrived before cwd did. Following its raw href
  // would leave the conversation's ownership and reach the hub's own route
  // (issue #5451), so the click is held until the authoritative context
  // resolves the same href as a source-owned file.
  const pendingFileClick = useRef<{ href: string; sessionRef: string; sourcePaneId: string } | null>(null);
  // Portal updates must not replace the innerHTML that owns their mount points.
  const markdown = useMemo(() => <Markdown ref={root} source={source} live={live} />, [source, live]);

  // One lifetime restores entities before files, including hydration with
  // unchanged text. source/live invalidate the sanitized Markdown DOM itself.
  // biome-ignore lint/correctness/useExhaustiveDependencies: source/live invalidate the child Markdown DOM
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    if (pendingFileClick.current !== null && pendingFileClick.current.sessionRef !== sessionRef) {
      pendingFileClick.current = null;
    }
    const affordances: ReactNode[] = [];
    const slots: HTMLElement[] = [];
    const qualifiedRef = sessionRef?.includes(":") ? sessionRef : `local:${sessionRef ?? ""}`;
    const context =
      sessionRef && isValidTranscriptRef(qualifiedRef) && cwd && sourcePaneId
        ? { sessionRef, cwd, sourcePaneId, artifactsDir, scratchDir }
        : undefined;
    if (!context && sessionRef && sourcePaneId) {
      const onClick = (event: MouseEvent) => {
        if (
          event.defaultPrevented ||
          event.button !== 0 ||
          event.ctrlKey ||
          event.metaKey ||
          event.shiftKey ||
          event.altKey
        ) {
          return;
        }
        const target = (event.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
        if (!target || !element.contains(target)) return;
        if (target.closest(`pre, [data-entity-host], [${MERMAID_DIAGRAM_ATTR}]`)) return;
        const href = target.getAttribute("href") ?? "";
        if (!deferrableFileHref(href)) return;
        event.preventDefault();
        event.stopPropagation();
        pendingFileClick.current = { href, sessionRef, sourcePaneId };
      };
      element.addEventListener("click", onClick, true);
      return () => element.removeEventListener("click", onClick, true);
    }
    if (context) {
      const held = pendingFileClick.current;
      pendingFileClick.current = null;
      if (held && held.sessionRef === sessionRef) {
        const reference = resolveAnchorReference(held.href, context.cwd, {
          artifactsDir: context.artifactsDir,
          scratchDir: context.scratchDir,
        });
        if (reference) {
          openDocBeside({ session: held.sessionRef, reference, sourcePaneId: held.sourcePaneId });
        }
      }
      // Only authored eligible anchors get the existing OpenButton. Capture
      // destinations before the DOM adapter rewrites hrefs or generates links.
      for (const link of element.querySelectorAll<HTMLAnchorElement>("a[href]")) {
        if (link.closest(`pre, [data-entity-host], [${MERMAID_DIAGRAM_ATTR}]`)) continue;
        const reference = resolveAnchorReference(link.getAttribute("href") ?? "", context.cwd, {
          artifactsDir: context.artifactsDir,
          scratchDir: context.scratchDir,
        });
        if (!reference) continue;
        const request = { session: context.sessionRef, reference, sourcePaneId: context.sourcePaneId };
        const slot = document.createElement("span");
        slot.setAttribute("data-file-action", "");
        link.after(slot);
        slots.push(slot);
        affordances.push(
          createPortal(
            <OpenButton label={`Open beside: ${reference.path}`} onClick={() => openDocBeside(request)} />,
            slot,
          ),
        );
      }
    }
    const restoreFiles = context ? enhanceFileReferences(element, context) : () => {};
    const entityResult = enhanceEntityText(element);
    setPortals([...affordances, ...entityResult.portals]);
    return () => {
      entityResult.cleanup();
      restoreFiles();
      for (const slot of slots) slot.remove();
    };
  }, [source, live, sessionRef, cwd, artifactsDir, scratchDir, sourcePaneId]);

  return (
    <>
      {markdown}
      {portals}
    </>
  );
}
