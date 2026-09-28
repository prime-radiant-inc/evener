// Find in session (spec 8.7; ruling 29): which loaded rows hold the words you
// typed, and stepping between them, newest first. It searches what the phone
// has loaded; the screen loads older pages as you step back past the oldest
// match.
import type { RunStep, TimelineRow } from "../timeline";
import { stepTarget } from "./transcriptRows";

function stepText(step: RunStep): string {
	return `${step.label}\n${step.detail.description ?? ""}\n${stepTarget(step.label, step.detail.arguments) ?? ""}`;
}

/** The words a row shows, or would show when opened. A time marker has none. */
export function rowText(row: TimelineRow): string {
	switch (row.kind) {
		case "user":
		case "note":
		case "notice":
			return row.text;
		case "assistant":
			return row.markdown;
		case "failure":
			return `${row.title}\n${row.detail}`;
		case "question":
			return row.questions.map((question) => `${question.header}\n${question.question}`).join("\n");
		case "details":
			return row.entries.map((entry) => entry.text).join("\n");
		case "activity":
			return stepText(row);
		case "run":
			return row.steps.map(stepText).join("\n");
		case "attachments":
			return row.items.map((item) => item.name ?? "").join("\n");
		case "time":
			return "";
	}
}

/** The indexes of the rows holding `query`, ignoring case, oldest first. */
export function findMatches(rows: readonly TimelineRow[], query: string): number[] {
	const needle = query.trim().toLowerCase();
	if (!needle) return [];
	const matches: number[] = [];
	rows.forEach((row, index) => {
		if (rowText(row).toLowerCase().includes(needle)) matches.push(index);
	});
	return matches;
}

/** The match to show next. With none shown yet it starts at the newest, since
 * you are usually at the end. Stepping back (-1) goes older and forward (1)
 * newer. Null means there is none that way: stepping back, the screen then
 * loads older history and tries again. */
export function stepMatch(matches: readonly number[], current: number | null, direction: 1 | -1): number | null {
	if (matches.length === 0) return null;
	if (current === null) return matches[matches.length - 1] ?? null;
	if (direction === 1) return matches.find((index) => index > current) ?? null;
	return matches.findLast((index) => index < current) ?? null;
}

/** Where you are among the matches, "2 of 3", or "No matches". */
export function matchLabel(matches: readonly number[], current: number | null): string {
	if (matches.length === 0) return "No matches";
	const position = current === null ? matches.length : matches.indexOf(current) + 1;
	return `${position} of ${matches.length}`;
}
