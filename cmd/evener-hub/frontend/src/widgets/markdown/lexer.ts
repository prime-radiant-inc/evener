import { Marked } from "marked";

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
export const markdownLexer = new Marked({ gfm: true });
