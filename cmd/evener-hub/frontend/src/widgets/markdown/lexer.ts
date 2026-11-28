import type { Token, Tokens } from "marked";
import { createGatedMarked } from "./setextGate";

// The app's shared default-options lexer: the same GFM tokenizer the Markdown
// widget parses with, minus the widget's custom renderer (lexer output never
// renders - renderer choice cannot change the token stream). Lexer-only
// consumers import this instead of constructing their own
// `new Marked({ gfm: true })`, so the tokenizer is initialized once.
//
// This module is UI-free by design: it imports only `marked` (no React, no
// DOMPurify, no CSS), so node-environment suites and non-widget modules like
// panes/session/transcript/messages/reasoningFormat.ts can import it without
// pulling the widget's browser dependencies. index.tsx re-exports it so
// existing `widgets/markdown` import sites keep working.
//
// Constructed through createGatedMarked so the shared tokenizer carries the
// setext gate (#5002): without it Marked's lheading rule rescans the whole
// remaining frame once per line inside list items, making large list-item
// documents quadratic to lex.
export const markdownLexer = createGatedMarked({ gfm: true });

export function plainTextFromToken(token: Token): string {
  switch (token.type) {
    case "image":
    case "link":
      return token.tokens?.map(plainTextFromToken).join("") ?? token.text;
    case "code":
    case "codespan":
    case "escape":
    case "html":
      return token.text;
    case "checkbox":
      return "";
    case "br":
    case "space":
      return " ";
    case "list":
      return token.items.map((item: Tokens.ListItem) => item.tokens.map(plainTextFromToken).join("")).join(" ");
    case "table":
      return [...token.header, ...token.rows.flat()]
        .map((cell) => cell.tokens.map(plainTextFromToken).join(""))
        .join(" ");
    default:
      if ("tokens" in token && token.tokens) return token.tokens.map(plainTextFromToken).join("");
      if ("text" in token && typeof token.text === "string") return token.text;
      return "";
  }
}

export function plainTextFromTokens(tokens: Token[] | undefined, fallback = ""): string {
  if (!tokens || tokens.length === 0) return fallback.trim();
  return tokens.map(plainTextFromToken).join("").trim();
}
