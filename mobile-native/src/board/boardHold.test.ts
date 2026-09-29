import { describe, expect, it } from "vitest";
import type { SyncStringStorage } from "../syncStringStorage";
import { BoardHold, boardHoldKey, type HeldAction, heldFor, turnStillSeen } from "./boardHold";

function memory(values = new Map<string, string>()): SyncStringStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const archive = (ref: string, archived = true): HeldAction => ({
	kind: "archive",
	ref,
	target: { kind: "session", id: ref },
	archived,
});
const stop = (ref: string, turnEndedAt: string | null = null): HeldAction => ({
	kind: "stop",
	ref,
	title: `Session ${ref}`,
	seen: { turnEndedAt },
});
const kinds = (hold: BoardHold) =>
	hold.getSnapshot().map((record) => {
		const { action } = record;
		if (action.kind === "archive") return `${action.archived ? "archive" : "unarchive"}:${action.ref}`;
		if (action.kind === "project") return `${action.action}:${action.project.key}`;
		if (action.kind === "rename") return `rename:${action.ref}:${action.name}`;
		if (action.kind === "pin") return `pin:${action.target.sessionRef}`;
		return `${action.kind}:${action.ref}`;
	});

describe("the Board's hold (phase 6 ruling 18)", () => {
	it("keeps the order held across a relaunch, per hub", () => {
		const storage = memory();
		const hold = new BoardHold(storage, "hub-1");
		hold.hold(archive("local:a"), 1);
		hold.hold(stop("local:b"), 2);
		expect(kinds(new BoardHold(storage, "hub-1"))).toEqual(["archive:local:a", "stop:local:b"]);
		expect(new BoardHold(storage, "hub-2").getSnapshot()).toEqual([]);
		expect(storage.values.has(boardHoldKey("hub-1"))).toBe(true);
	});

	it("keeps one action per subject, the last one, in the first one's place", () => {
		const hold = new BoardHold(memory(), "hub-1");
		hold.hold(archive("local:a"), 1);
		hold.hold(stop("local:b"), 2);
		hold.hold({ kind: "rename", ref: "local:c", title: "C", name: "One" }, 3);
		hold.hold({ kind: "rename", ref: "local:c", title: "C", name: "Two" }, 4);
		hold.hold(stop("local:b", "2026-09-28T10:00:00.000Z"), 5);
		expect(kinds(hold)).toEqual(["archive:local:a", "stop:local:b", "rename:local:c:Two"]);
		expect(hold.getSnapshot()[1]?.action).toMatchObject({ seen: { turnEndedAt: "2026-09-28T10:00:00.000Z" } });
	});

	it("drops a held change that the next one undoes", () => {
		const hold = new BoardHold(memory(), "hub-1");
		hold.hold(archive("local:a"), 1);
		hold.hold(archive("local:a", false), 2);
		const project = { key: "evener", workingDir: "/src/evener" };
		hold.hold({ kind: "project", project, action: "pin" }, 3);
		hold.hold({ kind: "project", project, action: "unpin" }, 4);
		hold.hold({ kind: "project", project, action: "archive" }, 5);
		expect(kinds(hold)).toEqual(["archive:evener"]);
	});

	it("tells subscribers once when a record is cancelled or settled", () => {
		const hold = new BoardHold(memory(), "hub-1");
		const first = hold.hold(archive("local:a"), 1);
		const second = hold.hold(stop("local:b"), 2);
		let calls = 0;
		hold.subscribe(() => calls++);
		hold.cancel(first.id);
		hold.settled(second.id);
		hold.settled("gone");
		expect(calls).toBe(2);
		expect(hold.getSnapshot()).toEqual([]);
	});

	it("reads damaged storage as empty, and keeps working in memory when a write fails", () => {
		const storage = memory(new Map([[boardHoldKey("hub-1"), "{not json"]]));
		expect(new BoardHold(storage, "hub-1").getSnapshot()).toEqual([]);
		const shapeless = memory(new Map([[boardHoldKey("hub-1"), JSON.stringify([{ id: 1 }, "x"])]]));
		expect(new BoardHold(shapeless, "hub-1").getSnapshot()).toEqual([]);
		const broken: SyncStringStorage = {
			getItemSync: () => null,
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {},
		};
		const hold = new BoardHold(broken, "hub-1");
		hold.hold(archive("local:a"), 1);
		expect(kinds(hold)).toEqual(["archive:local:a"]);
	});

	it("goes inert once its hub is forgotten", () => {
		const storage = memory();
		const hold = new BoardHold(storage, "hub-1");
		const held = hold.hold(stop("local:a"), 1);
		let calls = 0;
		hold.subscribe(() => calls++);
		hold.forget();
		expect(calls).toBe(1);
		expect(hold.getSnapshot()).toEqual([]);
		storage.values.clear();
		hold.hold(stop("local:b"), 2);
		hold.cancel(held.id);
		hold.settled(held.id);
		expect(storage.values.size).toBe(0);
		expect(calls).toBe(1);
		expect(hold.getSnapshot()).toEqual([]);
	});

	it("finds what waits for one session", () => {
		const hold = new BoardHold(memory(), "hub-1");
		hold.hold(archive("local:a"), 1);
		hold.hold(stop("local:b"), 2);
		hold.hold({ kind: "pin", target: { sessionRef: "local:a", sectionId: "s1" } }, 3);
		expect(heldFor(hold.getSnapshot(), "local:a").map((record) => record.action.kind)).toEqual(["archive", "pin"]);
	});
});

describe("a held Stop's turn (spec 7.5)", () => {
	const ended = "2026-09-28T10:00:00.000Z";
	const stamp = Date.parse(ended);
	it.each([
		["the turn seen still runs", { turnEndedAt: ended }, { activeTurnId: "t2", lastTurnEndedAt: stamp }, true],
		["a newer turn runs", { turnEndedAt: ended }, { activeTurnId: "t3", lastTurnEndedAt: stamp + 60_000 }, false],
		["the first turn still runs", { turnEndedAt: null }, { activeTurnId: "t1" }, true],
		[
			"a turn ended since a first turn was seen",
			{ turnEndedAt: null },
			{ activeTurnId: "t2", lastTurnEndedAt: stamp },
			false,
		],
		["no turn runs", { turnEndedAt: ended }, { lastTurnEndedAt: stamp }, false],
	] as const)("%s", (_name, seen, thread, expected) => {
		expect(turnStillSeen(seen, thread)).toBe(expected);
	});
});
