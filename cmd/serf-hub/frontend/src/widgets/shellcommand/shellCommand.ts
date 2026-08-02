export interface ShellCommandLine {
  text: string;
  indent: number;
}

export type ShellCommandTokenKind = "plain" | "command" | "operator" | "string" | "variable" | "flag" | "comment";

export interface ShellCommandToken {
  text: string;
  kind: ShellCommandTokenKind;
}

const SHELL_OPERATORS = [";;&", "&&", "||", "|&", ";;", ";&", "|", ";"] as const;
const CONTROL_OPERATORS = new Set<string>(SHELL_OPERATORS);
const TOKEN_OPERATORS = [
  ";;&",
  "&&",
  "||",
  "|&",
  ";;",
  ";&",
  "&>>",
  ">|",
  ">>",
  "<<-",
  "<<<",
  "<<",
  "<&",
  "<>",
  ">&",
  "&>",
  "|",
  ";",
  ">",
  "<",
];

function isEscaped(raw: string, index: number): boolean {
  let backslashes = 0;
  for (let cursor = index - 1; cursor >= 0 && raw[cursor] === "\\"; cursor -= 1) backslashes += 1;
  return backslashes % 2 === 1;
}

function startsComment(raw: string, index: number, lineStart: number): boolean {
  if (index === lineStart) return true;
  const previous = raw[index - 1];
  return (previous === " " || previous === "\t") && !isEscaped(raw, index - 1);
}

function operatorAt(raw: string, index: number): string | undefined {
  return SHELL_OPERATORS.find((operator) => raw.startsWith(operator, index));
}

function syntheticBoundaryEnd(raw: string, end: number): number | undefined {
  let next = end;
  while (next < raw.length && (raw[next] === " " || raw[next] === "\t")) next += 1;
  if (next === raw.length || raw[next] === "\n") return undefined;
  if (raw[next] === "\\" && raw[next + 1] === "\n") return undefined;
  return next;
}

export function formatShellCommand(raw: string): ShellCommandLine[] {
  const lines: ShellCommandLine[] = [];
  let lineStart = 0;
  let continuationIndent = 0;
  let quote: "'" | '"' | "`" | undefined;
  let escaped = false;
  let comment = false;
  const depth: string[] = [];

  const appendLine = (end: number, indent: number) => {
    lines.push({ text: raw.slice(lineStart, end), indent });
    lineStart = end + 1;
  };

  let index = 0;
  while (index < raw.length) {
    const character = raw[index];

    if (character === "\n") {
      appendLine(index, continuationIndent);
      continuationIndent = 0;
      comment = false;
      escaped = false;
      index += 1;
      continue;
    }

    if (comment) {
      index += 1;
      continue;
    }

    if (quote !== undefined) {
      if (quote === "'") {
        if (character === quote) quote = undefined;
        index += 1;
        continue;
      }
      if (escaped) {
        escaped = false;
        index += 1;
        continue;
      }
      if (character === "\\") {
        escaped = true;
        index += 1;
        continue;
      }
      if (character === quote) quote = undefined;
      index += 1;
      continue;
    }

    if (escaped) {
      escaped = false;
      index += 1;
      continue;
    }

    if (character === "\\") {
      escaped = true;
      index += 1;
      continue;
    }

    if (character === "'" || character === '"' || character === "`") {
      quote = character;
      index += 1;
      continue;
    }

    if (character === "#" && startsComment(raw, index, lineStart)) {
      comment = true;
      index += 1;
      continue;
    }

    if (character === "(" || character === "{") {
      depth.push(character);
      index += 1;
      continue;
    }

    const opener = depth.at(-1);
    if ((character === ")" && opener === "(") || (character === "}" && opener === "{")) {
      depth.pop();
      index += 1;
      continue;
    }

    if (depth.length === 0) {
      const operator = operatorAt(raw, index);
      if (operator !== undefined) {
        const end = index + operator.length;
        const boundary = syntheticBoundaryEnd(raw, end);
        if (boundary !== undefined) {
          lines.push({ text: raw.slice(lineStart, boundary), indent: continuationIndent });
          lineStart = boundary;
          continuationIndent = 2;
          index = boundary;
          continue;
        }
        index = end;
        continue;
      }
    }

    index += 1;
  }

  lines.push({ text: raw.slice(lineStart), indent: continuationIndent });
  return lines;
}

function token(kind: ShellCommandTokenKind, text: string): ShellCommandToken {
  return { kind, text };
}

function tokenOperatorAt(raw: string, index: number): string | undefined {
  return TOKEN_OPERATORS.find((operator) => raw.startsWith(operator, index));
}

function variableEnd(raw: string, index: number): number {
  if (raw[index + 1] === "{") {
    const close = raw.indexOf("}", index + 2);
    return close === -1 ? raw.length : close + 1;
  }
  if (raw[index + 1] === "?") return index + 2;
  let end = index + 1;
  while (/[A-Za-z0-9_]/.test(raw[end] ?? "")) end += 1;
  return end === index + 1 ? index + 1 : end;
}

function isAssignmentWord(text: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*=/.test(text);
}

/**
 * Decorates a command for display without parsing or normalizing shell input.
 * Every returned token is a direct slice of its formatted source line.
 */
export function tokenizeShellCommand(lines: readonly ShellCommandLine[]): ShellCommandToken[][] {
  let quote: "'" | '"' | "`" | undefined;
  let quoteOpening = false;
  let escaped = false;

  return lines.map(({ text }) => {
    const tokens: ShellCommandToken[] = [];
    let index = 0;
    let expectCommand = true;

    while (index < text.length) {
      const character = text[index] ?? "";
      if (/\s/.test(character)) {
        const start = index;
        while (/\s/.test(text[index] ?? "")) index += 1;
        tokens.push(token("plain", text.slice(start, index)));
        continue;
      }

      if (quote !== undefined) {
        let segmentStart = index;
        while (index < text.length) {
          const quoted = text[index] ?? "";
          if (quoteOpening) {
            quoteOpening = false;
            index += 1;
            continue;
          }
          if (quote !== "'" && !escaped && quoted === "$") {
            if (segmentStart < index) tokens.push(token("string", text.slice(segmentStart, index)));
            const end = variableEnd(text, index);
            tokens.push(token("variable", text.slice(index, end)));
            index = end;
            segmentStart = end;
            continue;
          }
          if (quote !== "'" && escaped) {
            escaped = false;
            index += 1;
            continue;
          }
          if (quote !== "'" && quoted === "\\") {
            escaped = true;
            index += 1;
            continue;
          }
          index += 1;
          if (quoted === quote) {
            quote = undefined;
            break;
          }
        }
        if (segmentStart < index) tokens.push(token("string", text.slice(segmentStart, index)));
        continue;
      }

      if (character === "#" && startsComment(text, index, 0)) {
        tokens.push(token("comment", text.slice(index)));
        break;
      }

      const operator = tokenOperatorAt(text, index);
      if (operator !== undefined) {
        tokens.push(token("operator", operator));
        if (CONTROL_OPERATORS.has(operator)) expectCommand = true;
        index += operator.length;
        continue;
      }

      if (character === "'" || character === '"' || character === "`") {
        quote = character;
        quoteOpening = true;
        escaped = false;
        continue;
      }

      if (character === "$") {
        const end = variableEnd(text, index);
        tokens.push(token("variable", text.slice(index, end)));
        index = end;
        continue;
      }

      const start = index;
      while (index < text.length) {
        const wordCharacter = text[index] ?? "";
        if (
          /\s/.test(wordCharacter) ||
          tokenOperatorAt(text, index) !== undefined ||
          wordCharacter === "$" ||
          wordCharacter === "'" ||
          wordCharacter === '"' ||
          wordCharacter === "`"
        ) {
          break;
        }
        if (wordCharacter === "\\") index += 1;
        index += 1;
      }
      const word = text.slice(start, index);
      const kind: ShellCommandTokenKind = word.startsWith("-")
        ? "flag"
        : expectCommand && !isAssignmentWord(word)
          ? "command"
          : "plain";
      tokens.push(token(kind, word));
      if (expectCommand && !isAssignmentWord(word)) expectCommand = false;
    }

    return tokens;
  });
}
