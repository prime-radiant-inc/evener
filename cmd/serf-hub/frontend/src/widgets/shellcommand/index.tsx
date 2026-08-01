import type { JSX } from "react";
import { CodeBlock } from "../codeblock";
import { formatShellCommand, tokenizeShellCommand } from "./shellCommand";
import styles from "./shellcommand.module.css";

export interface ShellCommandBlockProps {
  command: string;
}

export function ShellCommandBlock({ command }: ShellCommandBlockProps): JSX.Element {
  const lines = formatShellCommand(command);
  const tokens = tokenizeShellCommand(lines);
  const displayText = lines.map((line) => line.text).join("\n");

  return (
    <CodeBlock
      text={displayText}
      copyText={command}
      copyLabel="Copy command"
      language="bash"
      renderLine={(line, lineNumber) => {
        const layout = lines[lineNumber];
        const lineTokens = tokens[lineNumber] ?? [{ text: line, kind: "plain" as const }];
        let tokenStart = 0;
        return (
          <span style={{ paddingInlineStart: `${layout?.indent ?? 0}ch` }}>
            {lineTokens.map((part) => {
              const key = `${lineNumber}-${tokenStart}`;
              tokenStart += part.text.length;
              return (
                <span
                  key={key}
                  className={part.kind === "plain" ? undefined : styles[part.kind]}
                  data-shell-token-kind={part.kind}
                >
                  {part.text}
                </span>
              );
            })}
          </span>
        );
      }}
    />
  );
}
