import { describe, expect, it } from "vitest";
import {
	captureReaderAnchor,
	comparePosition,
	isReaderAnchorLoaded,
	ReaderPositionRepository,
	ReaderRestoreAttempts,
	type ReaderStorage,
	readerKey,
	resolveReaderAnchor,
	restoreReaderCommand,
	shouldApplyExactRestore,
} from "./readerPosition";
import type { TimelineRow } from "./timeline";

function storageFrom(values: Map<string, string>): ReaderStorage {
	return {
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}
function storage(): ReaderStorage {
	return storageFrom(new Map<string, string>());
}
const row = (
	id: string,
	position: { entry: number; item: number },
): TimelineRow => ({
	kind: "assistant",
	id,
	transcriptKey: `key-${id}`,
	markdown: id,
	streaming: false,
	position,
});
describe("reader positions", () => {
	it("uses stable transcript identity and pair ordering", () => {
		expect(readerKey(row("wire", { entry: 1, item: 2 }))).toBe("key-wire");
		expect(comparePosition({ entry: 1, item: 2 }, { entry: 1, item: 3 })).toBe(
			-1,
		);
	});
	it("captures content-space measurements and restores with a negative view offset", () => {
		const rows = [
			row("a", { entry: 1, item: 1 }),
			row("b", { entry: 2, item: 1 }),
		];
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
		expect(
			restoreReaderCommand(
				anchor,
				rows,
				[{ key: readerKey(rows[1]), y: 300, height: 80 }],
				80,
			),
		).toEqual({
			kind: "exact",
			index: 1,
			viewOffset: -18,
		});
	});
	it("does not capture an unmeasured row", () => {
		const item = row("a", { entry: 1, item: 1 });
		expect(captureReaderAnchor("hub", "session", item, 12, [], 1)).toBeNull();
	});
	it("restores position-matched rows using current geometry after their key changes", () => {
		const position = { entry: 2, item: 1 };
		const prior = row("live", position);
		const current = row("persisted", position);
		const oldMeasurement = { key: readerKey(prior), y: 300, height: 180 };
		const anchor = captureReaderAnchor(
			"hub",
			"session",
			prior,
			420,
			[oldMeasurement],
			1,
		);
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
		const rows = Array.from({ length: 30 }, (_, i) =>
			row(String(i), { entry: i, item: 1 }),
		);
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
		expect(
			restoreReaderCommand(
				anchor,
				rows,
				[{ key: readerKey(rows[20]), y: 600, height: 120 }],
				80,
			),
		).toEqual({
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
		expect(shouldApplyExactRestore(null, measurement, null, 12)).toBe(true);
		expect(shouldApplyExactRestore(measurement, measurement, 12, 12)).toBe(
			false,
		);
		expect(
			shouldApplyExactRestore(
				{ ...measurement, height: 80 },
				measurement,
				12,
				12,
			),
		).toBe(true);
	});
	it("requires exact identity or exact protocol position", () => {
		const rows = [
			row("a", { entry: 1, item: 1 }),
			row("b", { entry: 3, item: 1 }),
		];
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
		const anchor = captureReaderAnchor(
			"hub",
			"session",
			item,
			5159,
			[measurement],
			1,
		);
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
		expect(shouldApplyExactRestore(measurement, measurement, 332, 332)).toBe(
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
		expect(attempts.begin({ kind: "exact", index: 4, viewOffset: -2 }, 8)).toBe(
			true,
		);
		expect(attempts.begin(command, 0)).toBe(true);
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
		expect(
			resolveReaderAnchor(anchor, [
				row("a", { entry: 1, item: 1 }),
				row("b", { entry: 3, item: 1 }),
			]),
		).toBeNull();
		expect(
			resolveReaderAnchor({ ...anchor, itemKey: "key-a" }, [
				row("a", { entry: 1, item: 1 }),
			]),
		).toBe(0);
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
		for (let i = 0; i < 101; i += 1)
			repo.save(anchor("bounded", String(i), i + 3));
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
		expect(
			new ReaderPositionRepository(storageFrom(values)).read("gone", "one"),
		).toBeNull();
		expect(
			new ReaderPositionRepository(storageFrom(values)).read("kept", "one"),
		).not.toBeNull();
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
		expect(new ReaderPositionRepository(disk).read("hub", "session")).toEqual(
			anchor,
		);
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
		const disk: ReaderStorage = {
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
		const disk: ReaderStorage = {
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
	expect(
		isReaderAnchorLoaded({ ...anchor, itemKey: "details:event" }, source),
	).toBe(true);
	expect(
		isReaderAnchorLoaded(
			{ ...anchor, itemKey: "old", itemPosition: { entry: 9, item: 2 } },
			source,
		),
	).toBe(true);
	expect(
		isReaderAnchorLoaded(
			{ ...anchor, itemKey: "unloaded", itemPosition: { entry: 9, item: 3 } },
			source,
		),
	).toBe(false);
	expect(source).toEqual(before);
	expect(resolveReaderAnchor(anchor, [notice])).toBeNull();
	expect(
		resolveReaderAnchor(anchor, [
			{ ...activity, ...activity.members[0], kind: "activity" },
		]),
	).toBe(0);
});
