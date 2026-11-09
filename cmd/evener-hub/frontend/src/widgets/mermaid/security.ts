import { MERMAID_FORBID_TAGS, MERMAID_SANITIZE_CONFIG } from "@evener/appwire-client";
import DOMPurify from "dompurify";

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
