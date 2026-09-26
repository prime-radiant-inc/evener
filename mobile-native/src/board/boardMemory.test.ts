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
	it("treats everything as seen until the first load sets the epoch", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		expect(seen.isSeen({ ref: "a", updated_at: at(5) })).toBe(true);
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

	it("keeps unread marks and the newest 500 seen marks", () => {
		const storage = memoryStorage();
		const seen = new SeenMarkers(storage, "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		seen.markUnread("keep-unread");
		for (let i = 1; i <= 505; i++)
			seen.markSeen({ ref: `s${i}`, updated_at: new Date(Date.UTC(2026, 8, 26, 13, 0, i)).toISOString() });
		const stored = JSON.parse(storage.values.get("evener.native.seen.hub-a") as string);
		expect(Object.keys(stored.sessions)).toHaveLength(500);
		expect(stored.sessions["keep-unread"]).toEqual({ unread: true });
		expect(stored.sessions.s505).toBeDefined();
		expect(stored.sessions.s1).toBeUndefined();
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

it("forgetting a hub removes both of its keys and nothing else", () => {
	const storage = memoryStorage(
		new Map([
			["evener.native.seen.hub-a", "{}"],
			["evener.native.board-sections.hub-a", "{}"],
			["evener.native.seen.hub-b", "{}"],
		]),
	);
	forgetBoard(storage, "hub-a");
	expect([...storage.values.keys()]).toEqual(["evener.native.seen.hub-b"]);
});
