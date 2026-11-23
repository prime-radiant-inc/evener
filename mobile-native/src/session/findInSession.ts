// Find in session (spec 8.7; ruling 29): which loaded rows hold the words you
// typed, and stepping between them, newest first. It searches what the phone
// has loaded; the screen loads older pages as you step back past the oldest
// match.
import {
	composeStepWords,
	type EvenerDelegateInfo,
	findMatches as findSharedMatches,
	findNeedle,
	type MatchPosition,
	matchPosition,
	type SteeringFragment,
	stepMatch,
	textMatchesFind,
} from "@evener/appwire-client";
import { setDisclosureOpenAll } from "../nativeDisclosure";
import { readerKey } from "../readerPosition";
import { noticeLabel, type RunStep, type TimelineRow } from "../timeline";
import { rowDisclosureIds } from "./disclosureKeys";
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
function stepText(step: Pick<RunStep, "label" | "family" | "detail">): string {
	const { description, words, summary, output } = step.detail;
	const parts = [step.label, description ?? "", words ? composeStepWords(words) : (summary ?? "")];
	if (step.family === "reasoning" && output) parts.push(output);
	return parts.join("\n");
}

// A notification reads as its card, not its markup.
function fragmentText(fragment: SteeringFragment, delegates: readonly EvenerDelegateInfo[] | undefined): string {
	if (fragment.kind === "text") return notificationText(fragment.text);
	const line = notificationLine(fragment.notification, delegates);
	return [line.headline, line.detail, line.output].filter(Boolean).join("\n");
}

/** The words a row shows, or would show when opened. A time marker has none.
 * The session's subagents name the subagents its notifications report on.
 * answersByRow carries the recap an ask_user row renders beneath its
 * questions, which the row itself does not hold: the raw answer row is
 * hidden once the recap stands for it (#6328), so Find must read the
 * rendered answer here or the words would be unsearchable while on screen. */
export function rowText(
	row: TimelineRow,
	delegates?: readonly EvenerDelegateInfo[],
	answersByRow?: ReadonlyMap<string, string>,
): string {
	switch (row.kind) {
		case "user":
		case "note":
			return row.text;
		case "notice":
			return row.notifications
				? row.notifications.map((fragment) => fragmentText(fragment, delegates)).join("\n")
				: [row.label, row.text].filter(Boolean).join("\n");
		case "assistant":
			return row.markdown;
		case "failure":
			return `${row.title}\n${row.detail}`;
		case "question":
			return row.questions.map((question) => `${question.header}\n${question.question}`).join("\n");
		case "details":
			return row.entries.map((entry) => rowText(entry, delegates, answersByRow)).join("\n");
		case "activity": {
			const text = stepText(row);
			const answer = answersByRow?.get(row.id);
			return answer === undefined ? text : `${text}\n${answer}`;
		}
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
	answersByRow?: ReadonlyMap<string, string>,
): number[] {
	return findSharedMatches(rows, query, (row) => rowText(row, delegates, answersByRow));
}

export interface FindMatchTarget {
	key: string;
	rowIndex: number;
	entry?: TimelineRow;
}

/** Addressable match targets in `rows`, oldest first. Quiet-yield groups emit
 * one target per matching wake entry so Find steps between wakes inside the
 * group before asking for older history. */
export function findMatchTargets(
	rows: readonly TimelineRow[],
	query: string,
	delegates?: readonly EvenerDelegateInfo[],
	answersByRow?: ReadonlyMap<string, string>,
): FindMatchTarget[] {
	const needle = findNeedle(query);
	if (needle === "") return [];
	const targets: FindMatchTarget[] = [];
	rows.forEach((row, rowIndex) => {
		if (row.kind === "details" && row.id.startsWith("quiet-yields:")) {
			for (const entry of row.entries) {
				if (textMatchesFind(rowText(entry, delegates, answersByRow), needle)) {
					targets.push({ key: readerKey(entry), rowIndex, entry });
				}
			}
			return;
		}
		if (textMatchesFind(rowText(row, delegates, answersByRow), needle)) {
			targets.push({ key: readerKey(row), rowIndex });
		}
	});
	return targets;
}

function openRowDisclosures(
	hubId: string,
	sessionRef: string,
	row: TimelineRow,
	needle: string,
	delegates: readonly EvenerDelegateInfo[] | undefined,
	answersByRow: ReadonlyMap<string, string> | undefined,
): void {
	if (row.kind === "details") {
		setDisclosureOpenAll(rowDisclosureIds(hubId, sessionRef, row), true);
		for (const entry of row.entries) {
			if (textMatchesFind(rowText(entry, delegates, answersByRow), needle)) {
				openRowDisclosures(hubId, sessionRef, entry, needle, delegates, answersByRow);
			}
		}
		return;
	}
	if (row.kind === "activity") {
		if (row.family === "reasoning" && row.detail.output && textMatchesFind(row.detail.output, needle)) {
			setDisclosureOpenAll(rowDisclosureIds(hubId, sessionRef, row), true);
		}
		return;
	}
	if (row.kind !== "notice") return;
	if (noticeLabel(row) !== undefined && textMatchesFind(row.text, needle)) {
		setDisclosureOpenAll(rowDisclosureIds(hubId, sessionRef, row), true);
	}
	if (row.notifications) {
		const baseId = rowDisclosureIds(hubId, sessionRef, row)[0];
		if (!baseId) return;
		row.notifications.forEach((fragment, index) => {
			if (fragment.kind !== "notification") return;
			const line = notificationLine(fragment.notification, delegates);
			if (line.output && textMatchesFind(line.output, needle)) {
				setDisclosureOpenAll([`${baseId}:${index}`], true);
			}
		});
	}
}

/** Opens any collapsed group/notice disclosures that hold the active Find match. */
export function openDisclosuresForFindMatch(
	hubId: string,
	sessionRef: string,
	row: TimelineRow | undefined,
	query: string,
	delegates?: readonly EvenerDelegateInfo[],
	answersByRow?: ReadonlyMap<string, string>,
	activeEntry?: TimelineRow,
): void {
	if (row === undefined || !hubId || !sessionRef) return;
	const needle = findNeedle(query);
	if (needle === "") return;
	if (activeEntry !== undefined && row.kind === "details") {
		if (!textMatchesFind(rowText(activeEntry, delegates, answersByRow), needle)) return;
		setDisclosureOpenAll(rowDisclosureIds(hubId, sessionRef, row), true);
		openRowDisclosures(hubId, sessionRef, activeEntry, needle, delegates, answersByRow);
		return;
	}
	if (!textMatchesFind(rowText(row, delegates, answersByRow), needle)) return;
	openRowDisclosures(hubId, sessionRef, row, needle, delegates, answersByRow);
}

export { type MatchPosition, matchPosition, stepMatch };
/** The row Find's selection lands on: the key itself, or — when the row that
 * key named is gone because its answer now shows beneath its question — the
 * question row standing in for it. Null only when the key names no row at
 * all, which is the resume case that reloads a saved boundary.
 *
 * Without this, selecting a linked answer whose question sits on an older
 * page loses the selection when that page loads: the answer row is hidden,
 * the selection finds nothing, and Find treats the match as gone and pages
 * past the hits that are still there (#6328). */
export function selectedRowIndex(
	rows: readonly TimelineRow[],
	selectedKey: string | null,
	standIns: ReadonlyMap<string, string>,
): number | null {
	if (selectedKey === null) return null;
	const key = standIns.get(selectedKey) ?? selectedKey;
	const index = rows.findIndex((row) => rowMatchesSelectedKey(row, key));
	return index < 0 ? null : index;
}

function unwrapDetailsKey(key: string): string {
	if (key.startsWith("quiet-yields:")) return key.slice("quiet-yields:".length);
	if (key.startsWith("details:")) return key.slice("details:".length);
	return key;
}

function rowMatchesSelectedKey(row: TimelineRow, key: string): boolean {
	if (readerKey(row) === key) return true;
	if (row.kind === "details") {
		const targetKey = unwrapDetailsKey(key);
		return row.entries.some(
			(entry) =>
				readerKey(entry) === key || entry.id === key || readerKey(entry) === targetKey || entry.id === targetKey,
		);
	}
	return false;
}

/** Resolves `selectedKey` among member-level `targets`, preserving selection
 * across stand-ins, legacy group keys (`quiet-yields:<id>`), and rows
 * holding multiple hits. */
export function selectedTargetIndex(
	rows: readonly TimelineRow[],
	targets: readonly FindMatchTarget[],
	selectedKey: string | null,
	standIns: ReadonlyMap<string, string>,
): number | null {
	if (selectedKey === null) return null;
	const key = standIns.get(selectedKey) ?? selectedKey;
	const exact = targets.findIndex((target) => target.key === key);
	if (exact >= 0) return exact;
	const targetKey = unwrapDetailsKey(key);
	if (targetKey !== key) {
		const byMember = targets.findIndex((target) => target.key === targetKey || target.entry?.id === targetKey);
		if (byMember >= 0) return byMember;
	}
	const rowIndex = selectedRowIndex(rows, selectedKey, standIns);
	if (rowIndex === null) return null;
	let inRow = -1;
	targets.forEach((target, index) => {
		if (target.rowIndex === rowIndex) inRow = index;
	});
	if (inRow >= 0) return inRow;
	let atOrBefore = -1;
	targets.forEach((target, index) => {
		if (target.rowIndex <= rowIndex) atOrBefore = index;
	});
	return atOrBefore >= 0 ? atOrBefore : targets.length > 0 ? 0 : null;
}

/** Where Find's selection sits among the matches: the selected row when it
 * is itself a hit, and otherwise the newest hit at or before it.
 *
 * A stand-in row is not necessarily a hit — the question row shows the
 * answer, but its own searchable words are the questions and the recap, which
 * need not contain the query (a composed answer's `[answers]` framing and
 * numbering are not in the recap). Reporting a non-hit as the current match
 * made the label read "0 of N"; anchoring on the nearest hit keeps the count
 * honest while the reader is still looking at the row that carries the words. */
export function selectedMatch(
	rows: readonly TimelineRow[],
	hits: readonly number[],
	selectedKey: string | null,
	standIns: ReadonlyMap<string, string>,
): number | null {
	const index = selectedRowIndex(rows, selectedKey, standIns);
	if (index === null) return null;
	if (hits.includes(index)) return index;
	return hits.findLast((hit) => hit <= index) ?? hits[0] ?? null;
}

/** Where you are among the matches, "2 of 3", or "No matches". */
export function matchLabel(matches: readonly number[], current: number | null): string {
	const { position, count } = matchPosition(matches, current);
	return count === 0 ? "No matches" : `${position} of ${count}`;
}
