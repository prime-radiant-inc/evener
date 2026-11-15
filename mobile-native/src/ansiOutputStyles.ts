import { type AnsiColor, type AnsiLine, type AnsiRun, parseAnsiLines } from "@evener/appwire-client";
import { palettes } from "@evener/appwire-client/palette";

export interface NativeAnsiTextStyle {
	color?: string;
	backgroundColor?: string;
	fontWeight?: "600";
	opacity?: number;
	fontStyle?: "italic";
	textDecorationLine?: "underline" | "line-through" | "underline line-through";
}

function colorValue(color: AnsiColor, dark: boolean): string {
	return color.kind === "rgb" ? `rgb(${color.value})` : palettes[dark ? "dark" : "light"][`ansi-${color.name}`];
}

export function ansiRunTextStyle(run: AnsiRun, dark: boolean): NativeAnsiTextStyle {
	const decorations = [run.underline ? "underline" : undefined, run.strikethrough ? "line-through" : undefined].filter(
		(value): value is string => value !== undefined,
	);
	const style: NativeAnsiTextStyle = {};
	if (run.foreground !== undefined) style.color = colorValue(run.foreground, dark);
	if (run.background !== undefined) style.backgroundColor = colorValue(run.background, dark);
	if (run.bold) style.fontWeight = "600";
	if (run.dim) style.opacity = 0.65;
	if (run.hidden) style.opacity = 0;
	if (run.italic) style.fontStyle = "italic";
	if (decorations.length > 0) {
		style.textDecorationLine = decorations.join(" ") as NativeAnsiTextStyle["textDecorationLine"];
	}
	return style;
}

// A terminal's tab stops: every 8 columns.
const TAB_STOP = 8;

// Text with each tab expanded to spaces up to the next tab stop, starting at
// column; the column after it too. Tab stops count code points, one column
// each, which is how the phone draws Menlo: no character there takes two
// columns.
function expandTabsFrom(text: string, column: number): { text: string; column: number } {
	let expanded = "";
	let at = column;
	for (const char of text) {
		if (char === "\t") {
			const spaces = TAB_STOP - (at % TAB_STOP);
			expanded += " ".repeat(spaces);
			at += spaces;
		} else {
			expanded += char;
			at += 1;
		}
	}
	return { text: expanded, column: at };
}

/** A line with each tab expanded to spaces up to the next tab stop, as a
 * terminal sets it. A native Text draws a tab with no width ("tree.go\t32"
 * read "tree.go32"). */
export function expandTabs(line: string): string {
	return line.includes("\t") ? expandTabsFrom(line, 0).text : line;
}

/** A parsed output line with its tabs expanded (expandTabs). The column
 * carries across the line's runs, so a tab after colored text still lands on
 * the stop. */
export function expandLineTabs(line: AnsiLine): AnsiLine {
	let column = 0;
	return line.map((run) => {
		if (!run.text.includes("\t")) {
			column += [...run.text].length;
			return run;
		}
		const expanded = expandTabsFrom(run.text, column);
		column = expanded.column;
		return { ...run, text: expanded.text };
	});
}

/** Output text as the lines a native Text can draw: parsed for ANSI styling,
 * each tab expanded to its stop (expandLineTabs). Every output view parses
 * through here, so none draws a tab with no width. */
export function parseOutputLines(text: string): AnsiLine[] {
	return parseAnsiLines(text).map(expandLineTabs);
}
