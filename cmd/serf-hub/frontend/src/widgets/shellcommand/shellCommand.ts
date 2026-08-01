export interface ShellCommandLine {
  text: string;
  indent: number;
}

const SHELL_OPERATORS = ["&&", "||", "|&", "|", ";"] as const;

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

    if (character === "#" && (index === lineStart || raw[index - 1] === " " || raw[index - 1] === "\t")) {
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
