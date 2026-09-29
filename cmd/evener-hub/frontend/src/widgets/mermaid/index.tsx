import { type ReactNode, useEffect, useState } from "react";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import styles from "./mermaid.module.css";
import { mermaidThemeVariables, useResolvedScheme } from "./resolveScheme";
import { renderMermaidSvg } from "./security";

const CLASS = {
  root: requireClass(styles.root, "mermaid.module.css", "root"),
  clickable: requireClass(styles.clickable, "mermaid.module.css", "clickable"),
  error: requireClass(styles.error, "mermaid.module.css", "error"),
  svg: requireClass(styles.svg, "mermaid.module.css", "svg"),
};

type State = { status: "loading" } | { status: "ok"; svg: string } | { status: "error" };

/** Renders one mermaid diagram inline. The source is model-authored, so the
 * render pipeline is the two-layer sanitize in security.ts; the rendered SVG
 * is non-interactive (clicks open the viewer, added in Task 7, via the
 * container - the svg itself never takes pointer events, so no anchor that
 * could survive a future sanitizer regression is ever clickable). */
export function MermaidDiagram({
  source,
  onOpen,
  renderTimeoutMs = 10000,
}: {
  source: string;
  onOpen?: (svg: string, source: string) => void;
  renderTimeoutMs?: number;
}) {
  const scheme = useResolvedScheme();
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    const timeout = new Promise<never>((_resolve, reject) => {
      setTimeout(() => reject(new Error("render timed out")), renderTimeoutMs);
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
    };
  }, [source, scheme, renderTimeoutMs]);

  let body: ReactNode;
  if (state.status === "error") {
    // Same styling as a fenced code block, plus the honest note.
    body = (
      <div className={codeblockStyles.root}>
        <div className={codeblockStyles.header}>
          <span className={codeblockStyles.language}>mermaid</span>
        </div>
        <pre className={codeblockStyles.pre}>
          <code className={codeblockStyles.code}>{source}</code>
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
      <div className={CLASS.svg} dangerouslySetInnerHTML={{ __html: svg }} />
    );
    // A diagram with a viewer to open is a real control, not a static block:
    // render the container as a button so a keyboard user can open it too, and
    // the click handler is on a focusable element (no a11y suppression).
    if (onOpen !== undefined) {
      return (
        <button
          type="button"
          className={`${CLASS.root} ${CLASS.clickable}`}
          data-mermaid-diagram=""
          onClick={() => onOpen(svg, source)}
        >
          {body}
        </button>
      );
    }
  } else {
    body = <div className={CLASS.svg} aria-busy="true" />;
  }

  return (
    <div className={CLASS.root} data-mermaid-diagram="">
      {body}
    </div>
  );
}
