import { isValidTranscriptRef } from "@evener/appwire-client";
import {
  docFileRawURL,
  docImageURL,
  findFileReferences,
  isImagePath,
  parseFileReference,
} from "@evener/appwire-client/docContent";
import { MERMAID_DIAGRAM_ATTR } from "../../../../widgets/mermaid/markers";
import { browserDocPort } from "../../../doc/browserDocPort";
import { type DocumentOpenRequest, openDocBeside } from "../../../doc/openDoc";

const EXCLUDED = `a, pre, script, style, [data-entity-host], [data-file-action], [${MERMAID_DIAGRAM_ATTR}]`;
const BLOCKS = "p, h1, h2, h3, h4, h5, h6, li, td, th, blockquote, div, ul, ol, table, tr, hr, br";
const epochs = new WeakMap<HTMLElement, object>();

interface TextOffset {
  node: Text;
  start: number;
  end: number;
}

/** Enhances sanitized DOM only. Block offsets preserve token boundaries across
 * inline formatting, but ambiguous multi-node references remain unchanged. */
export function enhanceFileReferences(
  root: HTMLElement,
  context: { sessionRef: string; cwd: string; sourcePaneId: string },
): () => void {
  const epoch = {};
  epochs.set(root, epoch);
  const cleanups: Array<() => void> = [];
  const owns = (node: Node) => epochs.get(root) === epoch && root.contains(node);
  const qualifiedRef = context.sessionRef.includes(":") ? context.sessionRef : `local:${context.sessionRef}`;
  if (!isValidTranscriptRef(qualifiedRef) || !context.cwd || !context.sourcePaneId) return () => {};

  function activate(link: HTMLAnchorElement, request: DocumentOpenRequest) {
    const href = link.getAttribute("href");
    link.href = isImagePath(request.reference.path)
      ? docImageURL(browserDocPort.origin, request.session, request.reference.readTarget)
      : docFileRawURL(browserDocPort.origin, request.session, request.reference.readTarget);
    const onClick = (event: MouseEvent) => {
      if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      event.stopPropagation();
      openDocBeside(request);
    };
    link.addEventListener("click", onClick);
    cleanups.push(() => {
      link.removeEventListener("click", onClick);
      if (!owns(link)) return;
      if (href === null) link.removeAttribute("href");
      else link.setAttribute("href", href);
    });
  }

  for (const link of root.querySelectorAll<HTMLAnchorElement>("a[href]")) {
    if (link.closest(`pre, [data-entity-host], [${MERMAID_DIAGRAM_ATTR}]`)) continue;
    const reference = parseFileReference(link.getAttribute("href") ?? "", "link", context.cwd);
    if (reference) activate(link, { session: context.sessionRef, reference, sourcePaneId: context.sourcePaneId });
  }

  // Retain the original Text as an empty anchor, just like the entity pass.
  // Restoration touches only this epoch's still-owned DOM, not a newer render.
  function wrap(node: Text, spans: ReturnType<typeof findFileReferences>) {
    const original = node.data;
    const fragment = document.createDocumentFragment();
    let cursor = 0;
    for (const span of spans) {
      fragment.append(original.slice(cursor, span.start));
      const link = document.createElement("a");
      link.textContent = original.slice(span.start, span.end);
      fragment.append(link);
      activate(link, { session: context.sessionRef, reference: span.reference, sourcePaneId: context.sourcePaneId });
      cursor = span.end;
    }
    fragment.append(original.slice(cursor));
    const inserted = [...fragment.childNodes];
    node.before(fragment);
    node.data = "";
    cleanups.push(() => {
      if (!owns(node)) return;
      for (const child of inserted) child.parentNode?.removeChild(child);
      if (node.data === "") node.data = original;
    });
  }

  let text = "";
  let offsets: TextOffset[] = [];
  const codeElements: Array<{ element: HTMLElement; start: number; end: number }> = [];
  function flush() {
    const matches = findFileReferences(text, context.cwd);
    for (const offset of offsets) {
      const contained = matches
        .filter((span) => span.start >= offset.start && span.end <= offset.end)
        .map((span) => ({ ...span, start: span.start - offset.start, end: span.end - offset.start }));
      if (contained.length > 0) wrap(offset.node, contained);
    }
    if (codeElements.length > 0) {
      const delimiter = /[\s"'“”‘’]/u;
      const tokenStarts: number[] = [];
      let tokenStart = 0;
      for (let index = 0; index <= text.length; index += 1) {
        tokenStarts[index] = tokenStart;
        if (delimiter.test(text[index] ?? "")) tokenStart = index + 1;
      }
      const tokenEnds: number[] = [];
      let tokenEnd = text.length;
      for (let index = text.length; index >= 0; index -= 1) {
        if (delimiter.test(text[index] ?? "")) tokenEnd = index;
        tokenEnds[index] = tokenEnd;
      }
      for (const code of codeElements) {
        const reference = parseFileReference(code.element.textContent, "code", context.cwd);
        if (!reference) continue;
        // Code is a whole-element surface, not a suffix of a longer token.
        // Only surrounding delimiters may share its whitespace/quote token.
        const before = text.slice(tokenStarts[code.start], code.start);
        const after = text.slice(code.end, tokenEnds[code.end]);
        if (!/^[([{<]*$/u.test(before) || !/^[)\]}>.,;:!?…]*$/u.test(after)) continue;
        const child = code.element.firstChild;
        if (!(child instanceof Text) || code.element.childNodes.length !== 1) continue;
        wrap(child, [{ start: 0, end: child.data.length, reference }]);
      }
    }
    text = "";
    offsets = [];
    codeElements.length = 0;
  }

  function walk(node: Node) {
    if (node instanceof Text) {
      offsets.push({ node, start: text.length, end: text.length + node.data.length });
      text += node.data;
      return;
    }
    if (!(node instanceof HTMLElement)) return;
    if (node.matches(EXCLUDED)) {
      text += "\0"; // Not whitespace: a barrier must not authorize an adjacent suffix.
      return;
    }
    if (node.tagName === "CODE") {
      const start = text.length;
      text += node.textContent;
      codeElements.push({ element: node, start, end: text.length });
      return;
    }
    const block = node.matches(BLOCKS);
    if (block) flush();
    for (const child of [...node.childNodes]) walk(child);
    if (block) flush();
  }
  for (const child of [...root.childNodes]) walk(child);
  flush();

  return () => {
    for (const cleanup of cleanups.reverse()) cleanup();
  };
}
