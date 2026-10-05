import { type SourceRange, validateSourceRange } from "@evener/appwire-client";
import DOMPurify from "dompurify";
import { Lexer, Marked, type Token, Tokenizer, type Tokens, type TokensList } from "marked";
import { escapeHtml, markdownRenderer, SANITIZE_CONFIG } from "./rendering";
import { fenceTokenTerminated, isMermaidCodeToken, mermaidText } from "./segments";

export interface DocumentRenderIdentity {
  displayedSnapshotId: string;
  mappingId: string;
}
export interface DocumentTextRun {
  id: string;
  renderedText: string;
  sourceSpans: readonly SourceRange[];
}
export interface DocumentRenderMap extends DocumentRenderIdentity {
  source: string;
  textRuns: readonly DocumentTextRun[];
}
export type MappedSelection =
  | { state: "mapped"; sourceRange: SourceRange; selectedRenderedText: string }
  | { state: "unmapped"; reason: "outside" | "stale" | "chrome" | "unverified" };
export interface BoundDocumentMap {
  identity: DocumentRenderIdentity;
  capture(selection: Selection): MappedSelection;
  rangesFor(sourceRange: SourceRange): readonly Range[];
  dispose(): void;
}

// Every transformation below runs over the pinned parser's rules/captures.
// Recognition, token ordering, reference resolution and masking stay in Marked.
interface Trace {
  text: string;
  spans: SourceRange[];
  newline?: Trace;
}
interface Provenance {
  raw: Trace;
  text?: Trace;
}
function originalTrace(source: string): Trace {
  return { text: source, spans: Array.from({ length: source.length }, (_, start) => ({ start, end: start + 1 })) };
}
function slice(value: Trace, start = 0, end = value.text.length): Trace {
  const a = start < 0 ? Math.max(0, value.text.length + start) : start;
  const b = end < 0 ? Math.max(0, value.text.length + end) : end;
  return { text: value.text.slice(a, b), spans: value.spans.slice(a, b) };
}
function concat(...values: Trace[]): Trace {
  return { text: values.map((v) => v.text).join(""), spans: values.flatMap((v) => v.spans) };
}
const empty = (): Trace => ({ text: "", spans: [] });
function spelling(value: Trace, text: string): Trace {
  if (!text) return empty();
  const first = value.spans[0],
    last = value.spans.at(-1);
  if (!first || !last) throw new Error("Unverified Marked insertion");
  return { text, spans: Array.from({ length: text.length }, () => ({ start: first.start, end: last.end })) };
}
function replace(value: Trace, regex: RegExp, replacement: (match: Trace, captures: Trace[]) => Trace): Trace {
  const re = new RegExp(regex.source, regex.flags.includes("d") ? regex.flags : `${regex.flags}d`);
  const pieces: Trace[] = [];
  let cursor = 0;
  for (let match = re.exec(value.text); match !== null; match = re.exec(value.text)) {
    pieces.push(slice(value, cursor, match.index));
    const indices = match.indices;
    if (!indices) throw new Error("RegExp indices unavailable");
    pieces.push(
      replacement(
        slice(value, match.index, match.index + match[0].length),
        indices.slice(1).map((pair) => (pair ? slice(value, pair[0], pair[1]) : empty())),
      ),
    );
    cursor = match.index + match[0].length;
    if (!re.global) break;
    if (!match[0].length) throw new Error("Unverified empty Marked transformation");
  }
  pieces.push(slice(value, cursor));
  return concat(...pieces);
}
function remove(value: Trace, re: RegExp): Trace {
  return replace(value, re, () => empty());
}
function trim(value: Trace): Trace {
  const start = value.text.length - value.text.trimStart().length;
  return slice(value, start, start + value.text.trim().length);
}
function trimEnd(value: Trace): Trace {
  return slice(value, 0, value.text.trimEnd().length);
}
function trimNewlines(value: Trace): Trace {
  return slice(value, 0, value.text.replace(/\n+$/, "").length);
}
function capture(value: Trace, re: RegExp, index: number): Trace {
  const result = new RegExp(re.source, re.flags.includes("d") ? re.flags : `${re.flags}d`).exec(value.text);
  const pair = result?.indices?.[index];
  return pair ? slice(value, pair[0], pair[1]) : empty();
}
function traceLines(value: Trace): Trace[] {
  const result: Trace[] = [];
  let start = 0;
  for (let i = 0; i < value.text.length; i++)
    if (value.text[i] === "\n") {
      const line = slice(value, start, i);
      line.newline = slice(value, i, i + 1);
      result.push(line);
      start = i + 1;
    }
  result.push(slice(value, start));
  return result;
}
function joinLines(values: Trace[]): Trace {
  const pieces: Trace[] = [];
  for (const [i, value] of values.entries()) {
    pieces.push(value);
    if (i < values.length - 1) {
      if (!value.newline) throw new Error("Unverified line join");
      pieces.push(value.newline);
    }
  }
  return concat(...pieces);
}
function expandTabs(value: Trace, column = 0): Trace {
  const pieces: Trace[] = [];
  // Marked 18.0.6 expandTabs iterates Unicode code points but provenance uses
  // UTF-16. Advance the cursor by the actual point's unit count.
  let cursor = 0;
  for (const char of value.text) {
    const part = slice(value, cursor, cursor + char.length);
    cursor += char.length;
    if (char === "\t") {
      const count = 4 - (column % 4);
      pieces.push(spelling(part, " ".repeat(count)));
      column += count;
    } else {
      pieces.push(part);
      column++;
    }
  }
  return concat(...pieces);
}
function splitCells(value: Trace, count?: number): Trace[] {
  // Same splitCells transformation as Marked 18.0.6, retaining the escaped
  // pipe's entire spelling. This does not recognize a table, the tokenizer did.
  const parts: Trace[] = [];
  let start = 0;
  for (let i = 0; i < value.text.length; i++)
    if (value.text[i] === "|") {
      let escaped = false;
      for (let j = i - 1; j >= 0 && value.text[j] === "\\"; j--) escaped = !escaped;
      if (!escaped) {
        parts.push(slice(value, start, i));
        start = i + 1;
      }
    }
  parts.push(slice(value, start));
  if (!parts[0]?.text.trim()) parts.shift();
  if (parts.length && !parts.at(-1)?.text.trim()) parts.pop();
  if (count !== undefined) {
    parts.splice(count);
    while (parts.length < count) parts.push(empty());
  }
  return parts.map((p) => replace(trim(p), /\\\|/g, (m) => spelling(m, "|")));
}
function decodeEntities(value: Trace, raw = false): Trace {
  const textarea = document.createElement("textarea");
  const decode = (text: string) => {
    textarea.innerHTML = text;
    return textarea.value;
  };
  const regex = raw ? /&(?:#[xX][a-fA-F0-9]+|#\d+|[a-zA-Z0-9]+);?/g : /&(?:#\d{1,7}|#[xX][a-fA-F0-9]{1,6}|\w+);/g;
  return replace(value, regex, (m) => {
    const decoded = decode(m.text);
    if (decoded === m.text) return m;
    // The HTML parser can decode a valid prefix of an unknown named entity.
    // Leave the undecoded suffix's units at their own original positions.
    let suffix = 0;
    while (suffix < decoded.length - 1 && m.text.at(-suffix - 1) === decoded.at(-suffix - 1)) suffix++;
    while (suffix > 0) {
      const prefix = m.text.slice(0, -suffix);
      if (decode(prefix) === decoded.slice(0, -suffix)) break;
      suffix--;
    }
    const consumed = m.text.length - suffix;
    return concat(spelling(slice(m, 0, consumed), decoded.slice(0, decoded.length - suffix)), slice(m, consumed));
  });
}

interface Frame {
  input: Trace;
  tokens: Token[];
  kind: "block" | "inline";
  clipped: boolean;
}
interface Invocation {
  name: string;
  input: Trace;
  inline?: Trace;
  cells?: Trace[];
  cellIndex: number;
}
class ProvenanceLexer extends Lexer {
  readonly provenance = new WeakMap<Token, Provenance>();
  readonly queued = new WeakMap<Token[], Trace>();
  readonly tokenizerRef: ProvenanceTokenizer;
  private frames: Frame[] = [];
  private invocation?: Invocation;
  private pending?: Trace;
  private pendingToken?: Trace;
  constructor(input: Trace) {
    const tokenizer = new ProvenanceTokenizer();
    super({ gfm: true, tokenizer });
    this.tokenizerRef = tokenizer;
    tokenizer.owner = this;
    this.pending = input;
    const dispatch = tokenizer as unknown as Record<string, (src: string, ...args: unknown[]) => Token | undefined>;
    for (const name of [
      "space",
      "code",
      "fences",
      "heading",
      "hr",
      "blockquote",
      "list",
      "html",
      "def",
      "table",
      "lheading",
      "paragraph",
      "text",
      "escape",
      "tag",
      "link",
      "reflink",
      "emStrong",
      "codespan",
      "br",
      "del",
      "autolink",
      "url",
      "inlineText",
    ]) {
      const method = dispatch[name];
      if (!method) throw new Error(`Missing pinned tokenizer ${name}`);
      dispatch[name] = (src, ...args) => this.token(name, src, () => method.call(tokenizer, src, ...args));
    }
  }
  withSource<T>(input: Trace, action: () => T): T {
    const previous = this.pending;
    this.pending = input;
    try {
      return action();
    } finally {
      this.pending = previous;
    }
  }
  withTokenSource<T>(input: Trace, action: () => T): T {
    const previous = this.pendingToken;
    this.pendingToken = input;
    try {
      return action();
    } finally {
      this.pendingToken = previous;
    }
  }
  tokenInput(): Trace {
    if (!this.invocation) throw new Error("No tokenizer provenance");
    return this.invocation.input;
  }
  private inputFor(src: string): Trace {
    const frame = this.frames.at(-1);
    if (!frame) throw new Error("No lexer provenance");
    const input = slice(frame.input, frame.input.text.length - src.length);
    if (input.text !== src) throw new Error("Unverified Marked remainder");
    return input;
  }
  override blockTokens(src: string, tokens: Token[] = [], lastParagraphClipped = false): TokensList {
    const input = this.pending;
    this.pending = undefined;
    if (!input || input.text !== src) throw new Error("Unverified nested block input");
    this.frames.push({ input, tokens, kind: "block", clipped: lastParagraphClipped });
    try {
      return super.blockTokens(src, tokens, lastParagraphClipped) as TokensList;
    } finally {
      this.frames.pop();
    }
  }
  override inline(src: string, tokens: Token[] = []): Token[] {
    const input = this.inlineInput(src);
    this.queued.set(tokens, input);
    return super.inline(src, tokens);
  }
  override inlineTokens(src: string, tokens: Token[] = []): Token[] {
    let input = this.queued.get(tokens);
    if (input && input.text !== src) {
      input = remove(input, this.tokenizerRef.rules.other.listReplaceTask);
    }
    if (!input) input = this.inlineInput(src);
    if (input.text !== src) throw new Error("Unverified queued inline input");
    this.frames.push({ input, tokens, kind: "inline", clipped: false });
    try {
      return super.inlineTokens(src, tokens);
    } finally {
      this.frames.pop();
    }
  }
  private inlineInput(src: string): Trace {
    const call = this.invocation;
    if (!call) throw new Error("No inline provenance");
    const rules = this.tokenizerRef.rules;
    let input: Trace;
    switch (call.name) {
      case "heading": {
        input = trim(capture(call.input, rules.block.heading, 2));
        if (input.text.endsWith("#")) {
          const clipped = slice(input, 0, input.text.replace(/#+$/, "").length);
          if (!clipped.text || clipped.text.endsWith(" ")) input = trim(clipped);
        }
        break;
      }
      case "lheading":
        input = trim(capture(call.input, rules.block.lheading, 1));
        break;
      case "paragraph":
        input = capture(call.input, rules.block.paragraph, 1);
        if (input.text.endsWith("\n")) input = slice(input, 0, -1);
        break;
      case "text":
        input = capture(call.input, rules.block.text, 0);
        break;
      case "table": {
        if (!call.cells) {
          const header = capture(call.input, rules.block.table, 1);
          const rows = remove(capture(call.input, rules.block.table, 3), rules.other.tableRowBlankLine);
          const cells = splitCells(header);
          call.cells = [
            ...cells,
            ...(rows.text.trim() ? traceLines(rows).flatMap((row) => splitCells(row, cells.length)) : []),
          ];
        }
        input = call.cells[call.cellIndex++] ?? empty();
        break;
      }
      case "link":
      case "reflink": {
        const rule =
          call.name === "link"
            ? rules.inline.link
            : rules.inline.reflink.test(call.input.text)
              ? rules.inline.reflink
              : rules.inline.nolink;
        input = replace(capture(call.input, rule, 1), rules.other.outputLinkReplace, (m, c) =>
          spelling(m, c[0]?.text ?? ""),
        );
        break;
      }
      case "emStrong": {
        const offset = call.input.text.slice(1, 1 + src.length) === src ? 1 : 2;
        input = slice(call.input, offset, offset + src.length);
        break;
      }
      case "del": {
        const match = rules.inline.delLDelim.exec(call.input.text);
        const offset = (match?.[0].match(/^~+/)?.[0] ?? "").length;
        input = slice(call.input, offset, offset + src.length);
        break;
      }
      default:
        throw new Error(`Unverified inline entry ${call.name}`);
    }
    if (input.text !== src) throw new Error(`Unverified ${call.name} transformation`);
    call.inline = input;
    return input;
  }
  private leafText(call: Invocation, token: Token): Trace | undefined {
    if (!("text" in token) || typeof token.text !== "string") return undefined;
    if (call.inline && call.name !== "table") return call.inline;
    const rules = this.tokenizerRef.rules;
    const raw = slice(call.input, 0, token.raw.length);
    let text: Trace;
    switch (call.name) {
      case "code":
        text = remove(raw, rules.other.codeRemoveIndent);
        break;
      case "fences": {
        text = capture(call.input, rules.block.fences, 3);
        const indent = rules.other.indentCodeCompensation.exec(raw.text)?.[1];
        if (indent)
          text = joinLines(
            traceLines(text).map((line) => {
              const spaces = rules.other.beginningSpace.exec(line.text)?.[0];
              const result = spaces && spaces.length >= indent.length ? slice(line, indent.length) : line;
              result.newline = line.newline;
              return result;
            }),
          );
        break;
      }
      case "codespan": {
        text = replace(capture(call.input, rules.inline.code, 2), rules.other.newLineCharGlobal, (m) =>
          spelling(m, " "),
        );
        if (rules.other.nonSpaceChar.test(text.text) && text.text.startsWith(" ") && text.text.endsWith(" "))
          text = slice(text, 1, -1);
        break;
      }
      case "escape":
        text = spelling(raw, token.text);
        break;
      case "html":
      case "tag":
      case "inlineText":
      case "reflink":
        text = raw;
        break;
      case "autolink":
        text = capture(call.input, rules.inline.autolink, 1);
        break;
      case "url":
        text = slice(raw, 0, token.text.length);
        break;
      case "blockquote":
        return undefined;
      default:
        return undefined;
    }
    if (text.text !== token.text) throw new Error(`Unverified ${call.name} text`);
    return text;
  }
  private token(name: string, src: string, parse: () => Token | undefined): Token | undefined {
    const prior = this.invocation;
    const input = this.pendingToken ?? this.inputFor(src);
    this.pendingToken = undefined;
    if (input.text !== src) throw new Error("Unverified recursive tokenizer input");
    const call: Invocation = { name, input, cellIndex: 0 };
    this.invocation = call;
    try {
      const token = parse();
      if (!token) return token;
      const existing = this.provenance.get(token);
      const raw = existing?.raw ?? slice(input, 0, token.raw.length);
      if (raw.text !== token.raw) throw new Error(`Unverified ${name} raw`);
      const text = this.leafText(call, token) ?? existing?.text;
      this.provenance.set(token, { raw, text });
      if ((name === "url" || name === "autolink") && token.type === "link" && text) {
        for (const child of token.tokens ?? []) this.provenance.set(child, { raw: text, text });
      }
      const frame = this.frames.at(-1);
      const previous = frame?.tokens.at(-1);
      const old = previous ? this.provenance.get(previous) : undefined;
      if (previous && old) {
        if (
          frame?.kind === "inline" &&
          previous.type === "text" &&
          token.type === "text" &&
          (name === "inlineText" || name === "reflink")
        ) {
          this.provenance.set(previous, {
            raw: concat(old.raw, raw),
            text: old.text && text ? concat(old.text, text) : undefined,
          });
        } else if (frame?.kind === "block") {
          if (name === "space" && raw.text.length === 1)
            this.provenance.set(previous, { ...old, raw: concat(old.raw, raw) });
          const merge =
            ((name === "code" || name === "def") && (previous.type === "paragraph" || previous.type === "text")) ||
            (name === "text" && previous.type === "text") ||
            (name === "paragraph" && frame.clipped && previous.type === "paragraph");
          if (merge) {
            const newline = slice(old.raw, -1);
            if (newline.text !== "\n") throw new Error("Unverified block merge newline");
            const added = name === "def" ? raw : text;
            if (!old.text || !added) throw new Error("Unverified block merge text");
            const merged = concat(old.text, newline, added);
            this.provenance.set(previous, { raw: concat(old.raw, raw), text: merged });
            if ("tokens" in previous && previous.tokens) this.queued.set(previous.tokens, merged);
          }
        }
      }
      return token;
    } finally {
      this.invocation = prior;
    }
  }
}

// Only these two nested tokenizer methods are extended, keeping the exact
// Marked 18.0.6 control flow and rules while carrying parallel UTF-16 traces.
class ProvenanceTokenizer extends Tokenizer {
  owner?: ProvenanceLexer;
  private get mapping(): ProvenanceLexer {
    if (!this.owner) throw new Error("No provenance lexer");
    return this.owner;
  }
  override blockquote(src: string): Tokens.Blockquote | undefined {
    const cap = this.rules.block.blockquote.exec(src);
    if (cap) {
      let mappedLines = traceLines(trimNewlines(capture(this.mapping.tokenInput(), this.rules.block.blockquote, 0)));
      let lines = mappedLines.map((line) => line.text);
      let raw = "";
      let text = "";
      let rawTrace = empty();
      let textTrace = empty();
      let separator = empty();
      const tokens: Token[] = [];

      while (lines.length > 0) {
        let inBlockquote = false;
        const currentLines = [];

        let i: number;
        for (i = 0; i < lines.length; i++) {
          // get lines up to a continuation
          if (this.rules.other.blockquoteStart.test(lines[i] ?? "")) {
            currentLines.push(lines[i]);
            inBlockquote = true;
          } else if (!inBlockquote) {
            currentLines.push(lines[i]);
          } else {
            break;
          }
        }
        const currentMappedLines = mappedLines.slice(0, i);
        mappedLines = mappedLines.slice(i);
        lines = lines.slice(i);

        const currentRaw = currentLines.join("\n");
        const currentRawTrace = joinLines(currentMappedLines);
        const currentTextTrace = remove(
          replace(currentRawTrace, this.rules.other.blockquoteSetextReplace, (match, captures) =>
            concat(slice(match, 0, 1), spelling(slice(match, 0, 1), "    "), captures[0] ?? empty()),
          ),
          this.rules.other.blockquoteSetextReplace2,
        );
        const currentText = currentRaw
          // precede setext continuation with 4 spaces so it isn't a setext
          .replace(this.rules.other.blockquoteSetextReplace, "\n    $1")
          .replace(this.rules.other.blockquoteSetextReplace2, "");
        rawTrace = raw ? concat(rawTrace, separator, currentRawTrace) : currentRawTrace;
        textTrace = text ? concat(textTrace, separator, currentTextTrace) : currentTextTrace;
        separator = currentMappedLines.at(-1)?.newline ?? empty();
        raw = raw ? `${raw}\n${currentRaw}` : currentRaw;
        text = text ? `${text}\n${currentText}` : currentText;

        // parse blockquote lines as top level tokens
        // merge paragraphs if this is a continuation
        const top = this.lexer.state.top;
        this.lexer.state.top = true;
        this.mapping.withSource(currentTextTrace, () => this.lexer.blockTokens(currentText, tokens, true));
        this.lexer.state.top = top;

        // if there is no continuation then we are done
        if (lines.length === 0) {
          break;
        }

        const lastToken = tokens.at(-1);

        if (lastToken?.type === "code") {
          // blockquote continuation cannot be preceded by a code block
          break;
        } else if (lastToken?.type === "blockquote") {
          // include continuation in nested blockquote
          const oldToken = lastToken as Tokens.Blockquote;
          const oldTrace = this.mapping.provenance.get(oldToken);
          if (!oldTrace) throw new Error("Unverified quote continuation");
          const newTextTrace = concat(oldTrace.raw, separator, joinLines(mappedLines));
          const newText = `${oldToken.raw}\n${lines.join("\n")}`;
          const newToken = this.mapping.withTokenSource(newTextTrace, () => this.blockquote(newText));
          if (!newToken) throw new Error("Unverified nested quote");
          tokens[tokens.length - 1] = newToken;

          const newTrace = this.mapping.provenance.get(newToken);
          if (!newTrace) throw new Error("Unverified continuation result");
          rawTrace = concat(slice(rawTrace, 0, raw.length - oldToken.raw.length), newTrace.raw);
          raw = raw.substring(0, raw.length - oldToken.raw.length) + newToken.raw;
          textTrace = concat(slice(textTrace, 0, text.length - oldToken.text.length), newTrace.text ?? empty());
          text = text.substring(0, text.length - oldToken.text.length) + newToken.text;
          break;
        } else if (lastToken?.type === "list") {
          // include continuation in nested list
          const oldToken = lastToken as Tokens.List;
          const oldTrace = this.mapping.provenance.get(oldToken);
          if (!oldTrace) throw new Error("Unverified quote continuation");
          const newTextTrace = concat(oldTrace.raw, separator, joinLines(mappedLines));
          const newText = `${oldToken.raw}\n${lines.join("\n")}`;
          const newToken = this.mapping.withTokenSource(newTextTrace, () => this.list(newText));
          if (!newToken) throw new Error("Unverified quote list");
          tokens[tokens.length - 1] = newToken;

          const newTrace = this.mapping.provenance.get(newToken);
          if (!newTrace) throw new Error("Unverified continuation result");
          rawTrace = concat(slice(rawTrace, 0, raw.length - lastToken.raw.length), newTrace.raw);
          raw = raw.substring(0, raw.length - lastToken.raw.length) + newToken.raw;
          textTrace = concat(slice(textTrace, 0, text.length - oldToken.raw.length), newTrace.raw);
          text = text.substring(0, text.length - oldToken.raw.length) + newToken.raw;
          mappedLines = traceLines(slice(newTextTrace, newToken.raw.length));
          lines = newText.substring(newToken.raw.length).split("\n");
        }
      }

      const result: Tokens.Blockquote = {
        type: "blockquote",
        raw,
        tokens,
        text,
      };
      if (rawTrace.text !== raw || textTrace.text !== text) throw new Error("Unverified quote transform");
      this.mapping.provenance.set(result, { raw: rawTrace, text: textTrace });
      return result;
    }
  }

  override list(src: string): Tokens.List | undefined {
    let sourceTrace = this.mapping.tokenInput();
    const itemTraces = new WeakMap<Tokens.ListItem, Provenance>();
    let cap = this.rules.block.list.exec(src);
    if (cap) {
      let bull = (cap[1] ?? "").trim();
      const isordered = bull.length > 1;

      const list: Tokens.List = {
        type: "list",
        raw: "",
        ordered: isordered,
        start: isordered ? +bull.slice(0, -1) : "",
        loose: false,
        items: [],
      };

      bull = isordered ? `\\d{1,9}\\${bull.slice(-1)}` : `\\${bull}`;

      if (this.options.pedantic) {
        bull = isordered ? bull : "[*+-]";
      }

      // Get next list item
      const itemRegex = this.rules.other.listItemRegex(bull);
      let endsWithBlankLine = false;
      // Check if current bullet point can start a new List Item
      while (src) {
        let endEarly = false;
        let raw = "";
        let itemContents = "";
        let rawTrace = empty();
        let contentsTrace = empty();
        cap = itemRegex.exec(src);
        if (!cap) {
          break;
        }

        if (this.rules.block.hr.test(src)) {
          // End list if bullet was actually HR (possibly move into itemRegex?)
          break;
        }

        raw = cap[0];
        rawTrace = slice(sourceTrace, 0, raw.length);
        sourceTrace = slice(sourceTrace, raw.length);
        src = src.substring(raw.length);

        const firstLine = traceLines(capture(rawTrace, itemRegex, 2))[0] ?? empty();
        let lineTrace = expandTabs(firstLine, (cap[1] ?? "").length);
        let line = lineTrace.text;
        let nextLine = src.split("\n", 1)[0] ?? "";
        let blankLine = !line.trim();

        let indent = 0;
        if (this.options.pedantic) {
          indent = 2;
          contentsTrace = slice(lineTrace, line.length - line.trimStart().length);
          itemContents = line.trimStart();
        } else if (blankLine) {
          indent = (cap[1] ?? "").length + 1;
        } else {
          indent = line.search(this.rules.other.nonSpaceChar); // Find first non-space char
          indent = indent > 4 ? 1 : indent; // Treat indented code blocks (> 4 spaces) as having only 1 indent
          contentsTrace = slice(lineTrace, indent);
          itemContents = line.slice(indent);
          indent += (cap[1] ?? "").length;
        }

        if (blankLine && this.rules.other.blankLine.test(nextLine)) {
          // Items begin with at most one blank line
          rawTrace = concat(rawTrace, slice(sourceTrace, 0, nextLine.length + 1));
          sourceTrace = slice(sourceTrace, nextLine.length + 1);
          raw += `${nextLine}\n`;
          src = src.substring(nextLine.length + 1);
          endEarly = true;
        }

        if (!endEarly) {
          const nextBulletRegex = this.rules.other.nextBulletRegex(indent);
          const hrRegex = this.rules.other.hrRegex(indent);
          const fencesBeginRegex = this.rules.other.fencesBeginRegex(indent);
          const headingBeginRegex = this.rules.other.headingBeginRegex(indent);
          const htmlBeginRegex = this.rules.other.htmlBeginRegex(indent);
          const blockquoteBeginRegex = this.rules.other.blockquoteBeginRegex(indent);

          // Check if following lines should be included in List Item
          while (src) {
            const rawLine = src.split("\n", 1)[0] ?? "";
            const rawLineTrace = traceLines(sourceTrace)[0] ?? empty();
            let nextLineTrace = rawLineTrace;
            let nextLineWithoutTabs: string;
            nextLine = rawLine;

            // Re-align to follow commonmark nesting rules
            if (this.options.pedantic) {
              nextLine = nextLine.replace(this.rules.other.listReplaceNesting, "  ");
              nextLineWithoutTabs = nextLine;
            } else {
              nextLineTrace = replace(rawLineTrace, this.rules.other.tabCharGlobal, (match) => spelling(match, "    "));
              nextLineWithoutTabs = nextLine.replace(this.rules.other.tabCharGlobal, "    ");
            }

            // End list item if found code fences
            if (fencesBeginRegex.test(nextLine)) {
              break;
            }

            // End list item if found start of new heading
            if (headingBeginRegex.test(nextLine)) {
              break;
            }

            // End list item if found start of html block
            if (htmlBeginRegex.test(nextLine)) {
              break;
            }

            // End list item if found start of blockquote
            if (blockquoteBeginRegex.test(nextLine)) {
              break;
            }

            // End list item if found start of new bullet
            if (nextBulletRegex.test(nextLine)) {
              break;
            }

            // Horizontal rule found
            if (hrRegex.test(nextLine)) {
              break;
            }

            if (nextLineWithoutTabs.search(this.rules.other.nonSpaceChar) >= indent || !nextLine.trim()) {
              // Dedent if possible
              contentsTrace = concat(contentsTrace, slice(rawTrace, -1), slice(nextLineTrace, indent));
              itemContents += `\n${nextLineWithoutTabs.slice(indent)}`;
            } else {
              // not enough indentation
              if (blankLine) {
                break;
              }

              // paragraph continuation unless last line was a different block level element
              if (line.replace(this.rules.other.tabCharGlobal, "    ").search(this.rules.other.nonSpaceChar) >= 4) {
                // indented code block
                break;
              }
              if (fencesBeginRegex.test(line)) {
                break;
              }
              if (headingBeginRegex.test(line)) {
                break;
              }
              if (hrRegex.test(line)) {
                break;
              }

              contentsTrace = concat(contentsTrace, slice(rawTrace, -1), rawLineTrace);
              itemContents += `\n${nextLine}`;
            }

            blankLine = !nextLine.trim();

            rawTrace = concat(rawTrace, slice(sourceTrace, 0, rawLine.length + 1));
            sourceTrace = slice(sourceTrace, rawLine.length + 1);
            raw += `${rawLine}\n`;
            src = src.substring(rawLine.length + 1);
            lineTrace = slice(nextLineTrace, indent);
            line = nextLineWithoutTabs.slice(indent);
          }
        }

        if (!list.loose) {
          // If the previous item ended with a blank line, the list is loose
          if (endsWithBlankLine) {
            list.loose = true;
          } else if (this.rules.other.doubleBlankLine.test(raw)) {
            endsWithBlankLine = true;
          }
        }

        const item: Tokens.ListItem = {
          type: "list_item",
          raw,
          task: !!this.options.gfm && this.rules.other.listIsTask.test(itemContents),
          loose: false,
          text: itemContents,
          tokens: [],
        };
        itemTraces.set(item, { raw: rawTrace, text: contentsTrace });
        list.items.push(item);

        list.raw += raw;
      }

      // Do not consume newlines at end of final item. Alternatively, make itemRegex *start* with any newlines to simplify/speed up endsWithBlankLine logic
      const lastItem = list.items.at(-1);
      if (lastItem) {
        const lastTrace = itemTraces.get(lastItem);
        if (!lastTrace?.text) throw new Error("Unverified list item");
        itemTraces.set(lastItem, { raw: trimEnd(lastTrace.raw), text: trimEnd(lastTrace.text) });
        lastItem.raw = lastItem.raw.trimEnd();
        lastItem.text = lastItem.text.trimEnd();
      } else {
        // not a list since there were no items
        return;
      }
      list.raw = list.raw.trimEnd();

      // Item child tokens handled here at end because we needed to have the final item to trim it first
      for (const item of list.items) {
        this.lexer.state.top = false;
        const itemTrace = itemTraces.get(item);
        if (!itemTrace?.text || itemTrace.text.text !== item.text || itemTrace.raw.text !== item.raw)
          throw new Error("Unverified list transform");
        this.mapping.provenance.set(item, itemTrace);
        item.tokens = this.mapping.withSource(itemTrace.text, () => this.lexer.blockTokens(item.text, []));
        const itemToken = item.tokens[0];
        if (item.task && (itemToken?.type === "text" || itemToken?.type === "paragraph")) {
          // Remove checkbox markdown from item tokens
          const provenance = this.mapping.provenance.get(itemToken);
          if (provenance)
            this.mapping.provenance.set(itemToken, {
              raw: remove(provenance.raw, this.rules.other.listReplaceTask),
              text: provenance.text ? remove(provenance.text, this.rules.other.listReplaceTask) : undefined,
            });
          item.text = item.text.replace(this.rules.other.listReplaceTask, "");
          itemToken.raw = itemToken.raw.replace(this.rules.other.listReplaceTask, "");
          itemToken.text = itemToken.text.replace(this.rules.other.listReplaceTask, "");
          for (let i = this.lexer.inlineQueue.length - 1; i >= 0; i--) {
            const queued = this.lexer.inlineQueue[i];
            if (queued && this.rules.other.listIsTask.test(queued.src)) {
              queued.src = queued.src.replace(this.rules.other.listReplaceTask, "");
              break;
            }
          }

          const taskRaw = this.rules.other.listTaskCheckbox.exec(item.raw);
          if (taskRaw) {
            const checkboxToken: Tokens.Checkbox = {
              type: "checkbox",
              raw: `${taskRaw[0]} `,
              checked: taskRaw[0] !== "[ ]",
            };
            item.checked = checkboxToken.checked;
            if (list.loose) {
              if (
                item.tokens[0] &&
                ["paragraph", "text"].includes(item.tokens[0].type) &&
                "tokens" in item.tokens[0] &&
                item.tokens[0].tokens
              ) {
                item.tokens[0].raw = checkboxToken.raw + item.tokens[0].raw;
                item.tokens[0].text = checkboxToken.raw + item.tokens[0].text;
                item.tokens[0].tokens.unshift(checkboxToken);
              } else {
                item.tokens.unshift({
                  type: "paragraph",
                  raw: checkboxToken.raw,
                  text: checkboxToken.raw,
                  tokens: [checkboxToken],
                });
              }
            } else {
              item.tokens.unshift(checkboxToken);
            }
          }
        } else if (item.task) {
          item.task = false;
        }

        if (!list.loose) {
          // Check if list should be loose
          const spacers = item.tokens.filter((t) => t.type === "space");
          const hasMultipleLineBreaks = spacers.length > 0 && spacers.some((t) => this.rules.other.anyLine.test(t.raw));

          list.loose = hasMultipleLineBreaks;
        }
      }

      // Set all items to loose if list is loose
      if (list.loose) {
        for (const item of list.items) {
          item.loose = true;
          for (const token of item.tokens) {
            if (token.type === "text") {
              token.type = "paragraph";
            }
          }
        }
      }

      return list;
    }
  }
}

export type DocumentRenderSegment =
  | { kind: "markdown"; html: string; key: string }
  | { kind: "mermaid"; text: string; runId: string; key: string };
export function renderDocument(
  source: string,
  identity: DocumentRenderIdentity,
): { map: DocumentRenderMap; segments: DocumentRenderSegment[] } {
  let input = originalTrace(source);
  if (input.text.startsWith("\ufeff")) input = slice(input, 1);
  input = replace(input, /\r\n|\r/g, (m) => spelling(m, "\n"));
  const lexer = new ProvenanceLexer(input);
  const tokens = lexer.lex(input.text);
  const runs: DocumentTextRun[] = [];
  function run(token: Token, decode = false, raw = false): string {
    const trace = lexer.provenance.get(token)?.text;
    if (!trace) throw new Error(`Unverified rendered token ${token.type}`);
    const text = decode ? decodeEntities(trace, raw) : trace;
    const id = `document-run-${runs.length}`;
    runs.push({ id, renderedText: text.text, sourceSpans: text.spans });
    return `<span data-document-run="${id}">${escapeHtml(text.text)}</span>`;
  }
  const parser = new Marked({
    gfm: true,
    renderer: {
      ...markdownRenderer,
      text(token) {
        if ("tokens" in token && token.tokens) return this.parser.parseInline(token.tokens);
        return run(token, true, "escaped" in token && Boolean(token.escaped));
      },
      html(token) {
        return run(token);
      },
      codespan(token) {
        const html = markdownRenderer.codespan?.call(this, token);
        if (typeof html !== "string") throw new Error("Missing shared codespan renderer");
        return html.replace(`>${escapeHtml(token.text)}</code>`, `>${run(token)}</code>`);
      },
      code(token) {
        const html = markdownRenderer.code?.call(this, token);
        if (typeof html !== "string") throw new Error("Missing shared code renderer");
        return html.replace(`>${escapeHtml(token.text)}</code>`, `>${run(token)}</code>`);
      },
    },
  });
  const segments: DocumentRenderSegment[] = [];
  let prose: Token[] = [];
  function flush() {
    if (!prose.length) return;
    segments.push({
      kind: "markdown",
      html: DOMPurify.sanitize(parser.parser(prose), SANITIZE_CONFIG),
      key: `markdown-${segments.length}`,
    });
    prose = [];
  }
  for (const token of tokens) {
    if (isMermaidCodeToken(token) && fenceTokenTerminated(token.raw)) {
      flush();
      const text = mermaidText(token);
      const trace = lexer.provenance.get(token)?.text;
      if (!trace) throw new Error("Unverified diagram source");
      // Mermaid's established error fallback restores the content's final newline.
      // Recover its actual removed source newline, never invent an offset.
      let fallback = trace;
      if (text !== trace.text) {
        const raw = lexer.provenance.get(token)?.raw;
        if (!raw) throw new Error("Unverified diagram raw");
        const content = capture(raw, lexer.tokenizerRef.rules.block.fences, 3);
        const last = content.spans.at(-1);
        const next = last?.end;
        const normalized = replace(originalTrace(source), /\r\n|\r/g, (m) => spelling(m, "\n"));
        const newline = normalized.spans.findIndex((span) => span.start === next);
        const unit = slice(normalized, newline, newline + 1);
        if (unit.text !== "\n") throw new Error("Unverified Mermaid final newline");
        fallback = concat(trace, unit);
      }
      const runId = `document-run-${runs.length}`;
      runs.push({ id: runId, renderedText: text, sourceSpans: fallback.spans });
      segments.push({ kind: "mermaid", text, runId, key: `mermaid-${segments.length}` });
    } else prose.push(token);
  }
  flush();
  return { map: { ...identity, source, textRuns: runs }, segments };
}

const bindings = new WeakMap<HTMLElement, object>();
// React may replay layout effects without rebuilding their DOM. Markers have
// already been unwrapped, but only the same verified run object may rebind.
const verifiedRuns = new WeakMap<Text, DocumentTextRun>();
export function bindDocumentMap(root: HTMLElement, map: DocumentRenderMap): BoundDocumentMap {
  const lease = {};
  bindings.set(root, lease);
  let disposed = false;
  const nodes = new Map<Text, DocumentTextRun>();
  const expected = new Map(map.textRuns.map((run) => [run.id, run]));
  const valid = () => !disposed && root.isConnected && bindings.get(root) === lease;
  function register(element: Element, id: string, unwrap: boolean) {
    const run = expected.get(id);
    if (!run || element.textContent !== run.renderedText || run.sourceSpans.length !== run.renderedText.length) return;
    if (element.childNodes.length !== 1 || element.firstChild?.nodeType !== Node.TEXT_NODE) return;
    const text = element.firstChild as Text;
    nodes.set(text, run);
    verifiedRuns.set(text, run);
    if (unwrap) element.replaceWith(text);
  }
  for (const element of root.querySelectorAll("[data-document-run]"))
    register(element, element.getAttribute("data-document-run") ?? "", true);
  const existing = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let node = existing.nextNode(); node; node = existing.nextNode()) {
    const text = node as Text;
    const run = verifiedRuns.get(text);
    if (run && expected.get(run.id) === run && text.data === run.renderedText) nodes.set(text, run);
  }
  function refresh() {
    for (const element of root.querySelectorAll("[data-document-diagram-run]")) {
      const code = element.querySelector("pre code");
      if (code) register(code, element.getAttribute("data-document-diagram-run") ?? "", false);
    }
  }
  function verified(node: Text, run: DocumentTextRun) {
    return root.contains(node) && node.data === run.renderedText;
  }
  function splitSurrogate(text: string, offset: number) {
    return (
      offset > 0 &&
      offset < text.length &&
      /[\uD800-\uDBFF]/.test(text[offset - 1] ?? "") &&
      /[\uDC00-\uDFFF]/.test(text[offset] ?? "")
    );
  }
  return {
    identity: { displayedSnapshotId: map.displayedSnapshotId, mappingId: map.mappingId },
    capture(selection) {
      if (!valid()) return { state: "unmapped", reason: "stale" };
      if (selection.rangeCount !== 1 || selection.isCollapsed) return { state: "unmapped", reason: "outside" };
      const selected = selection.getRangeAt(0);
      if (!root.contains(selected.startContainer) || !root.contains(selected.endContainer))
        return { state: "unmapped", reason: "outside" };
      refresh();
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      let first: SourceRange | undefined, last: SourceRange | undefined;
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const node = n as Text;
        if (!selected.intersectsNode(node)) continue;
        const start = selected.startContainer === node ? selected.startOffset : 0;
        const end = selected.endContainer === node ? selected.endOffset : node.length;
        if (start >= end) continue;
        const run = nodes.get(node);
        if (!run) {
          if (node.data.trim()) return { state: "unmapped", reason: "chrome" };
          continue;
        }
        if (!verified(node, run) || splitSurrogate(node.data, start) || splitSurrogate(node.data, end))
          return { state: "unmapped", reason: "unverified" };
        const a = run.sourceSpans[start],
          b = run.sourceSpans[end - 1];
        if (!a || !b) return { state: "unmapped", reason: "unverified" };
        if (last && a.start < last.start) return { state: "unmapped", reason: "unverified" };
        first ??= a;
        last = b;
      }
      if (!first || !last) return { state: "unmapped", reason: "unverified" };
      const sourceRange = { start: first.start, end: last.end };
      try {
        validateSourceRange(map.source, sourceRange);
      } catch {
        return { state: "unmapped", reason: "unverified" };
      }
      return { state: "mapped", sourceRange, selectedRenderedText: selection.toString() };
    },
    rangesFor(sourceRange) {
      if (!valid()) return [];
      try {
        validateSourceRange(map.source, sourceRange);
      } catch {
        return [];
      }
      refresh();
      const result: Range[] = [];
      for (const [node, run] of nodes) {
        if (!verified(node, run)) continue;
        let start = -1;
        for (let i = 0; i <= run.sourceSpans.length; i++) {
          const span = run.sourceSpans[i];
          const included = span && span.start >= sourceRange.start && span.end <= sourceRange.end;
          if (included && start < 0) start = i;
          if (!included && start >= 0) {
            if (!splitSurrogate(node.data, start) && !splitSurrogate(node.data, i)) {
              const r = document.createRange();
              r.setStart(node, start);
              r.setEnd(node, i);
              result.push(r);
            }
            start = -1;
          }
        }
      }
      result.sort((a, b) => a.compareBoundaryPoints(Range.START_TO_START, b));
      return result;
    },
    dispose() {
      disposed = true;
      if (bindings.get(root) === lease) bindings.delete(root);
      nodes.clear();
    },
  };
}
