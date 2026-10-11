import DOMPurify from "dompurify";
import { MERMAID_FORBID_TAGS } from "./forbidTags";

// The forbid list lives in its zero-import leaf module (forbidTags.ts) so the
// native page generator (mobile-native/scripts/build-mermaid-page.mts) can
// import it without pulling in this file's dompurify/mermaid dependencies -
// the generator runs from mobile-native's own install, where those are absent.
// Re-exported here so existing importers of security.ts keep working.
export { MERMAID_FORBID_TAGS } from "./forbidTags";

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
