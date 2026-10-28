// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { ThreadItem, Turn } from "@evener/appwire-client";
import { sessionTokens } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { MobileConversation, MobileTimelineItem } from "../../projectedRows";
import type { ConversationReadProjection } from "../services/conversation";
import { createConversationStore } from "./conversation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	commandExecItem,
	makeConversation,
	makeReadProjectionResult,
	makeThread,
	makeTurn,
	openProjectedThreadWith,
	openedStore,
	readServing,
	resyncFrame,
	rowById,
	rows,
	turnsPage,
	userMessageItem,
	wireTurn,
	wireTurnFragment,
	yieldMicrotask,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	describe("stale safety — generation and operation identity", () => {
		it("close increments generation so stale completion cannot clear new error", async () => {
			const { store } = await openedStore();
			const gen = store.getState().conversationGeneration;
			store.getState().close();
			expect(store.getState().conversationGeneration).toBeGreaterThan(gen);
			expect(store.getState().conversation).toBeNull();
		});

		it("stale rehydrate catch does not set error on newer generation", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), "cursor-1");
			await store.getState().openProjected(service, "ref-1");

			// Start a rehydrate that will fail.
			let rejectRehydrate: ((e: Error) => void) | null = null as ((e: Error) => void) | null;
			const failProjection = new Promise<ConversationReadProjection>((_resolve, reject) => {
				rejectRehydrate = reject;
			});
			const origReadProjection = service.readProjection;
			service.readProjection = async () => failProjection;

			const rehydratePromise = store.getState().rehydrate(service);

			// While rehydrate is in-flight, open a new conversation (new generation).
			service.readProjection = origReadProjection;
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-2" }), "cursor-2");
			await store.getState().openProjected(service, "ref-2");

			// Now the stale rehydrate fails.
			rejectRehydrate?.(new Error("stale rehydrate error"));
			await rehydratePromise.catch(() => {});

			// The stale error must NOT have overwritten the newer conversation.
			expect(store.getState().conversation?.threadId).toBe("thread-2");
			expect(store.getState().error).toBeNull();
		});

		it("loadOlder checks generation before applying results", async () => {
			const { service, store } = await openedStore();
			// Set a cursor so loadOlder has a page to request.
			store.setState({ olderCursor: "cursor-1" });

			// Start loadOlder that hangs.
			let resolveOlder: (() => void) | null = null as (() => void) | null;
			const hangOlder = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((r) => {
				resolveOlder = () => r({ items: [], nextCursor: undefined });
			});
			service.olderItems = hangOlder as never;
			const p = store.getState().loadOlder(service);

			// While loadOlder is in-flight, reset (bump generation).
			store.getState().reset();

			// Resolve the stale loadOlder.
			resolveOlder?.();
			await p;

			// The stale results should not have been applied.
			expect(store.getState().conversation).toBeNull();
		});
	});

	describe("paging — only thread/turns/list receives cursor, dedupe by source identity", () => {
		it("loadOlder prepends older items and dedupes by item id", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Start with one existing item.
			service.openConv = makeConversation({
				items: [{ kind: "user", id: "item-1", text: "existing" }],
			});
			await store.getState().open(service, "ref-1");
			// Set a cursor so loadOlder has a page to request.
			store.setState({ olderCursor: "cursor-1" });

			// loadOlder returns items that include a duplicate of item-1.
			service.olderItems = {
				items: [
					{ kind: "user", id: "item-0", text: "older" },
					{ kind: "user", id: "item-1", text: "duplicate" }, // duplicate id
				],
				nextCursor: "next-cursor",
			};
			await store.getState().loadOlder(service);

			const conv = store.getState().conversation;
			// item-1 should appear only once (deduped), and the older item
			// prepended.
			const ids = conv?.items.map((i) => i.id);
			expect(ids?.filter((id) => id === "item-1")).toHaveLength(1);
			// The older item should be at the front (prepended).
			expect(conv?.items[0]?.id).toBe("item-0");
		});

		it("loadOlder dedupes an attachment for a clustered member by transcriptKey, not just top-level id", async () => {
			// The current conversation has ONE clustered activity whose SECOND
			// (non-first) member carries transcriptKey "key-A" and wire id
			// "wire-A". Its own identity lives only inside .members[], invisible
			// to a top-level id — the package's item identity (transcriptKey when
			// both sides carry one) is what a page's replay of it folds through.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation({
				turns: [
					{
						id: "t-live",
						status: "completed",
						items: [
							{
								id: "wire-X",
								transcriptKey: "key-X",
								turnId: "t-live",
								type: "commandExecution",
								toolName: "shell",
								text: "",
								status: "completed",
								output: "first",
								callId: "call-x",
							},
							{
								id: "wire-A",
								transcriptKey: "key-A",
								turnId: "t-live",
								type: "commandExecution",
								toolName: "shell",
								text: "",
								status: "completed",
								output: "second",
								callId: "call-a",
							},
						],
					},
				],
			});
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// An older page replays an attachment FOR the "key-A" member under a
			// DIFFERENT wire id ("wire-B") — same source key, different reference.
			// The merge folds it into the live member by transcript key (D23d:
			// the package's item identity is the dedupe), so the surviving
			// attachment row keeps the LIVE wire id — a replay under "wire-B"
			// never produces a second row. A control attachment for a genuinely
			// unrelated source is included too, and is admitted.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t-page", [
							{
								id: "wire-B",
								transcriptKey: "key-A",
								turnId: "t-page",
								type: "commandExecution",
								toolName: "shell",
								status: "completed",
								outputImages: [{ source: "dup", url: "https://example.com/dup.png" }],
							},
							{
								id: "wire-C",
								transcriptKey: "unrelated-key",
								turnId: "t-page",
								type: "commandExecution",
								toolName: "shell",
								status: "completed",
								outputImages: [{ source: "new", url: "https://example.com/new.png" }],
							},
						]),
					],
					"next-cursor",
				),
				nextCursor: "next-cursor",
			};
			await store.getState().loadOlder(service);

			const conv = store.getState().conversation;
			const ids = conv?.items.map((i) => i.id);
			// The replayed member folded into the live one: its attachment rides
			// the live wire id, never a second row under the replayed id.
			expect(ids).not.toContain("wire-B:attachments");
			// A genuinely new attachment for an unrelated source is still admitted.
			expect(ids).toContain("wire-C:attachments");
		});

		// RoboRev round 38 (panel Medium 1): a pagination boundary can split a
		// cluster — the older page replays a clustered activity row ONE of whose
		// members the live conversation already holds, while its other members
		// are genuinely older history nobody else has. Dropping the whole row
		// on the single member match deletes that history; the members must
		// dedupe individually and the cluster rebuild from the survivors,
		// exactly as retainedPageRow does on the rehydrate side ("keeps the
		// paged cluster's other members when the snapshot holds one of them").
		it("loadOlder rebuilds a paged cluster from members the live conversation does not hold", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// The live window already holds the boundary member m2.
			service.openConv = makeConversation({
				items: [
					{
						kind: "activity",
						id: "m2",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "m2 live" },
					},
				],
			});
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// An older page whose turn carries three shell calls with the
			// boundary member in the middle.
			const member = (id: string): ThreadItem => ({
				id,
				type: "commandExecution",
				toolName: "shell",
				status: "completed",
				output: `${id} output`,
			});
			service.olderItems = {
				turnsPage: turnsPage([wireTurnFragment("t-cluster", [member("m1"), member("m2"), member("m3")])], undefined),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);

			const conv = store.getState().conversation;
			// D23d: the page's items fold per member — the boundary member the
			// live model already holds merges into the one item (its live copy
			// wins the fold, the "keep the newer live tail" rule), and the
			// genuinely older members nobody else held survive. The projection
			// clusters the turn's adjacent tools into one row.
			const activityRows = (conv?.items ?? []).filter(
				(item): item is Extract<MobileTimelineItem, { kind: "activity" }> => item.kind === "activity",
			);
			expect(activityRows).toHaveLength(1);
			expect(activityRows[0]?.members?.map((m) => m.id)).toEqual(["m1", "m2", "m3"]);
			// The boundary member's live output won the fold.
			expect(activityRows[0]?.members?.find((m) => m.id === "m2")?.detail.output).toBe("m2 live");
			// Each member appears exactly once across the whole timeline.
			expect(
				(conv?.items ?? [])
					.flatMap((item) => (item.kind === "activity" ? (item.members ?? [item]).map((m) => m.id) : []))
					.filter((id) => id === "m2"),
			).toHaveLength(1);
		});

		it("loadOlder retains the newest 500 while following, and keeps paging", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Start with 400 items.
			const items: MobileConversation["items"] = [];
			for (let i = 100; i < 500; i++) {
				items.push({ kind: "user", id: `item-${i}`, text: "" });
			}
			service.openConv = makeConversation({ items });
			await store.getState().open(service, "ref-1");
			// Set a cursor so loadOlder has a page to request.
			store.setState({ olderCursor: "cursor-1" });

			// loadOlder returns 200 more items — total would be 600, capped to 500.
			const olderItems: MobileConversation["items"] = [];
			for (let i = 0; i < 200; i++) {
				olderItems.push({ kind: "user", id: `item-old-${i}`, text: "" });
			}
			service.olderItems = { items: olderItems, nextCursor: "more" };
			await store.getState().loadOlder(service);

			const conv = store.getState().conversation;
			expect(conv?.items.length).toBe(500);
			// Paging goes on from the oldest row kept (trimmedAbove).
			expect(store.getState().olderCursor).toBe("more");
			expect(store.getState().trimmedAbove).toBe(true);
		});

		it("does not orphan an attachment at the cap boundary, and keeps paging when the trimmed source cannot render", async () => {
			// capItems slices by row count with no source/attachment awareness. A
			// turn with exactly 501 rows — [source, attachment, 499 filler] — caps
			// to the newest 500, and the orphan rule drops the attachment that
			// lost its source with it: never an image row with no message beside
			// it. D23d: the trimmed source stays in the MODEL (the keep-window
			// bound), so a page that re-serves it folds into the held copy — one
			// identity, its images intact — while the cap owns the render: the
			// fillers hold the newest 500 slots, so the source's rows re-project
			// and re-drop on every merge. Paging goes on from the oldest row
			// kept: once the reader leaves the live end the cap stops trimming,
			// and the next page names that row.
			const filler: ThreadItem[] = [];
			for (let i = 0; i < 499; i++) filler.push(userMessageItem(`filler-${i}`, ""));
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [
								{
									...commandExecItem("src-1", "shell", "completed"),
									output: "done",
									outputImages: [{ source: "s", url: "https://example.com/img.png" }],
								} as ThreadItem,
								...filler,
							],
						}),
					],
				}),
			);

			const ids = rows(store).map((item) => item.id);
			expect(ids.length).toBe(499);
			// No orphaned attachment: either both rows survived the cap or
			// neither did, never the attachment alone. There is now exactly one
			// free slot before the cap (500) bites again.
			expect(ids).not.toContain("src-1:attachments");
			expect(ids).not.toContain("src-1");

			// The older page re-serves the trimmed source under a real cursor:
			// the fold must keep one identity, and the stop must come from the
			// cap's own verdict on the page's row, not from the cursor running
			// out.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [
					{
						kind: "activity",
						id: "src-1",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "done" },
					},
				],
				nextCursor: "more",
			};
			await store.getState().loadOlder(service);

			// No duplicate was admitted beside the held copy, and no orphaned
			// attachment renders without its source.
			const recovered = rows(store).map((item) => item.id);
			expect(recovered).not.toContain("src-1");
			expect(recovered).not.toContain("src-1:attachments");
			// The model still holds the source — one item, its images intact.
			const held = (store.getState().conversation?.turns ?? [])
				.flatMap((turn) => turn.items)
				.filter((item) => item.id === "src-1");
			expect(held).toHaveLength(1);
			expect(held[0]?.outputImages).toHaveLength(1);
			// The wire's "more" cursor stands, and the trim is recorded.
			expect(store.getState().olderCursor).toBe("more");
			expect(store.getState().trimmedAbove).toBe(true);
		});

		it("keeps offering earlier items after the cap trims a page", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const items: MobileConversation["items"] = [];
			for (let i = 100; i < 500; i++) {
				items.push({ kind: "user", id: `item-${i}`, text: "" });
			}
			service.openConv = makeConversation({ items });
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			const olderItems: MobileConversation["items"] = [];
			for (let i = 0; i < 200; i++) {
				olderItems.push({ kind: "user", id: `item-old-${i}`, text: "" });
			}
			// The server still reports earlier items, and the cap trimming the
			// page no longer ends paging.
			service.olderItems = {
				items: olderItems,
				nextCursor: "more",
				hasEarlierItems: true,
			};
			await store.getState().loadOlder(service);

			expect(store.getState().olderCursor).toBe("more");
			expect(store.getState().trimmedAbove).toBe(true);
			// The flag agrees with the wire, and the next load asks for a page.
			expect(store.getState().hasEarlierItems).toBe(true);
			service.olderRequests = [];
			expect((await store.getState().loadOlder(service)).status).not.toBe("ignored");
			expect(service.olderRequests.map((request) => request.cursor)).toEqual(["more"]);
		});
	});

	describe("conversation's wire cursor and turn ownership never derive from the store's capped cursor or item eviction", () => {
		it("initial: conversation.olderCursor and turns come straight from the hydrated model", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation({
				usage: null,
				turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
				olderCursor: "cursor-1",
			});
			await store.getState().open(service, "ref-1");
			// The store's own cursor is a UI-only pagination-enablement value that
			// plain open() always starts at null (the live path establishes it
			// separately) - it is not the wire truth conversation.olderCursor is.
			expect(store.getState().olderCursor).toBeNull();
			const conv = store.getState().conversation!;
			expect(conv.olderCursor).toBe("cursor-1");
			expect(conv.turns.map((t) => t.id)).toEqual(["t2"]);
			expect(sessionTokens(conv)?.scope).toBe("loaded");
		});

		// Failing-first (1): round 4's own fix made conversation.olderCursor take
		// mergedCursor, which is currentSnapshot.olderCursor - the STORE's capped
		// cursor - whenever there is page history to preserve. That happens to
		// equal the wire truth when the cap was never hit (this file's earlier
		// "rehydrate preserves the older turns..." test doesn't distinguish the
		// two), but diverges the moment the cap forces the store's cursor to
		// null while the wire still has more.
		it("cap hit then rehydrate: conversation.olderCursor stays the wire truth", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// The window sits exactly at the row cap. The page's own rows all
			// sit below it, so they all fall off the cut when they merge (the
			// reader follows the live end): the store keeps paging from the
			// oldest row kept, and the page's payloads bound away with its
			// rows, leaving its usage as the only trace.
			const windowItems: ThreadItem[] = [];
			for (let i = 0; i < 500; i++) {
				windowItems.push({
					...userMessageItem(`item-${i}`, ""),
					position: { entry: 300 + i, item: 0 },
				});
			}
			const opened = {
				...makeReadProjectionResult(
					makeThread({
						turns: [
							makeTurn({
								id: "t2",
								items: windowItems,
								usage: { inputTokens: 60, outputTokens: 40 },
							}),
						],
					}),
				).conversation,
				olderCursor: "cursor-1",
			};
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");

			// 200 more rows below the window: the cap discards every one of them,
			// and the wire truth is still "more".
			const pageItems: ThreadItem[] = [];
			for (let i = 0; i < 200; i++) {
				pageItems.push({
					...userMessageItem(`item-old-${i}`, ""),
					position: { entry: i, item: 0 },
				});
			}
			service.olderItems = {
				turnsPage: turnsPage([wireTurnFragment("t1", pageItems, { inputTokens: 500, outputTokens: 20 })], "more"),
				nextCursor: "more",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().olderCursor).toBe("more");
			expect(store.getState().trimmedAbove).toBe(true);
			expect(store.getState().conversation?.olderCursor).toBe("more");

			// A same-session rehydrate must keep the fresh cursor: the page's
			// rows all fell off the cut and its payloads bounded away with them,
			// so no retained transcript evidence claims coverage — the reread's
			// own window cursor is the wire truth.
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().rehydrate(service);
			const conv = store.getState().conversation!;
			expect(conv.olderCursor).toBe("cursor-1");
			expect(sessionTokens(conv)?.scope).toBe("loaded");
		});

		it("rehydrate without page history: conversation.olderCursor and turns come straight from the fresh reread", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const opened = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				turns: [{ id: "t1", status: "completed", items: [], usage: { inputTokens: 10, outputTokens: 5 } }],
				olderCursor: "cursor-1",
			});
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");
			// No loadOlder ever ran - pageOwnedIds/pageOwnedTurnIds stay empty, so
			// there is no page history to preserve.

			const fresh = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 20, outputTokens: 8 } }],
				olderCursor: undefined,
			});
			service.readProjectionResult = readServing(fresh, null);
			await store.getState().rehydrate(service);
			const conv = store.getState().conversation!;
			// t1 is gone - there was no page history to preserve it, so the fresh
			// reread's own (smaller) window is authoritative, same as it always was.
			expect(conv.turns.map((t) => t.id)).toEqual(["t2"]);
			expect(conv.olderCursor).toBeUndefined();
			expect(sessionTokens(conv)).toEqual({ inputTokens: 20, outputTokens: 8, scope: "session" });
		});

		// Failing-first (2): the item-history merge (preservePageHistory) gates
		// on pageOwnedIds, which only tracks items that actually survived
		// loadOlder's own dedupe. A page whose only item duplicates one already
		// in hand contributes nothing to pageOwnedIds, but its turn is real and
		// must still survive - which is exactly why pageOwnedTurnIds is tracked
		// separately from pageOwnedIds.
		//
		// #1919 follow-up: the page turn now also carries payload items that
		// back no retained row (the projector-filtered class), so the test pins
		// the retained-turn bound's other half too: the turn and its usage
		// survive the rehydrate, with the payloads bounded to the compact
		// identity + usage shape.
		it("evicted/filtered page: a turn survives a rehydrate even when none of that page's items did", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// A window exactly at the row cap: a page merging below it falls off
			// the cut at once, so none of its items ever backs a retained row.
			const windowItems: ThreadItem[] = [];
			for (let i = 0; i < 500; i++) {
				windowItems.push({
					...userMessageItem(`item-${i}`, ""),
					position: { entry: 100 + i, item: 0 },
				});
			}
			const opened = {
				...makeReadProjectionResult(
					makeThread({
						turns: [
							makeTurn({
								id: "t2",
								items: windowItems,
								usage: { inputTokens: 60, outputTokens: 40 },
							}),
						],
					}),
				).conversation,
				olderCursor: "cursor-1",
			};
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");

			// The page's turn is genuinely new, but every one of its rows sits
			// below the full window and falls off the cut: the turn contributes no
			// display row, so the retained-turn bound sheds its payloads the
			// moment the page lands — pageItemIds gets nothing of it, and only
			// pageOwnedTurnIds keeps the turn itself alive.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"t1",
							[
								{
									id: "t1-evicted-0",
									turnId: "t1",
									type: "userMessage",
									text: "page payload 0",
									position: { entry: 1, item: 0 },
									status: "completed",
								},
								{
									id: "t1-evicted-1",
									turnId: "t1",
									type: "userMessage",
									text: "page payload 1",
									position: { entry: 2, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"cursor-2",
				),
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.map((t) => t.id)).toEqual(["t1", "t2"]);
			const landed = store.getState().conversation?.turns.find((turn) => turn.id === "t1")!;
			expect(landed.items).toEqual([]);

			// The reread's own window re-serves the retained rows; the evicted
			// page turn must survive it with its usage.
			const fresh = opened;
			service.readProjectionResult = readServing(fresh, "cursor-1");
			await store.getState().rehydrate(service);
			const conv = store.getState().conversation!;
			// Order is not asserted (see the "rehydrate preserves..." test above).
			expect(conv.turns.map((t) => t.id).sort()).toEqual(["t1", "t2"]);
			// The turn and its usage survive... with the payloads bounded: none of
			// its items back a retained row, so the retained-turn bound trims them
			// to the compact identity + usage shape.
			const surviving = conv.turns.find((turn) => turn.id === "t1")!;
			expect(surviving.items).toEqual([]);
			expect(surviving.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
			expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
		});
	});

	// D18 B3 round 8: a racing loadOlder's CURSOR MOVEMENT must survive a held
	// rehydrate even when the page retained no display rows. Row ownership
	// (pageOwnedIds) is the wrong signal for the store's own paging cursor: a
	// fully deduped or cap-evicted page owns no rows yet still advances the
	// cursor the next loadOlder must continue from. Dropping the
	// entryLoadOlderToken disjunct (round 2, for failed pages) had let a held
	// rehydrate regress that advancement to the fresh read's own window
	// cursor — re-offering a page the racing loadOlder had already consumed,
	// or resurrecting paging at a cursor exhausted history had stopped. What
	// separates the racing outcomes is not the token (a FAILED page bumps it
	// too) but whether the store's own cursor actually MOVED during the await.
	describe("turn merges reuse the package's own identity-aware merge, never an id-only filter", () => {
		// Failing-first (a): thread/turns/list is itself item-paginated, so a
		// turn can be split into fragments across the page boundary. An id-only
		// filter treats a same-id fragment as a pure duplicate and drops it,
		// losing whatever content/usage it alone carries.
		it("a turn split across a page boundary keeps its usage instead of being dropped by an id-only filter", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// The turn's later fragment is already loaded, with no usage of its
			// own — usage arrives on the fragment that continues further back.
			service.openConv = makeConversation({
				usage: null,
				turns: [{ id: "t1", status: "completed", items: [] }],
				olderCursor: "cursor-1",
			});
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			service.olderItems = {
				turnsPage: turnsPage([wireTurn("t1", 500, 20)]),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);

			const conv = store.getState().conversation!;
			// The two fragments merge into one turn, not two, and the usage the
			// older fragment carried survives — an id-only filter would have kept
			// only the already-loaded (usage-less) copy and lost it.
			expect(conv.turns).toHaveLength(1);
			expect(sessionTokens(conv)).toEqual({ inputTokens: 500, outputTokens: 20, scope: "session" });
		});

		// Failing-first (b): preserveTurnHistory being true only means page
		// history EXISTS somewhere in this session's lifetime, not that THIS
		// rehydrate's merge actually contributed anything beyond the fresh
		// reread's own window (its own window can grow to cover what page
		// history already supplied). Carrying the accumulated cursor
		// unconditionally then mislabels a now-complete read as "loaded".
		it("rehydrate with page history but a fully-covering fresh window: scope follows the fresh read, not a stale accumulated cursor", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const opened = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
				olderCursor: "cursor-1",
			});
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");

			service.olderItems = {
				turnsPage: turnsPage([wireTurn("t1", 500, 20)], "cursor-2"),
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);
			// Page history now exists (pageOwnedTurnIds has "t1").

			// A fresh rehydrate whose own window now covers BOTH turns and says
			// there is nothing more beyond it.
			const fresh = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				turns: [
					{ id: "t1", status: "completed", items: [], usage: { inputTokens: 500, outputTokens: 20 } },
					{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } },
				],
				olderCursor: undefined,
			});
			service.readProjectionResult = readServing(fresh, null);
			await store.getState().rehydrate(service);
			const conv = store.getState().conversation!;
			expect(conv.turns.map((t) => t.id).sort()).toEqual(["t1", "t2"]);
			expect(conv.olderCursor).toBeUndefined();
			expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "session" });
		});
	});

	it("rehydrate keeps the fresh cursor for disjoint page-history windows", async () => {
		const service = new FakeConversationService();
		const store = createConversationStore();
		const opened = makeConversation({
			threadId: "thread-1",
			instanceId: "instance-1",
			usage: null,
			turns: [{ id: "turn-initial", status: "completed", items: [], usage: { inputTokens: 10, outputTokens: 1 } }],
			olderCursor: "cursor-initial",
		});
		service.readProjectionResult = readServing(opened, "cursor-initial");
		await store.getState().openProjected(service, "ref-1");

		// The page reaches the beginning of the old window. Its undefined
		// cursor must not replace a fresh cursor if the next reread is disjoint.
		service.olderItems = {
			turnsPage: turnsPage([wireTurn("turn-older", 500, 20)]),
			nextCursor: undefined,
		};
		await store.getState().loadOlder(service);
		expect(store.getState().conversation?.olderCursor).toBeUndefined();

		service.readProjectionResult = {
			conversation: makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				turns: [{ id: "turn-fresh", status: "completed", items: [], usage: { inputTokens: 20, outputTokens: 2 } }],
				olderCursor: "fresh-cursor",
			}),
			olderCursor: "fresh-cursor",
		};
		await store.getState().rehydrate(service);

		const conv = store.getState().conversation!;
		expect(conv.turns.map((turn) => turn.id)).toEqual(["turn-older", "turn-initial", "turn-fresh"]);
		expect(conv.olderCursor).toBe("fresh-cursor");
		expect(sessionTokens(conv)).toEqual({ inputTokens: 530, outputTokens: 23, scope: "loaded" });
	});

	it("replacement rehydrate clears page turn ownership before the next refresh", async () => {
		const service = new FakeConversationService();
		const store = createConversationStore();
		const initial = makeConversation({
			threadId: "thread-1",
			instanceId: "instance-a",
			usage: null,
			turns: [{ id: "turn-a", status: "completed", items: [], usage: { inputTokens: 10, outputTokens: 1 } }],
			olderCursor: "cursor-a",
		});
		service.readProjectionResult = readServing(initial, "cursor-a");
		await store.getState().openProjected(service, "ref-1");
		service.olderItems = {
			turnsPage: turnsPage([wireTurn("turn-page-a", 500, 20)], "cursor-page-a"),
			nextCursor: "cursor-page-a",
		};
		await store.getState().loadOlder(service);

		service.readProjectionResult = {
			conversation: makeConversation({
				threadId: "thread-1",
				instanceId: "instance-b",
				usage: null,
				turns: [{ id: "turn-b", status: "completed", items: [], usage: { inputTokens: 20, outputTokens: 2 } }],
				olderCursor: "cursor-b",
			}),
			olderCursor: "cursor-b",
		};
		await store.getState().rehydrate(service);
		expect(store.getState().conversation?.turns.map((turn) => turn.id)).toEqual(["turn-b"]);

		// Task 17 (v6 read path): this second rehydrate needs a real
		// incarnation to genuinely CLEAR turn-b (a /clear-like event in real
		// v6) — mergeHistory never removes a turn on its own, so a plain
		// non-versioned "empty" read (bootGeneration "") would only MERGE
		// nothing new in, leaving turn-b standing; applyReadModel's
		// disposition (from a real bootGeneration differing from the prior
		// rehydrate's own seeded "") reads this as a genuine replace instead.
		service.readProjectionResult = {
			conversation: makeConversation(
				{
					threadId: "thread-1",
					instanceId: "instance-b",
					usage: null,
					turns: [],
					olderCursor: undefined,
				},
				true,
			),
			olderCursor: null,
		};
		await store.getState().rehydrate(service);

		const conv = store.getState().conversation!;
		expect(conv.turns).toEqual([]);
		expect(conv.olderCursor).toBeUndefined();
	});

	// Task 17 (v6 read path): two tests removed here ("rehydrate merges an
	// older turn fragment's fallback fields and items..." and its
	// "out-of-window page fragment" counterpart, both marked "#1919 follow-up
	// reshape") pinned the pre-v6 merge coalescing a retained turn's
	// UNMATCHED items into a DIFFERENT turn id whenever any ONE of its items
	// reissued there — turn-level coalescing by partial item overlap, the
	// same obsoleted mechanism the longer note above "#1919 follow-up" (this
	// file, ~line 6304) and "page history in front of the snapshot's rows"
	// (~line 13457) describes. mergeHistory never coalesces turns this way
	// — a real v6 turn id is stable — and reconcileCrossTurnReissues (this
	// file, and reducer.history.test.ts) only relocates the SPECIFIC item
	// identity that reissued, never a sibling item the fresh read never
	// mentioned.

	describe("a racing loadOlder's cursor movement survives a held rehydrate", () => {
		it("a successful racing page that retains no rows keeps its cursor advancement through the rehydrate", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const opened = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				items: [{ kind: "user", id: "existing", text: "existing" }],
				turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
				olderCursor: "cursor-1",
			});
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// Hold the rehydrate open on its read.
			let releaseRead!: (value: ConversationReadProjection) => void;
			service.readProjectionBlock = new Promise((resolve) => {
				releaseRead = resolve;
			});
			const rehydratePromise = store.getState().rehydrate(service);
			await yieldMicrotask();

			// While the rehydrate is in flight, loadOlder succeeds with a page
			// whose every row duplicates one already in hand — F10's dedupe drops
			// them all, so pageOwnedIds stays empty — but its nextCursor still
			// advances the store's own paging cursor.
			service.olderItems = {
				turnsPage: turnsPage([wireTurn("t1", 500, 20)], "cursor-2"),
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().olderCursor).toBe("cursor-2");

			// The fresh reread reports its own itemLimit-bounded window cursor,
			// which knows nothing about the page this client just consumed.
			releaseRead({
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user", id: "existing", text: "existing" }],
					turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
					olderCursor: "cursor-1",
				}),
				olderCursor: "cursor-1",
			});
			await rehydratePromise;

			const conv = store.getState().conversation!;
			// The racing page's cursor advancement survives: the store's own
			// cursor must not regress to the fresh read's window cursor and
			// re-offer the page that was already consumed.
			expect(store.getState().olderCursor).toBe("cursor-2");
			// conversation.olderCursor is the separate wire truth: the page turn
			// t1 has no transcript overlap with the fresh window, so the fresh
			// read's own wire cursor still stands there (scope labeling only).
			expect(conv.olderCursor).toBe("cursor-1");
		});

		it("a successful racing page that exhausted history keeps the store's honest null cursor through the rehydrate", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const opened = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				items: [{ kind: "user", id: "existing", text: "existing" }],
				turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
				olderCursor: "cursor-1",
			});
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			let releaseRead!: (value: ConversationReadProjection) => void;
			service.readProjectionBlock = new Promise((resolve) => {
				releaseRead = resolve;
			});
			const rehydratePromise = store.getState().rehydrate(service);
			await yieldMicrotask();

			// The racing page reaches the beginning of history: every row
			// duplicates one already in hand (no retained rows, pageOwnedIds
			// empty) and the wire offers no next cursor, so the store's own
			// paging cursor stops at null.
			service.olderItems = {
				turnsPage: turnsPage([wireTurn("t1", 500, 20)]),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(store.getState().olderCursor).toBeNull();

			releaseRead({
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user", id: "existing", text: "existing" }],
					turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
					olderCursor: "cursor-1",
				}),
				olderCursor: "cursor-1",
			});
			await rehydratePromise;

			// The rehydrate must not resurrect paging at the fresh read's window
			// cursor after the racing page exhausted it.
			expect(store.getState().olderCursor).toBeNull();
		});

		// RoboRev review round 2: page turn ownership alone must not pin the
		// store's own paging cursor across a DISJOINT refresh. After the cap
		// trimmed a page (its page turn a usage-only compact survivor), a fresh
		// window that shares no history with what the model retains speaks for
		// a range the cap never judged — the wire's own cursor is the only truth
		// the UI can page from.
		it("restores the store's paging cursor when a disjoint refresh opens new history", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const windowItems: ThreadItem[] = [];
			for (let i = 0; i < 500; i++) {
				windowItems.push({
					...userMessageItem(`item-${i}`, ""),
					position: { entry: 100 + i, item: 0 },
				});
			}
			const opened = {
				...makeReadProjectionResult(
					makeThread({
						turns: [
							makeTurn({
								id: "t-latest",
								items: windowItems,
								usage: { inputTokens: 60, outputTokens: 40 },
							}),
						],
					}),
				).conversation,
				olderCursor: "cursor-1",
			};
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");

			// The page's rows all fall below the full window: the cap discards
			// every one, the page's turn surviving as a usage-only compact page
			// turn.
			const pageItems: ThreadItem[] = [];
			for (let i = 0; i < 200; i++) {
				pageItems.push({
					...userMessageItem(`item-old-${i}`, ""),
					position: { entry: i, item: 0 },
				});
			}
			service.olderItems = {
				turnsPage: turnsPage([wireTurnFragment("t-old", pageItems, { inputTokens: 500, outputTokens: 20 })], "more"),
				nextCursor: "more",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().olderCursor).toBe("more");
			expect(store.getState().trimmedAbove).toBe(true);

			// A disjoint refresh: its window shares nothing with the retained
			// history, and the wire says there is more beyond it.
			const disjoint = {
				...makeReadProjectionResult(
					makeThread({
						turns: [
							makeTurn({
								id: "t-new",
								items: [
									{
										...userMessageItem("fresh-1", "fresh"),
										position: { entry: 1000, item: 0 },
									},
								],
							}),
						],
					}),
				).conversation,
				olderCursor: "cursor-9",
			};
			service.readProjectionResult = readServing(disjoint, "cursor-9");
			store.getState().applyNotification(resyncFrame());
			const awaitRehydrateRead = async (count: number): Promise<void> => {
				for (let i = 0; i < 40 && service.readProjectionCalls.length < count; i++) {
					await Promise.resolve();
				}
				await Promise.resolve();
				await Promise.resolve();
				await Promise.resolve();
			};
			await awaitRehydrateRead(2);
			// Task 17 (v6 read path): this used to expect the fresh read's own
			// cursor ("cursor-9") to win outright, on the OLD merge's own
			// "transcript overlap" signal (a real item-identity match, not just
			// position order) reading none between the retained window and this
			// disjoint one. applyReadModel's simpler position-only rule — keep the
			// held cursor whenever the held history's earliest item precedes the
			// fresh window's start — has no such identity check, and correctly so
			// for what it protects: mergeHistory never drops the retained window
			// (positions 100-599 stay exactly as loaded), so "more" (before
			// position 0) is still exactly where paging further back from what
			// the client already holds resumes, gap in the middle or not.
			expect(store.getState().olderCursor).toBe("more");

			// The UI can page again from the fresh window's own cursor.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t-mid", [
							{
								...userMessageItem("mid-1", "mid"),
								position: { entry: 500, item: 0 },
							},
						]),
					],
					undefined,
				),
				nextCursor: undefined,
			};
			const reloaded = await store.getState().loadOlder(service);
			expect(reloaded).not.toMatchObject({ status: "ignored" });
			expect(rows(store).some((row) => row.id === "mid-1")).toBe(true);
		});

		it("a failed racing loadOlder still lets the fresh read's cursor signal win", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const opened = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				usage: null,
				items: [{ kind: "user", id: "existing", text: "existing" }],
				turns: [],
				olderCursor: "cursor-1",
			});
			service.readProjectionResult = readServing(opened, "cursor-1");
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			let releaseRead!: (value: ConversationReadProjection) => void;
			service.readProjectionBlock = new Promise((resolve) => {
				releaseRead = resolve;
			});
			const rehydratePromise = store.getState().rehydrate(service);
			await yieldMicrotask();

			// A racing loadOlder FAILS: it bumps the page token but moves the
			// cursor not at all and owns nothing.
			service.olderItems = Promise.reject(new Error("page boom")) as never;
			await store
				.getState()
				.loadOlder(service)
				.catch(() => {});

			// The fresh reread reports no further history.
			releaseRead({
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user", id: "existing", text: "existing" }],
					turns: [],
					olderCursor: undefined,
				}),
				olderCursor: null,
			});
			await rehydratePromise;

			// The store's paging cursor follows the fresh signal — a failed page
			// must not pin the stale pre-race cursor through the rehydrate.
			expect(store.getState().olderCursor).toBeNull();
		});
	});

	// RoboRev round 1 (Medium): mergeOlderItemPage's own versioned disposition
	// (mergeVersionedPage's discard/defer/invalidate branches, pageDisposition)
	// returns `turns` unchanged by reference when nothing merged — loadOlder
	// must honor that instead of always reporting "loaded" and advancing the
	// cursor regardless.
	describe("loadOlder honors mergeOlderItemPage's own versioned disposition", () => {
		async function openVersioned(turns: Turn[]): Promise<{
			store: ReturnType<typeof createConversationStore>;
			service: FakeConversationService;
		}> {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread({ turns }), ALL_TRUE_CAPS, true);
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			return { store, service };
		}

		it("a page discarded as stale (shorter same-incarnation length) does not advance the cursor", async () => {
			const { store, service } = await openVersioned([makeTurn({ id: "t0", items: [] })]);
			// V6_HISTORY_BASELINE always hydrates at length 0; give the held
			// model a real length to page a genuinely SHORTER one against.
			const held = store.getState().conversation!;
			store.setState({
				conversation: { ...held, history: held.history && { ...held.history, length: 10 } },
				olderCursor: "cursor-1",
			});
			service.olderItems = {
				turnsPage: {
					data: [wireTurn("t-old", 5, 1)],
					nextCursor: "cursor-2",
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 5 },
				},
				nextCursor: "cursor-2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("ignored");
			expect(store.getState().olderCursor).toBe("cursor-1");
			expect(store.getState().conversation?.turns.some((turn) => turn.id === "t-old")).toBe(false);
			expect(store.getState().loadingOlder).toBe(false);
		});

		it("a page that lands after a resync (a newer epoch) requests the recovering rehydrate instead of merging", async () => {
			const { store, service } = await openVersioned([makeTurn({ id: "t0", items: [] })]);
			store.setState({ olderCursor: "cursor-1" });
			const readsBefore = service.readProjectionCalls.length;
			service.olderItems = {
				turnsPage: {
					data: [wireTurn("t-old", 5, 1)],
					nextCursor: "cursor-2",
					bootGeneration: "1",
					epoch: 2,
					snapshot: { incarnation: "inc-1", length: 1 },
				},
				nextCursor: "cursor-2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("ignored");
			expect(store.getState().olderCursor).toBe("cursor-1");
			for (let i = 0; i < 40 && service.readProjectionCalls.length <= readsBefore; i++) {
				await Promise.resolve();
			}
			expect(service.readProjectionCalls.length).toBeGreaterThan(readsBefore);
		});

		// RoboRev round 4 (Low): mobile's issuedGeneration never advances (it
		// never calls issueLatestWindowRead), so invalidatedAtGeneration can
		// only ever move from undefined to 0 — comparing it before/after a page
		// merge cannot tell "still invalid, nothing new" apart from "invalid
		// again at a NEWER signal" (a second, newer epoch/incarnation arriving
		// before the first recovering read lands). Detect the latter from
		// `awaited`, which DOES change, instead.
		it("a page that re-invalidates an already-invalid thread at a newer epoch still requests a rehydrate", async () => {
			const { store, service } = await openVersioned([makeTurn({ id: "t0", items: [] })]);
			const held = store.getState().conversation!;
			store.setState({
				conversation: {
					...held,
					history: held.history && {
						...held.history,
						invalidatedAtGeneration: 0,
						awaited: { bootGeneration: "1", epoch: 1 },
					},
				},
				olderCursor: "cursor-1",
			});
			const readsBefore = service.readProjectionCalls.length;
			// A page observing a STILL NEWER epoch than what already invalidated
			// the thread — invalidatedAtGeneration cannot move (mobile's is
			// always 0), but `awaited` re-arms to this newer signal.
			service.olderItems = {
				turnsPage: {
					data: [wireTurn("t-old", 5, 1)],
					nextCursor: "cursor-2",
					bootGeneration: "1",
					epoch: 2,
					snapshot: { incarnation: "inc-1", length: 1 },
				},
				nextCursor: "cursor-2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("ignored");
			for (let i = 0; i < 40 && service.readProjectionCalls.length <= readsBefore; i++) {
				await Promise.resolve();
			}
			expect(service.readProjectionCalls.length).toBeGreaterThan(readsBefore);
		});

		// RoboRev round 4 (Medium): mergeHistory (and withDisplay/displayTurns,
		// which returns held.turns by reference when there is no live overlay)
		// return the held `turns` array BY REFERENCE not just on a real discard,
		// but also on a genuine "merge" disposition whose page turns out to
		// carry nothing newer than what is already held (every turn/item at an
		// equal-or-higher version). `versionedMerge.turns === currentConv.turns`
		// cannot tell that apart from discard/defer/invalidate, so it was
		// misreporting this case as "ignored" without advancing the cursor -
		// mobile-native's screens.tsx re-requests the same cursor forever.
		it("a page whose items are all already held (a genuine no-op merge) still advances the cursor", async () => {
			const { store, service } = await openVersioned([makeTurn({ id: "t0", items: [], version: 2 })]);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				// No items and no version: this copy of t0 does not supersede the
				// held version-2 turn, so mergeHistory's own `changed` flag stays
				// false and it returns the held turns array unchanged by reference
				// - a real merge, not a discard.
				turnsPage: {
					data: [wireTurn("t0", 5, 1)],
					nextCursor: "cursor-2",
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 1 },
				},
				nextCursor: "cursor-2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("loaded");
			expect(store.getState().olderCursor).toBe("cursor-2");
			expect(store.getState().conversation?.olderCursor).toBe("cursor-2");
		});
	});

	// RoboRev round 1 (Medium): a failed page turn projects its own row keyed
	// failure:<turnId> (projectedRows.ts), never an item identity. A trim that
	// discards it keeps paging like any other.
	describe("a trim of a page's own failure-row contribution", () => {
		it("keeps paging when the cap discards a page's failed-turn row", async () => {
			const windowItems: ThreadItem[] = [];
			for (let i = 0; i < 500; i++) {
				windowItems.push({ ...userMessageItem(`item-${i}`, ""), position: { entry: 100 + i, item: 0 } });
			}
			const { service, store } = await openProjectedThreadWith(
				makeThread({ turns: [makeTurn({ id: "t-latest", items: windowItems })] }),
			);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						{
							id: "t-failed",
							itemsView: "fragment",
							status: "failed",
							error: { message: "boom" },
							items: [],
						},
					],
					"cursor-2",
				),
				nextCursor: "cursor-2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("loaded");
			// The cap discarded the page's only contribution (its failure row,
			// identified failure:t-failed — never a raw item identity), and
			// paging goes on from the oldest row kept.
			expect(store.getState().olderCursor).toBe("cursor-2");
			expect(store.getState().trimmedAbove).toBe(true);
		});
	});

	// RoboRev round 1 (Medium): applyReadModel returns the held model BY
	// REFERENCE, completely unchanged, when the read's disposition is
	// "discard" (a stale same-incarnation length here) — rehydrate must not
	// treat that discarded read's own turns as fresh, authoritative content
	// for the snapshot-authority strip pass, or a payload the discarded read
	// omits (but the still-current held item carries) gets cleared.
	describe("rehydrate does not act on a discarded (stale) read", () => {
		it("a same-incarnation reread with a shorter length does not clear a held payload it omits", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							items: [{ ...commandExecItem("call-1", "shell", "completed"), output: "held output" } as ThreadItem],
						}),
					],
				}),
				true,
			);
			const held = store.getState().conversation!;
			store.setState({
				conversation: { ...held, history: held.history && { ...held.history, length: 10 } },
			});
			// A stale response: same incarnation, SHORTER length, and it omits
			// the output the held item already carries.
			service.readProjectionResult = {
				conversation: {
					...makeReadProjectionResult(
						makeThread({
							turns: [
								makeTurn({
									id: "t0",
									items: [
										{ type: "commandExecution", id: "call-1", toolName: "shell", status: "completed" } as ThreadItem,
									],
								}),
							],
						}),
						ALL_TRUE_CAPS,
						true,
					).conversation,
				},
				olderCursor: null,
			};
			await store.getState().rehydrate(service);
			expect(rowById(store, "call-1")).toMatchObject({ detail: { output: "held output" } });
		});

		// RoboRev round 3 (Medium): the discarded read still SUCCEEDED (the wire
		// answered; the model just declined to apply it) — resumeProjected must
		// not read the untouched acceptedRehydrate=null as a failure and tear
		// down a healthy subscription over a read that merely lost an identity
		// race (e.g. a resync bumped epoch/bootGeneration past what this read's
		// own snapshot carries).
		it("resumeProjected stays open and keeps the subscription when its read is discarded as stale", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({ turns: [makeTurn({ id: "t0", items: [] })] }),
				true,
			);
			const held = store.getState().conversation!;
			store.setState({
				conversation: { ...held, history: held.history && { ...held.history, length: 10 } },
			});
			store.getState().suspendProjected();
			// A stale response: same incarnation, SHORTER length than the held 10.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({ turns: [makeTurn({ id: "t0", items: [] })] }),
				ALL_TRUE_CAPS,
				true,
			);
			await store.getState().resumeProjected(service, "ref-1");
			expect(store.getState().status).toBe("open");
			expect(store.getState().error).toBeNull();
			expect(service.notificationHandler).not.toBeNull();
		});
	});

	// #1919 follow-up (retained-turn bound): loadOlder's page turns used to be
	// exempt from every retention bound — once any older page loaded, the store
	// retained every page turn's FULL item payloads for the conversation's
	// lifetime behind the 500-row RETAINED_ITEM_CAP, so memory and the
	// per-refresh merge/sum cost grew with the whole loaded transcript. The
	// bound: a retained turn keeps full payloads only while it is inside the
	// keep-window (its items intersect the retained display rows); outside it,
	// the turn survives as compact identity + usage so sessionTokens' turn-summed
	// fallback still covers everything actually loaded.
	describe("loadOlder keeps conversation.turns/olderCursor in sync with items", () => {
		it("merges the older page's turns into conversation.turns and advances conversation.olderCursor", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation({
				usage: null,
				turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
				olderCursor: "cursor-1",
			});
			await store.getState().open(service, "ref-1");
			// Set a cursor so loadOlder has a page to request.
			store.setState({ olderCursor: "cursor-1" });

			service.olderItems = {
				turnsPage: turnsPage([wireTurn("t1", 500, 20)], "cursor-2"),
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);

			const conv = store.getState().conversation!;
			// The older turn is prepended, ahead of the page-one turn.
			expect(conv.turns.map((t) => t.id)).toEqual(["t1", "t2"]);
			// conversation.olderCursor mirrors the same cursor that now governs
			// the store's own paging (there is still more history to load).
			expect(conv.olderCursor).toBe("cursor-2");
			// Both turns now count: a session with no cumulative usage must not
			// keep reporting only the first page's total once a second page loads.
			expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
		});

		it("does not duplicate a turn the store already holds, but still adds a new one from the same page", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation({
				turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
				olderCursor: "cursor-1",
			});
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// A page race can hand back a turn the store already has (the same
			// dedupe concern F10 already covers for items) alongside a genuinely
			// new older turn on the same page.
			service.olderItems = {
				turnsPage: turnsPage([wireTurn("t1", 500, 20), wireTurn("t2", 999, 999)]),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);

			const conv = store.getState().conversation!;
			expect(conv.turns.map((t) => t.id)).toEqual(["t1", "t2"]);
			// The already-held t2 keeps its own version, not the incoming duplicate.
			expect(conv.turns.find((t) => t.id === "t2")?.usage).toEqual({ inputTokens: 1, outputTokens: 1 });
		});

		// D18 B3 round 4 (1): conversation.olderCursor must tell sessionTokens
		// the WIRE truth — the daemon has more history — whatever the cap
		// trimmed.
		it("keeps conversation.olderCursor at the wire's cursor when the item cap trims a page", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// A window exactly at the row cap: the page merging below it falls
			// off the cut.
			const windowItems: ThreadItem[] = [];
			for (let i = 0; i < 500; i++) {
				windowItems.push({
					...userMessageItem(`item-${i}`, ""),
					position: { entry: 100 + i, item: 0 },
				});
			}
			service.openConv = {
				...makeReadProjectionResult(
					makeThread({
						turns: [
							makeTurn({
								id: "t2",
								items: windowItems,
								usage: { inputTokens: 60, outputTokens: 40 },
							}),
						],
					}),
				).conversation,
				olderCursor: "cursor-1",
			};
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// 200 more rows below the window: the cap discards them all.
			const pageItems: ThreadItem[] = [];
			for (let i = 0; i < 200; i++) {
				pageItems.push({
					...userMessageItem(`item-old-${i}`, ""),
					position: { entry: i, item: 0 },
				});
			}
			service.olderItems = {
				turnsPage: turnsPage([wireTurnFragment("t1", pageItems, { inputTokens: 500, outputTokens: 20 })], "more"),
				nextCursor: "more", // the wire says there IS more history...
			};
			await store.getState().loadOlder(service);

			// ...and the store keeps paging from the oldest row kept.
			expect(store.getState().olderCursor).toBe("more");
			expect(store.getState().trimmedAbove).toBe(true);
			const conv = store.getState().conversation!;
			expect(conv.olderCursor).toBe("more");
			expect(sessionTokens(conv)?.scope).toBe("loaded");
		});

		// D18 B3 round 4 (2): a same-session rehydrate's reread window only
		// covers the current itemLimit-bounded turns, so a turn loaded via an
		// earlier loadOlder falls outside it — the same reason the item-history
		// merge above (preservePageHistory) exists for items.
		it("rehydrate preserves the older turns loaded via loadOlder", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const latest = {
				...makeReadProjectionResult(
					makeThread({
						turns: [
							makeTurn({
								id: "t2",
								items: [{ ...userMessageItem("new", "new"), position: { entry: 10, item: 0 } }],
								usage: { inputTokens: 60, outputTokens: 40 },
							}),
						],
					}),
				).conversation,
				olderCursor: "cursor-1",
			};
			service.readProjectionResult = readServing(latest, "cursor-1");
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t1", [{ ...userMessageItem("old", "old"), position: { entry: 1, item: 0 } }], {
							inputTokens: 500,
							outputTokens: 20,
						}),
					],
					"cursor-2",
				),
				nextCursor: "cursor-2",
			};
			await store.getState().openProjected(service, "ref-1");
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.map((t) => t.id)).toEqual(["t1", "t2"]);

			await store.getState().rehydrate(service);
			const conv = store.getState().conversation!;
			// The page's row stays prepended and its turn survives the reread:
			// the reread window does not cover t1, so page ownership — not the
			// fresh read — keeps it. Turn order is not asserted: only the SET of
			// turns/usage matters to sessionTokens, which sums regardless of
			// order.
			expect(conv.items.map((i) => i.id)).toEqual(["old", "new"]);
			expect(conv.turns.map((t) => t.id).sort()).toEqual(["t1", "t2"]);
			// Both turns still count (not just the fresh reread's own window), and
			// the scope stays "loaded" because the fresh cursor is still present.
			expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
		});
	});
});
