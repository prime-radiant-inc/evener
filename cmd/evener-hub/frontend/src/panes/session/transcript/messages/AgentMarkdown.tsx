import { type ReactPortal, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { docFileRawURL, docImageURL } from "../../../../protocol/docContent";
import * as paneActions from "../../../../shell/paneActions";
import { useTranscriptRenderContext } from "../../../../transcriptDisplay/renderContext";
import { Markdown } from "../../../../widgets/markdown";
import { OpenButton } from "../../../../widgets/openbutton";
import { browserDocPort } from "../../../doc/browserDocPort";
import { fileDocParams } from "../fileOpenBeside";
import { isValidTranscriptRef } from "./steeringClassify";

function fileLinkPath(href: string): string | undefined {
  // URL references are not filesystem paths. Decode only the pathname, once,
  // before applying the same cwd boundary used by file tool cards.
  if (/^(?:[a-z][a-z\d+.-]*:|\/\/|[?#])/i.test(href)) return undefined;
  try {
    const path = decodeURIComponent(href.split(/[?#]/, 1)[0] ?? "");
    if (path.startsWith("//") || /[\\\p{Cc}]/u.test(path) || path.split("/").includes("..")) return undefined;
    return path;
  } catch {
    return undefined;
  }
}

/** Adds session file actions to sanitized agent prose without changing the
 * shared Markdown renderer's URL policy or admitting authored HTML. */
export function AgentMarkdown({ source, live = false }: { source: string; live?: boolean }) {
  const { thread } = useTranscriptRenderContext();
  const ref = thread?.ref;
  // Document endpoints serve local sessions only, addressed by bare ID or local:ID.
  const sessionRef =
    ref && (!ref.includes(":") || (ref.startsWith("local:") && isValidTranscriptRef(ref))) ? ref : undefined;
  const cwd = thread?.cwd;
  const root = useRef<HTMLDivElement>(null);
  const [affordances, setAffordances] = useState<ReactPortal[]>([]);
  // Portal updates must not replace the innerHTML that owns their mount points.
  const markdown = useMemo(() => <Markdown ref={root} source={source} live={live} />, [source, live]);

  // source/live determine when Markdown replaces its sanitized DOM. Rebind even
  // when the session is unchanged, including streamed and settled transitions.
  // biome-ignore lint/correctness/useExhaustiveDependencies: source/live invalidate the child Markdown DOM
  useLayoutEffect(() => {
    const portals: ReactPortal[] = [];
    const cleanups: Array<() => void> = [];
    for (const link of root.current?.querySelectorAll<HTMLAnchorElement>("a[href]") ?? []) {
      const href = link.getAttribute("href") ?? "";
      const params = fileDocParams(fileLinkPath(href), sessionRef, cwd);
      if (params === undefined) continue;
      const open = () => paneActions.openBeside({ type: "doc", params });
      const onClick = (event: MouseEvent) => {
        if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        event.stopPropagation();
        open();
      };
      link.href =
        params.kind === "image"
          ? docImageURL(browserDocPort.origin, params.session, params.path)
          : docFileRawURL(browserDocPort.origin, params.session, params.path);
      link.addEventListener("click", onClick);
      const slot = document.createElement("span");
      link.after(slot);
      portals.push(createPortal(<OpenButton label={`Open beside: ${params.path}`} onClick={open} />, slot));
      cleanups.push(() => {
        link.setAttribute("href", href);
        link.removeEventListener("click", onClick);
        slot.remove();
      });
    }
    setAffordances(portals);
    return () => {
      for (const cleanup of cleanups) cleanup();
    };
  }, [source, live, sessionRef, cwd]);

  return (
    <>
      {markdown}
      {affordances}
    </>
  );
}
