import type { CSSProperties } from "react";
import { requireClass } from "../internal/requireClass";
import type { AnsiColor, AnsiLine, AnsiRun } from "./ansi";
import styles from "./codeblock.module.css";

const CLASS = {
  bold: requireClass(styles.ansiBold, "codeblock.module.css", "ansiBold"),
  dim: requireClass(styles.ansiDim, "codeblock.module.css", "ansiDim"),
  italic: requireClass(styles.ansiItalic, "codeblock.module.css", "ansiItalic"),
  underline: requireClass(styles.ansiUnderline, "codeblock.module.css", "ansiUnderline"),
  hidden: requireClass(styles.ansiHidden, "codeblock.module.css", "ansiHidden"),
  strikethrough: requireClass(styles.ansiStrikethrough, "codeblock.module.css", "ansiStrikethrough"),
};

function colorValue(color: AnsiColor): string {
  return color.kind === "named" ? `var(--ansi-${color.name})` : `rgb(${color.value})`;
}

function runStyle(run: AnsiRun): CSSProperties | undefined {
  if (run.foreground === undefined && run.background === undefined) return undefined;
  return {
    color: run.foreground === undefined ? undefined : colorValue(run.foreground),
    backgroundColor: run.background === undefined ? undefined : colorValue(run.background),
  };
}

function runClasses(run: AnsiRun): string | undefined {
  const classes = [
    run.bold ? CLASS.bold : undefined,
    run.dim ? CLASS.dim : undefined,
    run.italic ? CLASS.italic : undefined,
    run.underline ? CLASS.underline : undefined,
    run.hidden ? CLASS.hidden : undefined,
    run.strikethrough ? CLASS.strikethrough : undefined,
  ].filter((value): value is string => value !== undefined);
  return classes.length === 0 ? undefined : classes.join(" ");
}

function hasPresentation(run: AnsiRun): boolean {
  return (
    run.foreground !== undefined ||
    run.background !== undefined ||
    run.bold ||
    run.dim ||
    run.italic ||
    run.underline ||
    run.hidden ||
    run.strikethrough
  );
}

export function AnsiLineContent({ line }: { line: AnsiLine }) {
  return line.map((run, index) =>
    hasPresentation(run) ? (
      <span
        // biome-ignore lint/suspicious/noArrayIndexKey: parsed runs have no durable identity and remain in source order
        key={index}
        className={runClasses(run)}
        style={runStyle(run)}
        data-ansi-fg={run.foreground?.kind === "named" ? run.foreground.name : undefined}
        data-ansi-bg={run.background?.kind === "named" ? run.background.name : undefined}
        data-ansi-bold={run.bold ? "true" : undefined}
        data-ansi-dim={run.dim ? "true" : undefined}
      >
        {run.text}
      </span>
    ) : (
      run.text
    ),
  );
}
