import type { EvenerThread, Thread } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { describe, expect, it, vi } from "vitest";
import type { NavigationActions } from "../navigationActions";
import type { SyncStringStorage } from "../syncStringStorage";
import { BoardHold, type HeldAction, turnSeen } from "./boardHold";
import { BoardReplay } from "./boardReplay";
import { type StopOutcome, stopToast } from "./boardStops";
import type { BoardOrganization } from "./useBoardOrganization";

// boardStops (stopToast) reaches the mutation runtime's native modules.
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

function memory(): SyncStringStorage {
	const values = new Map<string, string>();
	return {
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const ended = "2026-09-28T10:00:00.000Z";
const stamp = Date.parse(ended);
const thread = (evener: Partial<EvenerThread>) => ({ thread: { id: "t", evener } as unknown as Thread });

/** A replay over a real hold, a fake client, and a Stop that reports what
 * its guard said of `running`. */
function setup(running: Partial<EvenerThread> = { activeTurnId: "turn-2", lastTurnEndedAt: stamp }) {
	const hold = new BoardHold(memory(), "hub-1");
	const client = new FakeClient("ready");
	client.on("thread/read", () => thread(running) as never);
	const stops: string[] = [];
	const toasts: string[] = [];
	let live = true;
	const replay = new BoardReplay(hold, {
		stop: async (_client, ref, guard): Promise<StopOutcome> => {
			stops.push(ref);
			return guard && !guard(running as EvenerThread) ? "dropped" : "stopped";
		},
		toast: (text) => toasts.push(text),
	});
	return {
		hold,
		client,
		stops,
		toasts,
		replay,
		isLive: () => live,
		drop: () => {
			live = false;
		},
	};
}
const methods = (client: FakeClient) => client.calls.map((call) => call.method).filter((m) => m !== "thread/read");
const stopOf = (ref: string, turnEndedAt: string | null = ended): HeldAction => ({
	kind: "stop",
	ref,
	title: ref,
	seen: { turnEndedAt, running: true },
});

it("sends what goes at once in the order held, and settles each", async () => {
	const { hold, client, stops, replay, isLive } = setup();
	client.on("thread/shutdown", () => ({}) as never);
	client.on("evener/thread/name/set", () => ({}) as never);
	hold.hold({ kind: "rename", ref: "a", title: "A", name: "Renamed" }, 1);
	hold.hold(stopOf("b"), 2);
	hold.hold({ kind: "shutDown", ref: "c", title: "C", seen: { turnEndedAt: ended, running: true } }, 3);
	// Organization changes wait for the Board; they stay held here.
	hold.hold({ kind: "archive", ref: "d", target: { kind: "session", id: "d" }, archived: true }, 4);
	await replay.sendImmediate(client, isLive);
	expect(methods(client)).toEqual(["evener/thread/name/set", "thread/shutdown"]);
	expect(stops).toEqual(["b"]);
	expect(hold.getSnapshot().map((record) => record.action.kind)).toEqual(["archive"]);
});

it("drops a held Stop whose turn ended, and says so", async () => {
	const { hold, client, toasts, replay, isLive } = setup({ activeTurnId: "turn-3", lastTurnEndedAt: stamp + 60_000 });
	hold.hold(stopOf("b"), 1);
	await replay.sendImmediate(client, isLive);
	expect(toasts).toEqual(["The turn you stopped ended before you were back online"]);
	expect(hold.getSnapshot()).toEqual([]);
});

it("shuts down only when no newer turn runs than the one seen", async () => {
	const newer = setup({ activeTurnId: "turn-3", lastTurnEndedAt: stamp + 60_000 });
	newer.client.on("thread/shutdown", () => ({}) as never);
	newer.hold.hold({ kind: "shutDown", ref: "c", title: "C", seen: { turnEndedAt: ended, running: true } }, 1);
	await newer.replay.sendImmediate(newer.client, newer.isLive);
	expect(methods(newer.client)).toEqual([]);
	expect(newer.toasts).toEqual(["A newer turn started, so the session wasn't shut down"]);
	expect(newer.hold.getSnapshot()).toEqual([]);

	// Seen at rest, and a turn began since: that turn is work nobody saw.
	const began = setup({ activeTurnId: "turn-3", lastTurnEndedAt: stamp });
	began.client.on("thread/shutdown", () => ({}) as never);
	began.hold.hold({ kind: "shutDown", ref: "c", title: "C", seen: { turnEndedAt: ended, running: false } }, 1);
	await began.replay.sendImmediate(began.client, began.isLive);
	expect(methods(began.client)).toEqual([]);
	expect(began.toasts).toEqual(["A newer turn started, so the session wasn't shut down"]);

	// Pressed on a session awaiting you at rest, and a turn began since.
	const rested = setup({ activeTurnId: "turn-3", lastTurnEndedAt: stamp });
	rested.client.on("thread/shutdown", () => ({}) as never);
	rested.hold.hold(
		{ kind: "shutDown", ref: "c", title: "C", seen: turnSeen({ state: "awaiting", turn_ended_at: ended }) },
		1,
	);
	await rested.replay.sendImmediate(rested.client, rested.isLive);
	expect(methods(rested.client)).toEqual([]);

	// Nothing running: shutting down stops nothing the person didn't see.
	const resting = setup({ lastTurnEndedAt: stamp + 60_000 });
	resting.client.on("thread/shutdown", () => ({}) as never);
	resting.hold.hold({ kind: "shutDown", ref: "c", title: "C", seen: { turnEndedAt: ended, running: true } }, 1);
	await resting.replay.sendImmediate(resting.client, resting.isLive);
	expect(methods(resting.client)).toEqual(["thread/shutdown"]);
	expect(resting.toasts).toEqual(["Session shut down"]);
});

it("settles a request the hub refused, with the online action's toast, and keeps one the connection lost", async () => {
	const refused = setup();
	refused.client.on("evener/thread/name/set", () => {
		throw new Error("not allowed");
	});
	refused.hold.hold({ kind: "rename", ref: "a", title: "A", name: "B" }, 1);
	await refused.replay.sendImmediate(refused.client, refused.isLive);
	expect(refused.toasts).toEqual(["Couldn't rename “A”: not allowed"]);
	expect(refused.hold.getSnapshot()).toEqual([]);

	const lost = setup();
	lost.client.on("evener/thread/name/set", () => {
		lost.drop();
		throw new Error("socket closed");
	});
	lost.client.on("thread/shutdown", () => ({}) as never);
	lost.hold.hold({ kind: "rename", ref: "a", title: "A", name: "B" }, 1);
	lost.hold.hold({ kind: "shutDown", ref: "c", title: "C", seen: { turnEndedAt: ended, running: true } }, 2);
	await lost.replay.sendImmediate(lost.client, lost.isLive);
	expect(lost.toasts).toEqual([]);
	expect(lost.hold.getSnapshot().map((record) => record.action.kind)).toEqual(["rename", "shutDown"]);
	expect(methods(lost.client)).toEqual(["evener/thread/name/set"]);
});

it("ends when its hub is forgotten mid-replay, sending nothing more", async () => {
	const { hold, client, replay, isLive } = setup({ lastTurnEndedAt: stamp });
	let answer: () => void = () => {};
	client.on(
		"thread/shutdown",
		() =>
			new Promise((resolve) => {
				answer = () => resolve({} as never);
			}) as never,
	);
	hold.hold({ kind: "shutDown", ref: "c1", title: "C1", seen: { turnEndedAt: ended, running: true } }, 1);
	hold.hold({ kind: "shutDown", ref: "c2", title: "C2", seen: { turnEndedAt: ended, running: true } }, 2);
	const running = replay.sendImmediate(client, isLive);
	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/shutdown"]));
	hold.forget();
	answer();
	await running;
	expect(methods(client)).toEqual(["thread/shutdown"]);
});

it("keeps a request whose connection dropped and came back while it was out, on the same client", async () => {
	const { hold, client, toasts, replay } = setup();
	client.on("evener/thread/name/set", () => {
		// The socket drops and the client reconnects before the request's
		// failure arrives: ready again, and the same client.
		client.emitStateChange("reconnecting");
		client.emitStateChange("ready");
		throw new Error("request timed out");
	});
	hold.hold({ kind: "rename", ref: "a", title: "A", name: "B" }, 1);
	await replay.sendImmediate(client, () => client.state === "ready");
	expect(toasts).toEqual([]);
	expect(hold.getSnapshot().map((record) => record.action.kind)).toEqual(["rename"]);
});

it("says a held Stop that failed couldn't stop, not that it couldn't shut down", async () => {
	const { hold, client, toasts, isLive } = setup();
	const failing = new BoardReplay(hold, {
		stop: async () => {
			throw new Error("boom");
		},
		toast: (text) => toasts.push(text),
	});
	hold.hold(stopOf("b"), 1);
	await failing.sendImmediate(client, isLive);
	expect(toasts).toEqual([stopToast("unavailable", "b")]);
	expect(hold.getSnapshot()).toEqual([]);
});

it("won't cancel a held action while it is being sent", async () => {
	const { hold, client, replay, isLive } = setup();
	let answer: () => void = () => {};
	client.on(
		"evener/thread/name/set",
		() =>
			new Promise((resolve) => {
				answer = () => resolve({} as never);
			}) as never,
	);
	const held = hold.hold({ kind: "rename", ref: "a", title: "A", name: "B" }, 1);
	const running = replay.sendImmediate(client, isLive);
	await vi.waitFor(() => expect(methods(client)).toEqual(["evener/thread/name/set"]));
	expect(hold.isSending(held.id)).toBe(true);
	answer();
	await running;
	expect(hold.getSnapshot()).toEqual([]);
});

it("resolves each ask once its own replay has run", async () => {
	const { hold, replay } = setup();
	const old = new FakeClient("ready");
	let oldLive = true;
	let answer: () => void = () => {};
	old.on(
		"evener/thread/name/set",
		() =>
			new Promise((resolve) => {
				// Answered as the old connection goes.
				answer = () => {
					oldLive = false;
					resolve({} as never);
				};
			}) as never,
	);
	const fresh = new FakeClient("ready");
	fresh.on("evener/thread/name/set", () => ({}) as never);
	hold.hold({ kind: "rename", ref: "a", title: "A", name: "B" }, 1);
	const first = replay.sendImmediate(old, () => oldLive);
	await vi.waitFor(() => expect(methods(old)).toEqual(["evener/thread/name/set"]));
	hold.hold({ kind: "rename", ref: "c", title: "C", name: "D" }, 2);
	let secondDone = false;
	const second = replay
		.sendImmediate(fresh, () => true)
		.then(() => {
			secondDone = true;
		});
	await Promise.resolve();
	expect(secondDone).toBe(false);
	answer();
	await Promise.all([first, second]);
	expect(methods(fresh)).toEqual(["evener/thread/name/set"]);
});

it("keeps a Shut down whose connection dropped while it read the thread", async () => {
	const { hold, client, toasts, replay } = setup({ lastTurnEndedAt: stamp });
	client.on("thread/shutdown", () => ({}) as never);
	client.on("thread/read", () => {
		// The read comes back over a connection that dropped and returned:
		// what it says may be stale.
		client.emitStateChange("reconnecting");
		client.emitStateChange("ready");
		return thread({ lastTurnEndedAt: stamp }) as never;
	});
	hold.hold({ kind: "shutDown", ref: "c", title: "C", seen: { turnEndedAt: ended, running: true } }, 1);
	await replay.sendImmediate(client, () => client.state === "ready");
	expect(methods(client)).toEqual([]);
	expect(toasts).toEqual([]);
	expect(hold.getSnapshot().map((record) => record.action.kind)).toEqual(["shutDown"]);
});

describe("through the organization journal", () => {
	/** A journal that takes each change (publishing it pending, then
	 * confirmed) unless told it can't, recording what it was asked. */
	function journal({ takes = true, confirms = true } = {}) {
		const listeners = new Set<() => void>();
		let state = {
			pending: false,
			uncertain: false,
			storageUnavailable: false,
			recovery: null as unknown,
			error: null,
		};
		const sent: unknown[] = [];
		const change = async (request: unknown) => {
			if (!takes) return;
			sent.push(request);
			state = { ...state, pending: true };
			for (const listener of listeners) listener();
			// Unconfirmed, the journal keeps its recovery record to settle.
			state = { ...state, pending: false, ...(confirms ? {} : { uncertain: true, recovery: { request } }) };
			for (const listener of listeners) listener();
		};
		const actions = {
			getSnapshot: () => state,
			subscribe: (listener: () => void) => {
				listeners.add(listener);
				return () => listeners.delete(listener);
			},
			archive: (target: unknown, archived: boolean) => change({ archive: target, archived }),
			assignPin: (target: unknown) => change({ assignPin: target }),
			favorite: (id: string, favorited: boolean) => change({ favorite: id, favorited }),
			reconcile: vi.fn(async () => {}),
		} as unknown as NavigationActions & { reconcile: ReturnType<typeof vi.fn> };
		const organization = { actions, state, ready: true, isCurrent: () => true } as unknown as BoardOrganization;
		return { organization, actions, sent };
	}

	it("sends a held pin and project change with their targets, and settles each", async () => {
		const { hold, replay, isLive } = setup();
		const { organization, sent } = journal();
		hold.hold({ kind: "pin", target: { sessionRef: "local:a", sectionId: "s1" } }, 1);
		hold.hold({ kind: "project", project: { key: "evener", workingDir: "/src/evener" }, action: "archive" }, 2);
		hold.hold({ kind: "project", project: { key: "notes" }, action: "pin" }, 3);
		await replay.organize(organization, isLive);
		expect(sent).toEqual([
			{ assignPin: { sessionRef: "local:a", sectionId: "s1" } },
			{ archive: { kind: "project", id: "evener", workingDir: "/src/evener" }, archived: true },
			{ favorite: "notes", favorited: true },
		]);
		expect(hold.getSnapshot()).toEqual([]);
	});

	it("leaves a change another replay is sending to that replay", async () => {
		const { hold, replay, isLive } = setup();
		const { organization, sent } = journal();
		const held = hold.hold({ kind: "pin", target: { sessionRef: "local:a", sectionId: "s1" } }, 1);
		hold.claim(held.id);
		await replay.organize(organization, isLive);
		expect(sent).toEqual([]);
	});

	it("hands an unconfirmed change to the journal, whose settling may fail without an unhandled rejection", async () => {
		const { hold, replay, isLive } = setup();
		const { organization, actions } = journal({ confirms: false });
		// A plain function: a vi.fn records how its promise settles, which
		// handles the rejection and would hide a floating one.
		let reconciles = 0;
		(actions as { reconcile: () => Promise<void> }).reconcile = () => {
			reconciles += 1;
			return Promise.reject(new Error("the hub didn't answer"));
		};
		const unhandled = vi.fn();
		process.on("unhandledRejection", unhandled);
		try {
			hold.hold({ kind: "archive", ref: "local:a", target: { kind: "session", id: "a" }, archived: true }, 1);
			await replay.organize(organization, isLive);
			expect(reconciles).toBe(1);
			expect(hold.getSnapshot()).toEqual([]);
			await new Promise((resolve) => setTimeout(resolve, 10));
			expect(unhandled).not.toHaveBeenCalled();
		} finally {
			process.off("unhandledRejection", unhandled);
		}
	});

	it("keeps a change the journal didn't take, and doesn't hand it to the journal", async () => {
		const { hold, replay, isLive } = setup();
		const { organization, actions, sent } = journal({ takes: false });
		hold.hold({ kind: "archive", ref: "local:a", target: { kind: "session", id: "a" }, archived: true }, 1);
		await replay.organize(organization, isLive);
		expect(sent).toEqual([]);
		expect(actions.reconcile).not.toHaveBeenCalled();
		expect(hold.getSnapshot().map((record) => record.action.kind)).toEqual(["archive"]);
		expect(hold.isSending(hold.getSnapshot()[0]?.id ?? "")).toBe(false);
	});
});

it("leaves a record another replay is sending to that replay, sending nothing twice", async () => {
	// A Board unmounted mid-send leaves its replay's request out while a new
	// Board's replay starts over the same hold.
	const { hold, client, replay, isLive } = setup();
	client.on("thread/shutdown", () => ({}) as never);
	const first = hold.hold({ kind: "shutDown", ref: "c1", title: "C1", seen: { turnEndedAt: ended, running: true } }, 1);
	hold.hold({ kind: "shutDown", ref: "c2", title: "C2", seen: { turnEndedAt: ended, running: true } }, 2);
	hold.claim(first.id);
	await replay.sendImmediate(client, isLive);
	expect(methods(client)).toEqual([]);
	// Its sender settles it, and the next replay goes on from there.
	hold.settled(first.id);
	await replay.sendImmediate(client, isLive);
	expect(methods(client)).toEqual(["thread/shutdown"]);
});

it("keeps a held Stop its guard dropped over a connection that dropped", async () => {
	const { hold, client, toasts } = setup();
	const replay = new BoardReplay(hold, {
		stop: async () => {
			// The guard read a thread that came back over a dropped connection.
			client.emitStateChange("reconnecting");
			client.emitStateChange("ready");
			return "dropped";
		},
		toast: (text) => toasts.push(text),
	});
	hold.hold(stopOf("b"), 1);
	await replay.sendImmediate(client, () => client.state === "ready");
	expect(toasts).toEqual([]);
	expect(hold.getSnapshot().map((record) => record.action.kind)).toEqual(["stop"]);
});

it("runs one replay at a time", async () => {
	const { hold, client, stops, replay, isLive } = setup();
	hold.hold(stopOf("b"), 1);
	await Promise.all([replay.sendImmediate(client, isLive), replay.sendImmediate(client, isLive)]);
	expect(stops).toEqual(["b"]);
});

it("keeps a request whose connection dropped as it failed, deciding from the client itself", async () => {
	const { hold, client, toasts, replay } = setup();
	client.on("evener/thread/name/set", () => {
		// The socket closes: the client says so before the request fails, and
		// before any render could.
		client.emitStateChange("reconnecting");
		throw new Error("socket closed");
	});
	hold.hold({ kind: "rename", ref: "a", title: "A", name: "B" }, 1);
	await replay.sendImmediate(client, () => client.state === "ready");
	expect(toasts).toEqual([]);
	expect(hold.getSnapshot().map((record) => record.action.kind)).toEqual(["rename"]);
});

it("runs a replay asked for while one was finishing, on the newer connection", async () => {
	const { hold, replay } = setup();
	const old = new FakeClient("ready");
	let fail: () => void = () => {};
	let oldLive = true;
	old.on(
		"evener/thread/name/set",
		() =>
			new Promise((_, reject) => {
				fail = () => {
					oldLive = false;
					reject(new Error("socket closed"));
				};
			}) as never,
	);
	const fresh = new FakeClient("ready");
	fresh.on("evener/thread/name/set", () => ({}) as never);
	hold.hold({ kind: "rename", ref: "a", title: "A", name: "B" }, 1);
	const first = replay.sendImmediate(old, () => oldLive);
	await vi.waitFor(() => expect(methods(old)).toEqual(["evener/thread/name/set"]));
	const second = replay.sendImmediate(fresh, () => true);
	fail();
	await Promise.all([first, second]);
	expect(methods(fresh)).toEqual(["evener/thread/name/set"]);
	expect(hold.getSnapshot()).toEqual([]);
});
