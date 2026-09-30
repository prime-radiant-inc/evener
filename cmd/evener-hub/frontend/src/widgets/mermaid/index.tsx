import { type ReactNode, useEffect, useState } from "react";
import { useResolvedScheme } from "../../stores/prefs";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import { DiagramViewer } from "./DiagramViewer";
import { MERMAID_DIAGRAM_ATTR } from "./markers";
import styles from "./mermaid.module.css";
import { mermaidThemeVariables } from "./resolveScheme";
import { renderMermaidSvg } from "./security";

export { MERMAID_DIAGRAM_ATTR } from "./markers";

const CLASS = {
  root: requireClass(styles.root, "mermaid.module.css", "root"),
  clickable: requireClass(styles.clickable, "mermaid.module.css", "clickable"),
  error: requireClass(styles.error, "mermaid.module.css", "error"),
  svg: requireClass(styles.svg, "mermaid.module.css", "svg"),
};

// The error fallback renders the source through CodeBlock's own stylesheet so
// a failed diagram looks identical to a mermaid fenced block; the borrowed
// classes go through requireClass like every other class-lookup table
// (widgets/markdown/index.tsx's own CODEBLOCK_CLASS is the precedent).
const CODEBLOCK_CLASS = {
  root: requireClass(codeblockStyles.root, "codeblock.module.css", "root"),
  header: requireClass(codeblockStyles.header, "codeblock.module.css", "header"),
  language: requireClass(codeblockStyles.language, "codeblock.module.css", "language"),
  pre: requireClass(codeblockStyles.pre, "codeblock.module.css", "pre"),
  code: requireClass(codeblockStyles.code, "codeblock.module.css", "code"),
};

type State = { status: "loading" } | { status: "ok"; svg: string } | { status: "error" };

/** Renders one mermaid diagram inline. The source is model-authored, so the
 * render pipeline is the two-layer sanitize in security.ts; the rendered SVG
 * is non-interactive (the container opens the fullscreen DiagramViewer on
 * click - the svg itself never takes pointer events, so no anchor that could
 * survive a future sanitizer regression is ever clickable). The container is
 * only a <button> once a diagram has rendered; while loading (or after a
 * failed render) it stays a plain, non-focusable <div>, since there is no
 * rendered diagram for the viewer to open. */
export function MermaidDiagram({ source, renderTimeoutMs = 10000 }: { source: string; renderTimeoutMs?: number }) {
  const scheme = useResolvedScheme();
  const [state, setState] = useState<State>({ status: "loading" });
  // The SVG snapshot the viewer is showing, set on click. Holding the svg in
  // state (rather than threading an onOpen prop to a caller) is what makes the
  // diagram's own container the viewer's trigger.
  const [viewer, setViewer] = useState<{ svg: string } | null>(null);

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    let timeoutId: ReturnType<typeof setTimeout> | undefined;
    const timeout = new Promise<never>((_resolve, reject) => {
      timeoutId = setTimeout(() => reject(new Error("render timed out")), renderTimeoutMs);
    });
    Promise.race([renderMermaidSvg(source, mermaidThemeVariables(scheme)), timeout])
      .then((svg) => {
        if (!cancelled) setState({ status: "ok", svg });
      })
      .catch(() => {
        if (!cancelled) setState({ status: "error" });
      });
    return () => {
      cancelled = true;
      // The race leaves the timer pending once the render settles first, so it
      // must be cleared here or it lives for the full timeout after unmount.
      if (timeoutId !== undefined) clearTimeout(timeoutId);
    };
  }, [source, scheme, renderTimeoutMs]);

  let body: ReactNode;
  if (state.status === "error") {
    // Same styling as a fenced code block, plus the honest note.
    body = (
      <div className={CODEBLOCK_CLASS.root}>
        <div className={CODEBLOCK_CLASS.header}>
          <span className={CODEBLOCK_CLASS.language}>mermaid</span>
        </div>
        <pre className={CODEBLOCK_CLASS.pre}>
          <code className={CODEBLOCK_CLASS.code}>{source}</code>
        </pre>
        <p className={CLASS.error}>Couldn&apos;t render this diagram.</p>
      </div>
    );
  } else if (state.status === "ok") {
    const svg = state.svg;
    body = (
      // Sanitized by security.ts's two layers; see that file. pointer-events
      // come from mermaid.module.css's .svg rules.
      // biome-ignore lint/security/noDangerouslySetInnerHtml: sanitized via the mermaid pipeline's two DOMPurify layers, see security.ts
      <div className={CLASS.svg} aria-hidden="true" dangerouslySetInnerHTML={{ __html: svg }} />
    );
    // A rendered diagram is a real control, not a static block: render the
    // container as a button so a keyboard user can open the viewer too, and the
    // click handler is on a focusable element (no a11y suppression).
    return (
      <>
        <button
          type="button"
          className={`${CLASS.root} ${CLASS.clickable}`}
          {...{ [MERMAID_DIAGRAM_ATTR]: "" }}
          aria-label="Open diagram fullscreen"
          onClick={() => setViewer({ svg })}
        >
          {body}
        </button>
        {viewer !== null && <DiagramViewer open svg={viewer.svg} source={source} onClose={() => setViewer(null)} />}
      </>
    );
  } else {
    body = <div className={CLASS.svg} aria-busy="true" />;
  }

  return (
    <div className={CLASS.root} {...{ [MERMAID_DIAGRAM_ATTR]: "" }}>
      {body}
    </div>
  );
}
