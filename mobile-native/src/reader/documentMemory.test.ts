import { describe, expect, it } from "vitest";
import type { SyncStringStorage } from "../syncStringStorage";
import { memoryStorage } from "../syncStringStorageTestUtils";
import { CONTINUE_READING_MS, DocumentMemory, type DocumentKey, forgetDocuments } from "./documentMemory";

const plan: DocumentKey = { sessionRef: "local:s-pr2138", path: "docs/superpowers/plans/settle.md" };
const other: DocumentKey = { sessionRef: "local:s-pr2138", path: "docs/design/flake-triage.md" };
const leaving = (progress: number) => ({
	title: "Fix the settle/drain race",
	blocks: ["h1", "p1", "p2"],
	position: { blockIndex: 1, blockHash: "p1", offset: 12, progress },
	sessionTitle: "Get PR 2138 Test Clean",
	updatedAt: "2026-09-26T11:39:00.000Z",
});

describe("what the phone remembers about a document", () => {
	it("keeps your place and the version you last read across a relaunch", () => {
		const storage = memoryStorage();
		let now = 1_000;
		const memory = new DocumentMemory(storage, "hub-1", () => now);
		memory.savePosition(plan, { blockIndex: 4, blockHash: "p4", offset: 3, progress: 0.3 });
		now = 2_000;
		memory.left(plan, leaving(0.62));
		const reopened = new DocumentMemory(storage, "hub-1", () => now);
		expect(reopened.position(plan)).toEqual({ blockIndex: 1, blockHash: "p1", offset: 12, progress: 0.62 });
		expect(reopened.lastRead(plan)).toEqual({
			blocks: ["h1", "p1", "p2"],
			readAt: 2_000,
			updatedAt: "2026-09-26T11:39:00.000Z",
		});
		expect(reopened.lastRead(other)).toBeNull();
	});

	it("leaves the Board a Continue reading row for two hours when you stop before the end (spec 7.1)", () => {
		let now = 10_000;
		const memory = new DocumentMemory(memoryStorage(), "hub-1", () => now);
		memory.left(plan, leaving(0.62));
		expect(memory.continueReading()).toEqual({
			sessionRef: "local:s-pr2138",
			path: "docs/superpowers/plans/settle.md",
			title: "Fix the settle/drain race",
			sessionTitle: "Get PR 2138 Test Clean",
			progress: 0.62,
			leftAt: 10_000,
			updatedAt: "2026-09-26T11:39:00.000Z",
		});
		now = 10_000 + CONTINUE_READING_MS - 1;
		expect(memory.continueReading()?.path).toBe(plan.path);
		now = 10_000 + CONTINUE_READING_MS;
		expect(memory.continueReading()).toBeNull();
	});

	it("clears the row when you finish the document or open it again, and a newer one replaces it", () => {
		const memory = new DocumentMemory(memoryStorage(), "hub-1", () => 5_000);
		memory.left(plan, leaving(0.5));
		memory.left(plan, leaving(0.98));
		expect(memory.continueReading()).toBeNull();
		memory.left(plan, leaving(0.5));
		memory.opened(other);
		expect(memory.continueReading()?.path).toBe(plan.path);
		memory.opened(plan);
		expect(memory.continueReading()).toBeNull();
		memory.left(plan, leaving(0.5));
		memory.left(other, { ...leaving(0.2), title: "Flake triage" });
		expect(memory.continueReading()?.title).toBe("Flake triage");
	});

	it("keeps unsent comments per document, in the order you wrote them", () => {
		const storage = memoryStorage();
		const memory = new DocumentMemory(storage, "hub-1", () => 7_000);
		const first = memory.addComment(plan, {
			blockIndex: 2,
			blockHash: "p2",
			quote: "Both take the tree lock.",
			text: "Split this.",
		});
		memory.addComment(plan, {
			blockIndex: 5,
			blockHash: "li3",
			quote: "Add a regression test.",
			text: "Force settle first.",
		});
		expect(new DocumentMemory(storage, "hub-1").comments(plan).map((comment) => comment.text)).toEqual([
			"Split this.",
			"Force settle first.",
		]);
		expect(memory.comments(other)).toEqual([]);
		memory.removeComment(plan, first.id);
		expect(memory.comments(plan).map((comment) => comment.text)).toEqual(["Force settle first."]);
		memory.clearComments(plan);
		expect(memory.comments(plan)).toEqual([]);
	});

	it("gives every comment on a document its own id, even one written the same moment after a relaunch", () => {
		const storage = memoryStorage();
		const first = new DocumentMemory(storage, "hub-1", () => 9_000).addComment(plan, {
			blockIndex: 0,
			blockHash: "h",
			quote: "q",
			text: "one",
		});
		const relaunched = new DocumentMemory(storage, "hub-1", () => 9_000);
		const second = relaunched.addComment(plan, { blockIndex: 0, blockHash: "h", quote: "q", text: "two" });
		expect(second.id).not.toBe(first.id);
		relaunched.removeComment(plan, first.id);
		expect(relaunched.comments(plan).map((comment) => comment.text)).toEqual(["two"]);
	});

	it("keeps the 100 most recent documents, and never drops one with unsent comments for that", () => {
		const storage = memoryStorage();
		let now = 0;
		const memory = new DocumentMemory(storage, "hub-1", () => now);
		memory.addComment(plan, { blockIndex: 0, blockHash: "h", quote: "q", text: "keep me" });
		for (let index = 0; index < 120; index += 1) {
			now = index + 1;
			memory.savePosition(
				{ sessionRef: "local:s", path: `doc-${index}.md` },
				{ blockIndex: 0, blockHash: "h", offset: 0, progress: 0.1 },
			);
		}
		const stored = JSON.parse(storage.values.get("evener.native.documents.hub-1") as string);
		expect(Object.keys(stored)).toHaveLength(100);
		expect(new DocumentMemory(storage, "hub-1").comments(plan).map((comment) => comment.text)).toEqual(["keep me"]);
		expect(new DocumentMemory(storage, "hub-1").position({ sessionRef: "local:s", path: "doc-119.md" })).not.toBeNull();
		expect(new DocumentMemory(storage, "hub-1").position({ sessionRef: "local:s", path: "doc-0.md" })).toBeNull();
	});

	it("reads corrupt or malformed storage as empty, and keeps working when the store throws", () => {
		const storage = memoryStorage(
			new Map([
				["evener.native.documents.hub-1", JSON.stringify({ x: { touchedAt: 1, position: { blockIndex: -1 } } })],
				["evener.native.continue-reading.hub-1", "{not json"],
			]),
		);
		const memory = new DocumentMemory(storage, "hub-1");
		expect(memory.continueReading()).toBeNull();
		expect(memory.position(plan)).toBeNull();
		const broken: SyncStringStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {
				throw new Error("disk");
			},
		};
		const offline = new DocumentMemory(broken, "hub-1", () => 1);
		offline.left(plan, leaving(0.5));
		expect(offline.continueReading()?.path).toBe(plan.path);
		expect(() => offline.opened(plan)).not.toThrow();
		expect(offline.continueReading()).toBeNull();
	});

	it("drops a pre-#2871 Continue reading trail but still reads the document's own records", () => {
		// A trail the previous version wrote: its session named twice, no
		// sessionTitle. It is dropped, not migrated (see parseTrail), while the
		// per-document position and lastRead data from the same store still parse.
		const key = JSON.stringify([plan.sessionRef, plan.path]);
		const storage = memoryStorage(
			new Map([
				[
					"evener.native.continue-reading.hub-1",
					JSON.stringify({
						sessionRef: plan.sessionRef,
						path: plan.path,
						title: "Fix the settle/drain race",
						reviewRef: plan.sessionRef,
						reviewTitle: "Get PR 2138 Test Clean",
						progress: 0.62,
						leftAt: 10_000,
					}),
				],
				[
					"evener.native.documents.hub-1",
					JSON.stringify({
						[key]: {
							position: { blockIndex: 1, blockHash: "p1", offset: 12, progress: 0.62 },
							lastRead: { blocks: ["h1", "p1", "p2"], readAt: 2_000 },
							touchedAt: 2_000,
						},
					}),
				],
			]),
		);
		const memory = new DocumentMemory(storage, "hub-1", () => 10_000);
		expect(memory.continueReading()).toBeNull();
		expect(memory.position(plan)).toEqual({ blockIndex: 1, blockHash: "p1", offset: 12, progress: 0.62 });
		expect(memory.lastRead(plan)?.blocks).toEqual(["h1", "p1", "p2"]);
	});

	it("keeps hubs apart, and forgets a removed hub", () => {
		const storage = memoryStorage();
		new DocumentMemory(storage, "hub-1", () => 1).left(plan, leaving(0.5));
		expect(new DocumentMemory(storage, "hub-2").lastRead(plan)).toBeNull();
		forgetDocuments(storage, "hub-1");
		expect([...storage.values.keys()]).toEqual([]);
	});

	it("tells subscribers when something changes", () => {
		const memory = new DocumentMemory(memoryStorage(), "hub-1", () => 1);
		let calls = 0;
		const stop = memory.subscribe(() => {
			calls += 1;
		});
		const before = memory.getRevision();
		memory.savePosition(plan, { blockIndex: 0, blockHash: "h", offset: 0, progress: 0.1 });
		expect(calls).toBe(1);
		expect(memory.getRevision()).toBe(before + 1);
		stop();
		memory.savePosition(plan, { blockIndex: 1, blockHash: "p", offset: 0, progress: 0.2 });
		expect(calls).toBe(1);
	});
});
