import { describe, expect, it } from "vitest";
import type { SyncStringStorage } from "../syncStringStorage";
import { memoryStorage } from "../syncStringStorageTestUtils";
import { FoldedSections, forgetBoard, OrganizeByPreference, RecentSearches, SeenMarkers } from "./boardMemory";

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

	it("on first run, adopts the newest row as the epoch so nothing past arrives unseen", () => {
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
		const broken: SyncStringStorage = {
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

	it("keeps the newest 500 marks", () => {
		const storage = memoryStorage();
		const seen = new SeenMarkers(storage, "hub-a");
		seen.adoptEpoch([{ updated_at: at(0) }]);
		for (let i = 1; i <= 505; i++)
			seen.markSeen({ ref: `s${i}`, updated_at: new Date(Date.UTC(2026, 8, 26, 13, 0, i)).toISOString() });
		const stored = JSON.parse(storage.values.get("evener.native.seen.hub-a") as string);
		expect(Object.keys(stored.sessions)).toHaveLength(500);
		expect(stored.sessions.s505).toBeDefined();
		expect(stored.sessions.s6).toBeDefined();
		expect(stored.sessions.s5).toBeUndefined();
	});

	it("reads an unread record an older build stored as no mark at all", () => {
		const storage = memoryStorage(
			new Map([
				[
					"evener.native.seen.hub-a",
					JSON.stringify({ adopted: true, epoch: at(30), sessions: { a: { unread: true } } }),
				],
			]),
		);
		const seen = new SeenMarkers(storage, "hub-a");
		expect(seen.isSeen({ ref: "a", updated_at: at(1) })).toBe(true);
		expect(seen.isSeen({ ref: "a", updated_at: at(31) })).toBe(false);
	});

	it("tells subscribers when something changes", () => {
		const seen = new SeenMarkers(memoryStorage(), "hub-a");
		let calls = 0;
		const stop = seen.subscribe(() => calls++);
		const before = seen.getRevision();
		seen.markSeen({ ref: "a", updated_at: at(1) });
		expect(calls).toBe(1);
		expect(seen.getRevision()).toBe(before + 1);
		stop();
		seen.markSeen({ ref: "b", updated_at: at(1) });
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

describe("the Organize by choice", () => {
	it("starts at Project, then host and round-trips per hub", () => {
		const storage = memoryStorage();
		const first = new OrganizeByPreference(storage, "hub-a");
		expect(first.get()).toBe("project-host");
		first.set("host-project");
		expect(new OrganizeByPreference(storage, "hub-a").get()).toBe("host-project");
		expect(new OrganizeByPreference(storage, "hub-b").get()).toBe("project-host");
		expect(storage.values.get("evener.native.board-organize.hub-a")).toBe('"host-project"');
	});

	it("reads a value it doesn't know, or one that isn't JSON, as the default", () => {
		for (const stored of ['"sideways"', "host-project", "{not json"]) {
			const storage = memoryStorage(new Map([["evener.native.board-organize.hub-a", stored]]));
			expect(new OrganizeByPreference(storage, "hub-a").get()).toBe("project-host");
		}
	});

	it("never touches the folded sections, and they never touch it", () => {
		const storage = memoryStorage();
		const folded = new FoldedSections(storage, "hub-a");
		folded.setFolded("idle", false);
		new OrganizeByPreference(storage, "hub-a").set("host-project");
		folded.setFolded("projects", true);
		expect(JSON.parse(storage.values.get("evener.native.board-sections.hub-a") as string)).toEqual({
			idle: false,
			projects: true,
		});
		expect(new OrganizeByPreference(storage, "hub-a").get()).toBe("host-project");
	});

	it("keeps working in memory when storage throws", () => {
		const broken: SyncStringStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {},
		};
		const choice = new OrganizeByPreference(broken, "hub-a");
		expect(choice.get()).toBe("project-host");
		choice.set("host-project");
		expect(choice.get()).toBe("host-project");
	});
});

describe("recent searches", () => {
	it("keeps the most recent first, once each, under the hub's key", () => {
		const storage = memoryStorage();
		const recent = new RecentSearches(storage, "hub-a");
		expect(recent.list()).toEqual([]);
		recent.add("fix");
		recent.add("docs");
		recent.add(" fix ");
		expect(recent.list()).toEqual(["fix", "docs"]);
		expect(JSON.parse(storage.values.get("evener.native.recent-searches.hub-a") ?? "null")).toEqual(["fix", "docs"]);
		expect(new RecentSearches(storage, "hub-a").list()).toEqual(["fix", "docs"]);
	});

	it("keeps the last 8", () => {
		const recent = new RecentSearches(memoryStorage(), "hub-a");
		for (let index = 1; index <= 10; index++) recent.add(`query ${index}`);
		expect(recent.list()).toEqual([10, 9, 8, 7, 6, 5, 4, 3].map((index) => `query ${index}`));
	});

	it("keeps each hub's searches apart", () => {
		const storage = memoryStorage();
		new RecentSearches(storage, "hub-a").add("fix");
		expect(new RecentSearches(storage, "hub-b").list()).toEqual([]);
	});

	it("ignores an empty query", () => {
		const recent = new RecentSearches(memoryStorage(), "hub-a");
		recent.add("  ");
		expect(recent.list()).toEqual([]);
	});

	it("clears them all", () => {
		const storage = memoryStorage();
		const recent = new RecentSearches(storage, "hub-a");
		recent.add("fix");
		recent.clear();
		expect(recent.list()).toEqual([]);
		expect(new RecentSearches(storage, "hub-a").list()).toEqual([]);
	});

	it("reads garbled storage as no searches, and skips entries that aren't text", () => {
		const garbled = memoryStorage(new Map([["evener.native.recent-searches.hub-a", "{nope"]]));
		expect(new RecentSearches(garbled, "hub-a").list()).toEqual([]);
		const mixed = memoryStorage(new Map([["evener.native.recent-searches.hub-a", '["fix", 3, "", "docs"]']]));
		expect(new RecentSearches(mixed, "hub-a").list()).toEqual(["fix", "docs"]);
	});
});

describe("forgetBoard", () => {
	it("removes every key of a hub's own and nothing else, without throwing", () => {
		const storage = memoryStorage(
			new Map([
				["evener.native.seen.hub-a", "{}"],
				["evener.native.board-sections.hub-a", "{}"],
				["evener.native.board-organize.hub-a", '"host-project"'],
				["evener.native.recent-searches.hub-a", "[]"],
				["evener.native.board-hold.hub-a", "[]"],
				["evener.native.seen.hub-b", "{}"],
				["evener.native.board-organize.hub-b", '"host-project"'],
			]),
		);
		expect(() => forgetBoard(storage, "hub-a")).not.toThrow();
		expect([...storage.values.keys()].sort()).toEqual([
			"evener.native.board-organize.hub-b",
			"evener.native.seen.hub-b",
		]);
	});

	it("still removes the other keys when the seen key's removal fails, then throws", () => {
		const removed: string[] = [];
		const storage: SyncStringStorage = {
			getItemSync: () => null,
			setItemSync: () => {},
			removeItemSync: (key) => {
				if (key === "evener.native.seen.hub-a") throw new Error("disk");
				removed.push(key);
			},
		};
		expect(() => forgetBoard(storage, "hub-a")).toThrow();
		expect(removed).toEqual([
			"evener.native.board-sections.hub-a",
			"evener.native.board-organize.hub-a",
			"evener.native.recent-searches.hub-a",
			"evener.native.board-hold.hub-a",
		]);
	});
});
