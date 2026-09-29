import { describe, expect, it } from "vitest";
import type { SyncStringStorage } from "../syncStringStorage";
import { memoryStorage as memory } from "../syncStringStorageTestUtils";
import { BoardHold, boardHoldKey, type HeldAction, heldFor, turnSeen, turnStillSeen, waitingLine } from "./boardHold";

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
	seen: { turnEndedAt, running: true },
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

	it("keeps a record being sent: it can't be cancelled, and a new action on its subject waits behind it", () => {
		const hold = new BoardHold(memory(), "hub-1");
		const sending = hold.hold(archive("local:a"), 1);
		hold.claim(sending.id);
		expect(hold.isSending(sending.id)).toBe(true);
		hold.cancel(sending.id);
		// The opposite change can't undo a request already on its way, so it
		// waits its turn.
		hold.hold(archive("local:a", false), 2);
		expect(kinds(hold)).toEqual(["archive:local:a", "unarchive:local:a"]);
		// Released, it is an ordinary held record again.
		hold.release(sending.id);
		expect(hold.isSending(sending.id)).toBe(false);
		hold.hold(archive("local:a"), 3);
		expect(kinds(hold)).toEqual(["archive:local:a"]);
	});

	it("names the action last chosen on a row's line, whatever its place in the order", () => {
		const hold = new BoardHold(memory(), "hub-1");
		hold.hold({ kind: "rename", ref: "local:a", title: "A", name: "One" }, 1);
		hold.hold(stop("local:a"), 2);
		expect(waitingLine(hold.getSnapshot(), "local:a", false)).toBe("Stop waits for the connection");
		// The second rename takes the first one's place, but it is the latest word.
		hold.hold({ kind: "rename", ref: "local:a", title: "A", name: "Two" }, 3);
		expect(waitingLine(hold.getSnapshot(), "local:a", false)).toBe("Rename waits for the connection");
		// Connected, it waits only for the journal to be free.
		expect(waitingLine(hold.getSnapshot(), "local:a", true)).toBe("Rename is waiting to send");
	});

	it("finds what waits for one session", () => {
		const hold = new BoardHold(memory(), "hub-1");
		hold.hold(archive("local:a"), 1);
		hold.hold(stop("local:b"), 2);
		hold.hold({ kind: "pin", target: { sessionRef: "local:a", sectionId: "s1" } }, 3);
		expect(heldFor(hold.getSnapshot(), "local:a").map((record) => record.action.kind)).toEqual(["archive", "pin"]);
	});
});

describe("the turn a press saw", () => {
	it("names the row's stamp, and counts a session asking a question as running a turn", () => {
		const row = (state: string, turn_ended_at?: string, ask_pending?: boolean) => ({
			state,
			turn_ended_at,
			ask_pending,
		});
		expect(turnSeen(row("active", "2026-09-28T10:00:00.000Z"))).toEqual({
			turnEndedAt: "2026-09-28T10:00:00.000Z",
			running: true,
		});
		// A question waits inside a live turn (an approval keeps the row "active").
		expect(turnSeen(row("awaiting", undefined, true))).toEqual({ turnEndedAt: null, running: true });
		// Otherwise awaiting is at rest: the turn ended with the ball in your court.
		expect(turnSeen(row("awaiting"))).toEqual({ turnEndedAt: null, running: false });
		expect(turnSeen(row("idle"))).toEqual({ turnEndedAt: null, running: false });
	});
});

describe("a held Stop's turn (spec 7.5)", () => {
	const ended = "2026-09-28T10:00:00.000Z";
	const stamp = Date.parse(ended);
	it.each([
		[
			"the turn seen still runs",
			{ turnEndedAt: ended, running: true },
			{ activeTurnId: "t2", lastTurnEndedAt: stamp },
			true,
		],
		[
			"a newer turn runs",
			{ turnEndedAt: ended, running: true },
			{ activeTurnId: "t3", lastTurnEndedAt: stamp + 60_000 },
			false,
		],
		["the first turn still runs", { turnEndedAt: null, running: true }, { activeTurnId: "t1" }, true],
		[
			"a turn ended since a first turn was seen",
			{ turnEndedAt: null, running: true },
			{ activeTurnId: "t2", lastTurnEndedAt: stamp },
			false,
		],
		// Seen at rest, and a turn began since: no turn has ended, so the stamp
		// alone can't tell; the press remembers that nothing ran.
		[
			"a turn began since the session was seen at rest",
			{ turnEndedAt: ended, running: false },
			{ activeTurnId: "t3", lastTurnEndedAt: stamp },
			false,
		],
		["no turn runs", { turnEndedAt: ended, running: true }, { lastTurnEndedAt: stamp }, false],
	] as const)("%s", (_name, seen, thread, expected) => {
		expect(turnStillSeen(seen, thread)).toBe(expected);
	});
});
