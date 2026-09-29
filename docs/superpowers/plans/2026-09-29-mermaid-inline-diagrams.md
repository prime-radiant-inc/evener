# Mermaid Inline Diagrams Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render ```` ```mermaid ```` fenced blocks in session messages as inline diagrams on the web hub and the native mobile app, with a fullscreen viewer, per the spec.

**Architecture:** Split message markdown at top-level closed mermaid fences. Web cuts the marked token array into slices (link definitions resolve at lex time, so slices render identically to whole-document parsing); prose slices keep the existing marked → DOMPurify → innerHTML path, mermaid slices render a new lazy-loaded MermaidDiagram component. Native cuts source strings (with the message's link definitions prepended to each prose segment, because md4c parses each segment standalone); prose keeps rendering through EnrichedMarkdownText, mermaid renders in a locked-down react-native-webview hosting a bundled mermaid page.

**Tech Stack:** marked 18.0.6, DOMPurify 3, mermaid 11.17.2 (pinned exact), react-native-webview 13.16.1 (Expo SDK 57 pin), esbuild (page bundle), vitest, the repo's browser-guard harness.

**Spec:** `docs/superpowers/specs/2026-09-29-mermaid-inline-diagrams-design.md` — read it first; it carries the verified security findings this plan implements.

## Global Constraints

- Pin `mermaid` to exactly `11.17.2` (no `^`) in `cmd/evener-hub/frontend/package.json` and in the native page build. Version bumps are security-review events (spec: Decisions).
- Mermaid initializes everywhere with `securityLevel: "strict"` and `dompurifyConfig: { FORBID_TAGS }` with exactly the tag list in Task 3. Post-render sanitization applies the same FORBID_TAGS (spec: Security).
- Never test mocked behavior. jsdom tests render real mermaid via the six-shim harness (Task 3). The native component tests use the repo's existing string-tag seam for native modules (`vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }))` precedent in `MarkdownResponse.test.tsx`); that seam tests our wiring, not the native module.
- Biome: no `noNonNullAssertion`, no array-index keys, `noDangerouslySetInnerHtml` needs a biome-ignore with a justification comment (precedent: `widgets/markdown/index.tsx:419`). Run `npx biome check --write <touched>` from `cmd/evener-hub/frontend` before gates; never run biome from the repo root.
- Gates: `make test-web` (frontend), `make test-native` (native), `make lint` before the PR. Browser guards run via `make test-web-browser` on Chrome-capable hosts.
- Match surrounding code style. Tabs in `mobile-native`, spaces in the web frontend. CSS via `.module.css` files with `requireClass` on web.
- The spec's locked decisions (WebView on native, inline + fullscreen, render-when-fence-closes) are not contestable during implementation. A task that discovers one is unimplementable stops and reports instead of routing around it.

## Review Focus

Inputs the spec implies that no single task's happy path covers; each has a test in the owning task:

1. Hostile diagram labels (`<img src=https://…>`, `<a href>`, `click A href "https://…"`) must produce no network requests and no anchor/resource elements — Task 3 (sanitize tests), Task 4 (rendered-output tests), Task 6 (browser guard counts requests).
2. CRLF source streaming an open mermaid fence must render as a code block with no glued backticks or phantom fences — Tasks 1 and 2.
3. A link definition separated from its use by a diagram must still resolve, settled and live, web and native — Tasks 2, 5, 8.
4. A pathological or hanging diagram source must end in the error fallback, not a wedged transcript — Task 4 (`renderTimeoutMs` race, tested with `renderTimeoutMs={1}` against a real render, which cannot finish in 1 ms).
5. An empty mermaid fence (```` ```mermaid ```` immediately closed) must hit the error fallback, not crash — Task 4 (real render error path).

---

### Task 1: CRLF tolerance in closeOpenMarkdown

The pre-existing bug from the spec's "Pre-existing bug" section: every end-anchored line predicate in `streaming.ts` rejects CR-terminated lines, so CRLF streams get glued backticks, phantom fences, and stray `*` closers. One mechanical fix covers all predicates: strip a single trailing `\r` from each line copy at scan time. The function never rewrites the source (output appends to the original string), so scanning normalized line copies is safe and keeps the live balance gate (`closeOpenMarkdown(head) === head`) consistent.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/widgets/markdown/streaming.ts` (the `lines` computation in `closeOpenMarkdown`, currently line 380)
- Test: `cmd/evener-hub/frontend/src/widgets/markdown/streaming.test.ts`

**Interfaces:**
- Consumes: nothing new.
- Produces: `closeOpenMarkdown(source: string): string`, unchanged signature, now CRLF-correct. Later tasks rely on open CRLF fences closing cleanly.

- [ ] **Step 1: Write the failing tests**

Add to `streaming.test.ts` (match its existing style — table cases where used). Each case asserts the CRLF input closes exactly the way its LF control does:

```ts
it("closes an open CRLF fence with a newline before the closer", () => {
  expect(closeOpenMarkdown("```mermaid\r\ngraph LR\r\nA-->B\r\n")).toBe("```mermaid\r\ngraph LR\r\nA-->B\r\n\n```");
});

it("does not append a phantom fence for a CRLF closing fence at EOF", () => {
  expect(closeOpenMarkdown("```mermaid\r\ngraph LR\r\nA-->B\r\n```")).toBe("```mermaid\r\ngraph LR\r\nA-->B\r\n```");
});

it.each([
  ["thematic break", "**open\r\n***\r"],
  ["setext underline", "**open\r\n===\r"],
  ["empty ATX heading", "**open\r\n#\r"],
  ["bare list marker", "**open\r\n-\r"],
  ["bare ordered marker", "**open\r\n1.\r"],
])("does not let a CRLF %s kill emphasis closing differently than LF", (_name, crlf) => {
  const lf = crlf.replaceAll("\r", "");
  expect(closeOpenMarkdown(crlf)).toBe(closeOpenMarkdown(lf).replaceAll("\n", "\r\n").replace("\r\n", "\r\n"));
  // simpler and stronger: the appended suffix is identical
  expect(closeOpenMarkdown(crlf).slice(crlf.length)).toBe(closeOpenMarkdown(lf).slice(lf.length));
});
```

(The second assertion is the real one; if the first is awkward in context, keep only the suffix comparison. Existing fence/CRLF-absent tests must keep passing unchanged.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/markdown/streaming.test.ts`
Expected: FAIL — the fence cases produce `"A-->B```"` (glued) and double fences; boundary cases append `"**"`/`*` for CRLF but not LF.

- [ ] **Step 3: Implement the fix**

In `closeOpenMarkdown`, replace:

```ts
const lines = source.split("\n");
```

with:

```ts
// Scan line copies with any single trailing "\r" stripped: every boundary
// predicate below anchors at end-of-line, and "\r" defeats those anchors
// (glued fence closers, phantom fences, stray emphasis closers - see the
// CRLF tests). The source itself is never rewritten; closers append to it
// unchanged, and appended closers are LF-style as before.
const lines = source.split("\n").map((line) => (line.endsWith("\r") ? line.slice(0, -1) : line));
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/markdown/streaming.test.ts`
Expected: PASS, including all pre-existing cases.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/markdown/streaming.ts cmd/evener-hub/frontend/src/widgets/markdown/streaming.test.ts
git commit -m "fix(markdown): tolerate CR line ends in closeOpenMarkdown's boundary predicates"
```

---

### Task 2: Web segment split

The splitter behind the spec's Core mechanism. Lex once with the shared lexer; cut the top-level token array at terminated mermaid code tokens; demote a live-mode trailing mermaid fence that is open in the real source.

**Files:**
- Create: `cmd/evener-hub/frontend/src/widgets/markdown/segments.ts`
- Test: `cmd/evener-hub/frontend/src/widgets/markdown/segments.test.ts`

**Interfaces:**
- Consumes: `markdownLexer` from `./lexer` (`markdownLexer.lexer(source): Token[]`), marked's `Token` type.
- Produces (Task 5 consumes these exact names):
  - `type MarkdownSegment = { kind: "markdown"; tokens: Token[] } | { kind: "mermaid"; text: string }`
  - `splitMarkdownSegments(closedSource: string, realSource: string | null): MarkdownSegment[]`
  - `messageMayContainMermaid(source: string): boolean`

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, it } from "vitest";
import { messageMayContainMermaid, splitMarkdownSegments } from "./segments";

const MERMAID = "```mermaid\ngraph TD; A-->B\n```\n";

describe("splitMarkdownSegments", () => {
  it("splits a closed mermaid fence out of prose", () => {
    const segments = splitMarkdownSegments(`hello\n\n${MERMAID}\nworld\n`, null);
    expect(segments.map((s) => s.kind)).toEqual(["markdown", "mermaid", "markdown"]);
    expect(segments[1]).toMatchObject({ text: "graph TD; A-->B\n" });
  });

  it("keeps link definitions resolving across a diagram (lex-time registry)", () => {
    const segments = splitMarkdownSegments(`See [the docs][d].\n\n${MERMAID}\n[d]: https://example.com\n`, null);
    const use = segments[0];
    const def = segments[2];
    expect(use?.kind).toBe("markdown");
    expect(def?.kind).toBe("markdown");
    // resolution is proven at integration (Task 5); here the def token must
    // land in the tail slice, not vanish into the cut
    expect(JSON.stringify(segments)).toContain("https://example.com");
  });

  it.each([
    ["tilde fences", "~~~mermaid\ngraph TD; A-->B\n~~~\n"],
    ["indented fence", "   ```mermaid\n   graph TD; A-->B\n   ```\n"],
    ["info string after lang", "```mermaid some title\ngraph TD; A-->B\n```\n"],
    ["uppercase lang", "```Mermaid\ngraph TD; A-->B\n```\n"],
    ["CRLF", "```mermaid\r\ngraph TD; A-->B\r\n```\r\n"],
  ])("splits %s", (_name, fence) => {
    expect(splitMarkdownSegments(fence, null).map((s) => s.kind)).toEqual(["mermaid"]);
  });

  it.each([
    ["dot-prefixed lang", "```.mermaid\nx\n```\n"],
    ["suffixed lang", "```mermaidjs\nx\n```\n"],
    ["fence in blockquote", "> ```mermaid\n> graph TD; A-->B\n> ```\n"],
    ["fence in list item", "- ```mermaid\n  graph TD; A-->B\n  ```\n"],
  ])("does not split %s", (_name, source) => {
    expect(splitMarkdownSegments(source, null).map((s) => s.kind)).toEqual(["markdown"]);
  });

  it("demotes a trailing mermaid fence open in the real source", () => {
    const real = "intro\n\n```mermaid\ngraph TD; A-->";
    const closed = `${real}\n\`\`\``;
    const segments = splitMarkdownSegments(closed, real);
    expect(segments.map((s) => s.kind)).toEqual(["markdown", "markdown"]);
    // the demoted tail still renders as a code block downstream
    expect(JSON.stringify(segments[1])).toContain("graph TD");
  });

  it("demotes a bare CRLF mermaid opener with no content", () => {
    const real = "```mermaid\r\n";
    const closed = `${real}\`\`\``;
    expect(splitMarkdownSegments(closed, real).map((s) => s.kind)).toEqual(["markdown"]);
  });

  it("keeps a terminated mermaid fence when a later non-mermaid fence is open", () => {
    const real = `${MERMAID}\n\`\`\`js\nlet x = 1;`;
    const closed = `${real}\n\`\`\``;
    const segments = splitMarkdownSegments(closed, real);
    expect(segments.map((s) => s.kind)).toEqual(["mermaid", "markdown"]);
  });
});

describe("messageMayContainMermaid", () => {
  it.each([
    ["plain prose", "hello **world**", false],
    ["mermaid fence", MERMAID, true],
    ["tilde fence", "~~~mermaid\nx\n~~~", true],
    ["indented", "  ```mermaid\nx\n```", true],
    ["non-mermaid fence", "```js\nx\n```", false],
  ])("%s", (_name, source, expected) => {
    expect(messageMayContainMermaid(source)).toBe(expected);
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/markdown/segments.test.ts`
Expected: FAIL (module does not exist).

- [ ] **Step 3: Implement**

`segments.ts`:

```ts
// Splits a message at top-level CLOSED mermaid fences. Web markdown slices
// are token arrays (never re-serialized source), so link definitions resolve
// exactly as a whole-document parse: marked v18 resolves references at lex
// time against the lexer's document-wide registry (verified in the spec's
// review round: parser output is identical with a corrupted registry, and a
// def-stripped slice still renders its links).
//
// Live mode passes the closeOpenMarkdown output as closedSource and the
// pre-close string as realSource; a mermaid fence that is still open in the
// real source is DEMOTED to a markdown slice (it renders as a code block
// until its closing fence arrives - the spec's locked streaming decision).
// Identification compares the real source's own lex, never raw-vs-source
// suffix checks: marked normalizes CRLF at lex, so a CRLF fence's raw does
// not suffix-match the source (verified).
import type { Token } from "marked";
import { markdownLexer } from "./lexer";

export type MarkdownSegment = { kind: "markdown"; tokens: Token[] } | { kind: "mermaid"; text: string };

// Cheap gate so non-mermaid messages never pay for a second lex. A false
// positive (a nested or quoted fence) only takes the segmented path; a false
// negative would skip it, so the pattern allows leading indentation and both
// fence characters.
const MERMAID_FENCE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*mermaid(?:[ \t\r]|$)/im;

export function messageMayContainMermaid(source: string): boolean {
  return MERMAID_FENCE.test(source);
}

function isMermaidCodeToken(token: Token): boolean {
  return (
    token.type === "code" &&
    // marked passes the info string through as lang; the first word decides,
    // case-insensitive, so "```mermaid title" and "```Mermaid" both render.
    (token as { lang?: string }).lang?.trim().split(/\s+/)[0]?.toLowerCase() === "mermaid"
  );
}

const FENCE_CLOSE_LINE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*$/;

// A code token's raw ends with a closing fence line exactly when the fence
// terminated in the source. Normalized for CRLF (marked normalizes at lex,
// so raw never contains "\r"; the normalize call is belt-and-suspenders for
// the lexer's own future changes).
function fenceTokenTerminated(raw: string): boolean {
  const lines = raw.replace(/\r\n/g, "\n").split("\n");
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const line = lines[index] ?? "";
    if (line.trim() === "") continue;
    return FENCE_CLOSE_LINE.test(line);
  }
  return false;
}

// The real source's tail is an open mermaid fence exactly when its last
// non-space top-level token is an unterminated mermaid code token (an open
// fence swallows the rest of the document, so it is always the tail).
function realSourceTailIsOpenMermaid(realSource: string): boolean {
  let tokens: Token[];
  try {
    tokens = markdownLexer.lexer(realSource);
  } catch {
    return false; // lexer failure fails open: no demote, today's behavior
  }
  for (let index = tokens.length - 1; index >= 0; index -= 1) {
    const token = tokens[index];
    if (token === undefined || token.type === "space") continue;
    return isMermaidCodeToken(token) && !fenceTokenTerminated((token as { raw?: string }).raw ?? "");
  }
  return false;
}

export function splitMarkdownSegments(closedSource: string, realSource: string | null): MarkdownSegment[] {
  const tokens = markdownLexer.lexer(closedSource);
  const segments: MarkdownSegment[] = [];
  let markdownRun: Token[] = [];
  const flush = () => {
    if (markdownRun.length > 0) segments.push({ kind: "markdown", tokens: markdownRun });
    markdownRun = [];
  };
  for (const token of tokens) {
    if (isMermaidCodeToken(token)) {
      flush();
      segments.push({ kind: "mermaid", text: (token as { text?: string }).text ?? "" });
    } else {
      markdownRun.push(token);
    }
  }
  flush();
  // Demote: a live stream whose real source ends in an open mermaid fence
  // produced a closed-lex mermaid segment only because the auto-closer
  // appended the fence. Turn that last segment back into markdown so it
  // renders as the code block it still is.
  if (realSource !== null && realSourceTailIsOpenMermaid(realSource)) {
    const last = segments[segments.length - 1];
    if (last?.kind === "mermaid") {
      const demoted = markdownLexer.lexer(closedSource).slice(-1);
      segments[segments.length - 1] = { kind: "markdown", tokens: demoted };
    }
  }
  return segments;
}
```

Note for the implementer: if `markdownLexer` in `lexer.ts` is a `Marked` instance, `markdownLexer.lexer` is its bound lexer method; check the exact export shape in `lexer.ts` first and match it.

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/markdown/segments.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/markdown/segments.ts cmd/evener-hub/frontend/src/widgets/markdown/segments.test.ts
git commit -m "feat(markdown): split messages at closed mermaid fences into token slices"
```

---

### Task 3: Mermaid security config, sanitizer, and SVG fixtures

The shared security module (exact configs from the spec, verified in review) plus committed real-mermaid SVG fixtures and their generator.

**Files:**
- Create: `cmd/evener-hub/frontend/src/widgets/mermaid/security.ts`
- Create: `cmd/evener-hub/frontend/src/widgets/mermaid/testdata/sources/*.mmd` (8 diagram types)
- Create: `cmd/evener-hub/frontend/scripts/gen-mermaid-fixtures.mjs`
- Test: `cmd/evener-hub/frontend/src/widgets/mermaid/security.test.ts`
- Modify: `cmd/evener-hub/frontend/package.json` (add `"mermaid": "11.17.2"` to dependencies; add script `"generate:mermaid-fixtures": "node scripts/gen-mermaid-fixtures.mjs"`)

**Interfaces:**
- Produces (Tasks 4 and 6 consume):
  - `MERMAID_FORBID_TAGS: readonly string[]`
  - `MERMAID_SANITIZE_CONFIG` (a DOMPurify config)
  - `sanitizeMermaidSvg(svg: string): string`
  - `initMermaid(themeVariables: Record<string, string>): Promise<Mermaid>` — lazy `import("mermaid")`, `initialize({ startOnLoad: false, securityLevel: "strict", theme: "default", themeVariables, dompurifyConfig: { FORBID_TAGS: [...MERMAID_FORBID_TAGS] } })`, one call per themeVariables content, cached.
  - `renderMermaidSvg(source: string, themeVariables): Promise<string>` — sanitized SVG output (render + sanitizeMermaidSvg), with a unique render id per call.

- [ ] **Step 1: Write the failing tests**

`security.test.ts` runs in jsdom and needs no mermaid render — it tests the sanitizer against committed fixtures and handcrafted hostile SVG:

```ts
import { readFileSync, readdirSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { sanitizeMermaidSvg } from "./security";

const FIXTURE_DIR = new URL("./testdata/", import.meta.url);
const fixtures = readdirSync(FIXTURE_DIR).filter((f) => f.endsWith(".svg"));

describe("sanitizeMermaidSvg against real mermaid fixtures", () => {
  it("has a fixture for each common diagram type", () => {
    for (const name of ["flowchart", "sequence", "class", "state", "er", "gantt", "pie", "gitgraph"]) {
      expect(fixtures, `missing ${name}.svg`).toContain(`${name}.svg`);
    }
  });

  it.each(fixtures.map((f) => [f]))("%s keeps its labels and role", (file) => {
    const raw = readFileSync(new URL(`./testdata/${file}`, import.meta.url), "utf8");
    const clean = sanitizeMermaidSvg(raw);
    // every human-readable label string in the raw fixture survives
    const labels = [...raw.matchAll(/<span class="[^"]*Label[^"]*"[^>]*>([^<]+)</g)].map((m) => m[1]);
    const textLabels = [...raw.matchAll(/<text[^>]*>([^<]+)</g)].map((m) => m[1]);
    for (const label of [...labels, ...textLabels]) {
      expect(clean).toContain(label);
    }
    expect(clean).toContain('role="graphics-document document"');
  });

  it.each(fixtures.map((f) => [f]))("%s carries no anchor or resource elements", (file) => {
    const clean = sanitizeMermaidSvg(readFileSync(new URL(`./testdata/${file}`, import.meta.url), "utf8"));
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
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/mermaid/security.test.ts`
Expected: FAIL (module and fixtures do not exist).

- [ ] **Step 3: Implement the security module**

`security.ts`:

```ts
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
```

- [ ] **Step 4: Write the fixture generator and generate fixtures**

`scripts/gen-mermaid-fixtures.mjs` (node, direct ESM import of mermaid — never an eval'd bundle, which loses Node globals like `structuredClone`; one render per process because hostile or complex payloads can hang jsdom renders and poison the process):

```js
#!/usr/bin/env node
// Regenerates src/widgets/mermaid/testdata/*.svg from testdata/sources/*.mmd
// by rendering each with the REAL mermaid in jsdom (structure is real;
// geometry is not - jsdom has no SVG layout). Run after any mermaid version
// bump: npm run generate:mermaid-fixtures
import { spawnSync } from "node:child_process";
import { readdirSync, writeFileSync } from "node:fs";

const SOURCES = new URL("../src/widgets/mermaid/testdata/sources/", import.meta.url);
const OUT = new URL("../src/widgets/mermaid/testdata/", import.meta.url);

for (const file of readdirSync(SOURCES).filter((f) => f.endsWith(".mmd"))) {
  const result = spawnSync(process.execPath, [new URL("./render-one-mermaid-fixture.mjs", import.meta.url).pathname, file], {
    encoding: "utf8",
  });
  if (result.status !== 0) throw new Error(`${file}: ${result.stderr || result.stdout}`);
  writeFileSync(new URL(`${file.replace(/\.mmd$/, ".svg")}`, OUT), result.stdout);
  console.log(`${file} -> ${file.replace(/\.mmd$/, ".svg")}`);
}
```

`scripts/render-one-mermaid-fixture.mjs`:

```js
// Renders one .mmd source to SVG on stdout. Own process per fixture (see
// gen-mermaid-fixtures.mjs). Six shims cover jsdom's missing SVG layout
// APIs; verified sufficient for all eight common diagram types on mermaid
// 11.17.2 and 12.0.0 (direct ESM import keeps Node's structuredClone).
import { readFileSync } from "node:fs";
import { JSDOM } from "jsdom";

const { window } = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true });
globalThis.window = window;
globalThis.document = window.document;
Object.defineProperty(globalThis, "navigator", { value: window.navigator, configurable: true });
globalThis.Element = window.Element;
globalThis.SVGElement = window.SVGElement;
globalThis.Node = window.Node;
globalThis.HTMLElement = window.HTMLElement;
globalThis.getComputedStyle = window.getComputedStyle.bind(window);
globalThis.CSSStyleSheet = window.CSSStyleSheet ?? class CSSStyleSheet { replaceSync() {} replace() {} insertRule() {} };
window.CSSStyleSheet = globalThis.CSSStyleSheet;
if (!("adoptedStyleSheets" in window.document)) window.document.adoptedStyleSheets = [];
window.SVGElement.prototype.getBBox = () => ({ x: 0, y: 0, width: 50, height: 20 });
window.SVGElement.prototype.getComputedTextLength = () => 50;
window.SVGElement.prototype.getScreenCTM = function () {
  return { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0, inverse() { return this; }, multiply() { return this; } };
};
window.SVGElement.prototype.getPointAtLength = () => ({ x: 0, y: 0 });

const file = process.argv[2];
const source = readFileSync(new URL(`../src/widgets/mermaid/testdata/sources/${file}`, import.meta.url), "utf8");
const { default: mermaid } = await import("mermaid");
mermaid.initialize({ startOnLoad: false, securityLevel: "strict" });
const { svg } = await mermaid.render("fixture", source);
process.stdout.write(svg);
```

Sources (`testdata/sources/*.mmd`), one per type, each with at least two distinct human-readable labels: flowchart (`graph TD; A[Start node]-->B{Decision label}; B-->|yes path|C[Terminal end]`), sequence, class, state, er, gantt, pie, gitgraph. (The implementer writes standard examples; each must render.)

Generate: `cd cmd/evener-hub/frontend && npm install && npm run generate:mermaid-fixtures`

- [ ] **Step 5: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/mermaid/security.test.ts`
Expected: PASS. If a fixture has no labels matching the test's regexes (some types put labels in `<text>` only), adjust the label extraction to that type's real markup — never weaken the survival assertion.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/mermaid cmd/evener-hub/frontend/scripts/gen-mermaid-fixtures.mjs cmd/evener-hub/frontend/scripts/render-one-mermaid-fixture.mjs cmd/evener-hub/frontend/package.json cmd/evener-hub/frontend/package-lock.json
git commit -m "feat(mermaid): security config, sanitizer, and real-mermaid SVG fixtures"
```

---

### Task 4: MermaidDiagram component (web)

The inline diagram: lazy render, error fallback, theme, non-interactive container, render timeout.

**Files:**
- Create: `cmd/evener-hub/frontend/src/widgets/mermaid/index.tsx`
- Create: `cmd/evener-hub/frontend/src/widgets/mermaid/mermaid.module.css`
- Create: `cmd/evener-hub/frontend/src/widgets/mermaid/resolveScheme.ts`
- Test: `cmd/evener-hub/frontend/src/widgets/mermaid/mermaid.test.tsx`
- Create: `cmd/evener-hub/frontend/src/widgets/mermaid/jsdomSvgShims.ts` (test-only shim installer, the six lines from the generator's preamble)

**Interfaces:**
- Consumes: `renderMermaidSvg` from `./security` (Task 3).
- Produces (Tasks 5 and 7 consume):
  - `MermaidDiagram({ source, onOpen, renderTimeoutMs }: { source: string; onOpen?: (svg: string, source: string) => void; renderTimeoutMs?: number })`
  - Root element carries `data-mermaid-diagram=""` (EntityText/AgentMarkdown exclusions key on it).
  - `useResolvedScheme(): "light" | "dark"` from `resolveScheme.ts`.
  - `mermaidThemeVariables(scheme: "light" | "dark"): Record<string, string>` — maps the app's CSS tokens onto mermaid's `themeVariables` (reads `getComputedStyle(document.documentElement)` for `--ink-hi`, `--edge`, `--bg-raise`/`--surface` tokens; check the real token names in `src/styles/tokens.css` and use those).

- [ ] **Step 1: Write the failing tests**

`mermaid.test.tsx` (vitest, jsdom; call the shim installer in `beforeAll`; one render per test file is too slow to repeat per case, so keep renders few — benign flowchart, invalid source, anchor-payload source, and the timeout case):

```tsx
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { installJsdomSvgShims } from "./jsdomSvgShims";
import { MermaidDiagram } from "./index";

beforeAll(() => installJsdomSvgShims());
afterEach(cleanup);

describe("MermaidDiagram", () => {
  it("renders a real flowchart with its labels", async () => {
    render(<MermaidDiagram source={"graph TD; A[Start node]-->B{Decision label}"} />);
    await waitFor(() => expect(document.querySelector("[data-mermaid-diagram] svg")).not.toBeNull(), {
      timeout: 15000,
    });
    expect(screen.getByText("Start node")).toBeTruthy();
    expect(screen.getByText("Decision label")).toBeTruthy();
  }, 20000);

  it("falls back to the source with an error note on invalid mermaid", async () => {
    render(<MermaidDiagram source={"graph TD; A[unclosed"} />);
    await waitFor(() => expect(screen.getByText(/couldn't render this diagram/i)).toBeTruthy(), { timeout: 15000 });
    expect(screen.getByText(/graph TD/)).toBeTruthy();
  }, 20000);

  it("renders no anchor for an authored link label or click directive", async () => {
    render(
      <MermaidDiagram source={'graph TD; A["<a href=\'https://evil.example\'>click</a>"] --> B; click B href "https://evil.example/click";'} />,
    );
    await waitFor(() => expect(document.querySelector("[data-mermaid-diagram] svg")).not.toBeNull(), {
      timeout: 15000,
    });
    expect(document.querySelector("[data-mermaid-diagram] a")).toBeNull();
  }, 20000);

  it("times a wedged render out to the error fallback", async () => {
    render(<MermaidDiagram source={"graph TD; A-->B"} renderTimeoutMs={1} />);
    await waitFor(() => expect(screen.getByText(/couldn't render this diagram/i)).toBeTruthy(), { timeout: 15000 });
  }, 20000);

  it("hits the error fallback for an empty diagram", async () => {
    render(<MermaidDiagram source="" />);
    await waitFor(() => expect(screen.getByText(/couldn't render this diagram/i)).toBeTruthy(), { timeout: 15000 });
  }, 20000);
});
```

(If mermaid's jsdom render hangs on a payload here, that payload moves to the browser guard in Task 6 and the test says so. The three above completed in review experiments.)

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/mermaid/mermaid.test.tsx`
Expected: FAIL (component does not exist).

- [ ] **Step 3: Implement**

`index.tsx`:

```tsx
import { type ReactNode, useEffect, useState } from "react";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import styles from "./mermaid.module.css";
import { mermaidThemeVariables, useResolvedScheme } from "./resolveScheme";
import { renderMermaidSvg } from "./security";

const CLASS = {
  root: requireClass(styles.root, "mermaid.module.css", "root"),
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
    body = (
      // Sanitized by security.ts's two layers; see that file. pointer-events
      // come from mermaid.module.css's .svg rules.
      // biome-ignore lint/security/noDangerouslySetInnerHtml: sanitized via the mermaid pipeline's two DOMPurify layers, see security.ts
      <div className={CLASS.svg} dangerouslySetInnerHTML={{ __html: state.svg }} />
    );
  } else {
    body = <div className={CLASS.svg} aria-busy="true" />;
  }

  return (
    <div
      className={CLASS.root}
      data-mermaid-diagram=""
      onClick={state.status === "ok" && onOpen !== undefined ? () => onOpen(state.svg, source) : undefined}
    >
      {body}
    </div>
  );
}
```

`resolveScheme.ts`:

```ts
import { usePrefsStore } from "../../stores/prefs";
import { useSyncExternalStore } from "react";

function systemDark(): boolean {
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

function subscribe(callback: () => void): () => void {
  const query = window.matchMedia("(prefers-color-scheme: dark)");
  query.addEventListener("change", callback);
  return () => query.removeEventListener("change", callback);
}

/** The theme actually in effect: the pref, with "system" resolved. */
export function useResolvedScheme(): "light" | "dark" {
  const theme = usePrefsStore((state) => state.theme);
  const systemIsDark = useSyncExternalStore(subscribe, systemDark);
  if (theme === "system") return systemIsDark ? "dark" : "light";
  return theme;
}

/** Maps app tokens (read after the theme applies) onto mermaid's theme knobs. */
export function mermaidThemeVariables(scheme: "light" | "dark"): Record<string, string> {
  const styles = getComputedStyle(document.documentElement);
  const token = (name: string) => styles.getPropertyValue(name).trim();
  return {
    dark: String(scheme === "dark"),
    background: token("--surface-canvas"),
    primaryColor: token("--surface-1"),
    primaryBorderColor: token("--edge"),
    primaryTextColor: token("--ink-hi"),
    lineColor: token("--ink-mid"),
    fontFamily: token("--font-sans"),
  };
}
```

`mermaid.module.css`: `.root` (margin `0 0 var(--space-3)`, cursor pointer when clickable), `.svg` (`pointer-events: none` on the svg child, `max-width: 100%`, `overflow: hidden`), `.error` (small, `var(--ink-mid)`).

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/mermaid/mermaid.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/mermaid
git commit -m "feat(mermaid): inline diagram component with error fallback and render timeout"
```

---

### Task 5: Markdown widget integration and DOM-walk exclusions

Wire segments into `Markdown`; exclude diagrams from the entity and link walks.

**Files:**
- Modify: `cmd/evener-hub/frontend/src/widgets/markdown/index.tsx` (segmented render path)
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/EntityText.tsx:7` (skip selector)
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/AgentMarkdown.tsx:47` (link walk)
- Test: `cmd/evener-hub/frontend/src/widgets/markdown/markdown.test.tsx` (additions)
- Test: `cmd/evener-hub/frontend/src/panes/session/transcript/EntityText.test.tsx` or wherever its tests live (find with `grep -l "useEntityTextEnhancement\|EntityText" src/**/*.test.tsx`)

**Interfaces:**
- Consumes: `splitMarkdownSegments`, `messageMayContainMermaid` (Task 2); `MermaidDiagram` (Task 4).
- Produces: `Markdown` behaves identically for non-mermaid messages (byte-identical DOM); mermaid messages render segments under one wrapper div carrying the `ref`.

- [ ] **Step 1: Write the failing tests**

In `markdown.test.tsx` (match its existing render/assert style):

```tsx
it("renders a mermaid fence as a diagram container, prose as markdown", () => {
  const { container } = render(<Markdown source={"before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter"} />);
  expect(container.querySelectorAll("[data-mermaid-diagram]")).toHaveLength(1);
  expect(container.textContent).toContain("before");
  expect(container.textContent).toContain("after");
});

it("keeps an open mermaid fence a code block while live", () => {
  const { container } = render(<Markdown live source={"before\n\n```mermaid\ngraph TD; A-->"} />);
  expect(container.querySelector("[data-mermaid-diagram]")).toBeNull();
  expect(container.textContent).toContain("graph TD; A-->");
});

it("resolves a link definition used on the far side of a diagram", () => {
  const { container } = render(
    <Markdown source={"See [the docs][d].\n\n```mermaid\ngraph TD; A-->B\n```\n\n[d]: https://example.com\n"} />,
  );
  const link = container.querySelector("a[href='https://example.com']");
  expect(link).not.toBeNull();
});
```

For the walks (EntityText test file; mount a hand-built DOM — this tests our walker's exclusion, not mermaid):

```tsx
it("never enhances entity ids inside a diagram", async () => {
  // root contains a diagram subtree with an entity id in an SVG text node
  const root = document.createElement("div");
  root.innerHTML = `<p>plain dlg_0123456789abcdefghjkmnpqrst text</p><div data-mermaid-diagram=""><svg><text>job_0123456789abcdefghjkmnpqrst</text></svg></div>`;
  document.body.append(root);
  // render the hook against root (follow the file's existing hook-mount pattern)
  // ...assert: the prose id got a portal host, the svg one did not
  expect(root.querySelector("[data-mermaid-diagram] [data-entity-host]")).toBeNull();
  expect(root.querySelector("p [data-entity-host]")).not.toBeNull();
});
```

(Use real entity-id shapes from `appwire-client/typescript/entityIds.ts` — generate one via its helpers if exported, otherwise copy the test ids already used in EntityText's existing tests.) For AgentMarkdown: a test that an `<a href="docs/report.md">` inside `[data-mermaid-diagram]` gets no "Open beside" portal, while the same href in prose does.

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/markdown/markdown.test.tsx` (and the two walk test files)
Expected: FAIL (no diagram container; walks still enter it).

- [ ] **Step 3: Implement**

In `widgets/markdown/index.tsx`, keep the entire existing single-root path for messages without a mermaid fence, and add the segmented path:

```tsx
// at the top of Markdown():
const segmented = messageMayContainMermaid(source);
```

When `segmented` is false, return today's exact element (unchanged code path, including the windowed live throttle). When true:

```tsx
// Segmented path. Live mode: close the stream's open constructs first, then
// demote a still-open mermaid fence back to markdown (segments.ts). Cost per
// live render is one close+lex of the whole message - the same full-parse
// fallback today's windowed throttle already takes whenever a fence sits in
// the tail window (verified in review), so no new throttle tier is added.
const segments = useMemo(
  () => splitMarkdownSegments(live ? closeOpenMarkdown(source) : source, live ? source : null),
  [source, live],
);
return (
  <div ref={ref}>
    {segments.map((segment, index) =>
      segment.kind === "mermaid" ? (
        <MermaidDiagram key={`mermaid-${index}-${segment.text.length}`} source={segment.text} />
      ) : (
        <MarkdownSlice key={`markdown-${index}-${segment.tokens.length}-${segment.tokens[0]?.raw.length ?? 0}`} segment={segment} />
      ),
    )}
  </div>
);
```

Keys combine the segment index with content-derived values (never the bare
index — biome forbids it) so a settling fence or a theme flip does not reuse
the wrong DOM node. Each markdown slice renders through the same
parse+sanitize the single-root path uses:

```tsx
const MarkdownSlice = memo(function MarkdownSlice({ segment }: { segment: { kind: "markdown"; tokens: Token[] } }) {
  const html = useMemo(() => DOMPurify.sanitize(md.parser(segment.tokens), SANITIZE_CONFIG), [segment.tokens]);
  // biome-ignore lint/security/noDangerouslySetInnerHtml: same sanitized pipeline as the single-root path, see above
  return <div className={CLASS.root} dangerouslySetInnerHTML={{ __html: html }} />;
});
```

In `EntityText.tsx`:

```ts
const ENTITY_SKIP_SELECTOR = "code, pre, script, style, a, [data-entity-host], [data-mermaid-diagram]";
```

In `AgentMarkdown.tsx`, inside the link loop before any use of `link`:

```ts
if (link.closest("[data-mermaid-diagram]") !== null) continue;
```

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/markdown src/panes/session/transcript`
Expected: PASS, including every pre-existing markdown test unchanged.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/markdown cmd/evener-hub/frontend/src/panes/session/transcript/EntityText.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/AgentMarkdown.tsx
git commit -m "feat(markdown): render mermaid segments inline; exclude diagrams from DOM walks"
```

---

### Task 6: Browser guard and dev gallery entry

Real-Chrome proof: a diagram renders with labels, and a hostile diagram issues zero network requests. Plus a dev-gallery entry for visual checks (segment-boundary spacing is the known trade-off — look at it here).

**Files:**
- Create: `cmd/evener-hub/frontend/scripts/mermaidguard/run.mjs` (+ any case files it needs)
- Create: `cmd/evener-hub/frontend/src/dev/mermaidguard-entry.tsx`
- Modify: `cmd/evener-hub/frontend/package.json` (script `"mermaidguard": "node scripts/mermaidguard/run.mjs"`)
- Modify: `cmd/evener-dev/webbrowser.go:26` (append `"mermaidguard"` to `browserGuards`)
- Modify: `cmd/evener-hub/frontend/src/dev/gallery-sections/markdown.tsx` (add a mermaid section: prose + flowchart + prose, prose + diagram + prose with a heading right after, an error case)

**Interfaces:**
- Consumes: `MermaidDiagram` (Task 4), `startBrowserGuard` from `scripts/browserGuardProcess.mjs`, the CDP plumbing in `scripts/browserGuardCdp.mjs` (read a sibling guard's run.mjs for the exact call sequence before writing this one).

- [ ] **Step 1: Write the guard**

`mermaidguard-entry.tsx` mounts two diagrams: a benign flowchart with known labels and a hostile one (`<img src="https://invalid.example/pixel.png">` label, an authored `<a href>`, a `click B href "https://invalid.example/click"` directive). It records every resource-fetch attempt via a `PerformanceObserver` on `resource` entries plus wrapping `Image`/`fetch`, and exposes `window.mermaidGuardResult()` returning `{ labelsPresent: string[], missingLabels: string[], externalAttempts: string[], anchors: number }`.

`run.mjs` follows the sibling-guard pattern (startBrowserGuard, load the entry, evaluate, assert, clean up): asserts all known labels present, `externalAttempts` empty, `anchors` 0, and an `<svg>` with nonzero client size present in each `[data-mermaid-diagram]`.

- [ ] **Step 2: Run the guard to verify it fails on the unfixed pipeline**

Expected workflow: run `node scripts/mermaidguard/run.mjs` against a build where FORBID_TAGS is removed from `security.ts` (local mutation only, never committed) and watch `externalAttempts` list the pixel URL; restore and watch it pass. Record both outputs for the PR description.

- [ ] **Step 3: Gallery entry**

Add a "Mermaid" block to `gallery-sections/markdown.tsx` following its existing section pattern: one inline flowchart between prose paragraphs, one heading-immediately-after-diagram case (the pseudo-class spacing check), one invalid diagram (error fallback), one wide diagram (fullscreen affordance lands in Task 7).

- [ ] **Step 4: Run and commit**

Run: `node scripts/mermaidguard/run.mjs` (from `cmd/evener-hub/frontend`), then `make test-web-browser` from the repo root to prove the gate wiring.

```bash
git add cmd/evener-hub/frontend/scripts/mermaidguard cmd/evener-hub/frontend/src/dev/mermaidguard-entry.tsx cmd/evener-hub/frontend/src/dev/gallery-sections/markdown.tsx cmd/evener-hub/frontend/package.json cmd/evener-dev/webbrowser.go
git commit -m "test(mermaid): browser guard for real renders and zero network requests"
```

---

### Task 7: Web fullscreen viewer

OverlayPanel-based viewer: pan/zoom, source toggle, copy.

**Files:**
- Create: `cmd/evener-hub/frontend/src/widgets/mermaid/DiagramViewer.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/mermaid/index.tsx` (hold viewer state; pass `onOpen` internally)
- Test: `cmd/evener-hub/frontend/src/widgets/mermaid/DiagramViewer.test.tsx`

**Interfaces:**
- Consumes: `OverlayPanel` (`widgets/dialog/OverlayPanel`: `open`, `onClose`, `title`, `children`, `footer`, `panelClassName`), `CopyButton` (`widgets/copybutton`: `text`, `label`, `variant`, `size`).
- Produces: `DiagramViewer({ open, svg, source, onClose }: { open: boolean; svg: string; source: string; onClose: () => void })`. `MermaidDiagram` opens it on click.

- [ ] **Step 1: Write the failing tests**

```tsx
it("opens, toggles to source and back, and closes", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={onClose} />);
  expect(screen.getByRole("dialog")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: /show source/i }));
  expect(screen.getByText("graph TD; A-->B")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: /show diagram/i }));
  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalled();
});

it("zooms with the wheel and pans by dragging", async () => {
  // assert the transform state: wheel up increases scale, drag changes translate
  // (jsdom has no layout; assert the style string on the transform container)
});
```

(FIXTURE_SVG is the committed flowchart fixture read from testdata.)

- [ ] **Step 2: Run to verify failure** — component does not exist.

- [ ] **Step 3: Implement**

`DiagramViewer.tsx`: an `OverlayPanel` with `title="Diagram"`, footer holding the source-toggle button and a `CopyButton` with `text={source}` `label="Copy source"`, and a body that swaps between the zoomable SVG and a source `<pre>`. Pan/zoom is dependency-free:

```tsx
const MIN_SCALE = 0.25;
const MAX_SCALE = 8;

export function DiagramViewer({ open, svg, source, onClose }: { open: boolean; svg: string; source: string; onClose: () => void }) {
  const [showSource, setShowSource] = useState(false);
  const [view, setView] = useState({ scale: 1, x: 0, y: 0 });
  const drag = useRef<{ pointerId: number; startX: number; startY: number; baseX: number; baseY: number } | null>(null);

  function handleWheel(event: React.WheelEvent<HTMLDivElement>) {
    // Zoom around the cursor: keep the point under the cursor stationary.
    const rect = event.currentTarget.getBoundingClientRect();
    const cursorX = event.clientX - rect.left;
    const cursorY = event.clientY - rect.top;
    setView((current) => {
      const next = Math.min(MAX_SCALE, Math.max(MIN_SCALE, current.scale * (event.deltaY < 0 ? 1.2 : 1 / 1.2)));
      const ratio = next / current.scale;
      return {
        scale: next,
        x: cursorX - ratio * (cursorX - current.x),
        y: cursorY - ratio * (cursorY - current.y),
      };
    });
  }

  function handlePointerDown(event: React.PointerEvent<HTMLDivElement>) {
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = { pointerId: event.pointerId, startX: event.clientX, startY: event.clientY, baseX: view.x, baseY: view.y };
  }

  function handlePointerMove(event: React.PointerEvent<HTMLDivElement>) {
    const active = drag.current;
    if (active === null || active.pointerId !== event.pointerId) return;
    setView((current) => ({ ...current, x: active.baseX + event.clientX - active.startX, y: active.baseY + event.clientY - active.startY }));
  }

  function handlePointerUp(event: React.PointerEvent<HTMLDivElement>) {
    if (drag.current?.pointerId === event.pointerId) drag.current = null;
  }

  return (
    <OverlayPanel open={open} onClose={onClose} title="Diagram" panelClassName={CLASS.viewerPanel}
      footer={
        <>
          <button type="button" onClick={() => setShowSource((value) => !value)}>
            {showSource ? "Show diagram" : "Show source"}
          </button>
          <button type="button" onClick={() => setView({ scale: 1, x: 0, y: 0 })}>Reset zoom</button>
          <CopyButton text={source} label="Copy source" />
        </>
      }>
      {showSource ? (
        <pre className={CLASS.source}><code>{source}</code></pre>
      ) : (
        <div className={CLASS.zoomSurface} onWheel={handleWheel} onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove} onPointerUp={handlePointerUp} onPointerCancel={handlePointerUp}>
          {/* svg arrives already sanitized via security.ts's two layers */}
          {/* biome-ignore lint/security/noDangerouslySetInnerHtml: sanitized upstream in security.ts */}
          <div className={CLASS.zoomContent} style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
            dangerouslySetInnerHTML={{ __html: svg }} />
        </div>
      )}
    </OverlayPanel>
  );
}
```

`mermaid.module.css` gains `.viewerPanel` (near-full-viewport), `.zoomSurface` (`overflow: hidden; touch-action: none; cursor: grab` on the drag surface), `.zoomContent` (`transform-origin: 0 0`), `.source` (scrollable pre).

In `MermaidDiagram`, replace the `onOpen` prop wiring with internal state:

```tsx
const [viewer, setViewer] = useState<{ svg: string } | null>(null);
// container onClick: setViewer({ svg: state.svg })
// render: {viewer !== null && <DiagramViewer open svg={viewer.svg} source={source} onClose={() => setViewer(null)} />}
```

and drop the now-unused `onOpen` prop (Task 5's call site passes nothing).

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/mermaid`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/mermaid
git commit -m "feat(mermaid): fullscreen diagram viewer with pan/zoom, source toggle, copy"
```

---

### Task 8: Native segment split

The native splitter: source-string segments, link definitions prepended (md4c parses each segment standalone — verified loss both directions without this), demote by the terminated-fence check.

**Files:**
- Create: `mobile-native/src/markdownSegments.ts`
- Test: `mobile-native/src/markdownSegments.test.ts`

**Interfaces:**
- Consumes: `lexer` from `"marked"` (native dep, same version).
- Produces (Task 10 consumes):
  - `type NativeSegment = { kind: "markdown"; source: string } | { kind: "mermaid"; source: string }`
  - `splitNativeSegments(source: string): NativeSegment[]`

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, it } from "vitest";
import { splitNativeSegments } from "./markdownSegments";

describe("splitNativeSegments", () => {
  it("returns prose untouched when no mermaid fence exists", () => {
    expect(splitNativeSegments("hello **world**")).toEqual([{ kind: "markdown", source: "hello **world**" }]);
  });

  it("splits a closed mermaid fence out", () => {
    const segments = splitNativeSegments("before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter\n");
    expect(segments.map((s) => s.kind)).toEqual(["markdown", "mermaid", "markdown"]);
    expect(segments[1]).toEqual({ kind: "mermaid", source: "graph TD; A-->B\n" });
  });

  it("prepends link definitions to every prose segment so references resolve standalone", () => {
    const segments = splitNativeSegments(
      "See [the docs][d].\n\n```mermaid\ngraph TD; A-->B\n```\n\nAgain [the docs][d].\n\n[d]: https://example.com\n",
    );
    const prose = segments.filter((s) => s.kind === "markdown");
    expect(prose.length).toBe(2);
    for (const segment of prose) {
      expect(segment.kind === "markdown" && segment.source).toContain("[d]: https://example.com");
    }
  });

  it("collects definitions nested in blockquotes and lists", () => {
    const segments = splitNativeSegments("> [d]: https://example.com\n\nUse [x][d].\n\n```mermaid\ngraph TD; A-->B\n```\n");
    const prose = segments.find((s) => s.kind === "markdown");
    expect(prose?.kind === "markdown" && prose.source).toContain("[d]: https://example.com");
  });

  it("keeps a trailing open mermaid fence in the prose segment", () => {
    const segments = splitNativeSegments("intro\n\n```mermaid\ngraph TD; A-->");
    expect(segments.map((s) => s.kind)).toEqual(["markdown"]);
    expect(segments[0]?.kind === "markdown" && segments[0].source).toContain("graph TD; A-->");
  });

  it("handles CRLF fences", () => {
    const segments = splitNativeSegments("```mermaid\r\ngraph TD; A-->B\r\n```\r\n");
    expect(segments).toEqual([{ kind: "mermaid", source: "graph TD; A-->B\n" }]);
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd mobile-native && npx vitest run src/markdownSegments.test.ts`
Expected: FAIL (module does not exist).

- [ ] **Step 3: Implement**

```ts
// Splits a message at top-level CLOSED mermaid fences into the strings
// EnrichedMarkdownText renders. md4c parses each segment standalone, so a
// reference-style link whose definition sits across a diagram would render
// as literal "[text][label]" (verified against the package's own md4c build):
// every prose segment gets the whole message's link definitions prepended.
// Definitions render as nothing, so visible output is unchanged. A mermaid
// fence still open at the tail (stream in flight) stays prose and renders as
// a code block until its closer arrives - there is no closeOpenMarkdown on
// native, so this is a terminated-raw check, not a two-lex comparison.
import { lexer, type Token } from "marked";

export type NativeSegment = { kind: "markdown"; source: string } | { kind: "mermaid"; source: string };

const MERMAID_FENCE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*mermaid(?:[ \t\r]|$)/im;
const FENCE_CLOSE_LINE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*$/;

function isMermaidCodeToken(token: Token): boolean {
  return token.type === "code" && (token.lang ?? "").trim().split(/\s+/)[0]?.toLowerCase() === "mermaid";
}

function fenceTokenTerminated(raw: string): boolean {
  const lines = raw.split("\n");
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const line = lines[index] ?? "";
    if (line.trim() === "") continue;
    return FENCE_CLOSE_LINE.test(line);
  }
  return false;
}

function collectDefinitions(tokens: Token[], into: string[]): void {
  for (const token of tokens) {
    if (token.type === "def") {
      into.push(token.raw);
      continue;
    }
    if ("tokens" in token) collectDefinitions((token.tokens as Token[] | undefined) ?? [], into);
    if ("items" in token) for (const item of (token.items as { tokens?: Token[] }[] | undefined) ?? []) {
      collectDefinitions(item.tokens ?? [], into);
    }
  }
}

export function splitNativeSegments(source: string): NativeSegment[] {
  // Cheap gate: no mermaid fence, no lex, today's exact render path.
  if (!MERMAID_FENCE.test(source)) return [{ kind: "markdown", source }];
  const tokens = lexer(source);
  const definitions: string[] = [];
  collectDefinitions(tokens, definitions);
  const prefix = definitions.length > 0 ? `${definitions.join("")}\n` : "";

  const segments: NativeSegment[] = [];
  let markdownRun = "";
  const flush = () => {
    const body = markdownRun;
    markdownRun = "";
    if (body.trim() === "") return;
    segments.push({ kind: "markdown", source: prefix + body });
  };
  for (const token of tokens) {
    if (isMermaidCodeToken(token) && fenceTokenTerminated(token.raw)) {
      flush();
      segments.push({ kind: "mermaid", source: token.text });
    } else {
      markdownRun += token.raw;
    }
  }
  flush();
  return segments;
}
```

(If a mermaid fence appears in an existing test corpus message and this changes a snapshot, the snapshot update is part of this task — say so in the commit message.)

- [ ] **Step 4: Run to verify pass**

Run: `cd mobile-native && npx vitest run src/markdownSegments.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/markdownSegments.ts mobile-native/src/markdownSegments.test.ts
git commit -m "feat(native): split messages at closed mermaid fences, prepending link definitions"
```

---

### Task 9: Native mermaid page build

The self-contained WebView page: mermaid + DOMPurify + bootstrap, one generated module, a freshness test, and the new dependency.

**Files:**
- Modify: `mobile-native/package.json` (add dependency `"react-native-webview": "13.16.1"` — the Expo SDK 57 pin from `bundledNativeModules.json`; add devDependency `esbuild` pinned to the version you install)
- Create: `mobile-native/scripts/build-mermaid-page.mts`
- Create: `mobile-native/src/generated/mermaidPage.ts` (GENERATED — header comment says so and names the generator)
- Create: `mobile-native/src/mermaidPageContent.ts` (the bootstrap source the generator bundles — kept as a normal, lintable TS module)
- Test: `mobile-native/src/mermaidPage.test.ts` (freshness + content invariants)

**Interfaces:**
- Produces (Task 10 consumes): `MERMAID_PAGE_HTML: string` from `./generated/mermaidPage`.
- Page protocol (Task 10 implements against this exactly):
  - RN → page: `webView.postMessage(JSON.stringify(msg))` with `{type:"render", source: string, theme: Record<string,string>, mode: "fit" | "zoom"}`. The page listens on BOTH `document` (Android) and `window` (iOS) "message" events.
  - Page → RN: `window.ReactNativeWebView.postMessage(JSON.stringify(msg))` with `{type:"ready"}`, `{type:"height", value: number}`, `{type:"error", message: string}`.
  - CSP meta in the HTML head: `default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'` (the bundle is inlined; nothing else loads).

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from "vitest";
import { MERMAID_PAGE_HTML } from "./generated/mermaidPage";

describe("MERMAID_PAGE_HTML", () => {
  it("carries the lockdown CSP meta", () => {
    expect(MERMAID_PAGE_HTML).toContain(`default-src 'none'`);
  });
  it("initializes mermaid strict with the forbid list", () => {
    expect(MERMAID_PAGE_HTML).toContain("securityLevel");
    expect(MERMAID_PAGE_HTML).toContain("foreignObject");
  });
  it("is fresh (matches a rebuild)", async () => {
    const { buildMermaidPageHtml } = await import("../scripts/build-mermaid-page.mts");
    expect(await buildMermaidPageHtml()).toBe(MERMAID_PAGE_HTML);
  });
});
```

(If importing the script trips `check:scripts` conventions, the freshness check may instead spawn `tsx scripts/build-mermaid-page.mts --stdout` — pick whichever the repo's script-check accepts and adjust.)

- [ ] **Step 2: Run to verify failure**

Run: `cd mobile-native && npx vitest run src/mermaidPage.test.ts`
Expected: FAIL (generated module does not exist).

- [ ] **Step 3: Implement**

`mermaidPageContent.ts` exports the bootstrap source string (or a function the generator serializes — the generator inlines it into the page's inline script after the mermaid/DOMPurify bundles). Bootstrap behavior: on `render` message, `mermaid.initialize({ startOnLoad:false, securityLevel:"strict", theme:"default", themeVariables: msg.theme, dompurifyConfig: { FORBID_TAGS: [...] } })` with the same FORBID_TAGS list as web (duplicate the literal list with a comment pointing at `widgets/mermaid/security.ts` — there is no shared package for the two frontends), render with a fixed id, sanitize with the same config (`USE_PROFILES` html+svg+svgFilters, `ADD_TAGS: ["foreignObject"]`, `HTML_INTEGRATION_POINTS: { foreignobject: true }`, `ADD_ATTR: ["role"]`, `FORBID_TAGS`), write into the container, post `{type:"height", value: container.scrollHeight}`; on throw, post `{type:"error", message}`. Post `{type:"ready"}` once at load. `mode: "zoom"` sets the viewport meta to user-scalable yes (fullscreen viewer); `"fit"` sets user-scalable no. Do both by rewriting the meta content attribute before rendering.

`build-mermaid-page.mts`: esbuild `build({ entryPoints: [bootstrap entry], bundle: true, format: "iife", write: false, minify: true })` over a small entry that imports `mermaid`, `dompurify`, and the bootstrap; emits `src/generated/mermaidPage.ts`:

```ts
// GENERATED by scripts/build-mermaid-page.mts - do not edit (regenerate: npx tsx scripts/build-mermaid-page.mts)
export const MERMAID_PAGE_HTML = `<!doctype html>...${bundle}...`;
```

Build it, then run the freshness test.

- [ ] **Step 4: Run to verify pass**

Run: `cd mobile-native && npx vitest run src/mermaidPage.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/package.json mobile-native/package-lock.json mobile-native/scripts/build-mermaid-page.mts mobile-native/src/mermaidPageContent.ts mobile-native/src/generated/mermaidPage.ts mobile-native/src/mermaidPage.test.ts
git commit -m "feat(native): self-contained mermaid WebView page with lockdown CSP"
```

---

### Task 10: Native MermaidDiagram and MarkdownResponse integration

**Files:**
- Create: `mobile-native/src/MermaidDiagram.tsx`
- Modify: `mobile-native/src/MarkdownResponse.tsx`
- Test: `mobile-native/src/MermaidDiagram.test.tsx`
- Test: `mobile-native/src/MarkdownResponse.test.tsx` (additions)

**Interfaces:**
- Consumes: `splitNativeSegments` (Task 8), `MERMAID_PAGE_HTML` (Task 9), `copyText` from `./clipboard`, `useColors` from `./ui`.
- Produces:
  - `MermaidDiagram({ source, accessibilityActions, onAccessibilityAction }: { source: string; accessibilityActions?: AccessibilityActionInfo[]; onAccessibilityAction?: (event: AccessibilityActionEvent) => void })`
  - `MarkdownResponse` props unchanged.

- [ ] **Step 1: Write the failing tests**

`MermaidDiagram.test.tsx` uses the repo's mock seams (same style as `MarkdownResponse.test.tsx`: `vi.mock("react-native", ...)` via `renderNative.testkit`, plus `vi.mock("react-native-webview", () => ({ WebView: "WebView" }))`):

```tsx
it("locks the WebView down", () => {
  const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
  const webview = tree.root.findByType("WebView" as never);
  expect(webview.props.originWhitelist).toEqual(["about:blank"]);
  expect(webview.props.onShouldStartLoadWithRequest("about:blank")).toBe(true);
  expect(webview.props.onShouldStartLoadWithRequest("https://evil.example")).toBe(false);
});

it("sizes itself from the page's height message and posts the source as JSON", async () => {
  const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
  const webview = tree.root.findByType("WebView" as never);
  // simulate the page ready + our render post, then a height message
  act(() => webview.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "height", value: 213 }) } }));
  // assert the wrapper height became 213
});

it("falls back to a code view on an error message", () => {
  // onMessage {type:"error"} -> shows source text and the error note
});

it("carries accessibility actions on its own accessible wrapper", () => {
  const tree = render(<MermaidDiagram source="x" accessibilityActions={[{ name: "select" }]} onAccessibilityAction={() => {}} />);
  // the wrapper View has accessible={true}, an accessibility label, and the actions
});
```

`MarkdownResponse.test.tsx` additions:

```tsx
it("renders a mermaid fence as a diagram between prose segments", () => {
  const tree = render(<MarkdownResponse markdown={"before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter"} />);
  const prose = tree.root.findAllByType("EnrichedMarkdownText" as never);
  expect(prose.map((node) => node.props.markdown)).toEqual(["before\n\n", "\nafter\n"]);
  expect(tree.root.findAllByType("MermaidDiagram" as never)).toHaveLength(1);
});

it("keeps the whole message in every segment's Copy response item", () => {
  const markdown = "before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter";
  const tree = render(<MarkdownResponse markdown={markdown} />);
  for (const prose of tree.root.findAllByType("EnrichedMarkdownText" as never)) {
    const copy = prose.props.contextMenuItems.find((item: { text: string }) => item.text === "Copy response");
    copy.onPress(); // copies the full original markdown, not the segment
  }
  // assert Clipboard.setStringAsync received `markdown` (the test file already mocks expo-clipboard)
});

it("hands the caller's accessibility actions to the diagram for a diagram-only message", () => {
  const actions = [{ name: "select", label: "Select text" }];
  const tree = render(<MarkdownResponse markdown={"```mermaid\ngraph TD; A-->B\n```"} accessibilityActions={actions} onAccessibilityAction={() => {}} />);
  expect(tree.root.findByType("MermaidDiagram" as never).props.accessibilityActions).toBe(actions);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd mobile-native && npx vitest run src/MermaidDiagram.test.tsx src/MarkdownResponse.test.tsx`
Expected: FAIL (component does not exist; MarkdownResponse still renders one block).

- [ ] **Step 3: Implement**

`MermaidDiagram.tsx`:

```tsx
import { memo, useEffect, useRef, useState } from "react";
import { View, type AccessibilityActionEvent, type AccessibilityActionInfo } from "react-native";
import { WebView, type WebViewMessageEvent } from "react-native-webview";
import { MERMAID_PAGE_HTML } from "./generated/mermaidPage";
import { useColors } from "./ui";

// Last posted height per source, so a FlatList remount shows the right-sized
// placeholder while the WebView re-initializes (spec: Known trade-offs).
const heightCache = new Map<string, number>();

type PageMessage = { type: "ready" } | { type: "height"; value: number } | { type: "error"; message: string };

function parsePageMessage(raw: string): PageMessage | null {
  try {
    const message: unknown = JSON.parse(raw);
    if (typeof message !== "object" || message === null || !("type" in message)) return null;
    if (message.type === "ready") return { type: "ready" };
    if (message.type === "height" && "value" in message && typeof message.value === "number") {
      return { type: "height", value: message.value };
    }
    if (message.type === "error" && "message" in message && typeof message.message === "string") {
      return { type: "error", message: message.message };
    }
    return null;
  } catch {
    return null;
  }
}

export const MermaidDiagram = memo(function MermaidDiagram({
  source,
  accessibilityActions,
  onAccessibilityAction,
}: {
  source: string;
  accessibilityActions?: AccessibilityActionInfo[];
  onAccessibilityAction?: (event: AccessibilityActionEvent) => void;
}) {
  const colors = useColors();
  const webView = useRef<WebView>(null);
  const [height, setHeight] = useState<number | null>(heightCache.get(source) ?? null);
  const [failed, setFailed] = useState(false);

  // The page's bootstrap answers a render message; the source and theme cross
  // as JSON, never string-interpolated into JS (spec: Security).
  function postRender() {
    webView.current?.postMessage(
      JSON.stringify({ type: "render", source, theme: mermaidTheme(colors), mode: "fit" }),
    );
  }

  function handleMessage(event: WebViewMessageEvent) {
    const message = parsePageMessage(event.nativeEvent.data);
    if (message === null) return;
    if (message.type === "ready") postRender();
    if (message.type === "height") {
      heightCache.set(source, message.value);
      setHeight(message.value);
    }
    if (message.type === "error") setFailed(true);
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: keyed on the palette, same discipline as MarkdownResponse
  useEffect(postRender, [colors.palette, source]);

  if (failed) {
    return (
      <View accessible={true} accessibilityLabel="Diagram source">
        {/* code-styled source + "Couldn't render this diagram." note */}
      </View>
    );
  }
  return (
    <View
      accessible={true}
      accessibilityLabel="Diagram"
      accessibilityActions={accessibilityActions}
      onAccessibilityAction={onAccessibilityAction}
      style={{ height: height ?? 120 }}
    >
      <WebView
        ref={webView}
        originWhitelist={["about:blank"]}
        source={{ html: MERMAID_PAGE_HTML }}
        onShouldStartLoadWithRequest={(request) => request.url === "about:blank"}
        scrollEnabled={false}
        onMessage={handleMessage}
        onLoadEnd={postRender}
      />
    </View>
  );
});
```

(`mermaidTheme(colors)` maps the native palette onto the same mermaid variable names the web uses — `primaryColor`, `primaryBorderColor`, `primaryTextColor`, `lineColor`, `fontFamily` — from the `useColors()` palette; check `src/ui.ts` for the palette's exact keys.)

`MarkdownResponse.tsx`: replace the single `EnrichedMarkdownText` with a segment map (keep every existing prop on each prose segment — style, flavor, handlers — and pass the FULL original `markdown` to each segment's "Copy response" `onPress`; per the spec, each prose segment also carries the caller's `accessibilityActions`/`onAccessibilityAction`, and mermaid segments render `MermaidDiagram` with the same). Memoize `splitNativeSegments(markdown)` on `markdown`.

- [ ] **Step 4: Run to verify pass**

Run: `cd mobile-native && npx vitest run src/MermaidDiagram.test.tsx src/MarkdownResponse.test.tsx src/markdownSegments.test.ts`
Expected: PASS, existing MarkdownResponse tests unchanged and passing.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/MermaidDiagram.tsx mobile-native/src/MermaidDiagram.test.tsx mobile-native/src/MarkdownResponse.tsx mobile-native/src/MarkdownResponse.test.tsx
git commit -m "feat(native): render mermaid segments in a locked-down WebView diagram"
```

---

### Task 11: Native fullscreen viewer

**Files:**
- Modify: `mobile-native/src/MermaidDiagram.tsx`
- Test: `mobile-native/src/MermaidDiagram.test.tsx` (additions)

**Interfaces:**
- Consumes: RN `Modal`, `SafeAreaView` (pattern: `TimelineItem.tsx:348-362`), `copyText` from `./clipboard`, the page's `"zoom"` mode (Task 9 protocol).
- Produces: tap on the inline diagram opens the fullscreen viewer; no new exports.

- [ ] **Step 1: Write the failing tests**

```tsx
it("opens a fullscreen viewer on press, with source toggle and copy", async () => {
  const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
  // press the diagram -> a Modal appears hosting a second WebView posted mode:"zoom"
  // press "Show source" -> the source text renders
  // press "Copy source" -> copyText equivalent ran (expo-clipboard mock)
  // press Done -> Modal closes
});
```

- [ ] **Step 2: Run to verify failure** — no modal exists.

- [ ] **Step 3: Implement**

Wrap the inline WebView in a `Pressable` (it never takes the transcript's long-press anyway — the WebView owns its region, the spec's documented dead zone; the Pressable gives the tap-to-open). On press, set state `open`; render a `Modal` (`presentationStyle="fullScreen"`, `animationType="slide"`) with a `SafeAreaView`, a header row (`Done` on the right, `Show source`/`Show diagram` toggle and `Copy source` via `copyText(source)`), and either a second `WebView` with the same page and lockdown props posted `mode: "zoom"` (height unconstrained, `scrollEnabled`) or a `ScrollView` with the source `Text` when toggled.

- [ ] **Step 4: Run to verify pass**

Run: `cd mobile-native && npx vitest run src/MermaidDiagram.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/MermaidDiagram.tsx mobile-native/src/MermaidDiagram.test.tsx
git commit -m "feat(native): fullscreen diagram viewer with zoom, source toggle, copy"
```

---

### Task 12: Gates and fixtures regeneration note

- [ ] **Step 1:** `cd cmd/evener-hub/frontend && npx biome check --write src/widgets/mermaid src/widgets/markdown src/panes/session/transcript src/dev/mermaidguard-entry.tsx src/dev/gallery-sections/markdown.tsx` and the same paths' native counterparts under `mobile-native` with its own biome.
- [ ] **Step 2:** `make test-web` from the repo root (repairs the frontend install via preflight; do not `npm ci` a symlinked `node_modules`).
- [ ] **Step 3:** `make test-native`.
- [ ] **Step 4:** `make test-web-browser` (runs the new mermaidguard via the gate).
- [ ] **Step 5:** `make lint`.
- [ ] **Step 6:** Commit any gate-driven fixes.

```bash
git add -p  # review and stage only gate-driven fixes
git commit -m "chore(mermaid): gate fixes"
```

## Self-Review Notes (filled by the plan author)

- Spec coverage: CRLF fix (T1), segment split web (T2) and native (T8), security config + fixtures (T3), web component (T4), integration + walk exclusions (T5), browser guard + gallery (T6), web viewer (T7), native page (T9), native component + integration (T10), native viewer (T11), gates (T12). Spec's Testing section maps to T1/T2/T3/T4/T5/T6/T8/T10. Slices 0–4 all covered.
- Known soft spots an executor should watch: marked's `Token` typing for code tokens (`lang`/`text`/`raw` access may need narrowing); the exact token names in `mermaidThemeVariables`; whether `check:scripts` accepts the freshness test's import of a `.mts` script; biome key rules in the segment map.
