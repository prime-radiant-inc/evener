import { expect, it } from "vitest";
import type { NavigationReadParams } from "@evener/appwire-client";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { LIVE_PAGE_LIMIT, LiveSessionsReader, liveCountsByHost, liveSessionsText } from "./liveCounts";

const session = (ref: string, host_id: string) => ({
	ref,
	host_id,
	session_id: ref,
	title: ref,
	project: "evener",
	state: "active",
	kind: "session",
	live: true,
	children: [],
});

/** Answers the nth Live read with page(n). */
function liveHub(page: (read: number) => { sessions: ReturnType<typeof session>[]; remaining: number }) {
	const reads: NavigationReadParams[] = [];
	const client = {
		request: async (_method: string, params: NavigationReadParams) => {
			const answer = page(reads.length);
			reads.push(params);
			return wireSnapshot(
				{ ...params, representationVersion: 3, offset: params.offset ?? 0, limit: params.limit ?? 50 },
				answer,
				`etag-${reads.length}`,
				1,
				"generation-test",
			);
		},
		onNotification: () => () => {},
	} as unknown as ConversationClientLike;
	return { client, reads };
}

it("counts live sessions by host", () => {
	expect(liveCountsByHost([{ host_id: "local" }, { host_id: "paradise-park" }, { host_id: "paradise-park" }])).toEqual(
		new Map([
			["local", 1],
			["paradise-park", 2],
		]),
	);
});

it("words a host's live sessions (spec 12)", () => {
	expect(liveSessionsText(3, false)).toBe("3 live");
	expect(liveSessionsText(3, true)).toBe("3 live, out of reach");
	expect(liveSessionsText(0, false)).toBe("No live sessions");
});

it("reads every Live page before counting", async () => {
	const { client, reads } = liveHub((read) =>
		read === 0
			? { sessions: [session("a", "local"), session("b", "paradise-park")], remaining: 1 }
			: { sessions: [session("c", "paradise-park")], remaining: 0 },
	);
	const reader = new LiveSessionsReader(client);
	await reader.load();
	expect(reads).toHaveLength(2);
	expect(liveCountsByHost(reader.getSnapshot().rows)).toEqual(
		new Map([
			["local", 1],
			["paradise-park", 2],
		]),
	);
});

it("stops after ten pages", async () => {
	const { client, reads } = liveHub((read) => ({ sessions: [session(`s${read}`, "local")], remaining: 1 }));
	await new LiveSessionsReader(client).load();
	expect(reads).toHaveLength(LIVE_PAGE_LIMIT);
});
