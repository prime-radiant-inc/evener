// A Markdown source as its rendered page reads: formatting marks gone, a
// link's label without its address, code kept as written. Blocks (paragraphs,
// list items, table cells) are separated by a line break and inline runs are
// joined with nothing, the same way Find reads the rendered DOM.
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
      case "html":
        into.push(token.text.replace(/<[^>]*>/g, ""));
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

export function markdownAsRead(source: string): string {
  const into: string[] = [];
  read(markdownLexer.lexer(source), into);
  return into.join("");
}
