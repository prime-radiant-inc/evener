import type { Token, Tokens } from "marked";
import { markdownLexer } from "../widgets/markdown/lexer";

// Every index is an original UTF-16 code-unit offset, never a rendered offset.
type MappedText = { text: string; offsets: number[] };
type VisibleRun = MappedText;
type ProseBlock = { start: number; end: number; input: string; visible: VisibleRun[] };
export type ComicMarkdownPart = {
  kind: "prose" | "code" | "table";
  start: number;
  end: number;
  input: string;
  visible: VisibleRun[];
  plain: boolean;
  code?: string;
  language?: string;
  blocks?: ProseBlock[];
};
export type ComicMarkdownDocument = {
  source: string;
  live: boolean;
  references: string;
  parts: ComicMarkdownPart[];
};

function cut(value: MappedText, start: number, end = value.text.length): MappedText {
  return { text: value.text.slice(start, end), offsets: value.offsets.slice(start, end) };
}

function normalized(source: string): MappedText {
  let text = "";
  const offsets: number[] = [];
  for (let i = 0; i < source.length; i++) {
    offsets.push(i);
    text += source[i] === "\r" ? "\n" : source[i];
    if (source[i] === "\r" && source[i + 1] === "\n") i++;
  }
  return { text, offsets };
}

function lines(value: MappedText): MappedText[] {
  let start = 0;
  return value.text.split("\n").map((line) => {
    const result = cut(value, start, start + line.length);
    start += line.length + 1;
    return result;
  });
}

function joinLines(values: MappedText[], original: MappedText): MappedText {
  const separators = [...original.text.matchAll(/\n/g)].map((match) => original.offsets[match.index] ?? 0);
  return {
    text: values.map((line) => line.text).join("\n"),
    offsets: values.flatMap((line, i) => [...(i ? [separators[i - 1] ?? 0] : []), ...line.offsets]),
  };
}

/** The lexer removes container prefixes and indentation, not arbitrary occurrences of body text. */
function bodyLines(raw: MappedText, text: string, firstPrefix = 0): MappedText {
  const sourceLines = lines(raw);
  const targetLines = text.split("\n");
  const result = targetLines.map((target, i) => {
    const line = sourceLines[i] ?? { text: "", offsets: [] };
    const prefix = i === 0 ? firstPrefix : 0;
    const available = cut(line, prefix);
    const leading = available.text.match(/^[ \t]*/)?.[0].length ?? 0;
    const targetLeading = target.match(/^[ \t]*/)?.[0].length ?? 0;
    const content = cut(available, leading);
    const whitespace = cut(available, Math.max(0, leading - targetLeading), leading);
    return {
      text: target,
      offsets: [
        ...Array.from(
          { length: targetLeading },
          (_, j) => whitespace.offsets[j] ?? whitespace.offsets.at(-1) ?? available.offsets[0] ?? 0,
        ),
        ...content.offsets.slice(0, target.length - targetLeading),
      ],
    };
  });
  return joinLines(result, raw);
}

function unquote(raw: MappedText, text: string): MappedText {
  const stripped = joinLines(
    lines(raw).map((line) => cut(line, line.text.match(/^ {0,3}>[ \t]?/)?.[0].length ?? 0)),
    raw,
  );
  return cut(stripped, 0, text.length);
}

function listBody(raw: MappedText, item: Tokens.ListItem): MappedText {
  const prefix = item.raw.match(/^ {0,3}(?:[*+-]|\d{1,9}[.)])/)?.[0].length ?? 0;
  const checkbox = item.tokens[0]?.type === "checkbox" ? item.tokens[0].raw : "";
  const target = checkbox + item.text;
  const targetLines = target.split("\n");
  // Marked expands first-line tabs to tab stops and continuation tabs to four
  // spaces before removing the item's indentation. Each expanded unit still
  // belongs to the authored tab, including after a second block-level dedent.
  const expanded = joinLines(
    lines(raw).map((line, row) => {
      // Lazy continuation lines retain tabs verbatim in the lexer's item text.
      if (targetLines[row]?.includes("\t")) return line;
      let text = "";
      const offsets: number[] = [];
      for (let i = 0; i < line.text.length; i++) {
        const char = line.text[i] ?? "";
        const width = char === "\t" ? (row ? 4 : 4 - (text.length % 4)) : 1;
        text += char === "\t" ? " ".repeat(width) : char;
        for (let j = 0; j < width; j++) offsets.push(line.offsets[i] as number);
      }
      return { text, offsets };
    }),
    raw,
  );
  return bodyLines(expanded, target, prefix);
}

function decoded(value: MappedText): MappedText {
  if (!value.text.includes("&")) return value;
  const offsets: number[] = [];
  const decoder = document.createElement("textarea");
  let cursor = 0;
  let text = "";
  // Only an entity is passed to the platform decoder, never authored HTML.
  for (const match of value.text.matchAll(/&(?:#[xX][\da-fA-F]+|#\d+|[a-zA-Z][a-zA-Z\d]+);/g)) {
    text += value.text.slice(cursor, match.index);
    for (const offset of value.offsets.slice(cursor, match.index)) offsets.push(offset);
    decoder.innerHTML = match[0];
    text += decoder.value;
    for (let i = 0; i < decoder.value.length; i++)
      offsets.push(value.offsets[match.index + (decoder.value === match[0] ? i : 0)] as number);
    cursor = match.index + match[0].length;
  }
  return { text: text + value.text.slice(cursor), offsets: [...offsets, ...value.offsets.slice(cursor)] };
}

function inline(tokens: Token[], raw: MappedText): VisibleRun[] {
  let cursor = 0;
  return tokens.flatMap((token): VisibleRun[] => {
    const value = cut(raw, cursor, cursor + token.raw.length);
    cursor += token.raw.length;
    switch (token.type) {
      case "strong":
      case "em":
      case "del": {
        const t = token as Tokens.Strong;
        const prefix = (token.raw.length - t.text.length) / 2;
        return inline(t.tokens, cut(value, prefix, prefix + t.text.length));
      }
      case "link": {
        const t = token as Tokens.Link;
        // Explicit labels start after [, autolinks after <, GFM URLs have no delimiter.
        const prefix = token.raw[0] === "[" || token.raw[0] === "<" ? 1 : 0;
        const offsets: number[] = [];
        let cursor = prefix;
        // The lexer removes escapes before square brackets before lexing the label.
        // Walk exactly the normalized label, not a search for matching text.
        for (let i = 0; i < t.text.length; i++, cursor++) {
          if (value.text[cursor] === "\\" && /[[\]]/.test(value.text[cursor + 1] ?? "")) cursor++;
          offsets.push(value.offsets[cursor] as number);
        }
        return inline(t.tokens, { text: t.text, offsets });
      }
      case "image":
      case "br":
        return [];
      case "escape":
        return [cut(value, 1)];
      case "codespan": {
        const prefix = token.raw.match(/^`+/)?.[0].length ?? 0;
        let body = cut(value, prefix, value.text.length - prefix);
        body = { ...body, text: body.text.replace(/\n/g, " ") };
        if (/^ .* $/.test(body.text) && /[^ ]/.test(body.text)) body = cut(body, 1, body.text.length - 1);
        return [body];
      }
      case "html":
        return [value];
      default:
        return "tokens" in token && token.tokens ? inline(token.tokens, value) : [decoded(value)];
    }
  });
}

function tableRuns(token: Tokens.Table, raw: MappedText): VisibleRun[] {
  const rows = lines(raw);
  const cells = (line: MappedText): MappedText[] => {
    const result: MappedText[] = [];
    let start = 0;
    let escaped = false;
    for (let i = 0; i <= line.text.length; i++) {
      const c = line.text[i];
      if ((c === "|" && !escaped) || i === line.text.length) {
        const value = cut(line, start, i);
        const left = value.text.length - value.text.trimStart().length;
        const right = value.text.trimEnd().length;
        result.push(cut(value, left, right));
        start = i + 1;
      }
      escaped = c === "\\" && !escaped;
    }
    if (/^\s*\|/.test(line.text)) result.shift();
    if (/\|\s*$/.test(line.text)) result.pop();
    return result.map((cell) => {
      const offsets: number[] = [];
      let text = "";
      for (let i = 0; i < cell.text.length; i++) {
        if (cell.text[i] === "\\" && cell.text[i + 1] === "|") i++;
        text += cell.text[i];
        offsets.push(cell.offsets[i] ?? 0);
      }
      return { text, offsets };
    });
  };
  return [token.header, ...token.rows].flatMap((row, index) => {
    const mapped = cells(rows[index ? index + 1 : 0] ?? { text: "", offsets: [] });
    return row.flatMap((cell, i) => inline(cell.tokens, mapped[i] ?? { text: "", offsets: [] }));
  });
}

function visible(token: Token, raw: MappedText): VisibleRun[] {
  switch (token.type) {
    case "space":
    case "def":
    case "hr":
    case "checkbox":
      return [];
    case "blockquote": {
      const t = token as Tokens.Blockquote;
      return blockRuns(t.tokens, unquote(raw, t.text));
    }
    case "list": {
      const t = token as Tokens.List;
      let cursor = 0;
      return t.items.flatMap((item) => {
        const value = cut(raw, cursor, cursor + item.raw.length);
        cursor += item.raw.length;
        return blockRuns(item.tokens, listBody(value, item));
      });
    }
    case "table":
      return tableRuns(token as Tokens.Table, raw);
    case "heading": {
      const t = token as Tokens.Heading;
      const prefix =
        token.raw.match(/^ {0,3}#{1,6}[ \t]+/)?.[0].length ?? token.raw.length - token.raw.trimStart().length;
      return inline(t.tokens, cut(raw, prefix, prefix + t.text.length));
    }
    case "paragraph":
    case "text": {
      const t = token as Tokens.Paragraph;
      // A code-indented continuation can be folded into a paragraph/text token.
      // Its inline tokens refer to the dedented text, not the token's raw span.
      return inline(t.tokens, bodyLines(raw, t.text));
    }
    case "html":
      return [raw];
    default:
      return "tokens" in token && token.tokens ? inline(token.tokens, raw) : [decoded(raw)];
  }
}

function blockRuns(tokens: Token[], raw: MappedText): VisibleRun[] {
  let cursor = 0;
  return tokens.flatMap((token) => {
    const value = cut(raw, cursor, cursor + token.raw.length);
    cursor += token.raw.length;
    return visible(token, value);
  });
}

function hasInsert(token: Token): boolean {
  if (token.type === "code" || token.type === "table") return true;
  if (token.type === "list") return (token as Tokens.List).items.some((item) => item.tokens.some(hasInsert));
  return token.type === "blockquote" && (token as Tokens.Blockquote).tokens.some(hasInsert);
}

function isPlain(token: Token): boolean {
  return (
    token.type === "space" ||
    ((token.type === "paragraph" || token.type === "text") &&
      (!("tokens" in token) || !token.tokens || token.tokens.every((t) => t.type === "text" && !t.raw.includes("&"))))
  );
}

/** Parse before pagination. Raw ranges partition the original, derived inputs carry reference context. */
export function projectComicMarkdown(source: string, live: boolean): ComicMarkdownDocument {
  const original = normalized(source);
  const tokens = markdownLexer.lexer(original.text);
  const definitions: string[] = [];
  markdownLexer.walkTokens(tokens, (token) => {
    if (token.type === "def") definitions.push(token.raw);
  });
  const references = definitions.join("\n");
  const append = (parts: ComicMarkdownPart[], part: ComicMarkdownPart) => {
    const prior = parts.at(-1);
    if (part.kind === "prose" && prior?.kind === "prose") {
      prior.input += `\n\n${part.input}`;
      prior.end = part.end;
      for (const run of part.visible) prior.visible.push(run);
      for (const block of part.blocks ?? []) prior.blocks?.push(block);
      prior.plain &&= part.plain;
    } else parts.push(part);
  };
  const emit = (parts: ComicMarkdownPart[], token: Token, raw: MappedText) => {
    const start = raw.offsets[0] ?? source.length;
    const end = (raw.offsets.at(-1) ?? start - 1) + 1;
    if (token.type === "space" || token.type === "def" || token.type === "checkbox") return;
    if (token.type === "code") {
      const t = token as Tokens.Code;
      const fence = raw.text.match(/^ {0,3}(`{3,}|~{3,})[^\n]*(?:\n|$)/);
      const payload = fence ? cut(raw, fence[0].length) : raw;
      const code = bodyLines(payload, t.text);
      parts.push({
        kind: "code",
        start,
        end,
        input: raw.text,
        code: t.text,
        language: t.lang,
        visible: [code],
        plain: false,
      });
    } else {
      const input = raw.text;
      const runs = visible(token, raw);
      const kind = token.type === "table" ? "table" : "prose";
      const block = { start, end, input, visible: [...runs] };
      append(parts, {
        kind,
        start,
        end,
        input,
        visible: runs,
        plain: isPlain(token),
        blocks: kind === "prose" ? [block] : undefined,
      });
    }
  };
  const wrapFragments = (
    parts: ComicMarkdownPart[],
    fragments: ComicMarkdownPart[],
    wrap: (input: string) => string,
  ) => {
    for (const fragment of fragments) {
      if (fragment.kind !== "code") {
        fragment.input = wrap(fragment.input);
        fragment.plain = false;
        // A contiguous fragment of one authored container is indivisible.
        // Wrapping its paragraphs separately would invent list items/quotes.
        if (fragment.kind === "prose")
          fragment.blocks = [
            {
              start: fragment.start,
              end: fragment.end,
              input: fragment.input,
              visible: [...fragment.visible],
            },
          ];
      }
      append(parts, fragment);
    }
  };
  const visit = (values: Token[], input: MappedText): ComicMarkdownPart[] => {
    const parts: ComicMarkdownPart[] = [];
    let cursor = 0;
    for (const token of values) {
      const raw = cut(input, cursor, cursor + token.raw.length);
      cursor += token.raw.length;
      if (token.type === "blockquote" && hasInsert(token)) {
        const t = token as Tokens.Blockquote;
        wrapFragments(parts, visit(t.tokens, unquote(raw, t.text)), (text) =>
          text
            .split("\n")
            .map((line) => `> ${line}`)
            .join("\n"),
        );
      } else if (token.type === "list" && hasInsert(token)) {
        const list = token as Tokens.List;
        let offset = 0;
        list.items.forEach((item, index) => {
          const value = cut(raw, offset, offset + item.raw.length);
          offset += item.raw.length;
          const marker = list.ordered ? `${Number(list.start) + index}. ` : "- ";
          wrapFragments(parts, visit(item.tokens, listBody(value, item)), (text) =>
            text
              .split("\n")
              .map((line, i) => `${i ? " ".repeat(marker.length) : marker}${line}`)
              .join("\n"),
          );
        });
      } else emit(parts, token, raw);
    }
    return parts;
  };
  const parts = visit(tokens, original);
  if (!parts.length)
    parts.push({ kind: "prose", start: 0, end: source.length, input: source, visible: [], plain: !source.trim() });
  // Whitespace, container markers and definitions belong to an adjacent panel, never an empty insert surround.
  const first = parts[0];
  if (first) first.start = 0;
  parts.forEach((part, index) => {
    part.end = parts[index + 1]?.start ?? source.length;
    if (references) part.plain = false;
    part.blocks?.forEach((block, blockIndex, blocks) => {
      if (blockIndex === 0) block.start = part.start;
      block.end = blocks[blockIndex + 1]?.start ?? part.end;
    });
    if (part.plain) {
      part.input = source.slice(part.start, part.end);
      part.visible = [
        { text: part.input, offsets: Array.from({ length: part.input.length }, (_, i) => part.start + i) },
      ];
    } else if (part.kind !== "code" && references) part.input += `\n\n${references}`;
  });
  return { source, live, references, parts };
}
