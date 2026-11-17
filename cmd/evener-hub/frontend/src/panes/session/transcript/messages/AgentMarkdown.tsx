import { isValidTranscriptRef } from "@evener/appwire-client";
import { parseFileReference } from "@evener/appwire-client/docContent";
import { type ReactNode, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { Markdown, OpenButton } from "../../../../widgets";
import { MERMAID_DIAGRAM_ATTR } from "../../../../widgets/mermaid/markers";
import { openDocBeside } from "../../../doc/openDoc";
import { enhanceEntityText } from "../EntityText";
import { type AnalysisCache, createAnalysisCache } from "./analysisCache";
import { enhanceFileReferences, resolveAnchorReference } from "./fileReferenceDOM";

// A bare relative href the shared resolver accepts as a file link is the only
// shape worth holding before cwd arrives: anything with a scheme, an absolute
// path (a hub route), a fragment or a query already names its own
// destination. parseFileReference classifies a relative link without the real
// cwd, so every held href is one the replay resolves.
function deferrableFileHref(href: string): boolean {
  return !href.startsWith("/") && parseFileReference(href, "link", "/", undefined) !== undefined;
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
  // React writes the sanitized HTML again on every delta, so every node is new
  // and every reference is wrapped again. The scan that FINDS them is a pure
  // function of text and roots, so the blocks this stream did not touch are
  // answered from the previous pass instead of rescanned (issue #6455).
  // Only a live instance earns that memo: it is the one that runs the pass
  // again over the same text (issue #6603). A settled message fills a cache
  // once and then holds every block's text and span arrays for as long as it
  // stays mounted, so it keeps none - the effect below releases it on settle.
  const analysisRef = useRef<AnalysisCache | null>(null);
  if (live && analysisRef.current === null) analysisRef.current = createAnalysisCache();
  const analysis = analysisRef.current ?? undefined;
  // Portal updates must not replace the innerHTML that owns their mount points.
  const markdown = useMemo(() => <Markdown ref={root} source={source} live={live} />, [source, live]);

  // One lifetime restores entities before files, including hydration with
  // unchanged text. source/live invalidate the sanitized Markdown DOM itself.
  // biome-ignore lint/correctness/useExhaustiveDependencies: source/live invalidate the child Markdown DOM
  useLayoutEffect(() => {
    // Released after the commit rather than during render: a settled instance
    // keeps no analysis, and a stream that settles in place drops the cache it
    // filled. The settle pass itself may still answer from it.
    if (!live) analysisRef.current = null;
    const element = root.current;
    if (!element) return;
    if (pendingFileClick.current !== null && pendingFileClick.current.sessionRef !== sessionRef) {
      pendingFileClick.current = null;
    }
    const affordances: ReactNode[] = [];
    const slots: HTMLElement[] = [];
    const qualifiedRef = sessionRef?.includes(":") ? sessionRef : `local:${sessionRef ?? ""}`;
    const ownerRef = sessionRef && sourcePaneId && isValidTranscriptRef(qualifiedRef) ? sessionRef : undefined;
    const paneID = ownerRef === undefined ? undefined : sourcePaneId;
    const context =
      ownerRef && paneID && cwd
        ? { sessionRef: ownerRef, cwd, sourcePaneId: paneID, artifactsDir, scratchDir }
        : undefined;
    let removeClickHold = () => {};
    if (!context && ownerRef && paneID) {
      const holdOrGuard = (event: MouseEvent) => {
        if (event.defaultPrevented) return;
        const target = (event.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
        if (!target || !element.contains(target)) return;
        if (target.closest(`pre, [data-entity-host], [${MERMAID_DIAGRAM_ATTR}]`)) return;
        const href = target.getAttribute("href") ?? "";
        if (!deferrableFileHref(href)) return;
        event.preventDefault();
        if (event.button === 0 && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey) {
          pendingFileClick.current = { href, sessionRef: ownerRef, sourcePaneId: paneID };
        }
      };
      const onAuxClick = (event: MouseEvent) => {
        if (event.button === 1) holdOrGuard(event);
      };
      element.addEventListener("click", holdOrGuard, true);
      element.addEventListener("auxclick", onAuxClick, true);
      removeClickHold = () => {
        element.removeEventListener("click", holdOrGuard, true);
        element.removeEventListener("auxclick", onAuxClick, true);
      };
    }
    if (context) {
      const held = pendingFileClick.current;
      pendingFileClick.current = null;
      const refOptions = { artifactsDir: context.artifactsDir, scratchDir: context.scratchDir };
      if (held) {
        const reference = resolveAnchorReference(held.href, context.cwd, refOptions);
        if (reference) {
          openDocBeside({ session: held.sessionRef, reference, sourcePaneId: held.sourcePaneId });
        }
      }
      // Only authored eligible anchors get the existing OpenButton. Capture
      // destinations before the DOM adapter rewrites hrefs or generates links.
      for (const link of element.querySelectorAll<HTMLAnchorElement>("a[href]")) {
        if (link.closest(`pre, [data-entity-host], [${MERMAID_DIAGRAM_ATTR}]`)) continue;
        const reference = resolveAnchorReference(link.getAttribute("href") ?? "", context.cwd, refOptions);
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
    const restoreFiles = context ? enhanceFileReferences(element, context, analysis) : () => {};
    const entityResult = enhanceEntityText(element, analysis);
    setPortals([...affordances, ...entityResult.portals]);
    return () => {
      removeClickHold();
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
