// Find in session (spec 8.7; ruling 29): which loaded rows hold the words you
// typed, and stepping between them, newest first. It searches what the phone
// has loaded; the screen loads older pages as you step back past the oldest
// match.
import {
	composeStepWords,
	type EvenerDelegateInfo,
	findMatches as findSharedMatches,
	type MatchPosition,
	matchPosition,
	type SteeringFragment,
	stepMatch,
} from "@evener/appwire-client";
import type { RunStep, TimelineRow } from "../timeline";
import { notificationLine, notificationText } from "./notificationLine";
/** Find in session while it's open: what you typed, the current match's
 * reader key, whether older history is being searched, and whether that
 * search reached the start of history with nothing older. */
export interface FindState {
	query: string;
	key: string | null;
	seeking: boolean;
	exhausted: boolean;
}

/** A new query starts a new search: no current match yet, and older history
 * is searched only when there is something to look for. */
export function newFind(query: string): FindState {
	return { query, key: null, seeking: query.trim() !== "", exhausted: false };
}

// A step's intent and its words, which name what it acted on: a file, a
// command, a fetched page's URL, a search's query. An intent row keeps its
// words but not their composed summary, and its line shows their target.
function stepText(step: RunStep): string {
	const { description, words, summary } = step.detail;
	return [step.label, description ?? "", words ? composeStepWords(words) : (summary ?? "")].join("\n");
}

// A notification reads as its card, not its markup.
function fragmentText(fragment: SteeringFragment, delegates: readonly EvenerDelegateInfo[] | undefined): string {
	if (fragment.kind === "text") return notificationText(fragment.text);
	const line = notificationLine(fragment.notification, delegates);
	return [line.headline, line.detail, line.output].filter(Boolean).join("\n");
}

/** The words a row shows, or would show when opened. A time marker has none.
 * The session's subagents name the subagents its notifications report on. */
export function rowText(row: TimelineRow, delegates?: readonly EvenerDelegateInfo[]): string {
	switch (row.kind) {
		case "user":
		case "note":
			return row.text;
		case "notice":
			return row.notifications
				? row.notifications.map((fragment) => fragmentText(fragment, delegates)).join("\n")
				: row.text;
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

/** The indexes of the rows holding `query`, oldest first. What counts as a
 * match is the shared rule every client uses. */
export function findMatches(
	rows: readonly TimelineRow[],
	query: string,
	delegates?: readonly EvenerDelegateInfo[],
): number[] {
	return findSharedMatches(rows, query, (row) => rowText(row, delegates));
}

export { type MatchPosition, matchPosition, stepMatch };

/** Where you are among the matches, "2 of 3", or "No matches". */
export function matchLabel(matches: readonly number[], current: number | null): string {
	const { position, count } = matchPosition(matches, current);
	return count === 0 ? "No matches" : `${position} of ${count}`;
}
