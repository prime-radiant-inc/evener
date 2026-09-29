import { readdirSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { sanitizeMermaidSvg } from "./security";

// This suite runs under jsdom (DOMPurify needs a DOM), and jsdom's
// import.meta.url is not a file: URL, so `new URL(..., import.meta.url)`
// throws at import time. Resolve relative to vitest's root (this package
// directory) instead, the pattern the repo's other file-reading tests use.
const FIXTURE_DIR = "src/widgets/mermaid/testdata";
// The repo ships no @types/node; its node:fs shim only declares the
// withFileTypes form (src/styles/node-fs-shim.d.ts), so read names that way.
const fixtures = readdirSync(FIXTURE_DIR, { withFileTypes: true })
  .filter((entry) => entry.name.endsWith(".svg"))
  .map((entry) => entry.name);

describe("sanitizeMermaidSvg against real mermaid fixtures", () => {
  it("has a fixture for each common diagram type", () => {
    for (const name of ["flowchart", "sequence", "class", "state", "er", "gantt", "pie", "gitgraph"]) {
      expect(fixtures, `missing ${name}.svg`).toContain(`${name}.svg`);
    }
  });

  it.each(fixtures.map((f) => [f]))("%s keeps its labels and role", (file) => {
    const raw = readFileSync(`${FIXTURE_DIR}/${file}`, "utf8");
    const clean = sanitizeMermaidSvg(raw);
    // every human-readable label string in the raw fixture survives
    // flowchart/class/state/er emit their label text wrapped in <p> inside the
    // span (<span class="nodeLabel"><p>Start node</p></span>), so match the
    // span's inner content and strip nested tags to recover the text.
    const labels = [...raw.matchAll(/<span class="[^"]*Label[^"]*"[^>]*>(.*?)<\/span>/g)]
      .map((m) => (m[1] ?? "").replace(/<[^>]+>/g, "").trim())
      .filter((label) => label.length > 0);
    const textLabels = [...raw.matchAll(/<text[^>]*>([^<]+)</g)].map((m) => m[1] ?? "");
    for (const label of [...labels, ...textLabels]) {
      expect(clean).toContain(label);
    }
    expect(clean).toContain('role="graphics-document document"');
  });

  it.each(fixtures.map((f) => [f]))("%s carries no anchor or resource elements", (file) => {
    const clean = sanitizeMermaidSvg(readFileSync(`${FIXTURE_DIR}/${file}`, "utf8"));
    expect(clean).not.toMatch(/<a[\s>]/i);
    expect(clean).not.toMatch(/<(img|video|iframe|form|audio|object|embed)[\s>]/i);
  });
});

describe("sanitizeMermaidSvg against hostile markup", () => {
  it("strips anchors, images, handlers, and javascript: from foreignObject content", () => {
    const hostile = `<svg><foreignObject width="10" height="10"><div xmlns="http://www.w3.org/1999/xhtml"><span class="nodeLabel"><img src="https://evil.example/pixel.png"><a href="https://evil.example">x</a><b onclick="alert(1)">bold</b></span></div></foreignObject></svg>`;
    const clean = sanitizeMermaidSvg(hostile);
    expect(clean).not.toContain("<img");
    expect(clean).not.toContain("<a");
    expect(clean).not.toContain("onclick");
    expect(clean).toContain("bold");
    expect(clean).toContain("foreignObject");
  });

  it("strips javascript: hrefs and animation events in the SVG layer", () => {
    const hostile = `<svg><a href="javascript:alert(1)"><text>x</text></a><animate onbegin="alert(1)" attributeName="x" dur="1s"/></svg>`;
    const clean = sanitizeMermaidSvg(hostile);
    expect(clean).not.toContain("javascript:");
    expect(clean).not.toContain("onbegin");
    expect(clean).not.toContain("<a");
  });

  it("strips SVG <image> resource fetches", () => {
    const hostile = `<svg><image href="https://evil.example/pixel.png"/></svg>`;
    const clean = sanitizeMermaidSvg(hostile);
    expect(clean).not.toContain("<image");
  });
});
