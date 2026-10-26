// The mermaid forbid list, owned by the package so the web sanitizer
// (widgets/mermaid/security.ts) and the native page generator
// (mobile-native/scripts/build-mermaid-page.mts) cannot drift.
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
