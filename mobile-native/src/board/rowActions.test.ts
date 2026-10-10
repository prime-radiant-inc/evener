import { describe, expect, it } from "vitest";
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { nativeNavigationActions } from "../navigationActionRepository";
import { NavigationActions } from "../navigationActions";
import type { BoardState } from "./attention";
import { organizationHub, SESSION_ID } from "./organizationTestUtils";
import {
	archiveTarget,
	archivingSessionId,
	journalOutcome,
	type RowActionContext,
	renameSession,
	rowMenuActions,
	shutDownSession,
	swipeActions,
} from "./rowActions";

const local = `local:${SESSION_ID}`;
const row = (over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref: local,
	host_id: "local",
	session_id: SESSION_ID,
	title: "Session",
	project: "evener",
	state: "active",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const remote = { ref: "paradise-park:x", host_id: "paradise-park", session_id: "x" };
const unarchived: RowActionContext = { archived: false };

describe("the long-press menu per state (spec 7.3)", () => {
	it.each([
		[
			"a working session of this hub",
			row({ rename: true }),
			"working",
			unarchived,
			["pin", "stop", "shutDown", "archive", "rename"],
		],
		["an idle one", row({ state: "idle" }), "idle", unarchived, ["pin", "shutDown", "archive"]],
		["one needing your reply", row({ state: "awaiting" }), "needsYou", unarchived, ["pin", "shutDown", "archive"]],
		[
			"one asking a question",
			row({ state: "awaiting", ask_pending: true }),
			"question",
			unarchived,
			["pin", "shutDown", "archive"],
		],
		["one needing a restart", row({ state: "restartRequired" }), "restartNeeded", unarchived, ["pin", "archive"]],
		["one working on another host", row({ ...remote }), "working", unarchived, ["pin", "stop", "shutDown", "archive"]],
		[
			"one on an offline host",
			row({ ...remote, offline: true, live: false }),
			"shutDown",
			unarchived,
			["pin", "archive"],
		],
		["a fork", row({ kind: "fork" }), "working", unarchived, ["stop", "shutDown"]],
		[
			"one in an archived tier",
			row({ state: "ended", live: false }),
			"shutDown",
			{ archived: true },
			["pin", "unarchive"],
		],
	] as const)("%s", (_name, summary, state, context, expected) => {
		expect(rowMenuActions({ row: summary, state: state as BoardState }, context)).toEqual(expected);
	});
});

describe("swipes (spec 7.3)", () => {
	it.each([
		[
			"a working session of this hub",
			row(),
			"working",
			unarchived,
			{ leading: "archive", trailing: ["stop", "pin", "more"] },
		],
		["an idle one", row({ state: "idle" }), "idle", unarchived, { leading: "archive", trailing: ["pin", "more"] }],
		[
			"one working on another host",
			row({ ...remote }),
			"working",
			unarchived,
			{ leading: "archive", trailing: ["stop", "pin", "more"] },
		],
		[
			"one in an archived tier",
			row({ state: "ended", live: false }),
			"shutDown",
			{ archived: true },
			{ leading: "unarchive", trailing: ["pin", "more"] },
		],
	] as const)("%s", (_name, summary, state, context, expected) => {
		expect(swipeActions({ row: summary, state: state as BoardState }, context)).toEqual(expected);
	});
});

describe("archiving (rulings 16 and 20)", () => {
	it("archives a top-level session of this hub by its id and one of another host by its ref", () => {
		expect(archiveTarget(row())).toEqual({ kind: "session", id: SESSION_ID });
		expect(archiveTarget(row({ ...remote }))).toEqual({ kind: "session", id: "paradise-park:x" });
		expect(archiveTarget(row({ ...remote, kind: "subagent" }))).toBeNull();
		expect(archiveTarget(row({ ref: "cluster:abc" }))).toBeNull();
		expect(archiveTarget(row({ ref: "local:not-a-session", session_id: "not-a-session" }))).toBeNull();
		for (const kind of ["subagent", "fork", "cluster"]) expect(archiveTarget(row({ kind }))).toBeNull();
	});

	function journal() {
		const values = new Map<string, unknown>();
		let id = 0;
		return nativeNavigationActions("hub", {
			createId: () => String(++id),
			get: (key) => values.get(key),
			set: (key, value) => {
				values.set(key, value);
			},
			deleteIf: (key, value) => JSON.stringify(values.get(key)) === JSON.stringify(value) && values.delete(key),
		});
	}
	function organization({ current = true, accept = true } = {}) {
		const hub = organizationHub();
		hub.answerWrites(() => {
			if (!accept) throw new Error("refused");
			return { ok: true, changed: true, navigation: { generation_id: "g", targets: [] } };
		});
		const storage = journal();
		const actions = new NavigationActions(
			hub.client,
			async () => {},
			() => current,
			async () => {},
			storage,
		);
		return { hub, storage, actions };
	}

	it("archives through the journal and reports it confirmed", async () => {
		const { hub, storage, actions } = organization();
		expect(await journalOutcome(actions, () => actions.archive({ kind: "session", id: SESSION_ID }, true))).toBe(
			"confirmed",
		);
		expect(hub.writes).toEqual([
			{ method: "evener/archive/set", params: { kind: "session", id: SESSION_ID, archived: true } },
		]);
		expect(storage.load()).toBeNull();
	});

	it("reports a refused archive as unconfirmed, with the journal holding it for the Board to settle", async () => {
		const { storage, actions } = organization({ accept: false });
		expect(await journalOutcome(actions, () => actions.archive({ kind: "session", id: SESSION_ID }, true))).toBe(
			"unconfirmed",
		);
		expect(storage.load()).not.toBeNull();
		expect(actions.getSnapshot().uncertain).toBe(true);
	});

	it("sends nothing while another change is unresolved, or when the Board isn't on screen", async () => {
		const busy = organization();
		busy.storage.begin({ kind: "unpin", params: { sessionRef: "local:other" } });
		const blocked = new NavigationActions(
			busy.hub.client,
			async () => {},
			() => true,
			async () => {},
			busy.storage,
		);
		expect(await journalOutcome(blocked, () => blocked.archive({ kind: "session", id: SESSION_ID }, true))).toBe(
			"notTaken",
		);
		const covered = organization({ current: false });
		expect(
			await journalOutcome(covered.actions, () => covered.actions.archive({ kind: "session", id: SESSION_ID }, true)),
		).toBe("notTaken");
		expect([...busy.hub.writes, ...covered.hub.writes]).toEqual([]);
	});

	it("tells a change the journal took from one it never took (a held change replays only the latter)", async () => {
		const archive = (actions: NavigationActions) => () => actions.archive({ kind: "session", id: SESSION_ID }, true);
		const taken = organization();
		expect(await journalOutcome(taken.actions, archive(taken.actions))).toBe("confirmed");
		const refused = organization({ accept: false });
		expect(await journalOutcome(refused.actions, archive(refused.actions))).toBe("unconfirmed");
		// Off screen, the journal's run() returns without starting the change.
		const covered = organization({ current: false });
		expect(await journalOutcome(covered.actions, archive(covered.actions))).toBe("notTaken");
		expect(covered.hub.writes).toEqual([]);
	});

	it("pins select mode's sessions through the same journal", async () => {
		const { hub, actions } = organization();
		expect(await journalOutcome(actions, () => actions.assignPin({ sessionRef: local, sectionName: "Release" }))).toBe(
			"confirmed",
		);
		expect(hub.writes).toEqual([
			{ method: "evener/session-pin/assign", params: { sessionRef: local, sectionName: "Release" } },
		]);
	});

	it("names the session an unresolved archive is about, so its rows dim", () => {
		const recovery = {
			id: "1",
			operation: { kind: "archive" as const, params: { kind: "session", id: SESSION_ID, archived: true } },
			receipt: null,
		};
		const idle = { pending: false, uncertain: false, recovery: null };
		expect(archivingSessionId({ ...idle, pending: true, recovery })).toBe(SESSION_ID);
		expect(archivingSessionId({ ...idle, uncertain: true, recovery })).toBe(SESSION_ID);
		expect(archivingSessionId(idle)).toBeNull();
		expect(
			archivingSessionId({
				...idle,
				pending: true,
				recovery: {
					...recovery,
					operation: { kind: "archive", params: { kind: "project", id: "p", workingDir: "/w", archived: true } },
				},
			}),
		).toBeNull();
	});
});

describe("the Session's direct requests (ruling 19)", () => {
	function recorder() {
		const requests: { method: string; params: unknown }[] = [];
		const client = {
			request: async (method: string, params: unknown) => {
				requests.push({ method, params });
				return {};
			},
			onNotification: () => () => {},
		} as unknown as ConversationClientLike;
		return { client, requests };
	}

	it("shuts a session down with thread/shutdown", async () => {
		const { client, requests } = recorder();
		await shutDownSession(client, local);
		expect(requests).toEqual([{ method: "thread/shutdown", params: { ref: local } }]);
	});

	it("renames with the trimmed name, and sends nothing for a blank one", async () => {
		const { client, requests } = recorder();
		expect(await renameSession(client, local, "  Fix the settle race ")).toBe(true);
		expect(await renameSession(client, local, "   ")).toBe(false);
		expect(requests).toEqual([
			{ method: "evener/thread/name/set", params: { ref: local, name: "Fix the settle race" } },
		]);
	});
});
