// The mermaid forbid list, as a leaf module with ZERO imports. Two consumers
// share it across trees: the web sanitizer (security.ts re-exports it) and the
// native page generator (mobile-native/scripts/build-mermaid-page.mts), which
// runs from mobile-native's own install where the frontend's node_modules - and
// therefore dompurify/mermaid - are absent. Keeping the data in a leaf lets the
// generator import it directly without dragging in security.ts's dependencies.
//
// Resource-bearing and navigational tags are forbidden at BOTH sanitize layers
// (mermaid's own dompurifyConfig, and the post-render pass): mermaid at
// securityLevel "strict" strips handlers/scripts/javascript: but passes benign
// markup through labels, and the render-time network fetch (mermaid's temporary
// render DOM) can only be stopped at the mermaid layer. Verified in real Chrome
// against mermaid 11.17.2 and 12.0.0; see the spec's Security section.
export const MERMAID_FORBID_TAGS = [
  "a",
  "img",
  // SVG <image href> is a resource-fetch channel with no backstop in the
  // guard's instrumentation; no mermaid diagram type emits it.
  "image",
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
