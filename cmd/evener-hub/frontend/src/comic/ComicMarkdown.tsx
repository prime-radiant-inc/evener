import { useLayoutEffect, useRef } from "react";
import { CodeBlock, Markdown } from "../widgets";
import styles from "./comic.module.css";
import type { ComicMarkdownDocument, ComicMarkdownPart } from "./markdown";

type Glyph = { node: Text; index: number; source: number };
const glyphs = new WeakMap<HTMLElement, Glyph[]>();

/** Bind lexer-authored visible runs to DOM text, ignoring only markup's inter-element whitespace. */
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
    for (let index = 0; index < text.length; index++) {
      const next = expected[cursor];
      // Marked emits layout newlines between elements which are not token text.
      if (text.data[index] !== next?.char && /\s/.test(text.data[index] ?? "")) continue;
      if (!next) break;
      result.push({ node: text, index, source: next.source });
      cursor++;
    }
  }
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
  useLayoutEffect(() => {
    // Markdown may replace innerHTML even when its input and this part are unchanged.
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
      {part.plain ? (
        part.input
      ) : part.kind === "code" ? (
        <div className={styles.technicalContent}>
          <CodeBlock
            text={part.code ?? ""}
            language={part.language}
            fold={false}
            renderLine={(line) => <span className={styles.codeLine}>{line}</span>}
          />
        </div>
      ) : part.kind === "table" ? (
        <div className={styles.technicalContent}>
          <Markdown source={part.input} />
        </div>
      ) : (
        <Markdown source={part.input} />
      )}
    </div>
  );
}
