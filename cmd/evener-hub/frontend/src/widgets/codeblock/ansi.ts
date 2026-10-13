import { forEachJobOutputScalar } from "@evener/appwire-client";
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

const DECORATION_RESET = new Map([
  [23, 3],
  [24, 4],
  [27, 7],
  [28, 8],
  [29, 9],
]);

interface SgrState {
  decorations: Set<number>;
  foreground?: number[];
  background?: number[];
}

function defaultSgrState(): SgrState {
  return { decorations: new Set() };
}

function cloneSgrState(state: SgrState): SgrState {
  return {
    decorations: new Set(state.decorations),
    foreground: state.foreground?.slice(),
    background: state.background?.slice(),
  };
}

function isSgrSequence(sequence: string): boolean {
  if (!sequence.startsWith("\u001b[") || !sequence.endsWith("m")) return false;
  return Array.from(sequence.slice(2, -1)).every(
    (character) => character === ";" || (character >= "0" && character <= "9"),
  );
}

function normalizedSgr(sequence: string, state: SgrState): string {
  const rawParameters = sequence.slice(2, -1).split(";");
  const parameters = rawParameters.map((parameter) => (parameter === "" ? 0 : Number(parameter)));
  const normalized: Array<number | string> = [];

  for (let index = 0; index < parameters.length; index += 1) {
    const code = parameters[index] ?? 0;
    if ((code === 38 || code === 48) && (parameters[index + 1] === 2 || parameters[index + 1] === 5)) {
      const truecolor = parameters[index + 1] === 2;
      const colorLength = truecolor ? 5 : 3;
      const color = parameters.slice(index, index + colorLength);
      const rawColor = rawParameters.slice(index, index + colorLength);
      if (color.length < colorLength) {
        normalized.push(rawParameters[index] ?? code, rawParameters[index + 1] ?? "");
        index += 1;
        continue;
      }
      const channels = truecolor ? color.slice(2) : color.slice(2, 3);
      const rawChannels = truecolor ? rawColor.slice(2) : rawColor.slice(2, 3);
      const validColor =
        rawChannels.every((channel) => channel !== "") &&
        channels.every((channel) => Number.isInteger(channel) && channel >= 0 && channel <= 255);
      if (validColor) {
        if (code === 38) state.foreground = color;
        else state.background = color;
      }
      normalized.push(...rawColor);
      index += colorLength - 1;
      continue;
    }
    if (code === 0) {
      state.decorations.clear();
      state.foreground = undefined;
      state.background = undefined;
      normalized.push(code);
      continue;
    }
    if ((code >= 30 && code <= 37) || (code >= 90 && code <= 97)) {
      state.foreground = [code];
      normalized.push(code);
      continue;
    }
    if (code === 39) {
      state.foreground = undefined;
      normalized.push(code);
      continue;
    }
    if ((code >= 40 && code <= 47) || (code >= 100 && code <= 107)) {
      state.background = [code];
      normalized.push(code);
      continue;
    }
    if (code === 49) {
      state.background = undefined;
      normalized.push(code);
      continue;
    }
    if (code === 21) {
      state.decorations.delete(1);
      normalized.push(code);
      continue;
    }
    if (code === 22) {
      state.decorations.delete(1);
      state.decorations.delete(2);
      normalized.push(code);
      continue;
    }
    const resetEnable = DECORATION_RESET.get(code);
    if (resetEnable !== undefined) {
      state.decorations.delete(resetEnable);
      normalized.push(code);
      continue;
    }
    if (code === 1 || code === 2 || code === 3 || code === 4 || code === 7 || code === 8 || code === 9) {
      if (!state.decorations.has(code)) {
        state.decorations.add(code);
        normalized.push(code);
      }
      continue;
    }
    normalized.push(code);
  }

  return normalized.length === 0 ? "" : `\u001b[${normalized.join(";")}m`;
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

function sgrSequence(state: SgrState): string {
  const decorations = [1, 2, 3, 4, 7, 8, 9].filter((code) => state.decorations.has(code));
  const codes = [...decorations, ...(state.foreground ?? []), ...(state.background ?? [])];
  return codes.length === 0 ? "" : `\u001b[${codes.join(";")}m`;
}

type ControlState =
  | { kind: "text" }
  | { kind: "escape"; intermediate: boolean }
  | { kind: "csi"; sequence: string; overflow: boolean }
  | { kind: "osc"; escape: boolean }
  | { kind: "string"; escape: boolean };

interface TerminalState {
  sgr: SgrState;
  control: ControlState;
}

export type AnsiByteState = TerminalState;
export type AnsiByteRow = {
  offsetBytes: number;
  endBytes: number;
  line: AnsiLine;
  /** Original byte offsets within this bounded fragment, one per visible UTF16 unit. */
  textByteOffsets: Uint16Array;
};
export type AnsiByteRows = {
  rows: AnsiByteRow[];
  endState: AnsiByteState;
  boundaries: Array<{ offsetBytes: number; state: AnsiByteState }>;
};

const MAX_CSI_SEQUENCE = 128;

function defaultTerminalState(): TerminalState {
  return { sgr: defaultSgrState(), control: { kind: "text" } };
}

function cloneTerminalState(state: TerminalState): TerminalState {
  return { sgr: cloneSgrState(state.sgr), control: { ...state.control } };
}

// Every control variant is a flat record of primitives, and sgrSequence is the
// canonical form the parser renders a style from, so equal states render alike.
function terminalStatesEqual(left: TerminalState, right: TerminalState): boolean {
  const control = left.control as Record<string, unknown>;
  const other = right.control as Record<string, unknown>;
  const keys = Object.keys(control);
  return (
    keys.length === Object.keys(other).length &&
    keys.every((key) => control[key] === other[key]) &&
    sgrSequence(left.sgr) === sgrSequence(right.sgr)
  );
}

function scanTerminalText(text: string, state: TerminalState, emit: (text: string) => void) {
  let index = 0;
  while (index < text.length) {
    const character = text[index] ?? "";
    const code = character.charCodeAt(0);

    if (state.control.kind === "osc" || state.control.kind === "string") {
      if ((state.control.kind === "osc" && character === "\u0007") || character === "\u009c") {
        state.control = { kind: "text" };
      } else if (state.control.escape && character === "\\") {
        state.control = { kind: "text" };
      } else if (character === "\u001b") {
        state.control.escape = true;
      } else {
        state.control.escape = false;
      }
      index += 1;
      continue;
    }

    if (state.control.kind === "escape") {
      if (!state.control.intermediate && character === "[") {
        state.control = { kind: "csi", sequence: "\u001b[", overflow: false };
      } else if (!state.control.intermediate && character === "]") state.control = { kind: "osc", escape: false };
      else if (
        !state.control.intermediate &&
        (character === "P" || character === "X" || character === "^" || character === "_")
      ) {
        state.control = { kind: "string", escape: false };
      } else if (code >= 0x20 && code <= 0x2f) {
        state.control.intermediate = true;
        index += 1;
        continue;
      } else if (code >= 0x30 && code <= 0x7e) state.control = { kind: "text" };
      else {
        state.control = { kind: "text" };
        continue;
      }
      index += 1;
      continue;
    }

    if (state.control.kind === "csi") {
      if (character === "\u001b") {
        state.control = { kind: "escape", intermediate: false };
        index += 1;
        continue;
      }
      if (code === 0x18 || code === 0x1a) {
        state.control = { kind: "text" };
        index += 1;
        continue;
      }
      if (code >= 0x40 && code <= 0x7e) {
        const sequence = `${state.control.sequence}${character}`;
        if (!state.control.overflow && character === "m" && isSgrSequence(sequence)) {
          emit(normalizedSgr(sequence, state.sgr));
        }
        state.control = { kind: "text" };
        index += 1;
        continue;
      }
      if (!isNonTextControl(code)) {
        if (state.control.sequence.length < MAX_CSI_SEQUENCE) state.control.sequence += character;
        else state.control.overflow = true;
      }
      index += 1;
      continue;
    }

    if (character === "\u001b") state.control = { kind: "escape", intermediate: false };
    else if (character === "\u009b") state.control = { kind: "csi", sequence: "\u001b[", overflow: false };
    else if (character === "\u009d") state.control = { kind: "osc", escape: false };
    else if (character === "\u0090" || character === "\u0098" || character === "\u009e" || character === "\u009f") {
      state.control = { kind: "string", escape: false };
    } else if (!isNonTextControl(code)) emit(character);
    index += 1;
  }
}

function presentationSequencesOnly(text: string): string {
  const presentation: string[] = [];
  scanTerminalText(text, defaultTerminalState(), (part) => presentation.push(part));
  return presentation.join("");
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
  private boundary = defaultTerminalState();
  private truncated = false;

  constructor(private readonly max: number) {}

  update(source: string): AnsiTailSnapshot {
    if (source.length < this.sourceLength) this.reset();
    const delta = source.slice(this.sourceLength);
    if (delta !== "") {
      const raw = `${this.copyText}${delta}`;
      let cut = Math.max(0, raw.length - this.max);
      const code = raw.charCodeAt(cut);
      if (code >= 0xdc00 && code <= 0xdfff) cut += 1;

      const boundary = cloneTerminalState(this.boundary);
      scanTerminalText(raw.slice(0, cut), boundary, () => undefined);
      this.boundary = boundary;
      this.copyText = raw.slice(cut);
      this.truncated = this.truncated || cut > 0;

      const rendered: string[] = [sgrSequence(boundary.sgr)];
      scanTerminalText(this.copyText, cloneTerminalState(boundary), (part) => rendered.push(part));
      this.renderedText = rendered.join("");
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
    this.boundary = defaultTerminalState();
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

export const ANSI_BYTE_FRAGMENT_BYTES = 4096;

// The index of the boundary at offsetBytes, or -1. Boundaries strictly increase.
function boundaryIndexAt(boundaries: AnsiByteRows["boundaries"], offsetBytes: number): number {
  let low = 0;
  let high = boundaries.length - 1;
  while (low <= high) {
    const middle = (low + high) >> 1;
    const found = boundaries[middle]?.offsetBytes ?? 0;
    if (found === offsetBytes) return middle;
    if (found < offsetBytes) low = middle + 1;
    else high = middle - 1;
  }
  return -1;
}

// Display fragments and eviction checkpoints keep the original byte spans,
// including malformed UTF8 and control strings that emit no visible text.
//
// `earlier` holds parses of byte ranges that overlap or adjoin this one with
// identical bytes where they overlap, so a window that gains a page parses
// only what changed. Parsing resumed from a boundary with that boundary's
// state reproduces everything after it, so whenever this parse reaches an
// earlier parse's boundary in an equal state, it copies that parse's rows up
// to its last settled boundary inside this range and scans on from there. An
// earlier parse's final boundary is not settled: bytes past it can complete a
// trailing scalar or extend its last fragment.
export function parseAnsiByteRows(
  bytes: Uint8Array,
  offsetBytes: number,
  initialState?: AnsiByteState,
  earlier: ReadonlyArray<Pick<AnsiByteRows, "rows" | "boundaries">> = [],
): AnsiByteRows {
  let state = initialState === undefined ? defaultTerminalState() : cloneTerminalState(initialState);
  const endOffset = offsetBytes + bytes.length;
  const rows: AnsiByteRow[] = [];
  const boundaries: AnsiByteRows["boundaries"] = [{ offsetBytes, state: cloneTerminalState(state) }];
  let fragmentStart = offsetBytes;
  let prefix = sgrSequence(state.sgr);
  let presentation = "";
  let textByteOffsets: number[] = [];
  // Copies an earlier parse's settled rows from the boundary just reached,
  // leaving fragmentStart and state at the last of them. Reports whether it did.
  const reuse = (): boolean => {
    for (const parse of earlier) {
      const from = boundaryIndexAt(parse.boundaries, fragmentStart);
      const reached = parse.boundaries[from];
      if (reached === undefined || !terminalStatesEqual(reached.state, state)) continue;
      let to = parse.boundaries.length - 2;
      while (to > from && (parse.boundaries[to]?.offsetBytes ?? endOffset) > endOffset) to--;
      const resume = parse.boundaries[to];
      if (to <= from || resume === undefined) continue;
      for (const row of parse.rows)
        if (row.offsetBytes >= fragmentStart && row.endBytes <= resume.offsetBytes) rows.push(row);
      for (let index = from + 1; index <= to; index++) boundaries.push(parse.boundaries[index] ?? resume);
      state = cloneTerminalState(resume.state);
      fragmentStart = resume.offsetBytes;
      prefix = sgrSequence(state.sgr);
      return true;
    }
    return false;
  };
  const flush = (endBytes: number, newline: boolean) => {
    // Without visible text the presentation is only SGR codes, which render
    // no runs; skipping Anser keeps blank-line-heavy output cheap.
    const line =
      textByteOffsets.length === 0
        ? []
        : (parseAnsiLines(prefix + (newline ? presentation.slice(0, -1) : presentation))[0] ?? []);
    if (newline || line.some((run) => run.text.length !== 0))
      rows.push({ offsetBytes: fragmentStart, endBytes, line, textByteOffsets: Uint16Array.from(textByteOffsets) });
    fragmentStart = endBytes;
    prefix = sgrSequence(state.sgr);
    presentation = "";
    textByteOffsets = [];
    boundaries.push({ offsetBytes: endBytes, state: cloneTerminalState(state) });
  };
  const settle = (endBytes: number, newline: boolean): boolean => {
    flush(endBytes, newline);
    return reuse();
  };
  // Each pass scans from fragmentStart until a flush lands on reusable rows;
  // the next pass resumes after them.
  reuse();
  let resumed: boolean;
  do {
    const base = fragmentStart;
    resumed = false;
    forEachJobOutputScalar(bytes.subarray(base - offsetBytes), (value, start, end) => {
      const absoluteStart = base + start;
      const absoluteEnd = base + end;
      if (absoluteEnd - fragmentStart > ANSI_BYTE_FRAGMENT_BYTES && absoluteStart > fragmentStart) {
        resumed = settle(absoluteStart, false);
        if (resumed) return false;
      }
      let newline = false;
      scanTerminalText(value, state, (part) => {
        presentation += part;
        if (part.endsWith("\n")) newline = true;
        else if (!part.startsWith("\u001b["))
          for (let index = 0; index < part.length; index++) textByteOffsets.push(absoluteStart - fragmentStart);
      });
      if (newline) {
        resumed = settle(absoluteEnd, true);
        if (resumed) return false;
      }
    });
  } while (resumed);
  if (fragmentStart < endOffset) flush(endOffset, false);
  return { rows, endState: cloneTerminalState(state), boundaries };
}
