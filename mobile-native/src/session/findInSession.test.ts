import { type EvenerDelegateInfo, parseSteeringNotifications } from "@evener/appwire-client";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { describe, expect, it } from "vitest";
import type { TimelineRow } from "../timeline";
import {
	findMatches,
	findMatchTargets,
	matchLabel,
	matchPosition,
	rowText,
	selectedMatch,
	selectedRowIndex,
	selectedTargetIndex,
	stepMatch,
} from "./findInSession";

const rows: TimelineRow[] = [
	{ kind: "time", id: "time:turn_1", turnId: "turn_1", at: 0 },
	{ kind: "user", id: "u", text: "Fix the flaky Settle test" },
	{ kind: "assistant", id: "a", markdown: "The race is in **settle**.", streaming: false },
	{
		kind: "run",
		id: "run:s",
		steps: [
			{
				kind: "activity",
				id: "s",
				label: "shell",
				family: "tool",
				state: "failed",
				detail: {
					description: "Run the tests",
					arguments: JSON.stringify({ command: "go test ./agent/..." }),
					summary: "Ran go test ./agent/...",
				},
			},
		],
	},
	{ kind: "failure", id: "f", title: "Failed", detail: "go test exited 1" },
];

describe("finding words in the loaded transcript (ruling 29)", () => {
	it("matches any row's text, ignoring case", () => {
		expect(findMatches(rows, "settle")).toEqual([1, 2]);
		expect(findMatches(rows, "GO TEST")).toEqual([3, 4]);
		expect(findMatches(rows, "   ")).toEqual([]);
	});

	it("reads a run's steps, targets included, and nothing from a time marker", () => {
		expect(rowText(rows[3] as TimelineRow)).toContain("go test ./agent/...");
		expect(rowText(rows[0] as TimelineRow)).toBe("");
	});

	it("matches the answer a question row renders, whose raw row the recap hid", () => {
		// hideAnswerMessages drops the raw answer row once the question row's
		// recap stands for it, so the recap's words are the only searchable
		// copy left (#6328). Find reads them beside the row it draws them in.
		const answered: TimelineRow[] = [
			{
				kind: "activity",
				id: "q",
				label: "ask_user",
				family: "tool",
				state: "completed",
				detail: { arguments: JSON.stringify({ questions: [] }) },
			},
		];
		const answers = new Map([["q", "celsius to fahrenheit"]]);
		expect(findMatches(answered, "fahrenheit", undefined, answers)).toEqual([0]);
		expect(findMatches(answered, "fahrenheit")).toEqual([]);
	});

	// A step's words name what it acted on: a page's URL, a search's query.
	it("matches a fetched page by its URL and a web search by its query", () => {
		const web: TimelineRow[] = [
			{
				kind: "run",
				id: "run:web",
				steps: [
					{
						kind: "activity",
						id: "fetch",
						label: "web_fetch",
						family: "tool",
						state: "completed",
						detail: {
							description: "Read the docs",
							arguments: JSON.stringify({ url: "https://example.com/guide" }),
							summary: "Fetched https://example.com/guide",
						},
					},
					{
						kind: "activity",
						id: "search",
						label: "web_search",
						family: "tool",
						state: "completed",
						detail: {
							description: "Look it up",
							arguments: JSON.stringify({ query: "rust lifetimes" }),
							summary: 'Searched the web for "rust lifetimes"',
						},
					},
				],
			},
		];
		expect(findMatches(web, "example.com/guide")).toEqual([0]);
		expect(findMatches(web, "rust lifetimes")).toEqual([0]);
	});

	// A settled step with an intent of its own keeps its words but not its
	// summary (projectedRows' intentRow), and its line sets their target in
	// Menlo: the target is on screen, so it's findable.
	it("matches an intent row by the target its words name", () => {
		const intent: TimelineRow[] = [
			{
				kind: "run",
				id: "run:i",
				steps: [
					{
						kind: "activity",
						id: "i",
						label: "read_file",
						family: "tool",
						state: "completed",
						summaryOnly: true,
						detail: { description: "Read the settle pass", words: { verb: "Read", target: "agent/tree.go" } },
					},
				],
			},
		];
		expect(findMatches(intent, "agent/tree.go")).toEqual([0]);
	});

	it("starts at the newest match and steps both ways", () => {
		const matches = [1, 2, 4];
		expect(stepMatch(matches, null, -1)).toBe(4);
		expect(stepMatch(matches, 4, -1)).toBe(2);
		expect(stepMatch(matches, 1, -1)).toBeNull();
		expect(stepMatch(matches, 2, 1)).toBe(4);
		expect(stepMatch(matches, 4, 1)).toBeNull();
		expect(stepMatch([], null, -1)).toBeNull();
	});

	it("says where you are", () => {
		const numbers = (label: string) => label.match(/\d+/g)?.map(Number) ?? [];
		// The position, then how many.
		expect(numbers(matchLabel([1, 2, 4], 2))).toEqual([2, 3]);
		expect(numbers(matchLabel([1, 2, 4], null))).toEqual([3, 3]);
		expect(numbers(matchLabel([], null))).toEqual([]);
	});
});

describe("keeping the find selection when an answer row hides", () => {
	// The reviewer's paging transition (#6328): Find is on a linked answer
	// whose question sits on an older page. Loading that page hides the answer
	// row, since the question row now recaps it. The selection must move to
	// the question row — the row that survives and shows the same words — or
	// Find sees no current match, treats it as lost, and pages past the older
	// hits that are still there, finally reporting "No older matches" for
	// something the reader can see.
	const answerRow: TimelineRow = { kind: "user", id: "ans", text: "sure, go with Yes" };
	const olderQuestion: TimelineRow = {
		kind: "activity",
		id: "q",
		label: "ask_user",
		family: "tool",
		state: "completed",
		detail: { arguments: "{}" },
	};
	// Older history, holding an earlier match, arrives above the two rows.
	const olderMatch: TimelineRow = { kind: "user", id: "older", text: "fahrenheit" };

	it("moves the selection to the question row the answer now shows beneath", () => {
		const loadedPage: TimelineRow[] = [answerRow];
		const standIns = new Map([["ans", "q"]]);
		// On the page that held the answer row, the selection is the answer.
		expect(selectedRowIndex(loadedPage, "ans", new Map())).toBe(0);
		// The older page arrives and hides it; the same key still resolves.
		const withOlderPage: TimelineRow[] = [olderMatch, olderQuestion];
		expect(selectedRowIndex(withOlderPage, "ans", standIns)).toBe(1);
		// And stepping back from there finds the older hit instead of stopping.
		const hits = findMatches(withOlderPage, "fahrenheit");
		expect(stepMatch(hits, selectedRowIndex(withOlderPage, "ans", standIns), -1)).toBe(0);
	});

	it("keeps a selection that names no row null, so a resume still reloads its boundary", () => {
		// A saved key with nothing to remap is the resume case, not a hide.
		expect(selectedRowIndex([olderQuestion], "gone", new Map())).toBeNull();
		expect(selectedRowIndex([olderQuestion], "q", new Map([["other", "q"]]))).toBe(0);
	});

	it("counts the stand-in as current only when it is itself a hit", () => {
		// The question row carries the answer it renders, but a composed
		// answer's "[answers]" framing and numbering are not in the recap, so
		// the query can hit only the older row. A non-hit stand-in must not
		// become the current match, or the label reads "0 of N" while a row
		// is highlighted.
		const rows: TimelineRow[] = [olderMatch, olderQuestion];
		const hits = findMatches(rows, "fahrenheit");
		expect(hits).toEqual([0]);
		expect(selectedMatch(rows, hits, "ans", new Map([["ans", "q"]]))).toBe(0);
		expect(matchPosition(hits, selectedMatch(rows, hits, "ans", new Map([["ans", "q"]])))).toEqual({
			position: 1,
			count: 1,
		});
		// A direct hit is kept as the current match.
		expect(selectedMatch(rows, hits, "older", new Map())).toBe(0);
		// No hit at all leaves nothing current, which reads "No matches".
		expect(selectedMatch(rows, [], "ans", new Map([["ans", "q"]]))).toBeNull();
	});
});

describe("finding words in a delegate or job notification", () => {
	const text = notificationWireItem("delegate-reported").text ?? "";
	const row: TimelineRow = {
		kind: "notice",
		id: "n",
		origin: "steering",
		family: "informational",
		tone: "info",
		text,
		notifications: parseSteeringNotifications(text),
	};

	it("matches an unnamed subagent by the title the session's subagents give it", () => {
		const text = notificationWireItem("delegate-failed-unnamed").text ?? "";
		const unnamed: TimelineRow = { ...row, notifications: parseSteeringNotifications(text) };
		const delegates = [{ delegateId: "dlg_2", description: "Split the retry loop" }] as EvenerDelegateInfo[];
		expect(findMatches([unnamed], "split the retry loop failed", delegates)).toEqual([0]);
		// The frame names no subagent, so without the session's subagents the
		// card (and so its search text) says "Subagent failed".
		const withoutDelegates = undefined;
		expect(findMatches([unnamed], "split the retry loop", withoutDelegates)).toEqual([]);
		expect(findMatches([unnamed], "subagent failed", withoutDelegates)).toEqual([0]);
	});

	it("reads a truncated frame as its neutral line", () => {
		const truncated = text.slice(0, 60);
		const remnant: TimelineRow = { ...row, text: truncated, notifications: [{ kind: "text", text: truncated }] };
		expect(findMatches([remnant], "delegate_id")).toEqual([]);
		expect(findMatches([remnant], "couldn't be read")).toEqual([0]);
	});

	it("matches the words the card shows, never its markup", () => {
		expect(findMatches([row], "settle finished")).toEqual([0]);
		expect(findMatches([row], "waits for the drain")).toEqual([0]);
		expect(findMatches([row], "delegate_id")).toEqual([]);
	});

	it("matches text inside a grouped thought body (detail.output)", () => {
		const thought: Extract<TimelineRow, { kind: "activity" }> = {
			kind: "activity",
			id: "r_1",
			turnId: "t1",
			label: "Thought",
			family: "reasoning",
			state: "completed",
			detail: { output: "hidden-quokka-reasoning-body" },
		};
		const quiet: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_1",
			turnId: "t1",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			text: "Ended turn quietly",
		};
		const group: TimelineRow = {
			kind: "details",
			id: "quiet-yields:r_1",
			entries: [thought, quiet],
			summary: "2 quiet yields",
		};
		expect(findMatches([group], "quokka-reasoning-body")).toEqual([0]);
	});

	it("resolves selectedKey onto the containing details group when a standalone wake folds", () => {
		const q1: Extract<TimelineRow, { kind: "notice" }> = {
			kind: "notice",
			id: "qy_1",
			turnId: "t1",
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			label: "Ended turn quietly",
			text: "quokka-folded-prose",
			rendersMarkdown: true,
		};
		const q2: Extract<TimelineRow, { kind: "notice" }> = {
			...q1,
			id: "qy_2",
			turnId: "t2",
			text: "other",
		};
		const foldedRows: TimelineRow[] = [
			{ kind: "details", id: "quiet-yields:qy_1", entries: [q1, q2], summary: "2 quiet yields" },
		];
		const hits = findMatches(foldedRows, "quokka-folded-prose");
		expect(hits).toEqual([0]);
		expect(selectedRowIndex(foldedRows, "qy_1", new Map())).toBe(0);
		expect(selectedMatch(foldedRows, hits, "qy_1", new Map())).toBe(0);
		expect(matchPosition(hits, selectedMatch(foldedRows, hits, "qy_1", new Map()))).toEqual({ position: 1, count: 1 });
	});

	it("keeps selectedKey when an older page extends quiet-yields:<oldFirstId> to quiet-yields:<newFirstId>", () => {
		const q = (id: string, turnId: string, text: string): Extract<TimelineRow, { kind: "notice" }> => ({
			kind: "notice",
			id,
			turnId,
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			label: "Ended turn quietly",
			text,
			rendersMarkdown: true,
		});
		const priorHitRow: Extract<TimelineRow, { kind: "assistant" }> = {
			kind: "assistant",
			id: "m_older",
			turnId: "t_older",
			markdown: "quokka-earliest",
			streaming: false,
		};
		const older = q("qy_0", "t0", "quokka-older");
		const q1 = q("qy_1", "t1", "quokka-selected");
		const q2 = q("qy_2", "t2", "other");
		const extended: TimelineRow[] = [
			priorHitRow,
			{ kind: "details", id: "quiet-yields:qy_0", entries: [older, q1, q2], summary: "3 quiet yields" },
		];
		const hits = findMatches(extended, "quokka");
		expect(selectedRowIndex(extended, "quiet-yields:qy_1", new Map())).toBe(1);
		expect(selectedMatch(extended, hits, "quiet-yields:qy_1", new Map())).toBe(1);
		expect(stepMatch(hits, selectedMatch(extended, hits, "quiet-yields:qy_1", new Map()), -1)).toBe(0);
	});

	it("exposes per-member FindMatchTargets inside quiet-yields groups so Previous steps within the group before paging", () => {
		const q = (id: string, turnId: string, text: string): Extract<TimelineRow, { kind: "notice" }> => ({
			kind: "notice",
			id,
			turnId,
			origin: "system",
			family: "lifecycle",
			tone: "system",
			eventKind: "quiet_yield",
			label: "Ended turn quietly",
			text,
			rendersMarkdown: true,
		});
		const w1 = q("qy_m1", "t1", "quokka-repeat alpha");
		const w2 = q("qy_m2", "t2", "quokka-repeat beta");
		const w3 = q("qy_m3", "t3", "quokka-repeat gamma");
		const nonMatch = q("qy_other", "t2b", "plain");
		const initialRows: TimelineRow[] = [
			{ kind: "details", id: "quiet-yields:qy_m2", entries: [w2, nonMatch, w3], summary: "3 quiet yields" },
		];
		const targets = findMatchTargets(initialRows, "quokka-repeat");
		expect(targets.map((target) => ({ key: target.key, rowIndex: target.rowIndex }))).toEqual([
			{ key: "qy_m2", rowIndex: 0 },
			{ key: "qy_m3", rowIndex: 0 },
		]);
		const targetIndexes = targets.map((_, index) => index);
		expect(selectedTargetIndex(initialRows, targets, "qy_m3", new Map())).toBe(1);
		expect(stepMatch(targetIndexes, 1, -1)).toBe(0);
		expect(selectedTargetIndex(initialRows, targets, "quiet-yields:qy_m2", new Map())).toBe(0);

		const extendedRows: TimelineRow[] = [
			{ kind: "details", id: "quiet-yields:qy_m1", entries: [w1, w2, nonMatch, w3], summary: "4 quiet yields" },
		];
		const extendedTargets = findMatchTargets(extendedRows, "quokka-repeat");
		expect(extendedTargets.map((target) => target.key)).toEqual(["qy_m1", "qy_m2", "qy_m3"]);
		expect(selectedTargetIndex(extendedRows, extendedTargets, "qy_m2", new Map())).toBe(1);
		expect(
			stepMatch(
				extendedTargets.map((_, index) => index),
				1,
				-1,
			),
		).toBe(0);
	});
});
