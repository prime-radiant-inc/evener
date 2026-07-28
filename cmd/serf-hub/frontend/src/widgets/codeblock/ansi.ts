import Anser from "anser";

export type AnsiNamedColor =
  | "black"
  | "red"
  | "green"
  | "yellow"
  | "blue"
  | "magenta"
  | "cyan"
  | "white"
  | "bright-black"
  | "bright-red"
  | "bright-green"
  | "bright-yellow"
  | "bright-blue"
  | "bright-magenta"
  | "bright-cyan"
  | "bright-white";

export type AnsiColor = { kind: "named"; name: AnsiNamedColor } | { kind: "rgb"; value: string };

export interface AnsiRun {
  text: string;
  foreground?: AnsiColor;
  background?: AnsiColor;
  bold: boolean;
  dim: boolean;
  italic: boolean;
  underline: boolean;
  hidden: boolean;
  strikethrough: boolean;
}

export type AnsiLine = AnsiRun[];

interface ParsedAnsiBundle {
  content: string;
  fg: string | null;
  bg: string | null;
  fg_truecolor: string | null;
  bg_truecolor: string | null;
  decorations: string[];
}

const NAMED_COLORS = new Set<AnsiNamedColor>([
  "black",
  "red",
  "green",
  "yellow",
  "blue",
  "magenta",
  "cyan",
  "white",
  "bright-black",
  "bright-red",
  "bright-green",
  "bright-yellow",
  "bright-blue",
  "bright-magenta",
  "bright-cyan",
  "bright-white",
]);

const RGB_CHANNELS = /^\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*$/;

const NAMED_FOREGROUND_CODES: Record<AnsiNamedColor, number> = {
  black: 30,
  red: 31,
  green: 32,
  yellow: 33,
  blue: 34,
  magenta: 35,
  cyan: 36,
  white: 37,
  "bright-black": 90,
  "bright-red": 91,
  "bright-green": 92,
  "bright-yellow": 93,
  "bright-blue": 94,
  "bright-magenta": 95,
  "bright-cyan": 96,
  "bright-white": 97,
};

const DECORATION_RESET = new Map([
  [23, 3],
  [24, 4],
  [27, 7],
  [28, 8],
  [29, 9],
]);

function isSgrSequence(sequence: string): boolean {
  if (!sequence.startsWith("\u001b[") || !sequence.endsWith("m")) return false;
  return Array.from(sequence.slice(2, -1)).every(
    (character) => character === ";" || (character >= "0" && character <= "9"),
  );
}

function escapeSequenceEnd(text: string, start: number): number {
  let index = start;
  while (index < text.length) {
    const code = text.charCodeAt(index);
    if (code < 0x20 || code > 0x2f) break;
    index += 1;
  }
  if (index < text.length) {
    const code = text.charCodeAt(index);
    if (code >= 0x30 && code <= 0x7e) return index;
  }
  return start - 1;
}

function normalizedSgr(sequence: string, decorations: Set<number>): string {
  const parameters = sequence
    .slice(2, -1)
    .split(";")
    .map((parameter) => (parameter === "" ? 0 : Number(parameter)));
  const normalized: number[] = [];

  for (let index = 0; index < parameters.length; index += 1) {
    const code = parameters[index] ?? 0;
    if ((code === 38 || code === 48) && (parameters[index + 1] === 2 || parameters[index + 1] === 5)) {
      const colorLength = parameters[index + 1] === 2 ? 5 : 3;
      normalized.push(...parameters.slice(index, index + colorLength));
      index += colorLength - 1;
      continue;
    }
    if (code === 0) {
      decorations.clear();
      normalized.push(code);
      continue;
    }
    if (code === 22) {
      decorations.delete(1);
      decorations.delete(2);
      normalized.push(code);
      continue;
    }
    const resetEnable = DECORATION_RESET.get(code);
    if (resetEnable !== undefined) {
      decorations.delete(resetEnable);
      normalized.push(code);
      continue;
    }
    if (code === 1 || code === 2 || code === 3 || code === 4 || code === 7 || code === 8 || code === 9) {
      if (!decorations.has(code)) {
        decorations.add(code);
        normalized.push(code);
      }
      continue;
    }
    normalized.push(code);
  }

  return normalized.length === 0 ? "" : `\u001b[${normalized.join(";")}m`;
}

function controlStringEnd(text: string, start: number, bellTerminated: boolean): number {
  for (let index = start; index < text.length; index += 1) {
    const character = text[index];
    if (bellTerminated && character === "\u0007") return index;
    if (character === "\u009c") return index;
    if (character === "\u001b" && text[index + 1] === "\\") return index + 1;
  }
  return text.length;
}

function csiEnd(text: string, start: number): number {
  for (let index = start; index < text.length; index += 1) {
    const code = text.charCodeAt(index);
    if (code >= 0x40 && code <= 0x7e) return index;
  }
  return text.length;
}

function isNonTextControl(code: number): boolean {
  return (
    (code >= 0x00 && code <= 0x08) ||
    code === 0x0b ||
    code === 0x0c ||
    code === 0x0d ||
    (code >= 0x0e && code <= 0x1a) ||
    (code >= 0x1c && code <= 0x1f) ||
    (code >= 0x7f && code <= 0x9f)
  );
}

interface PresentationScan {
  text: string;
  pending: string;
}

function presentationSequences(text: string): PresentationScan {
  let result = "";
  const decorations = new Set<number>();
  for (let index = 0; index < text.length; index += 1) {
    const character = text[index] ?? "";
    const code = character.charCodeAt(0);

    if (character === "\u001b") {
      const next = text[index + 1];
      if (next === "[") {
        const end = csiEnd(text, index + 2);
        if (end === text.length) return { text: result, pending: text.slice(index) };
        const sequence = text.slice(index, end + 1);
        if (isSgrSequence(sequence)) result += normalizedSgr(sequence, decorations);
        index = end;
        continue;
      }
      if (next === "]") {
        const end = controlStringEnd(text, index + 2, true);
        if (end === text.length) return { text: result, pending: text.slice(index) };
        index = end;
        continue;
      }
      if (next === "P" || next === "X" || next === "^" || next === "_") {
        const end = controlStringEnd(text, index + 2, false);
        if (end === text.length) return { text: result, pending: text.slice(index) };
        index = end;
        continue;
      }
      if (next === "\\") index += 1;
      else {
        const end = escapeSequenceEnd(text, index + 1);
        if (end === index) return { text: result, pending: text.slice(index) };
        index = end;
      }
      continue;
    }

    if (character === "\u009b") {
      const end = csiEnd(text, index + 1);
      if (end === text.length) return { text: result, pending: text.slice(index) };
      const sequence = `\u001b[${text.slice(index + 1, end + 1)}`;
      if (isSgrSequence(sequence)) result += normalizedSgr(sequence, decorations);
      index = end;
      continue;
    }
    if (character === "\u009d") {
      const end = controlStringEnd(text, index + 1, true);
      if (end === text.length) return { text: result, pending: text.slice(index) };
      index = end;
      continue;
    }
    if (character === "\u0090" || character === "\u0098" || character === "\u009e" || character === "\u009f") {
      const end = controlStringEnd(text, index + 1, false);
      if (end === text.length) return { text: result, pending: text.slice(index) };
      index = end;
      continue;
    }

    if (!isNonTextControl(code)) result += character;
  }
  return { text: result, pending: "" };
}

function presentationSequencesOnly(text: string): string {
  return presentationSequences(text).text;
}

function rgbValue(value: string | null): string | undefined {
  if (value === null) return undefined;
  const match = RGB_CHANNELS.exec(value);
  if (!match) return undefined;
  const channels = match.slice(1).map(Number);
  if (channels.some((channel) => channel < 0 || channel > 255)) return undefined;
  return channels.join(", ");
}

function paletteColor(index: number): string | undefined {
  if (!Number.isInteger(index) || index < 0 || index > 255) return undefined;
  if (index < 16) return undefined;
  if (index < 232) {
    const offset = index - 16;
    const levels = [0, 95, 135, 175, 215, 255];
    const red = levels[Math.floor(offset / 36)];
    const green = levels[Math.floor((offset % 36) / 6)];
    const blue = levels[offset % 6];
    if (red === undefined || green === undefined || blue === undefined) return undefined;
    return `${red}, ${green}, ${blue}`;
  }
  const gray = 8 + (index - 232) * 10;
  return `${gray}, ${gray}, ${gray}`;
}

function parsedColor(value: string | null, truecolor: string | null): AnsiColor | undefined {
  if (value === null) return undefined;
  if (value === "ansi-truecolor") {
    const rgb = rgbValue(truecolor);
    return rgb === undefined ? undefined : { kind: "rgb", value: rgb };
  }
  if (value.startsWith("ansi-palette-")) {
    const rgb = paletteColor(Number(value.slice("ansi-palette-".length)));
    return rgb === undefined ? undefined : { kind: "rgb", value: rgb };
  }
  const name = value.slice("ansi-".length) as AnsiNamedColor;
  return value.startsWith("ansi-") && NAMED_COLORS.has(name) ? { kind: "named", name } : undefined;
}

function runFromBundle(bundle: ParsedAnsiBundle, text: string): AnsiRun {
  const decorations = new Set(bundle.decorations);
  return {
    text,
    foreground: parsedColor(bundle.fg, bundle.fg_truecolor),
    background: parsedColor(bundle.bg, bundle.bg_truecolor),
    bold: decorations.has("bold"),
    dim: decorations.has("dim"),
    italic: decorations.has("italic"),
    underline: decorations.has("underline"),
    hidden: decorations.has("hidden"),
    strikethrough: decorations.has("strikethrough"),
  };
}

function colorCodes(color: AnsiColor, background: boolean): number[] {
  if (color.kind === "named") {
    const foreground = NAMED_FOREGROUND_CODES[color.name];
    return [background ? foreground + 10 : foreground];
  }
  const channels = color.value.split(", ").map(Number);
  return [background ? 48 : 38, 2, ...channels];
}

function presentationSequence(run: AnsiRun): string {
  const codes = [
    run.bold ? 1 : undefined,
    run.dim ? 2 : undefined,
    run.italic ? 3 : undefined,
    run.underline ? 4 : undefined,
    run.hidden ? 8 : undefined,
    run.strikethrough ? 9 : undefined,
    ...(run.foreground === undefined ? [] : colorCodes(run.foreground, false)),
    ...(run.background === undefined ? [] : colorCodes(run.background, true)),
  ].filter((code): code is number => code !== undefined);
  return codes.length === 0 ? "" : `\u001b[${codes.join(";")}m`;
}

function presentationAt(text: string, cut: number): string {
  const parsed = new Anser().ansiToJson(`${text.slice(0, cut)}x`, {
    use_classes: true,
    remove_empty: true,
  }) as ParsedAnsiBundle[];
  const bundle = parsed[parsed.length - 1];
  return bundle === undefined ? "" : presentationSequence(runFromBundle(bundle, ""));
}

function ansiSafeCut(text: string, max: number): number {
  let cut = text.length - max;
  for (let index = 0; index < text.length; index += 1) {
    if (text[index] !== "\u001b" || text[index + 1] !== "[") continue;
    const end = csiEnd(text, index + 2);
    if (end === text.length) break;
    if (index < cut && cut <= end) {
      cut = end + 1;
      break;
    }
    index = end;
  }
  const code = text.charCodeAt(cut);
  return code >= 0xdc00 && code <= 0xdfff ? cut + 1 : cut;
}

interface BoundedAnsiTail {
  text: string;
  pending: string;
  truncated: boolean;
}

function boundedAnsiTail(text: string, max: number): BoundedAnsiTail {
  if (text.length <= max) return { text, pending: "", truncated: false };

  const presentation = presentationSequences(text);
  const safeText = presentation.text;
  if (safeText.length <= max) return { text: safeText, pending: presentation.pending, truncated: false };

  const cut = ansiSafeCut(safeText, max);
  return {
    text: `${presentationAt(safeText, cut)}${safeText.slice(cut)}`,
    pending: presentation.pending,
    truncated: true,
  };
}

function rawTailSlice(text: string, max: number): string {
  if (text.length <= max) return text;
  let cut = text.length - max;
  const code = text.charCodeAt(cut);
  if (code >= 0xdc00 && code <= 0xdfff) cut += 1;
  return text.slice(cut);
}

export interface AnsiTailSnapshot {
  renderedText: string;
  copyText: string;
  truncated: boolean;
}

/**
 * Maintains a bounded presentation tail for an append-only shell output
 * stream. Once the first snapshot is read, each update parses only the
 * previous bounded tail plus the appended delta.
 */
export class AnsiTailBuffer {
  private sourceLength = 0;
  private renderedText = "";
  private copyText = "";
  private pending = "";
  private truncated = false;

  constructor(private readonly max: number) {}

  update(source: string): AnsiTailSnapshot {
    if (source.length < this.sourceLength) this.reset();
    const delta = source.slice(this.sourceLength);
    if (delta !== "") {
      const tail = boundedAnsiTail(`${this.renderedText}${this.pending}${delta}`, this.max);
      this.renderedText = tail.text;
      this.copyText = rawTailSlice(`${this.copyText}${delta}`, this.max);
      this.pending = tail.pending;
      this.truncated = this.truncated || tail.truncated;
      this.sourceLength = source.length;
    }
    return {
      renderedText: this.renderedText,
      copyText: this.copyText,
      truncated: this.truncated,
    };
  }

  private reset() {
    this.sourceLength = 0;
    this.renderedText = "";
    this.copyText = "";
    this.pending = "";
    this.truncated = false;
  }
}

export function parseAnsiLines(text: string): AnsiLine[] {
  const parsed = new Anser().ansiToJson(presentationSequencesOnly(text), {
    use_classes: true,
    remove_empty: true,
  }) as ParsedAnsiBundle[];
  const lines: AnsiLine[] = [[]];

  for (const bundle of parsed) {
    const segments = bundle.content.split("\n");
    for (const [index, segment] of segments.entries()) {
      if (index > 0) lines.push([]);
      if (segment !== "") lines[lines.length - 1]?.push(runFromBundle(bundle, segment));
    }
  }

  return lines;
}
