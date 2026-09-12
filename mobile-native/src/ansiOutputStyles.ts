import type {
  AnsiColor,
  AnsiRun,
} from "../../cmd/evener-hub/frontend/src/widgets/codeblock/ansi";

export interface NativeAnsiTextStyle {
  color?: string;
  backgroundColor?: string;
  fontWeight?: "600";
  opacity?: number;
  fontStyle?: "italic";
  textDecorationLine?: "underline" | "line-through" | "underline line-through";
}

const ANSI_COLORS = {
  dark: {
    black: "#5E6E80",
    red: "#E06C75",
    green: "#4FBF9A",
    yellow: "#E8B04B",
    blue: "#81B4E8",
    magenta: "#A88FD8",
    cyan: "#6BC4D4",
    white: "#E5ECF4",
    "bright-black": "#71839A",
    "bright-red": "#EC858D",
    "bright-green": "#6CD3B1",
    "bright-yellow": "#F0C374",
    "bright-blue": "#9CC6EF",
    "bright-magenta": "#BFA9E6",
    "bright-cyan": "#8AD4E1",
    "bright-white": "#FFFFFF",
  },
  light: {
    black: "#1F2124",
    red: "#A33226",
    green: "#2E6E4E",
    yellow: "#7A5208",
    blue: "#2450B8",
    magenta: "#7A3E9D",
    cyan: "#0E6B6E",
    white: "#62656B",
    "bright-black": "#62656B",
    "bright-red": "#B3382C",
    "bright-green": "#35835F",
    "bright-yellow": "#8A5A0B",
    "bright-blue": "#2E5FD0",
    "bright-magenta": "#8E4EC6",
    "bright-cyan": "#0E7490",
    "bright-white": "#17181A",
  },
} as const;

function colorValue(color: AnsiColor, dark: boolean): string {
  return color.kind === "rgb"
    ? `rgb(${color.value})`
    : ANSI_COLORS[dark ? "dark" : "light"][color.name];
}

export function ansiRunTextStyle(
  run: AnsiRun,
  dark: boolean,
): NativeAnsiTextStyle {
  const decorations = [
    run.underline ? "underline" : undefined,
    run.strikethrough ? "line-through" : undefined,
  ].filter((value): value is string => value !== undefined);
  const style: NativeAnsiTextStyle = {};
  if (run.foreground !== undefined)
    style.color = colorValue(run.foreground, dark);
  if (run.background !== undefined)
    style.backgroundColor = colorValue(run.background, dark);
  if (run.bold) style.fontWeight = "600";
  if (run.dim) style.opacity = 0.65;
  if (run.hidden) style.opacity = 0;
  if (run.italic) style.fontStyle = "italic";
  if (decorations.length > 0) {
    style.textDecorationLine = decorations.join(
      " ",
    ) as NativeAnsiTextStyle["textDecorationLine"];
  }
  return style;
}
