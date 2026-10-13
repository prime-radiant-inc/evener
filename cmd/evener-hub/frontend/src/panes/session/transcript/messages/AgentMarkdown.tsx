import { isValidTranscriptRef } from "@evener/appwire-client";
import { parseFileReference } from "@evener/appwire-client/docContent";
import { type ReactNode, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { Markdown, OpenButton } from "../../../../widgets";
import { MERMAID_DIAGRAM_ATTR } from "../../../../widgets/mermaid/markers";
import { useDocOpeningOrigin } from "../../../doc/docOpeningContext";
import { openDocBeside } from "../../../doc/openDoc";
import { enhanceEntityText } from "../EntityText";
import { enhanceFileReferences } from "./fileReferenceDOM";

/** Adds source-bound file actions to sanitized assistant prose without changing
 * the shared Markdown renderer's URL policy or admitting authored HTML. */
export function AgentMarkdown({ source, live = false }: { source: string; live?: boolean }) {
  const renderContext = useTranscriptRenderContext();
  const sessionRef = renderContext.thread?.ref;
  const cwd = renderContext.thread?.cwd;
  const origin = useDocOpeningOrigin();
  // A live pane reading surface names the opening pane through the render
  // context; a scope that renders transcripts WITHOUT one (each Zoom column)
  // still declares it through the doc-opening origin, whose pane identity is
  // the same record the real open path keys on.
  const providerOriginPaneId =
    origin === undefined ? undefined : origin.kind === "browser-origin" ? origin.paneId : origin.sourcePaneId;
  const sourcePaneId = renderContext.sourcePaneId ?? providerOriginPaneId;
  const root = useRef<HTMLDivElement>(null);
  const [portals, setPortals] = useState<ReactNode[]>([]);
  // Portal updates must not replace the innerHTML that owns their mount points.
  const markdown = useMemo(() => <Markdown ref={root} source={source} live={live} />, [source, live]);

  // One lifetime restores entities before files, including hydration with
  // unchanged text. source/live invalidate the sanitized Markdown DOM itself.
  // biome-ignore lint/correctness/useExhaustiveDependencies: source/live invalidate the child Markdown DOM
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    const affordances: ReactNode[] = [];
    const slots: HTMLElement[] = [];
    const qualifiedRef = sessionRef?.includes(":") ? sessionRef : `local:${sessionRef ?? ""}`;
    const context =
      sessionRef && isValidTranscriptRef(qualifiedRef) && cwd && sourcePaneId
        ? { sessionRef, cwd, sourcePaneId }
        : undefined;
    if (context) {
      // Only authored eligible anchors get the existing OpenButton. Capture
      // destinations before the DOM adapter rewrites hrefs or generates links.
      for (const link of element.querySelectorAll<HTMLAnchorElement>("a[href]")) {
        if (link.closest(`pre, [data-entity-host], [${MERMAID_DIAGRAM_ATTR}]`)) continue;
        const reference = parseFileReference(link.getAttribute("href") ?? "", "link", context.cwd);
        if (!reference) continue;
        const request = {
          session: context.sessionRef,
          reference,
          sourcePaneId: context.sourcePaneId,
          ...(origin === undefined ? {} : { origin }),
        };
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
  }, [source, live, sessionRef, cwd, sourcePaneId, origin]);

  return (
    <>
      {markdown}
      {portals}
    </>
  );
}
