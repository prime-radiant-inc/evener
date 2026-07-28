import Anser from "anser";
import ansiRegex from "ansi-regex";

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

function isSgrSequence(sequence: string): boolean {
  if (!sequence.startsWith("\u001b[") || !sequence.endsWith("m")) return false;
  return Array.from(sequence.slice(2, -1)).every(
    (character) => character === ";" || (character >= "0" && character <= "9"),
  );
}

function presentationalText(text: string): string {
  let result = "";
  for (let index = 0; index < text.length; index += 1) {
    const character = text[index] ?? "";
    const code = character.charCodeAt(0);
    if (character === "\u001b") {
      const end = text.indexOf("m", index + 2);
      const sequence = end === -1 ? "" : text.slice(index, end + 1);
      if (isSgrSequence(sequence)) {
        result += sequence;
        index = end;
      }
      continue;
    }
    const nonTextControl =
      (code >= 0x00 && code <= 0x08) ||
      code === 0x0b ||
      code === 0x0c ||
      (code >= 0x0e && code <= 0x1a) ||
      (code >= 0x1c && code <= 0x1f) ||
      code === 0x7f;
    if (!nonTextControl) result += character;
  }
  return result;
}

function presentationSequencesOnly(text: string): string {
  const csiNormalized = text.replaceAll("\u009b", "\u001b[");
  const withoutTerminalControls = csiNormalized.replace(ansiRegex(), (sequence) =>
    isSgrSequence(sequence) ? sequence : "",
  );
  return presentationalText(withoutTerminalControls);
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
