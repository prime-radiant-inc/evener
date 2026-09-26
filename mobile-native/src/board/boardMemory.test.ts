import { describe, expect, it } from "vitest";
import { type BoardStorage, FoldedSections, forgetBoard, SeenMarkers } from "./boardMemory";

function memoryStorage(values = new Map<string, string>()): BoardStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
const at = (minutes: number) => new Date(Date.UTC(2026, 8, 26, 12, minutes)).toISOString();

describe("seen markers", () => {
	it("treats everything as seen until the first load is adopted", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		expect(seen.isSeen({ ref: "a", updated_at: at(5) })).toBe(true);
	});

	it("says whether first run is done, and remembers it across a relaunch", () => {
		const storage = memoryStorage();
		const seen = new SeenMarkers(storage, "hub-a");
		expect(seen.adopted).toBe(false);
		seen.adoptEpoch([{ updated_at: at(0) }]);
		expect(seen.adopted).toBe(true);
		expect(new SeenMarkers(storage, "hub-a").adopted).toBe(true);
	});

	it("on first run, adopts the newest row as the epoch so nothing past floods Finished", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(5) }, { updated_at: at(9) }, {}]);
		expect(seen.isSeen({ ref: "old", updated_at: at(9) })).toBe(true);
		expect(seen.isSeen({ ref: "new", updated_at: at(10) })).toBe(false);
	});

	it("never moves the epoch once set", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(9) }]);
		seen.adoptEpoch([{ updated_at: at(30) }]);
		expect(seen.isSeen({ ref: "x", updated_at: at(20) })).toBe(false);
	});

	it.each([
		["an empty fleet", []],
		["a fleet whose rows carry no readable timestamp", [{}, { updated_at: "not a time" }]],
	])("completes first run on %s, so a later turn arrives unseen", (_, rows: { updated_at?: string }[]) => {
		const storage = memoryStorage();
		new SeenMarkers(storage, "hub-a").adoptEpoch(rows);
		const relaunched = new SeenMarkers(storage, "hub-a");
		expect(relaunched.isSeen({ ref: "later", updated_at: at(5) })).toBe(false);
		relaunched.adoptEpoch([{ updated_at: at(9) }]);
		expect(relaunched.isSeen({ ref: "later", updated_at: at(5) })).toBe(false);
	});

	it("treats a row with a missing or unreadable updated_at as seen", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		expect(seen.isSeen({ ref: "missing" })).toBe(true);
		expect(seen.isSeen({ ref: "garbled", updated_at: "not a time" })).toBe(true);
	});

	it("compares hub timestamps only, so a later turn is unseen again", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		seen.markSeen({ ref: "a", updated_at: at(10) });
		expect(seen.isSeen({ ref: "a", updated_at: at(10) })).toBe(true);
		expect(seen.isSeen({ ref: "a", updated_at: at(11) })).toBe(false);
	});

	it("keeps Mark as unread until the session is opened", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		seen.adoptEpoch([{ updated_at: at(30) }]);
		seen.markUnread("a");
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(false);
		seen.markSeen({ ref: "a", updated_at: at(1) });
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(true);
	});

	it("survives a relaunch and keeps hubs apart", () => {
		const storage = memoryStorage();
		const first = new SeenMarkers(storage, "hub-a");
		first.adoptEpoch([{ updated_at: at(0) }]);
		first.markSeen({ ref: "a", updated_at: at(10) });
		expect(new SeenMarkers(storage, "hub-a").isSeen({ ref: "a", updated_at: at(10) })).toBe(true);
		expect(storage.values.has("evener.native.seen.hub-a")).toBe(true);
		const other = new SeenMarkers(storage, "hub-b");
		other.adoptEpoch([{ updated_at: at(0) }]);
		expect(other.isSeen({ ref: "a", updated_at: at(10) })).toBe(false);
	});

	it("drops an epoch or a seen mark whose timestamp doesn't parse", () => {
		const storage = memoryStorage(
			new Map([
				[
					"evener.native.seen.hub-a",
					JSON.stringify({ adopted: true, epoch: "not a time", sessions: { a: { through: "nope" } } }),
				],
			]),
		);
		const seen = new SeenMarkers(storage, "hub-a");
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(true);
		seen.adoptEpoch([{ updated_at: at(5) }]);
		expect(seen.isSeen({ ref: "a", updated_at: at(5) })).toBe(true);
		expect(seen.isSeen({ ref: "a", updated_at: at(6) })).toBe(false);
		const stored = JSON.parse(storage.values.get("evener.native.seen.hub-a") as string);
		expect(stored).toEqual({ adopted: true, epoch: at(5), sessions: {} });
	});

	it("reads corrupt storage as empty", () => {
		const storage = memoryStorage(new Map([["evener.native.seen.hub-a", "{not json"]]));
		const seen = new SeenMarkers(storage, "hub-a");
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(true);
	});

	it("keeps working in memory when storage throws", () => {
		const broken: BoardStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {},
		};
		const seen = new SeenMarkers(broken, "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		seen.markSeen({ ref: "a", updated_at: at(3) });
		expect(seen.isSeen({ ref: "a", updated_at: at(3) })).toBe(true);
	});

	it("keeps 500 marks in all, unread kept first", () => {
		const storage = memoryStorage();
		const seen = new SeenMarkers(storage, "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		seen.markUnread("keep-unread");
		for (let i = 1; i <= 505; i++)
			seen.markSeen({ ref: `s${i}`, updated_at: new Date(Date.UTC(2026, 8, 26, 13, 0, i)).toISOString() });
		const stored = JSON.parse(storage.values.get("evener.native.seen.hub-a") as string);
		expect(Object.keys(stored.sessions)).toHaveLength(500);
		expect(stored.sessions["keep-unread"]).toEqual({ unread: true });
		// The unread mark takes one of the 500, so the newest 499 seen marks stay.
		expect(stored.sessions.s505).toBeDefined();
		expect(stored.sessions.s7).toBeDefined();
		expect(stored.sessions.s6).toBeUndefined();
	});

	it("tells subscribers when something changes", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		let calls = 0;
		const stop = seen.subscribe(() => calls++);
		const before = seen.getRevision();
		seen.markUnread("a");
		expect(calls).toBe(1);
		expect(seen.getRevision()).toBe(before + 1);
		stop();
		seen.markUnread("b");
		expect(calls).toBe(1);
	});
});

describe("folded sections", () => {
	it("falls back to the section's default, then remembers per hub", () => {
		const storage = memoryStorage();
		const folded = new FoldedSections(storage, "hub-a");
		expect(folded.isFolded("idle", true)).toBe(true);
		folded.setFolded("idle", false);
		expect(new FoldedSections(storage, "hub-a").isFolded("idle", true)).toBe(false);
		expect(new FoldedSections(storage, "hub-b").isFolded("idle", true)).toBe(true);
	});
});

describe("forgetBoard", () => {
	it("removes both of a hub's keys and nothing else, without throwing", () => {
		const storage = memoryStorage(
			new Map([
				["evener.native.seen.hub-a", "{}"],
				["evener.native.board-sections.hub-a", "{}"],
				["evener.native.seen.hub-b", "{}"],
			]),
		);
		expect(() => forgetBoard(storage, "hub-a")).not.toThrow();
		expect([...storage.values.keys()]).toEqual(["evener.native.seen.hub-b"]);
	});

	it("still removes the folded key when the seen key's removal fails, then throws", () => {
		const removed: string[] = [];
		const storage: BoardStorage = {
			getItemSync: () => null,
			setItemSync: () => {},
			removeItemSync: (key) => {
				if (key === "evener.native.seen.hub-a") throw new Error("disk");
				removed.push(key);
			},
		};
		expect(() => forgetBoard(storage, "hub-a")).toThrow();
		expect(removed).toEqual(["evener.native.board-sections.hub-a"]);
	});
});
