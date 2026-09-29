import DOMPurify from "dompurify";

// Resource-bearing and navigational tags are forbidden at BOTH layers
// (mermaid's own sanitize via dompurifyConfig, and this post-render pass):
// mermaid at securityLevel "strict" strips handlers/scripts/javascript: but
// passes benign markup through labels, and the render-time network fetch
// (mermaid's temporary render DOM) can only be stopped at the mermaid layer.
// Verified in real Chrome against mermaid 11.17.2 and 12.0.0; see the spec's
// Security section.
export const MERMAID_FORBID_TAGS = [
  "a",
  "img",
  "video",
  "audio",
  "iframe",
  "object",
  "embed",
  "source",
  "track",
  "form",
  "input",
] as const;

// Mermaid emits flowchart/class/state/ER labels as XHTML inside
// <foreignObject>; an svg-only profile strips every label (round-1 review
// finding). The html profile plus the integration point keeps them;
// "role" keeps role="graphics-document document" for screen readers.
export const MERMAID_SANITIZE_CONFIG = {
  USE_PROFILES: { html: true, svg: true, svgFilters: true },
  ADD_TAGS: ["foreignObject"],
  HTML_INTEGRATION_POINTS: { foreignobject: true },
  ADD_ATTR: ["role"],
  FORBID_TAGS: [...MERMAID_FORBID_TAGS],
};

export function sanitizeMermaidSvg(svg: string): string {
  return DOMPurify.sanitize(svg, MERMAID_SANITIZE_CONFIG);
}

type Mermaid = typeof import("mermaid").default;

let cached: { key: string; mermaid: Mermaid } | null = null;

// Lazy-loads mermaid (keeps ~1 MB gz out of the main bundle) and initializes
// it once per distinct themeVariables content. Re-initialization on theme
// change is intentional: themeVariables only apply at render time from the
// active config.
export async function initMermaid(themeVariables: Record<string, string>): Promise<Mermaid> {
  const key = JSON.stringify(themeVariables);
  const { default: mermaid } = await import("mermaid");
  if (cached === null || cached.key !== key) {
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: "strict",
      theme: "default",
      themeVariables,
      dompurifyConfig: { FORBID_TAGS: [...MERMAID_FORBID_TAGS] },
    });
    cached = { key, mermaid };
  }
  return cached.mermaid;
}

let renderCounter = 0;

export async function renderMermaidSvg(source: string, themeVariables: Record<string, string>): Promise<string> {
  const mermaid = await initMermaid(themeVariables);
  renderCounter += 1;
  const { svg } = await mermaid.render(`mermaid-diagram-${renderCounter}`, source);
  return sanitizeMermaidSvg(svg);
}
