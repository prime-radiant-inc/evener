import "fake-indexeddb/auto";
import { hydrateThread, makeTranscriptDisplayConfig, threadModelFromCache } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { fireEvent, render } from "@testing-library/react";
import { Component, type ReactNode } from "react";
import { expect, test, vi } from "vitest";
import "../panes/session";
import { documentPaneState, resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { TranscriptRenderProvider } from "../transcriptDisplay/renderContext";
import { ComicMarkdown, comicMarkdownPoint } from "./ComicMarkdown";
import { publishComicPaneCoverage } from "./comicPaneCoverage";
import { composeComicRows } from "./layout";
import { projectComicMarkdown } from "./markdown";

test("cached child Markdown acquires source-owned file links when live cwd arrives", () => {
  const ref = "local:child";
  const cached = threadModelFromCache(
    {
      ref,
      threadId: "child",
      name: "child",
      modelProvider: "test",
      model: "test",
      savedAt: 1,
      history: {
        bootGeneration: "boot",
        epoch: 1,
        incarnation: "inc",
        length: 0,
        appliedGeneration: 1,
        issuedGeneration: 1,
        turns: [],
      },
    },
    1,
  );
  expect(cached.cwd).toBe("");
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  const source = "[COMIC_DOCUMENT_CHILD](comic-document-child.md)";
  const view = (thread: typeof cached) => (
    <TranscriptRenderProvider config={config} thread={thread} sourcePaneId="root-pane">
      {formatted(source)}
    </TranscriptRenderProvider>
  );
  const rendered = render(view(cached));
  const anchor = rendered.getByRole("link", { name: "COMIC_DOCUMENT_CHILD" });
  if (!(anchor instanceof HTMLAnchorElement)) throw new Error("expected document anchor");
  expect(anchor.getAttribute("href")).toBe("comic-document-child.md");
  expect(new URL(anchor.href).pathname).not.toBe("/doc/file");

  rendered.rerender(view({ ...cached, cwd: "/actual/child" }));
  expect(rendered.getByRole("link", { name: "COMIC_DOCUMENT_CHILD" })).toBe(anchor);
  const url = new URL(anchor.href);
  expect(url.pathname).toBe("/doc/file");
  expect(url.searchParams.get("session")).toBe(ref);
  expect(url.searchParams.get("path")).toBe("/actual/child/comic-document-child.md");
  expect(anchor.textContent).toBe("COMIC_DOCUMENT_CHILD");
  rendered.unmount();
});

test.each(["popout", "redocked"])("%s Comic Markdown retains child file authority across realms", (phase) => {
  resetWorkspaceStoreForTests();
  const rootRef = "local:034MXwo6BpPH0QQCgdICSf";
  const childRef = "remote:034MXwo6BpPH0QQCgdICSg";
  const child = {
    ...hydrateThread({ thread: wireThread(childRef) }, childRef, 0),
    cwd: "/workspace/child",
  };
  const ownerId = workspaceStore.getState().openPane("session", { ref: rootRef });
  const owner = workspaceStore.getState().panes.find((pane) => pane.id === ownerId);
  if (!owner) throw new Error("missing containing pane");
  // Supply the mounted reader's coverage, not a mocked opener or enhancer.
  const retire = publishComicPaneCoverage(owner, rootRef, [childRef]);
  const decoy = workspaceStore.getState().openPane("session", { ref: childRef });
  const source = (prefix: string) =>
    `${prefix}/bare.md and \`${prefix}/code.md\` and [AUTHORED](${prefix}/authored.md)`;
  const view = (text: string, cwd = child.cwd) => (
    <TranscriptRenderProvider thread={{ ...child, cwd }} sourcePaneId={ownerId}>
      {formatted(text)}
    </TranscriptRenderProvider>
  );
  const rendered = render(view(source("before")));
  const frame = document.createElement("iframe");
  document.body.append(frame);
  const foreign = frame.contentDocument;
  const checkLinks = (prefix: string, cwd: string) => {
    // The authored anchor is a control: it already works in the old helper.
    for (const [label, path] of [
      ["AUTHORED", `${prefix}/authored.md`],
      [`${prefix}/bare.md`, `${prefix}/bare.md`],
      [`${prefix}/code.md`, `${prefix}/code.md`],
    ] as const) {
      const anchor = [...rendered.container.querySelectorAll("a")].find((link) => link.textContent === label);
      expect(anchor, `${phase}: missing ${label}`).toBeDefined();
      if (!anchor) throw new Error(`missing ${label}`);
      const url = new URL(anchor.href);
      expect(url.pathname).toBe("/doc/file");
      expect(url.searchParams.get("session")).toBe(childRef);
      expect(url.searchParams.get("path")).toBe(`${cwd}/${path}`);
      expect(url.searchParams.get("format")).toBe("raw");
      const firstGlyph = label[0];
      if (firstGlyph === undefined) throw new Error("empty link label");
      glyph(rendered.container, source(prefix).indexOf(label), anchor, 0, firstGlyph);
      workspaceStore.getState().focusPane(decoy);
      expect(fireEvent.click(anchor)).toBe(false);
      const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
      expect(doc?.params).toEqual({ session: childRef, path, kind: "file" });
      expect(doc && documentPaneState(doc)?.reference).toMatchObject({ cwd, readTarget: `${cwd}/${path}` });
      expect(doc && documentPaneState(doc)?.origin).toBe(owner);
      expect(workspaceStore.getState().focusedPaneId).toBe(doc?.id);
      if (doc) workspaceStore.getState().closePane(doc.id);
    }
    expect(rendered.container.querySelectorAll("a")).toHaveLength(3);
  };
  try {
    if (!foreign) throw new Error("missing popout document");
    checkLinks("before", child.cwd);
    const host = rendered.container.querySelector("[data-comic-markdown]");
    foreign.body.append(foreign.adoptNode(rendered.container));
    // A real renderer update creates new formatted descendants in the popout.
    rendered.rerender(view(source("after")));
    expect(rendered.container.querySelector("[data-comic-markdown]")).toBe(host);
    const paragraph = rendered.container.querySelector("p");
    const code = rendered.container.querySelector("code");
    const authored = [...rendered.container.querySelectorAll("a")].find((a) => a.textContent === "AUTHORED");
    if (!paragraph || !code || !authored) throw new Error("missing formatted popout content");
    const authoredText = authored.firstChild;
    expect(paragraph.ownerDocument).toBe(foreign);
    expect(paragraph instanceof HTMLElement).toBe(false);
    expect(authoredText instanceof Text).toBe(false);
    let cwd = child.cwd;
    if (phase === "redocked") {
      document.body.append(document.adoptNode(rendered.container));
      cwd = "/relocated/child";
      // Only context changes: the foreign descendants must survive and rebind.
      rendered.rerender(view(source("after"), cwd));
      expect(rendered.container.querySelector("p")).toBe(paragraph);
      expect(rendered.container.querySelector("code")).toBe(code);
      expect(authored.firstChild).toBe(authoredText);
      expect(paragraph.ownerDocument).toBe(document);
      expect(paragraph instanceof HTMLElement).toBe(false);
      expect(authoredText instanceof Text).toBe(false);
      expect(host instanceof HTMLElement).toBe(true);
    }
    checkLinks("after", cwd);
  } finally {
    rendered.unmount();
    rendered.container.remove();
    frame.remove();
    retire();
    resetWorkspaceStoreForTests();
  }
});

function formatted(source: string, live = false) {
  const document = projectComicMarkdown(source, live);
  return document.parts.map((part) => <ComicMarkdown key={part.start} document={document} part={part} />);
}

class RenderErrors extends Component<{ errors: unknown[]; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(error: unknown) {
    this.props.errors.push(error);
  }
  render() {
    return this.state.failed ? null : this.props.children;
  }
}

function composed(source: string, live: boolean, errors: unknown[], split = false) {
  const rows = composeComicRows(
    [
      {
        id: "fixture",
        sourceItemId: "fixture",
        sourceIndex: 0,
        kind: "dialogue",
        speaker: "human",
        sourceLabel: "fixture",
        text: source,
        streaming: live,
      },
    ],
    760,
    () => !split,
  );
  expect(rows.flatMap((row) => row.panels.map(({ slice }) => source.slice(slice.start, slice.end))).join("")).toBe(
    source,
  );
  return (
    <RenderErrors errors={errors}>
      {rows.flatMap((row) =>
        row.panels.map(({ document, part }) => <ComicMarkdown key={part.start} document={document} part={part} />),
      )}
    </RenderErrors>
  );
}

function glyph(container: HTMLElement, offset: number, element: Element, local: number, value: string) {
  const root = element.closest<HTMLElement>("[data-comic-markdown]");
  if (!root) throw new Error("missing glyph root");
  const probe = ranges(container.ownerDocument);
  try {
    comicMarkdownPoint(root, offset);
    expect(probe.last?.startContainer).toBe(element.firstChild);
    expect(probe.last?.startOffset).toBe(local);
    expect(probe.last?.toString()).toBe(value);
    expect(probe.last?.startContainer.isConnected).toBe(true);
  } finally {
    probe.spy.mockRestore();
  }
}

test.each([
  "\t",
  "\v",
  "\f",
  "\u00a0",
  "\u1680",
  "\u2000",
  "\u2001",
  "\u2002",
  "\u2003",
  "\u2004",
  "\u2005",
  "\u2006",
  "\u2007",
  "\u2008",
  "\u2009",
  "\u200a",
  "\u202f",
  "\u205f",
  "\u3000",
  "\ufeff",
])("heading whitespace binds original glyphs: %j", (space) => {
  for (const [source, offset, selector] of [
    [`# ${space}Title${space}`, 3, "h1"],
    [`#${space}Title${space}`, 2, "h1"],
  ] as const) {
    const errors: unknown[] = [];
    const rendered = render(composed(source, false, errors), { onCaughtError: () => {} });
    expect(errors).toEqual([]);
    const heading = rendered.container.querySelector(selector);
    expect(heading?.textContent).toBe("Title");
    if (!heading) throw new Error("missing heading");
    glyph(rendered.container, offset, heading, 0, "T");
    glyph(rendered.container, offset + 4, heading, 4, "e");
    rendered.unmount();
  }
});

test.each([
  { source: "# \u00a0Title", selector: "h1", offsets: [3, 4, 5, 6, 7] },
  { source: "  ### \u00a0\u2003**😀é**\u00a0 ###\u2003", selector: "h3 strong", offsets: [10, 11, 12] },
  { source: "\u00a0**😀é**\u2003\n===", selector: "h1 strong", offsets: [3, 4, 5] },
  { source: "  **😀é**\u00a0\n---", selector: "h2 strong", offsets: [4, 5, 6] },
  { source: "# a\u00a0###", selector: "h1", offsets: [2, 3, 4, 5, 6] },
  { source: "# a \t###", selector: "h1", offsets: [2, 3, 4, 5, 6, 7] },
  { source: "# a\u00a0 ###\u2003", selector: "h1", offsets: [2] },
  { source: "# **😀é**\r\n", selector: "h1 strong", offsets: [4, 5, 6] },
] as const)("heading formatting keeps UTF16 endpoints through settlement: $source", ({ source, selector, offsets }) => {
  const errors: unknown[] = [];
  const rendered = render(composed(source, false, errors), { onCaughtError: () => {} });
  const check = () => {
    expect(errors).toEqual([]);
    const element = rendered.container.querySelector(selector);
    if (!element) throw new Error("missing heading content");
    expect(element.textContent).toBe(offsets.map((offset) => source[offset]).join(""));
    if (selector !== "h1") {
      glyph(rendered.container, offsets[0], element, 0, "😀");
      glyph(rendered.container, offsets[1], element, 0, "😀");
      glyph(rendered.container, offsets[2], element, 2, "é");
    } else {
      for (const [local, offset] of offsets.entries())
        glyph(rendered.container, offset, element, local, source.charAt(offset));
    }
  };
  check();
  for (let end = 1; end <= source.length; end++) {
    rendered.rerender(composed(source.slice(0, end), true, errors));
    expect(errors).toEqual([]);
  }
  rendered.rerender(composed(source, false, errors));
  check();
});

test.each([
  "[r]: /first\n\n[r]",
  "[r]: /first\n\n[r]: /second\n\n[r]",
  "[r]: /first\n[r]: /second\n[r]",
  "> [r]: /first\n>\n> [r]: /second\n>\n> [r]",
  "- [r]: /first\n\n  [r]: /second\n\n  [r]",
  "[r]: /first\n\n> [r]: /second\n>\n> [r]",
  "[r]: /first\n\n- [r]: /second\n\n  [r]",
  "> [r]: /first\n>\n> [r]: /second\n>\n> ```txt\n> code\n> ```\n>\n> [r]",
  "- [r]: /first\n\n  [r]: /second\n\n  ```txt\n  code\n  ```\n\n  [r]",
])("reference spans mount through rows and streaming: %s", (source) => {
  const errors: unknown[] = [];
  const rendered = render(composed(source, false, errors), { onCaughtError: () => {} });
  const check = () => {
    expect(errors).toEqual([]);
    expect(
      projectComicMarkdown(source, false)
        .parts.flatMap((part) => part.visible)
        .filter((run) => run.text === "r")
        .flatMap((run) => run.offsets),
    ).toEqual([source.lastIndexOf("[r]") + 1]);
    const link = rendered.container.querySelector("a");
    expect(link?.getAttribute("href")).toBe("/first");
    if (!link) throw new Error("missing link");
    glyph(rendered.container, source.lastIndexOf("[r]") + 1, link, 0, "r");
  };
  check();
  for (let end = 1; end <= source.length; end++) {
    rendered.rerender(composed(source.slice(0, end), true, errors));
    expect(errors).toEqual([]);
    expect(rendered.container.querySelector("[data-comic-markdown]")).not.toBeNull();
  }
  rendered.rerender(composed(source, false, errors));
  check();
});

test.each(
  ["", "| "].flatMap((prefix) =>
    ["", "  "].flatMap((spaces) =>
      [1, 2, 3, 4].flatMap((slashes) => [false, true].map((outer) => ({ prefix, spaces, slashes, outer }))),
    ),
  ),
)(
  "terminal table pipe binds parity $slashes, outer $outer, prefix '$prefix', spaces '$spaces'",
  ({ prefix, spaces, slashes, outer }) => {
    const source = `${prefix}a | b${prefix ? " |" : ""}\n${prefix}- | -${prefix ? " |" : ""}\n${prefix}x | y${"\\".repeat(slashes)}|${outer ? " |" : ""}${spaces}`;
    const errors: unknown[] = [];
    const rendered = render(composed(source, false, errors), { onCaughtError: () => {} });
    const check = () => {
      expect(errors).toEqual([]);
      const cell = rendered.container.querySelector("td:last-child");
      expect(cell?.textContent).toBe(`y${"\\".repeat(Math.floor(slashes / 2))}${slashes % 2 ? "|" : ""}`);
      if (!cell) throw new Error("missing cell");
      glyph(rendered.container, source.indexOf("y"), cell, 0, "y");
      if (slashes % 2)
        glyph(rendered.container, source.indexOf("y") + slashes + 1, cell, 1 + Math.floor(slashes / 2), "|");
    };
    check();
    for (let end = source.indexOf("x"); end <= source.length; end++) {
      rendered.rerender(composed(source.slice(0, end), true, errors));
      expect(errors).toEqual([]);
      expect(rendered.container.querySelector("table")).not.toBeNull();
    }
    rendered.rerender(composed(source, false, errors));
    check();
  },
);

test.each([
  "<div>\nword\n</div>",
  "<div>\n  word\t \n</div>",
  "<div>\n\n**word**\n\n</div>",
  "a\n\n<!--x-->\n\nb",
  "> a\n>\n> <!--x-->\n>\n> b",
  "> <div>\n>\n> **word**\n>\n> </div>",
  "a\n\n<!-- x\n  y -->\n\nb",
  "a\n\n<!--x-->\n\n```txt\n  code\n```\n\n<div>\n\n**word**\n\n</div>\n\na | b\n- | -\nx | y\n\n<!--end-->",
])("escaped HTML separators mount with exact authored glyphs: %s", (source) => {
  for (const split of [false, true]) {
    const errors: unknown[] = [];
    const rendered = render(composed(source, false, errors, split), { onCaughtError: () => {} });
    expect(errors).toEqual([]);
    expect(
      rendered.container.querySelectorAll("script, [onerror], [data-comic-markdown] div:not([class])"),
    ).toHaveLength(0);
    const probe = ranges(rendered.container.ownerDocument);
    try {
      // Independently enumerate every literal HTML character, including authored whitespace.
      for (const match of source.matchAll(/<!--[\s\S]*?-->|<\/?div>/g)) {
        const root = [...rendered.container.querySelectorAll<HTMLElement>("[data-comic-markdown]")].find((root) =>
          root.textContent.includes(match[0]),
        );
        expect(root).toBeTruthy();
        if (!root) throw new Error("missing literal HTML");
        const walker = root.ownerDocument.createTreeWalker(root, 4);
        let literal: Text | undefined;
        for (let node = walker.nextNode(); node; node = walker.nextNode()) {
          if ((node as Text).data.includes(match[0])) {
            literal = node as Text;
            break;
          }
        }
        expect(literal).toBeTruthy();
        const local = literal?.data.indexOf(match[0]) ?? -1;
        for (let i = 0; i < match[0].length; i++) {
          comicMarkdownPoint(root, match.index + i);
          expect(probe.last?.startContainer).toBe(literal);
          expect(probe.last?.startOffset).toBe(local + i);
          expect(probe.last?.toString()).toBe(match[0][i]);
          expect(probe.last?.startContainer.isConnected).toBe(true);
        }
      }
    } finally {
      probe.spy.mockRestore();
    }
    const strong = rendered.container.querySelector("strong");
    if (source.includes("**word**")) {
      expect(strong).not.toBeNull();
      if (strong) glyph(rendered.container, source.indexOf("word"), strong, 0, "w");
    }
    if (source.endsWith("b")) {
      const last = [...rendered.container.querySelectorAll("p")].at(-1);
      if (!last) throw new Error("missing following paragraph");
      glyph(rendered.container, source.length - 1, last, 0, "b");
    }
    rendered.unmount();
  }
});

// jsdom has no glyph geometry. Record the REAL Range endpoints at the platform boundary.
function ranges(doc: Document) {
  const create = doc.createRange.bind(doc);
  let last: Range | undefined;
  const spy = vi.spyOn(doc, "createRange").mockImplementation(() => {
    const range = create();
    range.getBoundingClientRect = () => {
      last = range;
      return new DOMRect(0, 0, 1, 1);
    };
    return range;
  });
  return {
    spy,
    get last() {
      return last;
    },
  };
}

test.each([
  { source: "- [x] ax", points: [["li", "ax", 1]] },
  { source: "- [ ] ax", points: [["li", "ax", 1]] },
  { source: "- ax", points: [["li", "ax", 0]] },
  {
    source: "- [x] ax\n\n- [ ] by",
    points: [
      ["li:first-child p", "ax", 1],
      ["li:last-child p", "by", 1],
    ],
  },
  {
    source: "> - [x] ax\n>   - [ ] by",
    points: [
      ["blockquote > ul > li", "ax", 1],
      ["li li", "by", 1],
    ],
  },
  {
    source: "- [ ] ax\n\n  > - [x] by",
    points: [
      ["ul > li > p", "ax", 0],
      ["blockquote li", "by", 1],
    ],
  },
  { source: "- [x]\t**ax**", points: [["li strong", "ax", 0]] },
  { source: "- [ ] `a  b`", points: [["li code", "a  b", 0]] },
  {
    source: "- [x] ax\n\n  ```txt\n  by\n  ```\n\n  cz",
    points: [
      ["li", "ax", 0],
      ["code span span", "by", 0],
      ["[data-comic-markdown]:last-child li", "cz", 0],
    ],
  },
] as const)("task-list glyphs bind and stay connected for $source", ({ source, points }) => {
  const document = projectComicMarkdown(source, false);
  expect(document.parts.map((part) => source.slice(part.start, part.end)).join("")).toBe(source);
  const view = () => document.parts.map((part) => <ComicMarkdown key={part.start} document={document} part={part} />);
  const rendered = render(view());
  const probe = ranges(rendered.container.ownerDocument);
  const check = () => {
    expect(rendered.container.querySelectorAll("input")).toHaveLength(0);
    for (const [selector, authored, local] of points) {
      const element = rendered.container.querySelector(selector);
      const node = element?.firstChild;
      const root = element?.closest<HTMLElement>("[data-comic-markdown]");
      expect(root).toBeTruthy();
      if (!root) throw new Error("missing root");
      // Expected positions come from the authored fixture, never the projector's map.
      const start = source.indexOf(authored);
      expect(start).toBeGreaterThanOrEqual(0);
      for (let i = 0; i < authored.length; i++) {
        comicMarkdownPoint(root, start + i);
        expect(probe.last?.startContainer).toBe(node);
        expect(probe.last?.startOffset).toBe(local + i);
        expect(probe.last?.toString()).toBe(source[start + i]);
        expect(node?.isConnected).toBe(true);
        expect(root.contains(node ?? null)).toBe(true);
      }
    }
  };
  try {
    check();
    rendered.rerender(view());
    check();
  } finally {
    probe.spy.mockRestore();
  }
});

test("task-list streaming prefixes retain original glyphs through settlement", () => {
  const source = "- [x] ax\n- [ ] by";
  const rendered = render(formatted("", true));
  const probe = ranges(rendered.container.ownerDocument);
  const check = (end: number) => {
    for (const [item, authored] of ["ax", "by"].entries()) {
      const start = source.indexOf(authored);
      for (let offset = start; offset < Math.min(start + authored.length, end); offset++) {
        const node = rendered.container.querySelectorAll("li")[item]?.firstChild;
        comicMarkdownPoint(rendered.container, offset);
        expect(probe.last?.startContainer).toBe(node);
        expect(probe.last?.startOffset).toBe(1 + offset - start);
        expect(probe.last?.toString()).toBe(source[offset]);
        expect(node?.isConnected).toBe(true);
      }
    }
  };
  try {
    for (let end = 1; end <= source.length; end++) {
      rendered.rerender(formatted(source.slice(0, end), true));
      check(end);
    }
    rendered.rerender(formatted(source, false));
    check(source.length);
  } finally {
    probe.spy.mockRestore();
  }
});

test.each([
  {
    source: "  abc\n  ===",
    selector: "h1",
    text: "abc",
    points: [
      [2, 0],
      [3, 1],
      [4, 2],
    ],
  },
  {
    source: "[x\\]](/url) after",
    selector: "a",
    text: "x]",
    points: [
      [1, 0],
      [3, 1],
    ],
  },
  {
    source: "- a\n\t\tb",
    selector: "li",
    text: "a\n  b",
    points: [
      [2, 0],
      [6, 4],
    ],
  },
  {
    source: "&bogus;",
    selector: "p",
    text: "&bogus;",
    points: [
      [0, 0],
      [1, 1],
      [5, 5],
      [6, 6],
    ],
  },
  {
    source: "- a\nb\tc",
    selector: "li",
    text: "a\nb\tc",
    points: [
      [2, 0],
      [4, 2],
      [5, 3],
      [6, 4],
    ],
  },
])("normalized source $source retains actual DOM glyph endpoints", ({ source, selector, text, points }) => {
  const { container } = render(formatted(source));
  const node = container.querySelector(selector)?.firstChild;
  expect(node?.textContent).toBe(text);
  const probe = ranges(container.ownerDocument);
  try {
    for (const [sourceOffset, domOffset] of points) {
      comicMarkdownPoint(container, sourceOffset as number);
      expect(probe.last?.startContainer).toBe(node);
      expect(probe.last?.startOffset).toBe(domOffset);
      expect(node?.textContent?.[domOffset as number]).toBe(source[sourceOffset as number]);
    }
  } finally {
    probe.spy.mockRestore();
  }
});

test("source offsets identify exact DOM nodes and positions, including repeated nested labels", () => {
  const source = "# **same [same](https://x.test) same**\r\n\r\n&amp; \\* 😀\tZ";
  const { container } = render(formatted(source));
  const probe = ranges(container.ownerDocument);
  const strong = container.querySelector("strong");
  const link = container.querySelector("a");
  const paragraph = container.querySelector("p");
  const expected = [
    [4, strong?.firstChild, 0],
    [7, strong?.firstChild, 3],
    [10, link?.firstChild, 0],
    [13, link?.firstChild, 3],
    [32, strong?.lastChild, 1],
    [35, strong?.lastChild, 4],
    [42, paragraph?.firstChild, 0],
    [49, paragraph?.firstChild, 2],
    [51, paragraph?.firstChild, 4],
    [54, paragraph?.firstChild, 7],
    // Hidden heading / emphasis delimiters, link destination and entity spelling.
    [0, strong?.firstChild, 0],
    [9, link?.firstChild, 0],
    [21, link?.firstChild, 3],
    [44, paragraph?.firstChild, 0],
  ] as const;
  try {
    for (const [offset, node, local] of expected) {
      expect(node).toBeTruthy();
      comicMarkdownPoint(container, offset);
      expect(probe.last?.startContainer, `source ${offset}`).toBe(node);
      expect(probe.last?.startOffset, `source ${offset}`).toBe(local);
    }
    // Reverse oracle: these DOM text positions correspond to independent original UTF-16 positions.
    for (const [sourceOffset, node, local] of expected.slice(0, 10)) {
      expect(node?.textContent?.codePointAt(local)).toBe(source.codePointAt(sourceOffset));
    }
  } finally {
    probe.spy.mockRestore();
  }
});

test.each(["xbc", "ab", "abcd"])("inconsistent visible map %s cannot bind another glyph", (text) => {
  const document = projectComicMarkdown("**abc**", false);
  const part = document.parts[0];
  if (!part) throw new Error("missing part");
  const inconsistent = { ...part, visible: [{ text, offsets: [2, 3, 4, 5] }] };
  expect(() => render(<ComicMarkdown document={document} part={inconsistent} />)).toThrow(TypeError);
});

test.each(["a\n\n<!-- x\n  y -->\n\nb", "`a  b`", "```txt\n \n```"])(
  "authored whitespace cannot be discarded as a generated separator: %s",
  (source) => {
    const document = projectComicMarkdown(source, false);
    const part = document.parts[0];
    if (!part) throw new Error("missing part");
    const inconsistent = {
      ...part,
      visible: part.visible.map((run) => ({
        text: run.text.replace(/\s/g, ""),
        offsets: run.offsets.filter((_, i) => !/\s/.test(run.text[i] ?? "")),
      })),
    };
    expect(() => render(<ComicMarkdown document={document} part={inconsistent} />)).toThrow(TypeError);
  },
);

test("whole-document references resolve on both sides of a code insert without rendering definitions", () => {
  const source = "[same][r]\n\n```txt\nsame\n```\n\n[same][r]\n\n[r]: https://example.com\n";
  const { container } = render(formatted(source));
  expect([...container.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual([
    "https://example.com",
    "https://example.com",
  ]);
  expect(container.querySelectorAll("p")).toHaveLength(2);
  expect([...container.querySelectorAll("p")].map((p) => p.textContent)).toEqual(["same", "same"]);
});

test("a table nested in a list stays semantic and resolves references defined after the list", () => {
  const source =
    "- before\n\n  | A | B |\n  | - | - |\n  | [one][r] | two |\n\n  after\n\n[r]: https://example.com/table";
  const doc = projectComicMarkdown(source, false);
  expect(doc.parts.map((part) => part.kind)).toEqual(["prose", "table", "prose"]);
  expect(doc.parts.map((part) => source.slice(part.start, part.end)).join("")).toBe(source);
  const { container } = render(formatted(source));
  expect(container.querySelectorAll("ul li table")).toHaveLength(1);
  expect(container.querySelector("td a")?.getAttribute("href")).toBe("https://example.com/table");
  expect(container.querySelectorAll("[data-comic-markdown='prose'] ul li")).toHaveLength(2);
});

test("nested list and quote prose keeps its containers around character-free block boundaries", () => {
  const source = "> - before\n>\n>   ```ts\n>   same\n>   ```\n>\n>   **after**\n";
  const { container } = render(formatted(source));
  expect(container.querySelectorAll("blockquote ul li")).toHaveLength(2);
  expect(container.querySelector("pre code")?.textContent).toBe("same");
  expect(container.querySelector("blockquote strong")?.textContent).toBe("after");
  const codeRoot = container.querySelector<HTMLElement>("[data-comic-markdown='code']");
  if (!codeRoot) throw new Error("missing code");
  const probe = ranges(container.ownerDocument);
  try {
    comicMarkdownPoint(codeRoot, 29);
    expect(probe.last?.startContainer.textContent).toBe("same");
    expect(probe.last?.startOffset).toBe(2);
  } finally {
    probe.spy.mockRestore();
  }
});

test.each([
  { marker: "- ", selector: "ul > li", ordered: false },
  { marker: "7. ", selector: "ol > li", ordered: true },
  { marker: "> ", selector: "blockquote", ordered: false },
])("insert fragments preserve paragraphs in one $selector", ({ marker, selector, ordered }) => {
  const body = "before\n\nsecond\n\n```txt\ncode\n```\n\nafter\n\nlast";
  const source = body
    .split("\n")
    .map((line, index) => `${marker === "> " || index === 0 ? marker : " ".repeat(marker.length)}${line}`)
    .join("\n");
  const doc = projectComicMarkdown(source, false);
  expect(doc.parts.map((part) => source.slice(part.start, part.end)).join("")).toBe(source);
  if (ordered)
    expect(doc.parts.filter((part) => part.kind === "prose").every((part) => part.input.startsWith("7. "))).toBe(true);
  const { container } = render(formatted(source));
  const prose = [...container.querySelectorAll("[data-comic-markdown='prose']")];
  expect(prose).toHaveLength(2);
  for (const fragment of prose) {
    expect(fragment.querySelectorAll(selector)).toHaveLength(1);
    expect(fragment.querySelector(selector)?.querySelectorAll(":scope > p")).toHaveLength(2);
    if (ordered) expect(fragment.querySelector("ol")?.start).toBe(7);
  }
  expect(container.querySelector("pre code")?.textContent).toBe("code");
});

test("nested ordered and unordered item identity survives an insert inside a quote", () => {
  const source =
    "> 3. outer\n>\n>    - before\n>\n>      second\n>\n>      ```txt\n>      code\n>      ```\n>\n>      after\n>\n>      last\n>\n>    outer end";
  const doc = projectComicMarkdown(source, false);
  expect(doc.parts.map((part) => source.slice(part.start, part.end)).join("")).toBe(source);
  expect(doc.parts.filter((part) => part.kind === "prose").every((part) => part.input.startsWith("> 3. "))).toBe(true);
  const { container } = render(formatted(source));
  const prose = [...container.querySelectorAll("[data-comic-markdown='prose']")];
  expect(prose).toHaveLength(2);
  for (const fragment of prose) {
    expect(fragment.querySelectorAll("blockquote, ol, ul")).toHaveLength(3);
    expect(fragment.querySelectorAll("li")).toHaveLength(2);
    expect(fragment.querySelector("ol")?.start).toBe(3);
    expect(fragment.querySelectorAll("ol > li > p")).toHaveLength(1);
    expect(fragment.querySelectorAll("ul > li > p")).toHaveLength(2);
  }
  expect(container.querySelector("pre code")?.textContent).toBe("code");
});

test("safe shared rendering escapes authored HTML and strips unsafe destinations while preserving selectable text", () => {
  const html = "<img src=x onerror=alert(1)>";
  const source = `${html}\n\n[safe](https://example.com) [unsafe](javascript:alert%281%29)`;
  const { container } = render(formatted(source));
  expect(container.querySelectorAll("img, script, [onerror]")).toHaveLength(0);
  expect(container.textContent).toContain(html);
  expect([...container.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual([
    "https://example.com",
    null,
  ]);
  const selection = container.ownerDocument.getSelection();
  const range = container.ownerDocument.createRange();
  range.selectNodeContents(container);
  selection?.removeAllRanges();
  selection?.addRange(range);
  expect(selection?.toString()).toBe(container.textContent);
  selection?.removeAllRanges();
});

test("live fence and table prefixes settle and document edits replace prior markup", () => {
  const rendered = render(formatted("```ts\n  x", true));
  expect(rendered.container.querySelector("code")?.textContent).toBe("  x");
  rendered.rerender(formatted("```ts\n  x\n```\n\n| A | B |\n| - |", true));
  expect(rendered.container.querySelectorAll("code")).toHaveLength(1);
  rendered.rerender(formatted("```ts\n  x\n```\n\n| A | B |\n| - | - |\n| 1 | 2 |", false));
  expect(rendered.container.querySelectorAll("table")).toHaveLength(1);
  expect(rendered.container.querySelectorAll("code")).toHaveLength(1);
  rendered.rerender(formatted("# Replaced\n\n**new**", false));
  expect(rendered.container.querySelectorAll("table, code")).toHaveLength(0);
  expect(rendered.container.querySelectorAll("h1, strong")).toHaveLength(2);
});

test("an unchanged formatted rerender maps to connected glyphs rather than replaced widget text", () => {
  const source = "literal fixture *text*";
  const document = projectComicMarkdown(source, false);
  const part = document.parts[0];
  if (!part) throw new Error("missing part");
  const view = () => <ComicMarkdown document={document} part={part} />;
  const rendered = render(view());
  const probe = ranges(rendered.container.ownerDocument);
  try {
    comicMarkdownPoint(rendered.container, 7);
    expect(rendered.container.contains(probe.last?.startContainer ?? null)).toBe(true);
    rendered.rerender(view());
    comicMarkdownPoint(rendered.container, 7);
    expect(rendered.container.contains(probe.last?.startContainer ?? null)).toBe(true);
  } finally {
    probe.spy.mockRestore();
  }
});

test("table and reference-definition offsets resolve in the element's owning document", () => {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  const foreign = frame.contentDocument;
  if (!foreign) throw new Error("missing frame document");
  const host = foreign.createElement("div");
  foreign.body.append(host);
  const source = "| A | B |\r\n| - | - |\r\n| &amp; | 😀 |\r\n";
  const rendered = render(formatted(source), { container: host });
  const probe = ranges(foreign);
  const globalRange = vi.spyOn(document, "createRange");
  try {
    const cells = host.querySelectorAll("th, td");
    for (const [offset, cell, local] of [
      [2, 0, 0],
      [6, 1, 0],
      [24, 2, 0],
      [32, 3, 0],
    ] as const) {
      comicMarkdownPoint(host, offset);
      expect(probe.last?.startContainer).toBe(cells[cell]?.firstChild);
      expect(probe.last?.startOffset).toBe(local);
      expect(probe.last?.startContainer.ownerDocument).toBe(foreign);
    }
    expect(globalRange).not.toHaveBeenCalled();
    rendered.rerender(formatted("[same][r]\n\n[r]: /destination"));
    comicMarkdownPoint(host, 18);
    expect(probe.last?.startContainer).toBe(host.querySelector("a")?.firstChild);
    expect(probe.last?.startOffset).toBe(3);
  } finally {
    globalRange.mockRestore();
    probe.spy.mockRestore();
    rendered.unmount();
    frame.remove();
  }
});
