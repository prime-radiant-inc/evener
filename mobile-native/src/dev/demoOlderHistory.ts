// EVENER_DEMO_FLEET_OLDER: fifteen older turns ahead of Get PR 2138's own,
// served a page of items at a time as a v6 hub does (thread/read's itemLimit
// and olderCursor, then thread/turns/list pages), so loading older history as
// you scroll up can be checked on a simulator.
import type { Thread, ThreadItem, Turn } from "@evener/appwire-client";

const OLDER_TURNS = 15;
const TURN_MS = 20 * 60 * 1000;

/** Puts OLDER_TURNS finished turns, seven items each, ahead of `thread`'s. */
export function withOlderHistory(thread: Thread): void {
	const firstStart = thread.turns?.[0]?.startedAt ?? Date.now();
	const base = firstStart - (OLDER_TURNS + 1) * TURN_MS;
	const older = Array.from({ length: OLDER_TURNS }, (_, t): Turn => {
		const start = base + t * TURN_MS;
		const id = (part: string) => `demo-older-${t}-${part}`;
		const items: ThreadItem[] = [
			{
				id: id("u"),
				type: "userMessage",
				text: `Older request number ${t + 1}: check the retirement path again and tell me what changed since the last run.`,
				status: "completed",
				startedAt: start,
			},
			{
				id: id("a"),
				type: "agentMessage",
				text: `Turn ${t + 1}. I read the retirement path again. Nothing structural changed since the last run, but two helpers moved between files.\n\nI'll run the three flaky tests 50 times each and report the counts.`,
				status: "completed",
				startedAt: start + 10_000,
			},
			...Array.from(
				{ length: 4 },
				(_, k): ThreadItem => ({
					id: id(`s${k}`),
					type: "commandExecution",
					toolName: "shell",
					callId: id(`c${k}`),
					description: `Ran check ${k + 1}`,
					argumentsJson: JSON.stringify({ command: `go test ./agent/... -run Retire -count=50 # ${k}` }),
					status: "completed",
					startedAt: start + 40_000 + k * 20_000,
					completedAt: start + 55_000 + k * 20_000,
					output: "ok",
				}),
			),
			{
				id: id("f"),
				type: "agentMessage",
				text: `Turn ${t + 1} result: ${48 + (t % 3)} of 50 passed on the first test, 50 of 50 on the other two.`,
				status: "completed",
				startedAt: start + 200_000,
			},
		];
		return {
			id: `demo-older-turn-${t}`,
			itemsView: "full",
			status: "completed",
			startedAt: start,
			completedAt: start + 600_000,
			durationMs: 600_000,
			items,
		};
	});
	thread.turns = [...older, ...(thread.turns ?? [])];
}

/** Items [start, end) of `turns`, flattened, rebuilt into turns; a turn cut
 * at the page's start says it has earlier items. */
function itemPage(turns: Turn[], start: number, end: number): Turn[] {
	const flat = turns.flatMap((turn) => (turn.items ?? []).map((item) => ({ turn, item })));
	const page: Turn[] = [];
	for (const { turn, item } of flat.slice(start, end)) {
		let last = page.at(-1);
		if (!last || last.id !== turn.id) {
			last = { ...turn, items: [], ...(turn.items?.[0] === item ? {} : { hasEarlierItems: true }) };
			page.push(last);
		}
		last.items?.push(item);
	}
	return page;
}

function itemCount(turns: Turn[]): number {
	return turns.reduce((sum, turn) => sum + (turn.items?.length ?? 0), 0);
}

const CURSOR = "older:";

/** A page of `turns`' items, and the cursor to the items before it (none at
 * the beginning). */
export interface ItemPage {
	turns: Turn[];
	olderCursor?: string;
}

function pageEndingAt(turns: Turn[], end: number, limit: number): ItemPage {
	const start = Math.max(0, end - limit);
	return { turns: itemPage(turns, start, end), ...(start > 0 ? { olderCursor: `${CURSOR}${start}` } : {}) };
}

/** The latest `limit` items: what thread/read answers with an itemLimit. */
export function latestPage(turns: Turn[], limit: number): ItemPage {
	return pageEndingAt(turns, itemCount(turns), limit);
}

/** The `limit` items before a cursor this hub issued, or null for anyone
 * else's: what thread/turns/list answers. */
export function pageBefore(turns: Turn[], cursor: unknown, limit: number): ItemPage | null {
	if (typeof cursor !== "string" || !cursor.startsWith(CURSOR)) return null;
	const end = Number(cursor.slice(CURSOR.length));
	return Number.isInteger(end) && end > 0 ? pageEndingAt(turns, end, limit) : null;
}
