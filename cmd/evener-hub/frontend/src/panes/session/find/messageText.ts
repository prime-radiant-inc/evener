// What a message says, as its row shows it. A reply renders as Markdown, so
// it is read as the rendered page reads: formatting marks gone, a link's label
// without its address, code and authored HTML kept as written. Blocks
// (paragraphs, list items, table cells) are separated by a line break and
// inline runs are joined with nothing, the same way Find reads the rendered
// DOM. This module stays free of the transcript's renderers so the palette's
// search can share it.
import { fenceTokenTerminated, type ItemModel, isMermaidCodeToken } from "@evener/appwire-client";
import type { Token } from "marked";
import { markdownLexer } from "../../../widgets/markdown/lexer";

// A finished Mermaid fence at the top of a reply is drawn as a diagram
// (widgets/markdown/segments.ts), so its source is not on the page.
function drawnAsDiagram(token: Token): boolean {
  return isMermaidCodeToken(token) && fenceTokenTerminated(token.raw);
}

// Character references ("&amp;", "&#36;") as the page shows them. The
// browser's own parser decodes them, exactly as it does for the rendered
// reply; a textarea's content is text only, so nothing in it is parsed as
// markup. Without a document (a node-environment caller) the text is left as
// written.
let decoder: HTMLTextAreaElement | undefined;

function decodeReferences(text: string): string {
  if (!text.includes("&") || typeof document === "undefined") return text;
  decoder ??= document.createElement("textarea");
  decoder.innerHTML = text;
  return decoder.value;
}

function read(tokens: readonly Token[], into: string[], top = false): void {
  for (const token of tokens) {
    if (top && drawnAsDiagram(token)) continue;
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
        into.push(token.text, "\n");
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

function markdownAsRead(source: string): string {
  const into: string[] = [];
  read(markdownLexer.lexer(source), into, true);
  return into.join("");
}

/** A message's text as its row shows it, or null for an item Find does not
 * search. A reply renders as Markdown, so it is read as the page reads; a
 * prompt is shown as typed. */
export function messageAsShown(item: ItemModel): string | null {
  if (!item.text) return null;
  if (item.type === "agentMessage") return markdownAsRead(item.text);
  return item.type === "userMessage" ? item.text : null;
}
