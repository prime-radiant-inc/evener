import { describe, expect, it } from "vitest";
import {
	captureReaderAnchor,
	comparePosition,
	furthestMeasuredRowBeforeTarget,
	isReaderAnchorLoaded,
	openingTarget,
	type ReaderAnchor,
	ReaderPositionRepository,
	ReaderRestoreAttempts,
	reachableReaderOffset,
	readerAnchorRow,
	readerKey,
	resolveReaderAnchor,
	restoreReaderCommand,
	exactRestoreDue,
} from "./readerPosition";
import type { SyncStringStorage } from "./syncStringStorage";
import type { TimelineRow } from "./timeline";

function storageFrom(values: Map<string, string>): SyncStringStorage {
	return {
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
function storage(): SyncStringStorage {
	return storageFrom(new Map<string, string>());
}
const row = (id: string, position: { entry: number; item: number }): TimelineRow => ({
	kind: "assistant",
	id,
	transcriptKey: `key-${id}`,
	markdown: id,
	streaming: false,
	position,
});
const anchor = (over: Partial<ReaderAnchor> = {}): ReaderAnchor => ({
	hubId: "hub-1",
	sessionRef: "ref-1",
	itemKey: "none",
	withinItemOffset: 0,
	touchedAt: 1,
	...over,
});
describe("reader positions", () => {
	it("retries a clamped restore when the saved position becomes reachable", () => {
		const desired = 4746;
		const clamped = reachableReaderOffset(desired, 4934, 598);
		expect(clamped).toBe(4336);
		const cut = { key: "anchor", height: 127, offset: clamped, clamped: true };
		expect(exactRestoreDue(cut, { key: "anchor", height: 127 }, clamped)).toBe(false);
		const reachable = reachableReaderOffset(desired, 6120, 598);
		expect(reachable).toBe(desired);
		expect(exactRestoreDue(cut, { key: "anchor", height: 127 }, reachable)).toBe(true);
		expect(reachableReaderOffset(desired, 300, 598)).toBe(0);
	});
	it("restores an anchor exactly once it has landed, however its row's measured y moves after", () => {
		// A virtualized list re-estimating rows it unmounted moves the anchor
		// row's measured y back and forth; chasing it made the list ping-pong.
		const landed = { key: "anchor", height: 144, offset: 9731, clamped: false };
		expect(exactRestoreDue(landed, { key: "anchor", height: 144 }, 9570)).toBe(false);
		expect(exactRestoreDue(landed, { key: "anchor", height: 144 }, 9731)).toBe(false);
	});
	it("restores again when the anchor's own row reflows, as a text-size change does", () => {
		const landed = { key: "anchor", height: 144, offset: 9731, clamped: false };
		expect(exactRestoreDue(landed, { key: "anchor", height: 180 }, 9731)).toBe(true);
	});
	it("restores a new anchor, or the first time", () => {
		expect(exactRestoreDue(null, { key: "anchor", height: 10 }, 100)).toBe(true);
		expect(
			exactRestoreDue({ key: "other", height: 10, offset: 5, clamped: false }, { key: "anchor", height: 10 }, 100),
		).toBe(true);
	});
	it("uses stable transcript identity and pair ordering", () => {
		expect(readerKey(row("wire", { entry: 1, item: 2 }))).toBe("key-wire");
		// A reply's round key outlasts its stream's wire id and its recording.
		const reply: TimelineRow = {
			kind: "assistant",
			id: "wire",
			transcriptKey: "key-wire",
			roundKey: "round:r1:agentMessage",
			markdown: "",
			streaming: false,
		};
		expect(readerKey(reply)).toBe("round:r1:agentMessage");
		// A communicate reply keys by its call, which the preview and the
		// recorded message both carry (issue #3173).
		const communicate: TimelineRow = {
			kind: "assistant",
			id: "wire",
			transcriptKey: "key-wire",
			roundKey: "round:r1:agentMessage",
			callKey: "call:c1:agentMessage",
			markdown: "",
			streaming: false,
		};
		expect(readerKey(communicate)).toBe("call:c1:agentMessage");
		expect(comparePosition({ entry: 1, item: 2 }, { entry: 1, item: 3 })).toBe(-1);
	});
	it("captures content-space measurements and restores with a negative view offset", () => {
		const rows = [row("a", { entry: 1, item: 1 }), row("b", { entry: 2, item: 1 })];
		const anchor = captureReaderAnchor(
			"hub",
			"session",
			rows[1],
			318,
			[{ key: readerKey(rows[1]), y: 300, height: 80 }],
			1,
		);
		if (!anchor) throw new Error("expected measured anchor");
		expect(anchor.withinItemOffset).toBe(18);
		expect(restoreReaderCommand(anchor, rows, [{ key: readerKey(rows[1]), y: 300, height: 80 }], 80)).toEqual({
			kind: "exact",
			index: 1,
			viewOffset: -18,
		});
	});
	it("anchors on the first row in view, passing over a time marker", () => {
		// A marker can leave the list when an older page lands, so an anchor on
		// it would find nothing to restore to.
		const marker: TimelineRow = { kind: "time", id: "time-turn_2", turnId: "turn_2", at: 0 };
		const rows = [row("a", { entry: 1, item: 1 }), marker, row("b", { entry: 2, item: 1 })];
		const measurements = new Map([
			[readerKey(rows[0]), { key: readerKey(rows[0]), y: 0, height: 100 }],
			[readerKey(marker), { key: readerKey(marker), y: 100, height: 30 }],
			[readerKey(rows[2]), { key: readerKey(rows[2]), y: 130, height: 80 }],
		]);
		expect(readerAnchorRow(rows, measurements, 50)).toBe(rows[0]);
		expect(readerAnchorRow(rows, measurements, 110)).toBe(rows[2]);
		expect(readerAnchorRow(rows, measurements, 300)).toBeUndefined();
	});
	it("does not capture an unmeasured row", () => {
		const item = row("a", { entry: 1, item: 1 });
		expect(captureReaderAnchor("hub", "session", item, 12, [], 1)).toBeNull();
	});
	it("captures turnsSeen when the caller supplies it", () => {
		const item = row("a", { entry: 1, item: 1 });
		const measurement = { key: readerKey(item), y: 0, height: 40 };
		const captured = captureReaderAnchor("hub", "session", item, 12, [measurement], 1, undefined, "turn_5");
		expect(captured?.turnsSeen).toBe("turn_5");
	});
	it("restores position-matched rows using current geometry after their key changes", () => {
		const position = { entry: 2, item: 1 };
		const prior = row("live", position);
		const current = row("persisted", position);
		const oldMeasurement = { key: readerKey(prior), y: 300, height: 180 };
		const anchor = captureReaderAnchor("hub", "session", prior, 420, [oldMeasurement], 1);
		if (!anchor) throw new Error("expected measured anchor");
		const rows = [row("earlier", { entry: 1, item: 0 }), current];
		const measurement = { key: readerKey(current), y: 600, height: 80 };
		expect(restoreReaderCommand(anchor, rows, [oldMeasurement], 96)).toEqual({
			kind: "approximate",
			offset: 216,
		});
		for (const measurements of [[measurement], [oldMeasurement, measurement]]) {
			expect(restoreReaderCommand(anchor, rows, measurements, 96)).toEqual({
				kind: "exact",
				index: 1,
				viewOffset: -80,
			});
		}
	});
	it("advances virtualization before exact restoration and preserves anchor on reflow", () => {
		const rows = Array.from({ length: 30 }, (_, i) => row(String(i), { entry: i, item: 1 }));
		const anchor = {
			hubId: "hub",
			sessionRef: "session",
			itemKey: readerKey(rows[20]),
			itemPosition: { entry: 20, item: 1 },
			withinItemOffset: 12,
			touchedAt: 1,
		};
		expect(restoreReaderCommand(anchor, rows, [], 80, false)).toBeNull();
		expect(restoreReaderCommand(anchor, rows, [], 80)).toEqual({
			kind: "approximate",
			offset: 20 * 80 + 12,
		});
		expect(restoreReaderCommand(anchor, rows, [{ key: readerKey(rows[20]), y: 600, height: 120 }], 80)).toEqual({
			kind: "exact",
			index: 20,
			viewOffset: -12,
		});
		expect(
			restoreReaderCommand(
				{ ...anchor, withinItemOffset: 999 },
				rows,
				[{ key: readerKey(rows[20]), y: 600, height: 120 }],
				80,
			),
		).toEqual({ kind: "exact", index: 20, viewOffset: -120 });
		const measurement = { key: readerKey(rows[20]), y: 600, height: 120 };
		// The exact command's view offset is -12: the row at y 600 restores to 612.
		const applied = { key: measurement.key, height: 120, offset: 612, clamped: false };
		expect(exactRestoreDue(null, measurement, 612)).toBe(true);
		expect(exactRestoreDue(applied, measurement, 612)).toBe(false);
		expect(exactRestoreDue({ ...applied, height: 80 }, measurement, 612)).toBe(true);
	});
	it("requires exact identity or exact protocol position", () => {
		const rows = [row("a", { entry: 1, item: 1 }), row("b", { entry: 3, item: 1 })];
		const anchor = {
			hubId: "hub",
			sessionRef: "session",
			itemKey: "gone",
			itemPosition: { entry: 2, item: 1 },
			withinItemOffset: 1,
			touchedAt: 1,
		};
		expect(resolveReaderAnchor(anchor, rows)).toBeNull();
	});
	it("recovers an anchor unmounted after a measured text-size restoration", () => {
		const item = row("marker09", { entry: 9, item: 0 });
		const measurement = { key: readerKey(item), y: 4827, height: 463 };
		const anchor = captureReaderAnchor("hub", "session", item, 5159, [measurement], 1);
		if (!anchor) throw new Error("expected anchor");
		const attempts = new ReaderRestoreAttempts();
		const missing = restoreReaderCommand(anchor, [item], [], 96);
		const measured = restoreReaderCommand(anchor, [item], [measurement], 96);
		if (!missing || !measured) throw new Error("expected restoration commands");
		expect(attempts.begin(missing)).toBe(true);
		expect(attempts.begin(missing)).toBe(false);
		for (let failure = 0; failure < 3; failure += 1) {
			expect(attempts.retryUnmeasured()).toBe(true);
			expect(attempts.begin(missing)).toBe(true);
		}
		expect(attempts.retryUnmeasured()).toBe(false);
		expect(attempts.begin(measured)).toBe(true);
		expect(exactRestoreDue({ key: measurement.key, height: 463, offset: 332, clamped: false }, measurement, 332)).toBe(
			false,
		);
		// Virtualization can remove the measured cell before the next effect.
		expect(attempts.begin(missing)).toBe(true);
		expect(attempts.begin(missing)).toBe(false);
		for (let failure = 0; failure < 3; failure += 1) {
			expect(attempts.retryUnmeasured()).toBe(true);
			expect(attempts.begin(missing)).toBe(true);
		}
		expect(attempts.retryUnmeasured()).toBe(false);
		expect(attempts.begin(missing)).toBe(false);
	});
	it("resets restore attempts for a new route or focus lifetime", () => {
		const attempts = new ReaderRestoreAttempts();
		const command = { kind: "approximate", offset: 1868 } as const;
		expect(attempts.begin(command)).toBe(true);
		for (let failure = 0; failure < 3; failure += 1) {
			expect(attempts.retryUnmeasured()).toBe(true);
			expect(attempts.begin(command)).toBe(true);
		}
		expect(attempts.retryUnmeasured()).toBe(false);
		attempts.reset();
		expect(attempts.begin(command)).toBe(true);
		expect(attempts.retryUnmeasured()).toBe(true);
	});
	it("retries an unchanged approximate target after monotonic layout progress", () => {
		const attempts = new ReaderRestoreAttempts();
		const command = { kind: "approximate", offset: 1868 } as const;
		expect(attempts.begin(command, 4)).toBe(true);
		expect(attempts.begin(command, 4)).toBe(false);
		expect(attempts.begin(command, 5)).toBe(true);
		expect(attempts.begin(command, 5)).toBe(false);
		expect(attempts.begin(command, 6)).toBe(true);
		expect(attempts.begin(command, 7)).toBe(true);
		expect(attempts.begin(command, 6)).toBe(false);
		expect(attempts.begin(command, 7)).toBe(false);
		expect(attempts.begin(command, 8)).toBe(true);
		expect(attempts.begin(command, 8)).toBe(false);
		expect(attempts.begin({ kind: "exact", index: 4, viewOffset: -2 }, 8)).toBe(true);
		expect(attempts.begin(command, 0)).toBe(true);
	});
	it("restarts the failure budget only after measured progress advances", () => {
		const attempts = new ReaderRestoreAttempts();
		const command = { kind: "approximate", offset: 1868 } as const;
		expect(attempts.begin(command, 4)).toBe(true);
		expect(attempts.retryUnmeasured(4)).toBe(true);
		expect(attempts.retryUnmeasured(4)).toBe(true);
		expect(attempts.retryUnmeasured(4)).toBe(true);
		expect(attempts.retryUnmeasured(4)).toBe(false);
		expect(attempts.begin(command, 5)).toBe(true);
		expect(attempts.retryUnmeasured(5)).toBe(true);
		expect(attempts.retryUnmeasured(5)).toBe(true);
		expect(attempts.retryUnmeasured(5)).toBe(true);
		expect(attempts.retryUnmeasured(5)).toBe(false);
	});
	it("uses row index progress when a virtualization window keeps its size", () => {
		const rows = [row("a", { entry: 1, item: 0 }), row("b", { entry: 2, item: 0 }), row("c", { entry: 3, item: 0 })];
		expect(furthestMeasuredRowBeforeTarget(rows, 2, [{ key: readerKey(rows[0]), y: 0, height: 40 }])).toBe(0);
		expect(furthestMeasuredRowBeforeTarget(rows, 2, [{ key: readerKey(rows[1]), y: 40, height: 40 }])).toBe(1);
	});
	it("resolves an exact row or exact protocol position only", () => {
		const anchor = {
			hubId: "hub",
			sessionRef: "session",
			itemKey: "gone",
			itemPosition: { entry: 2, item: 1 },
			withinItemOffset: 18,
			touchedAt: 1,
		};
		expect(resolveReaderAnchor(anchor, [row("a", { entry: 1, item: 1 }), row("b", { entry: 3, item: 1 })])).toBeNull();
		expect(resolveReaderAnchor({ ...anchor, itemKey: "key-a" }, [row("a", { entry: 1, item: 1 })])).toBe(0);
		expect(
			resolveReaderAnchor({ ...anchor, itemPosition: { entry: 3, item: 1 } }, [
				row("a", { entry: 1, item: 1 }),
				row("b", { entry: 3, item: 1 }),
			]),
		).toBe(1);
	});
	it("retains independent hub/session anchors and bounds the store", () => {
		const disk = storage();
		const repo = new ReaderPositionRepository(disk);
		const anchor = (hubId: string, sessionRef: string, touchedAt: number) => ({
			hubId,
			sessionRef,
			itemKey: "item",
			withinItemOffset: 4,
			touchedAt,
		});
		repo.save(anchor("a", "one", 1));
		repo.save(anchor("b", "two", 2));
		expect(repo.read("a", "one")?.withinItemOffset).toBe(4);
		expect(repo.read("b", "two")?.withinItemOffset).toBe(4);
		for (let i = 0; i < 101; i += 1) repo.save(anchor("bounded", String(i), i + 3));
		expect(repo.read("bounded", "0")).toBeNull();
		expect(repo.read("bounded", "100")).not.toBeNull();
	});
	it("removes every session for one hub from memory and persisted storage", () => {
		const values = new Map<string, string>();
		const disk = storageFrom(values);
		const repo = new ReaderPositionRepository(disk);
		const anchor = (hubId: string, sessionRef: string) => ({
			hubId,
			sessionRef,
			itemKey: `${hubId}-${sessionRef}`,
			withinItemOffset: 1,
			touchedAt: 1,
		});
		repo.save(anchor("gone", "one"));
		repo.save(anchor("gone", "two"));
		repo.save(anchor("kept", "one"));
		repo.removeHub("gone");
		expect(repo.read("gone", "one")).toBeNull();
		expect(repo.read("gone", "two")).toBeNull();
		expect(repo.read("kept", "one")).not.toBeNull();
		expect(new ReaderPositionRepository(storageFrom(values)).read("gone", "one")).toBeNull();
		expect(new ReaderPositionRepository(storageFrom(values)).read("kept", "one")).not.toBeNull();
	});
	it("removes a cached anchor left by a failed save and preserves other caches", () => {
		const values = new Map<string, string>();
		const disk = storageFrom(values);
		disk.setItemSync = () => {
			throw new Error("full");
		};
		const repo = new ReaderPositionRepository(disk);
		const anchor = (hubId: string) => ({
			hubId,
			sessionRef: "session",
			itemKey: hubId,
			withinItemOffset: 1,
			touchedAt: 1,
		});
		repo.save(anchor("gone"));
		repo.save(anchor("kept"));
		disk.setItemSync = (key, value) => void values.set(key, value);
		repo.removeHub("gone");
		expect(repo.read("gone", "session")).toBeNull();
		expect(repo.read("kept", "session")).toEqual(anchor("kept"));
	});
	it("does not throw when reader storage is unavailable", () => {
		const repo = new ReaderPositionRepository({
			getItemSync: () => {
				throw new Error("offline");
			},
			setItemSync: () => {
				throw new Error("offline");
			},
			removeItemSync: () => undefined,
		});
		expect(repo.read("hub", "session")).toBeNull();
		expect(() =>
			repo.save({
				hubId: "hub",
				sessionRef: "session",
				itemKey: "item",
				withinItemOffset: 1,
				touchedAt: 1,
			}),
		).not.toThrow();
	});
	it("keeps the in-memory anchor when persistence fails", () => {
		const disk = storage();
		disk.setItemSync = () => {
			throw new Error("full");
		};
		const anchor = {
			hubId: "hub",
			sessionRef: "session",
			itemKey: "item",
			withinItemOffset: 1,
			touchedAt: 1,
		};
		const repo = new ReaderPositionRepository(disk);
		repo.save(anchor);
		expect(new ReaderPositionRepository(disk).read("hub", "session")).toEqual(anchor);
	});
	it("keeps anchors when hub removal persistence fails", () => {
		const disk = storage();
		const anchor = {
			hubId: "hub",
			sessionRef: "session",
			itemKey: "item",
			withinItemOffset: 1,
			touchedAt: 1,
		};
		const repo = new ReaderPositionRepository(disk);
		repo.save(anchor);
		disk.removeItemSync = () => {
			throw new Error("full");
		};
		expect(() => repo.removeHub("hub")).toThrow("full");
		expect(repo.read("hub", "session")).toEqual(anchor);
	});
	it("propagates a failed disk read without clearing cached anchors", () => {
		let writes = 0;
		const disk: SyncStringStorage = {
			getItemSync: () => {
				throw new Error("unavailable");
			},
			setItemSync: () => {
				writes += 1;
			},
			removeItemSync: () => undefined,
		};
		const repo = new ReaderPositionRepository(disk);
		const anchor = {
			hubId: "hub",
			sessionRef: "session",
			itemKey: "item",
			withinItemOffset: 1,
			touchedAt: 1,
		};
		repo.save(anchor);
		expect(() => repo.removeHub("hub")).toThrow("unavailable");
		expect(repo.read("hub", "session")).toEqual(anchor);
		expect(writes).toBe(0);
	});
	it("does not write after a failed disk read", () => {
		let writes = 0;
		const repo = new ReaderPositionRepository({
			getItemSync: () => {
				throw new Error("unavailable");
			},
			setItemSync: () => {
				writes += 1;
			},
			removeItemSync: () => undefined,
		});
		repo.save({
			hubId: "hub",
			sessionRef: "session",
			itemKey: "item",
			withinItemOffset: 1,
			touchedAt: 1,
		});
		expect(writes).toBe(0);
	});
	it("rejects negative protocol positions", () => {
		const repo = new ReaderPositionRepository({
			...storage(),
			getItemSync: () =>
				JSON.stringify({
					"hub\u0000session": {
						hubId: "hub",
						sessionRef: "session",
						itemKey: "item",
						itemPosition: { entry: -1, item: 0 },
						withinItemOffset: 1,
						touchedAt: 1,
					},
				}),
		});
		expect(repo.read("hub", "session")).toBeNull();
	});
	it("keeps a newer failed write over older disk data and retains other hubs", () => {
		const old = {
			hubId: "hub",
			sessionRef: "session",
			itemKey: "old",
			withinItemOffset: 1,
			touchedAt: 1,
		};
		const other = {
			hubId: "other",
			sessionRef: "session",
			itemKey: "other",
			withinItemOffset: 2,
			touchedAt: 1,
		};
		const values = new Map([
			[
				"evener.reader-positions",
				JSON.stringify({
					"hub\u0000session": old,
					"other\u0000session": other,
				}),
			],
		]);
		const disk: SyncStringStorage = {
			getItemSync: (key) => values.get(key) ?? null,
			setItemSync: () => {
				throw new Error("disk full");
			},
			removeItemSync: (key) => void values.delete(key),
		};
		const newer = { ...old, itemKey: "new", touchedAt: 2 };
		new ReaderPositionRepository(disk).save(newer);
		const remounted = new ReaderPositionRepository(disk);
		expect(remounted.read("hub", "session")).toEqual(newer);
		expect(remounted.read("other", "session")).toEqual(other);
	});
});

it("recognizes filtered source members and grouped notices without paging or mutation", () => {
	const notice = {
		kind: "notice" as const,
		id: "event",
		transcriptKey: "event-key",
		position: { entry: 8, item: 1 },
		origin: "system" as const,
		tone: "system" as const,
		family: "diagnostic" as const,
		text: "event",
	};
	const activity = {
		kind: "activity" as const,
		id: "cluster",
		label: "tools",
		family: "tool" as const,
		state: "completed" as const,
		detail: {},
		members: [
			{
				id: "later",
				transcriptKey: "later-key",
				position: { entry: 9, item: 2 },
				label: "tool",
				family: "tool" as const,
				state: "completed" as const,
				detail: {},
			},
		],
	};
	const source = [notice, activity];
	const before = structuredClone(source);
	const anchor = {
		hubId: "hub",
		sessionRef: "session",
		itemKey: "later-key",
		withinItemOffset: 12,
		touchedAt: 1,
	};
	expect(isReaderAnchorLoaded(anchor, source)).toBe(true);
	expect(isReaderAnchorLoaded({ ...anchor, itemKey: "details:event" }, source)).toBe(true);
	expect(isReaderAnchorLoaded({ ...anchor, itemKey: "old", itemPosition: { entry: 9, item: 2 } }, source)).toBe(true);
	expect(isReaderAnchorLoaded({ ...anchor, itemKey: "unloaded", itemPosition: { entry: 9, item: 3 } }, source)).toBe(
		false,
	);
	expect(source).toEqual(before);
	expect(resolveReaderAnchor(anchor, [notice])).toBeNull();
	expect(resolveReaderAnchor(anchor, [{ ...activity, ...activity.members[0], kind: "activity" }])).toBe(0);
});

describe("a reading position inside a folded run (Review Focus 4)", () => {
	const run: TimelineRow = {
		kind: "run",
		id: "run:a",
		transcriptKey: "key-a",
		position: { entry: 4, item: 0 },
		steps: [
			{
				kind: "activity",
				id: "a",
				label: "read_file",
				family: "tool",
				state: "completed",
				detail: {},
				transcriptKey: "key-a",
				position: { entry: 4, item: 0 },
			},
			{
				kind: "activity",
				id: "b",
				label: "grep",
				family: "tool",
				state: "completed",
				detail: {},
				transcriptKey: "key-b",
				position: { entry: 4, item: 1 },
			},
		],
	};
	const rows: TimelineRow[] = [{ kind: "user", id: "u", text: "hi" }, run];

	it("finds the run by a later step's key or position", () => {
		expect(resolveReaderAnchor(anchor({ itemKey: "key-b" }), rows)).toBe(1);
		expect(resolveReaderAnchor(anchor({ itemKey: "gone", itemPosition: { entry: 4, item: 1 } }), rows)).toBe(1);
		expect(resolveReaderAnchor(anchor({ itemKey: "gone" }), rows)).toBeNull();
	});

	it("keys a run by its first step, so an older anchor on it still resolves", () => {
		expect(readerKey(run)).toBe("key-a");
		expect(resolveReaderAnchor(anchor({ itemKey: "key-a" }), rows)).toBe(1);
	});
});

describe("where a session opens (spec 7.3, ruling 31)", () => {
	const rows: TimelineRow[] = [
		{ kind: "user", id: "u1", text: "one", turnId: "turn_1" },
		{ kind: "assistant", id: "a1", markdown: "one", streaming: false, turnId: "turn_1" },
		{ kind: "time", id: "time:turn_2", turnId: "turn_2", at: 0 },
		{ kind: "user", id: "u2", text: "two", turnId: "turn_2" },
		{ kind: "assistant", id: "a2", markdown: "two", streaming: false, turnId: "turn_2" },
	];
	const turnIds = ["turn_1", "turn_2"];

	it("opens at the live end while a question or approval waits", () => {
		expect(openingTarget(anchor({ turnsSeen: "turn_1" }), rows, turnIds, true)).toEqual({ kind: "live" });
	});
	it("opens at the start of a reply that finished since you last reached the end", () => {
		expect(openingTarget(anchor({ turnsSeen: "turn_1" }), rows, turnIds, false)).toEqual({ kind: "row", index: 4 });
	});
	// The "unread result" (spec 7.3) starts with the agent's first row, runs
	// included, so a newer turn whose agent side opens with a run opens there,
	// not at the assistant reply that follows it.
	it("opens at a run when a newer turn's agent side starts with one", () => {
		const rowsWithRun: TimelineRow[] = [
			{ kind: "user", id: "u1", text: "one", turnId: "turn_1" },
			{ kind: "assistant", id: "a1", markdown: "one", streaming: false, turnId: "turn_1" },
			{ kind: "time", id: "time:turn_2", turnId: "turn_2", at: 0 },
			{ kind: "user", id: "u2", text: "two", turnId: "turn_2" },
			{ kind: "run", id: "run:a", steps: [], turnId: "turn_2" },
			{ kind: "assistant", id: "a2", markdown: "two", streaming: false, turnId: "turn_2" },
		];
		expect(openingTarget(anchor({ turnsSeen: "turn_1" }), rowsWithRun, turnIds, false)).toEqual({
			kind: "row",
			index: 4,
		});
	});
	// Your own images follow your message as an attachments row. They are not
	// the reply, and neither is any other attachments row: a step's images
	// ride inside its run, and any other attachments row follows the row that
	// produced it, so skipping them never skips the start of the reply.
	it("opens past the images you sent, at the reply itself", () => {
		const rowsWithImages: TimelineRow[] = [
			...rows.slice(0, 4),
			{
				kind: "attachments",
				id: "u2:attachments",
				items: [{ id: "u2:0", src: "/doc/image?1" }],
				sourceTranscriptKey: "u2",
				turnId: "turn_2",
			},
			{ kind: "run", id: "run:a", steps: [], turnId: "turn_2" },
		];
		expect(openingTarget(anchor({ turnsSeen: "turn_1" }), rowsWithImages, turnIds, false)).toEqual({
			kind: "row",
			index: 5,
		});
	});
	it("opens where you left off when nothing is newer, or the seen turn isn't loaded", () => {
		expect(openingTarget(anchor({ turnsSeen: "turn_2" }), rows, turnIds, false)).toEqual({ kind: "anchor" });
		expect(openingTarget(anchor({ turnsSeen: "turn_0" }), rows, turnIds, false)).toEqual({ kind: "anchor" });
		expect(openingTarget(anchor({}), rows, turnIds, false)).toEqual({ kind: "anchor" });
	});
	it("opens at the live end with no saved position", () => {
		expect(openingTarget(null, rows, turnIds, false)).toEqual({ kind: "live" });
	});
	it("stores turnsSeen, and still reads an anchor saved before it existed", () => {
		const disk = storage();
		new ReaderPositionRepository(disk).save(anchor({ turnsSeen: "turn_2" }));
		expect(new ReaderPositionRepository(disk).read("hub-1", "ref-1")?.turnsSeen).toBe("turn_2");
		new ReaderPositionRepository(disk).save(anchor({ sessionRef: "ref-old" }));
		expect(new ReaderPositionRepository(disk).read("hub-1", "ref-old")?.itemKey).toBe("none");
	});
});
