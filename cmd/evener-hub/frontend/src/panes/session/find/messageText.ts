// What a message says, as its row shows it. A reply renders as Markdown, so
// it is read as the rendered page reads: formatting marks gone, a link's label
// without its address, code and authored HTML kept as written. Blocks
// (paragraphs, list items, table cells) are separated by a line break and
// inline runs are joined with nothing, the same way Find reads the rendered
// DOM. This module stays free of the transcript's renderers so the palette's
// search can share it.
import { fenceLanguage, type ItemModel, joinedReasoningParagraphs, pendingTextJoined } from "@evener/appwire-client";
import type { Token } from "marked";
import { splitMarkdownSegments } from "../../../widgets/markdown/segments";
import { closeOpenMarkdown } from "../../../widgets/markdown/streaming";
import { MERMAID_FENCE_LANGUAGE } from "../../../widgets/mermaid/markers";
import { mermaidSourceShown } from "../../../widgets/mermaid/renderState";

// Marked preserves only semicolon-terminated references in ordinary text;
// it escapes other ampersands before the browser parses the reply. Decode
// only those preserved references, one at a time, so a decoded ampersand
// cannot turn the rest of the text into another reference.
// Without a document (a node-environment caller) the text stays as written.
let decoder: HTMLTextAreaElement | undefined;

function decodeReferences(text: string): string {
  if (!text.includes("&") || typeof document === "undefined") return text;
  decoder ??= document.createElement("textarea");
  const element = decoder;
  return text.replace(/&(?:#\d{1,7}|#[Xx][a-fA-F0-9]{1,6}|\w+);/g, (reference) => {
    element.innerHTML = reference;
    return element.value;
  });
}

function read(tokens: readonly Token[], into: string[]): void {
  for (const token of tokens) {
    switch (token.type) {
      case "table":
        for (const cell of [...token.header, ...token.rows.flat()]) {
          read(cell.tokens, into);
          into.push("\n");
        }
        break;
      case "list":
        read(token.items, into);
        break;
      // An image shows its picture, not its description.
      case "image":
      case "checkbox":
        break;
      case "br":
      case "space":
      case "hr":
        into.push("\n");
        break;
      // The widget shows authored HTML as written rather than rendering it.
      case "html":
        into.push(token.text);
        if (token.block) into.push("\n");
        break;
      case "code":
        // The row shows the fence's language in a header above the code, on its
        // own line, so the label is a word of its own before the block's text.
        into.push(fenceLanguage(token), "\n", token.text, "\n");
        break;
      default:
        if ("tokens" in token && token.tokens) {
          read(token.tokens, into);
          // Only a block carries inline tokens of its own.
          if (token.type !== "link" && token.type !== "strong" && token.type !== "em" && token.type !== "del") {
            into.push("\n");
          }
        } else if ("text" in token && typeof token.text === "string") {
          // Ordinary text shows a character reference as its character; an
          // escaped one (an "escape" token) shows as written.
          into.push(token.type === "text" ? decodeReferences(token.text) : token.text);
        }
    }
  }
}

/** A Markdown source read as its rendered page reads: formatting marks gone,
 * a link's label without its address, code and authored HTML kept as written.
 * Shared with the palette and with thought summaries, which render Markdown
 * too. */
export function markdownAsRead(source: string, live = false): string {
  const into: string[] = [];
  const closed = live ? closeOpenMarkdown(source) : source;
  // The renderer segments closed diagrams while keeping an unfinished live
  // Mermaid fence as code: the synthetic closer is not a finished diagram.
  for (const segment of splitMarkdownSegments(closed, live ? source : null)) {
    // A diagram that failed to render falls back to a code block holding its
    // source under the fence's language header, so those words are on screen
    // and must be searchable.
    if (segment.kind === "markdown") read(segment.tokens, into);
    else if (mermaidSourceShown(segment.text)) into.push(MERMAID_FENCE_LANGUAGE, "\n", segment.text, "\n");
  }
  return into.join("");
}

/** A message's text as its row shows it, or null for an item Find does not
 * search. A reply renders as Markdown, so it is read as the page reads; a
 * prompt is shown as typed. */
export function messageAsShown(item: ItemModel): string | null {
  if (item.type === "agentMessage") {
    const live = item.status === "inProgress";
    const source = live ? pendingTextJoined(item.pendingText ?? []) : item.text;
    return source ? markdownAsRead(source, live) : null;
  }
  return item.type === "userMessage" ? item.text : null;
}

/** A thought's summaries as its row reads them: one string per rendered
 * paragraph, Markdown formatting gone. A live thought is rendered with its
 * unclosed tail closed, exactly as ThinkBlock's live body does; settled text
 * keeps its markers literal. Shared with the palette. */
export function reasoningSummariesAsRead(item: ItemModel, live = item.status === "inProgress"): string[] {
  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  return paragraphs.map((paragraph, index) => markdownAsRead(paragraph, live && index === paragraphs.length - 1));
}
