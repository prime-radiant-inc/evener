import { type ReactNode, useLayoutEffect, useMemo, useRef } from "react";
import { enhanceFileReferences } from "../panes/session/transcript/messages/fileReferenceDOM";
import { useTranscriptRenderContext } from "../transcriptDisplay/renderContext";
import { CodeBlock, Markdown } from "../widgets";
import styles from "./comic.module.css";
import type { ComicMarkdownDocument, ComicMarkdownPart } from "./markdown";

type Glyph = { node: Text; index: number; source: number };
const glyphs = new WeakMap<HTMLElement, Glyph[]>();

// Marked emits one newline after these block/cell elements and after the
// opening tags below. Escaped HTML can share that newline's Text node.
const separatorAfter = new Set([
  "P",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "HR",
  "BLOCKQUOTE",
  "UL",
  "OL",
  "LI",
  "TABLE",
  "THEAD",
  "TR",
  "TH",
  "TD",
]);
const separatorInside = new Set(["BLOCKQUOTE", "UL", "OL", "TABLE", "THEAD", "TR"]);

/** Bind authored glyphs strictly, accounting for each structural separator first. */
function bind(root: HTMLElement, part: ComicMarkdownPart): void {
  const expected = part.visible.flatMap((run) =>
    Array.from({ length: run.text.length }, (_, i) => ({
      char: run.text[i],
      source: run.offsets[i] ?? part.start,
    })),
  );
  const content = part.kind === "code" ? root.querySelector("code") : root;
  if (!content) return;
  const walker = root.ownerDocument.createTreeWalker(content, 4);
  const result: Glyph[] = [];
  let cursor = 0;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node as Text;
    const generated =
      !part.plain &&
      part.kind !== "code" &&
      (separatorAfter.has(text.previousSibling?.nodeName ?? "") ||
        (!text.previousSibling && separatorInside.has(text.parentElement?.tagName ?? "")));
    if (generated && text.data[0] !== "\n") throw new TypeError("Comic Markdown block separator is missing");
    for (let index = generated ? 1 : 0; index < text.length; index++) {
      const next = expected[cursor];
      if (!next || text.data[index] !== next.char) {
        throw new TypeError("Comic Markdown glyph does not match its source map");
      }
      result.push({ node: text, index, source: next.source });
      cursor++;
    }
  }
  if (cursor !== expected.length) throw new TypeError("Comic Markdown source map has unbound glyphs");
  glyphs.set(root, result);
}

/** Original source offset -> rendered glyph, with hidden syntax clamped to its nearest visible boundary. */
export function comicMarkdownPoint(root: HTMLElement, offset: number): DOMRect | undefined {
  const content = root.matches("[data-comic-markdown]")
    ? root
    : root.querySelector<HTMLElement>("[data-comic-markdown]");
  if (!content) return undefined;
  const points = glyphs.get(content) ?? [];
  let low = 0;
  let high = points.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if ((points[middle]?.source ?? Infinity) < offset) low = middle + 1;
    else high = middle;
  }
  const before = points[low - 1];
  const after = points[low];
  const closest = !after || (before && offset - before.source < after.source - offset) ? before : after;
  if (!closest) return content.getBoundingClientRect();
  let { index } = closest;
  const text = closest.node.data;
  if (index > 0 && /[\uDC00-\uDFFF]/.test(text[index] ?? "") && /[\uD800-\uDBFF]/.test(text[index - 1] ?? "")) index--;
  const range = content.ownerDocument.createRange();
  range.setStart(closest.node, index);
  range.setEnd(closest.node, index + ((text.codePointAt(index) ?? 0) > 0xffff ? 2 : 1));
  return range.getBoundingClientRect();
}

/** Bespoke source anchoring surrounds shared safe widgets, never replaces their rendering policy. */
export function ComicMarkdown({ document, part }: { document: ComicMarkdownDocument; part: ComicMarkdownPart }) {
  const root = useRef<HTMLDivElement>(null);
  const { thread, sourcePaneId } = useTranscriptRenderContext();
  // Keep unchanged DOM through a native pointer down/up sequence. A fresh
  // Markdown element can replace innerHTML and lose the pressed link target.
  const content = useMemo<ReactNode>(() => {
    if (part.plain) return part.input;
    if (part.kind === "code") {
      return (
        <div className={styles.technicalContent}>
          <CodeBlock
            text={part.code ?? ""}
            language={part.language}
            fold={false}
            renderLine={(line) => <span className={styles.codeLine}>{line}</span>}
          />
        </div>
      );
    }
    if (part.kind === "table") {
      return (
        <div className={styles.technicalContent}>
          <Markdown source={part.input} />
        </div>
      );
    }
    return <Markdown source={part.input} />;
  }, [part.plain, part.input, part.kind, part.code, part.language]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: content identity is the DOM replacement epoch that must reinstall link listeners.
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    return part.kind !== "code" && thread?.cwd && sourcePaneId
      ? enhanceFileReferences(element, {
          sessionRef: thread.ref,
          cwd: thread.cwd,
          artifactsDir: thread.artifactsDir,
          sourcePaneId,
        })
      : undefined;
  }, [content, part.plain, part.kind, thread?.ref, thread?.cwd, thread?.artifactsDir, sourcePaneId]);
  useLayoutEffect(() => {
    // File enhancement preserves authored text but may replace its Text nodes.
    // Do not add entity/action portals, whose text has no authored source offset.
    if (root.current) bind(root.current, part);
  });
  return (
    <div
      ref={root}
      data-comic-markdown={part.kind}
      data-comic-text=""
      data-comic-live={document.live || undefined}
      className={
        part.kind === "prose"
          ? `${styles.balloonText} ${part.plain ? "" : styles.formattedText}`
          : styles.technicalScroll
      }
    >
      {content}
    </div>
  );
}
