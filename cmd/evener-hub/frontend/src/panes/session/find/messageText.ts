// What a message says, as its row shows it. A reply renders as Markdown, so
// it is read as the rendered page reads: formatting marks gone, a link's label
// without its address, code and authored HTML kept as written. Blocks
// (paragraphs, list items, table cells) are separated by a line break and
// inline runs are joined with nothing, the same way Find reads the rendered
// DOM. This module stays free of the transcript's renderers so the palette's
// search can share it.
import type { ItemModel } from "@evener/appwire-client";
import type { Token } from "marked";
import { markdownLexer } from "../../../widgets/markdown/lexer";

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
          into.push(token.text);
        }
    }
  }
}

function markdownAsRead(source: string): string {
  const into: string[] = [];
  read(markdownLexer.lexer(source), into);
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
