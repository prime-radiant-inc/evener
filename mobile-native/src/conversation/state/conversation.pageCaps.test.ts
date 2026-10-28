// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { AnyNotification, MutationReceipt, OverlayItem, ThreadItem } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { ActivityMember, MobileConversation, MobileTimelineItem } from "../../projectedRows";
import { createConversationStore, MAX_ITEM_BYTES, TRUNCATION_MARKER } from "./conversation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	agentMessageItem,
	commandExecItem,
	evenerWith,
	historyUpdated,
	makeControlledRead,
	makeConversation,
	makeReadProjectionResult,
	makeReceipt,
	makeThread,
	makeTurn,
	openProjectedThreadWith,
	openRunningTurn,
	readServing,
	resyncFrame,
	rowById,
	rows,
	statusChanged,
	textInput,
	userMessageItem,
	yieldMicrotask,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	// Fix round 1: Replace insertion-only ownership with accepted per-item live
	// ownership + monotonic revision. Every accepted lifecycle insertion OR
	// replacement, agent/reasoning/tool delta, reset, warning increments/marks;
	// rejected/missing/wrong/frozen no mark. Rehydrate captures entry live
	// revision. At commit: if authoritative contains ID but current revision
	// advanced after entry, preserve current updated version in authoritative
	// position and keep ownership; otherwise accept authoritative and clear that
	// ID's ownership. If authoritative omits ID, append only genuinely live-owned
	// current item; page IDs still prepend; unowned old drops.
	// The cut, measured end to end: the transport delivers one wire message per
	// onmessage event (client.ts:524, handleMessage :760-792), the service does
	// only synchronous work after awaiting the response (services/conversation.ts
	// :635-663), and this store does only synchronous work between that await
	// and its set — so a frame the transport delivers after the response reaches
	// applyNotification only after the snapshot has committed, and applies on
	// top of it.

	// A read response is ordered at the snapshot cut: every frame this store
	// folded before the response is already reflected in the snapshot that
	// arrives. So the snapshot decides every row it names AND every row it
	// omits — there is no live-ownership side to weigh against it. The one
	// thing the snapshot cannot know about is older history this client paged
	// in, which is prepended until D23d moves the pages into the model.
	describe("the reread's snapshot decides the rows", () => {
		// Each case: a row X that a live frame changes while the read is in
		// flight, and a reread that either carries its own X or omits it.
		const liveCases = [
			{
				kind: "a history/updated replacement",
				initialX: () => agentMessageItem("X", "original", "inProgress"),
				staleX: () => agentMessageItem("X", "from-the-reread", "completed"),
				frame: {
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 1,
						snapshot: { incarnation: "inc-1", length: 1 },
						items: [{ ...agentMessageItem("X", "live-final-text", "completed"), turnId: "t0" }],
					},
				} as AnyNotification,
				live: "live-final-text",
				settled: "from-the-reread",
			},
		] as const;

		// B + X + A open, a reread starts and hangs, `frame` lands while it is in
		// flight, then the reread commits `rereadItems`.
		async function raceReread(
			initialX: ThreadItem,
			rereadItems: ThreadItem[],
			frame: AnyNotification,
			options: { pageItems?: MobileConversation["items"] } = {},
		) {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							status: "inProgress",
							items: [userMessageItem("B", "base"), initialX, userMessageItem("A", "old-unowned")],
						}),
					],
					evener: evenerWith({ activeTurnId: "t0" }),
				}),
			);
			store.setState({ olderCursor: "cursor-1" });
			const ctrl = makeControlledRead(service);
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t0", status: "inProgress", items: rereadItems })],
					evener: evenerWith({ activeTurnId: "t0" }),
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			if (options.pageItems !== undefined) {
				service.olderItems = { items: options.pageItems, nextCursor: "cursor-2" };
				await store.getState().loadOlder(service);
				const pageIds = new Set((options.pageItems ?? []).map((item) => item.id));
				expect(rows(store).some((item) => pageIds.has(item.id))).toBe(true);
			}
			store.getState().applyNotification(frame);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			return { store, service };
		}

		const markdownOf = (store: ReturnType<typeof createConversationStore>, id: string): string | undefined => {
			const row = rowById(store, id);
			return row?.kind === "assistant" ? row.markdown : undefined;
		};

		it.each(liveCases)(
			"shows the live change under $kind until the reread commits",
			async ({ initialX, frame, live }) => {
				// The frame lands on the open conversation and is on screen at once;
				// the reread that follows is what settles it.
				const { store } = await openRunningTurn(
					[initialX()],
					{
						turns: [makeTurn({ id: "t0", status: "inProgress", items: [initialX()] })],
						evener: evenerWith({ activeTurnId: "t0" }),
					},
					true,
				);
				store.getState().applyNotification(frame);
				expect(markdownOf(store, "X")).toBe(live);
			},
		);

		it.each(liveCases)(
			"commits the reread's own X under $kind when the snapshot carries it",
			async ({ initialX, staleX, frame, settled }) => {
				const { store } = await raceReread(
					initialX(),
					[userMessageItem("B", "base"), staleX(), userMessageItem("C", "fresh-authoritative")],
					frame,
				);
				const ids = rows(store).map((item) => item.id);
				expect(markdownOf(store, "X")).toBe(settled);
				// Exactly once, in the position the snapshot gave it.
				expect(ids.filter((id) => id === "X")).toHaveLength(1);
				expect(ids.indexOf("B")).toBeLessThan(ids.indexOf("X"));
				expect(ids.indexOf("X")).toBeLessThan(ids.indexOf("C"));
				// Rows the snapshot does not carry are gone.
				expect(ids).not.toContain("A");
			},
		);

		it.each(liveCases)("drops X under $kind when the snapshot omits it", async ({ initialX, frame }) => {
			const { store } = await raceReread(
				initialX(),
				[userMessageItem("B", "base"), userMessageItem("C", "fresh-authoritative")],
				frame,
			);
			const ids = rows(store).map((item) => item.id);
			expect(ids).toEqual(["B", "C"]);
		});

		// The page history the snapshot cannot know about is prepended, keeps the
		// page's own cursor, and the cap still trims from the oldest end.
	});

	// Where a delta lands, now that every frame folds into the model and the
	// rows are projected from it: a reasoning summary delta is the reasoning
	// row's text, a tool-output delta is the tool row's output, and a delta
	// that names a row of the other kind is invisible — the field it writes is
	// not the field that row displays. None of them asks for a reread; the
	// next snapshot settles what a mis-addressed frame did (the read response
	// is ordered at the snapshot cut).
	describe("Residual 1: deferred trailing reread rechecks mutation revision inside effect", () => {
		it("M1 settles and queues; M2 starts before effect; release => zero reread; settle M2 => exactly one", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			// Start a rehydrate (R) that hangs.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, start M1 that hangs (mutation pending).
			let resolveM1: (() => void) | null = null as (() => void) | null;
			const hangM1 = new Promise<MutationReceipt>((r) => {
				resolveM1 = () => r(makeReceipt());
			});
			service.send = async () => hangM1;
			store.getState().send(service, textInput("m1"));
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			// Release R — detects mutation owner changed, schedules deferred
			// trailing reread. But M1 is still pending, so it stays deferred.
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			await yieldMicrotask();
			await yieldMicrotask();
			// Zero trailing reads while M1 is pending.
			expect(ctrl.getStartedCount()).toBe(1);
			// Settle M1 (success) — drainTrailingReread enqueues the trailing reread
			// via scheduler.request(). The effect is deferred to a microtask.
			(resolveM1 as () => void)();
			await yieldMicrotask(); // M1's settle resolves, drainTrailingReread fires
			// Now, BEFORE the trailing reread's scheduler effect fires, start M2
			// (new mutation pending). This must be synchronous — no await between
			// M1 settle and M2 start.
			let resolveM2: (() => void) | null = null as (() => void) | null;
			const hangM2 = new Promise<MutationReceipt>((r) => {
				resolveM2 = () => r(makeReceipt());
			});
			service.send = async () => hangM2;
			store.getState().send(service, textInput("m2"));
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			// Now let the trailing reread's scheduler effect fire. It must detect
			// M2 is pending (mutationOwnerRev advanced, mutation pending) and do
			// ZERO read — re-defer for M2's revision.
			await yieldMicrotask();
			await yieldMicrotask();
			await yieldMicrotask();
			// Still only 1 started read (the original R). No trailing reread fired.
			expect(ctrl.getStartedCount()).toBe(1);
			// Settle M2 (success) — drainTrailingReread should now fire exactly one
			// trailing reread.
			(resolveM2 as () => void)();
			await yieldMicrotask();
			await yieldMicrotask();
			// The trailing reread should start (2nd controlled read).
			await ctrl.started(2);
			await yieldMicrotask();
			ctrl.release();
			await ctrl.completed(2);
			await yieldMicrotask();
			// Exactly one trailing reread after M2 settles — no third reread.
			expect(ctrl.getStartedCount()).toBe(2);
		});

		it("B transition drops A: re-deferred trailing reread suppressed after switch to serviceB", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-A" }));
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			// Start a rehydrate (R) on serviceA that hangs.
			const ctrlA = makeControlledRead(serviceA);
			store.getState().applyNotification(resyncFrame("thread-A", "ref-A"));
			await ctrlA.started(1);
			await yieldMicrotask();
			// While R is in-flight, start M1 that hangs (mutation pending).
			let resolveM1: (() => void) | null = null as (() => void) | null;
			const hangM1 = new Promise<MutationReceipt>((r) => {
				resolveM1 = () => r(makeReceipt());
			});
			serviceA.send = async () => hangM1;
			store.getState().send(serviceA, textInput("m1"));
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			// Release R — schedules deferred trailing reread, M1 pending.
			ctrlA.release();
			await ctrlA.completed(1);
			await yieldMicrotask();
			await yieldMicrotask();
			// Settle M1 — drainTrailingReread enqueues trailing reread via scheduler.
			(resolveM1 as () => void)();
			await yieldMicrotask();
			// Before the effect fires, start M2 (new mutation pending).
			let resolveM2: (() => void) | null = null as (() => void) | null;
			const hangM2 = new Promise<MutationReceipt>((r) => {
				resolveM2 = () => r(makeReceipt());
			});
			serviceA.send = async () => hangM2;
			store.getState().send(serviceA, textInput("m2"));
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			// Let the effect fire — it should re-defer (M2 pending, zero read).
			await yieldMicrotask();
			await yieldMicrotask();
			// Switch to serviceB — new binding epoch.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-B" }));
			await store.getState().openProjected(serviceB, "ref-B");
			// Settle M2 on serviceA — drainTrailingReread should check binding,
			// find it stale (switched to B), and drop. Zero trailing reread on A.
			(resolveM2 as () => void)();
			await yieldMicrotask();
			await yieldMicrotask();
			await yieldMicrotask();
			// serviceA: 1 (openProjected) + 1 (R) = 2. No trailing reread.
			expect(serviceA.readProjectionCalls.length).toBe(2);
			// serviceB: 1 (openProjected) only.
			expect(serviceB.readProjectionCalls.length).toBe(1);
		});
	});

	// Residual 2: exact live-notification-owned item IDs per binding, separate
	// from pageOwned IDs. Mark IDs only from actual accepted item lifecycle/live
	// notifications (and relevant deltas); clear/reconcile on open/transition/
	// authoritative inclusion. Page merge: prepend only current-only pageOwned
	// history, commit authoritative projection, append only current-only liveOwned
	// tail; drop current-only items owned by neither as omitted old history.
	// Dedupe/order/cap newest tail.

	describe("dedupe within one page", () => {
		it("dedupes duplicate IDs within the incoming page (the later fragment wins), preserving order", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							items: [userMessageItem("base", "base")],
						}),
					],
				}),
			);
			store.setState({ olderCursor: "cursor-1" });

			// Page with a duplicate ID within itself: page-A, page-B, page-A (repeat).
			service.olderItems = {
				items: [
					{ kind: "user", id: "page-A", text: "A" },
					{ kind: "user", id: "page-B", text: "B" },
					{ kind: "user", id: "page-A", text: "A-repeat" },
				],
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);

			const items = store.getState().conversation?.items ?? [];
			const ids = items.map((i) => i.id);
			// page-A appears exactly once.
			expect(ids.filter((id) => id === "page-A").length).toBe(1);
			// The package's reconcile is later-wins: the repeat fragment's provided
			// content wins, and the survivor sits at the identity's first page
			// position, so the page's own order is preserved.
			const pageA = items.find((i) => i.id === "page-A");
			if (pageA?.kind === "user") {
				expect(pageA.text).toBe("A-repeat");
			}
			// page-B appears exactly once.
			expect(ids.filter((id) => id === "page-B").length).toBe(1);
			// The page's rows land below the retained window, in page order.
			expect(items.map((i) => i.id)).toEqual(["page-A", "page-B", "base"]);
		});

		it("dedupes duplicate IDs within page AND against retained items", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							items: [userMessageItem("existing", "existing")],
						}),
					],
				}),
			);
			store.setState({ olderCursor: "cursor-1" });

			// Page: "existing" (dup of retained), "page-A", "page-A" (dup within page).
			service.olderItems = {
				items: [
					{ kind: "user", id: "existing", text: "dup" },
					{ kind: "user", id: "page-A", text: "A" },
					{ kind: "user", id: "page-A", text: "A-repeat" },
				],
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);

			const items = store.getState().conversation?.items ?? [];
			const ids = items.map((i) => i.id);
			// "existing" appears exactly once (retained version kept).
			expect(ids.filter((id) => id === "existing").length).toBe(1);
			const existing = items.find((i) => i.id === "existing");
			if (existing?.kind === "user") {
				expect(existing.text).toBe("existing");
			}
			// "page-A" appears exactly once.
			expect(ids.filter((id) => id === "page-A").length).toBe(1);
		});

		it("dedupes overlapping fragments by transcriptKey when wire IDs differ", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							items: [
								{
									type: "userMessage",
									id: "wire-current",
									transcriptKey: "stable-item",
									position: { entry: 3, item: 0 },
									text: "current",
								},
							],
						}),
					],
				}),
			);
			store.setState({ olderCursor: "opaque-cursor" });
			// The hub lists a page's turns in chronological order, so the
			// page-only row precedes the fragment that overlaps the window.
			service.olderItems = {
				items: [
					{ kind: "user", id: "older", text: "older", position: { entry: 1, item: 0 } },
					{
						kind: "user",
						id: "wire-overlap",
						transcriptKey: "stable-item",
						position: { entry: 2, item: 0 },
						text: "stale overlap",
					},
				],
				nextCursor: "next",
			};

			await store.getState().loadOlder(service);

			const items = store.getState().conversation?.items ?? [];
			expect(items.filter((item) => item.transcriptKey === "stable-item")).toHaveLength(1);
			expect(items.find((item) => item.transcriptKey === "stable-item")?.id).toBe("wire-current");
			expect(items.map((item) => item.id)).toEqual(["older", "wire-current"]);
		});

		it("rehydrates visible state after a stale v4 cursor", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [makeTurn({ id: "t0", items: [userMessageItem("old", "old")] })],
				}),
			);
			store.setState({ olderCursor: "stale" });
			service.olderItems = Promise.reject(
				new WireError("stale transcript cursor", -32020, {
					evenerErrorInfo: "transcriptItemCursorStale",
				}),
			) as never;
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t1", items: [userMessageItem("fresh", "fresh")] })],
				}),
			);

			await store.getState().loadOlder(service);

			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["fresh"]);
			expect(service.readProjectionCalls.length).toBe(2);
		});
	});

	// --- who owns a row's truncation across a page, a reread and the cap
	// from FINAL retained/merged items on every authoritative install path
	// (open/openProjected/rehydrate/page/lifecycle): the byte bound is applied
	// to whatever text a row carries at publish time, so an authoritative short
	// version is short, a row the cap drops leaves nothing behind, and a row
	// that keeps growing keeps showing the same bounded prefix.
	//
	// C3: authoritative oversized→short→delta applies (open + rehydrate + openProjected)
	// C4: the cap shows the newest rows; a fresh item is judged on its own
	// protocol reset→start→delta: the reset removes the row, the restart streams anew
	// paged oversized item bounded with the marker once
	// lifecycle started/completed oversized then short
	// No vacuous `if` assertions — direct expects on the resolved item.
	describe("who owns a row's truncation", () => {
		// Helper: open a conversation via openProjected with the given raw ThreadItem
		// array (uses projectConversation so families are set from the canonical projector).
		async function openProjectedWithItems(items: ThreadItem[]): Promise<{
			store: ReturnType<typeof createConversationStore>;
			service: FakeConversationService;
		}> {
			const service = new FakeConversationService();
			// Versioned: the test's own frames are history/updated, which an
			// unversioned model always invalidates (drops) on arrival — a real
			// gap, not the live delta this test means to pin.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({ turns: [makeTurn({ id: "t0", items })] }),
				ALL_TRUE_CAPS,
				true,
			);
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			return { store, service };
		}

		// Decision 2: item/agentMessage/reset removes the item it names — the
		// model drops it, so its row goes with it and the stream starts again
		// under the item/started that follows.

		// Truncation ownership must key off ONE canonical identity (transcriptKey
		// ?? id — timelineIdentity) everywhere. Projection/rehydrate already freeze
		// by that identity; these three cover the live delta guards and the
		// lifecycle/rehydrate cleanup paths, which used the raw wire id instead.

		it("identity: a short completion shows short, whatever the started item carried", async () => {
			// item/started carries an oversized output, so the row is bounded; the
			// completion that replaces it is short, and the row is short with it.
			const { store } = await openProjectedWithItems([userMessageItem("base", "base")]);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("tool-1", "shell", "inProgress"),
							transcriptKey: "shared-key",
							callId: "call-A",
							output: "x".repeat(MAX_ITEM_BYTES + 100),
							turnId: "t0",
						},
					],
				}),
			);
			const started = rowById(store, "tool-1");
			expect(started?.kind === "activity" && started.detail.output?.endsWith(TRUNCATION_MARKER)).toBe(true);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("tool-1", "shell", "completed"),
							transcriptKey: "shared-key",
							callId: "call-A",
							output: "short-result",
							turnId: "t0",
						},
					],
				}),
			);

			expect(rowById(store, "tool-1")).toMatchObject({
				kind: "activity",
				detail: { output: "short-result" },
			});
		});

		// The snapshot is authoritative over the delta that preceded its
		// response: the row shows the reread's short text, and the next delta
		// appends to that.
	});

	// --- loadOlder preserves
	// already-frozen current items; I2 rehydrate preserves only superseded IDs
	// still actually frozen after the accepted live update; M1 observational
	// omission/cap tests through reread/page/delta (no lifecycle clearing).
	describe("a page load preserves what the reread committed", () => {
		async function openProjectedWithItems(items: ThreadItem[]): Promise<{
			store: ReturnType<typeof createConversationStore>;
			service: FakeConversationService;
		}> {
			const { service, store } = await openProjectedThreadWith(
				makeThread({ turns: [makeTurn({ id: "t0", items })] }),
				true,
			);
			return { store, service };
		}

		// I1: loadOlder — a row already at the bound stays at it after a page
		// load, and so does an oversized row the page itself brings: both are cut
		// to MAX_ITEM_BYTES by the same pass over whatever text they carry.

		// I1: loadOlder — incoming raw oversized page item freezes independently, and
		// an already-frozen current item AND the page item both freeze with one
		// marker each.

		// I1: loadOlder — capped current item loses freeze (no stale effect). When
		// the cap trims an already-frozen current item, re-introducing it via a page
		// load with short content must not re-freeze.
		it("I1 loadOlder: capped frozen current item loses freeze, re-introduced short not frozen", async () => {
			// Open with an oversized item (frozen) + one small item. The oversized
			// item is retained (under cap). Then rehydrate OMITTING the oversized
			// item — reconciliation removes its freeze. Then load a page bringing
			// it back with SHORT content — it must NOT be frozen.
			const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
			const { store, service } = await openProjectedWithItems([
				userMessageItem("B", "base"),
				agentMessageItem("X", oversized, "inProgress"),
			]);
			// Verify X is frozen.
			const xBefore = store.getState().conversation?.items.find((i) => i.id === "X");
			expect(xBefore?.kind === "assistant" && xBefore.markdown.endsWith("… truncated")).toBe(true);

			// Rehydrate omitting X — reconciliation removes the freeze for X.
			// Task 17 (v6 read path): a genuinely new incarnation is what actually
			// drops X here — the initial open above is a real v6 model (versioned:
			// true), so mergeHistory never removes an item on its own, and a
			// same-incarnation reread would only ADD "B" rather than dropping X.
			const omittingX = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t0", items: [userMessageItem("B", "base")] })],
				}),
				ALL_TRUE_CAPS,
				true,
			).conversation;
			service.readProjectionResult = readServing(
				{ ...omittingX, history: omittingX.history && { ...omittingX.history, incarnation: "inc-2" } },
				null,
			);
			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.items.find((i) => i.id === "X")).toBeUndefined();

			// Load a page bringing X back with SHORT content — must NOT be frozen.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [{ kind: "user", id: "X", text: "short-page" }],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			const reintroduced = store.getState().conversation?.items.find((i) => i.id === "X");
			expect(reintroduced).toBeDefined();
			expect(reintroduced?.kind).toBe("user");
		});

		// A reread that omits the row removes it; the row comes back only when a
		// later frame or snapshot carries it, judged on its own content.

		// M1: observational cap via rehydrate+page. Open with an oversized item,
		// rehydrate OMITTING it (the row goes with the snapshot), then load a page
		// bringing it back with short content — the row is short, with nothing
		// about the oversized version carried over.
		it("M1 loadOlder cap: capped frozen item re-introduced via page with short content not frozen", async () => {
			const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
			const { store, service } = await openProjectedWithItems([
				userMessageItem("B", "base"),
				agentMessageItem("X", oversized, "inProgress"),
			]);
			// Verify X is frozen.
			const xBefore = store.getState().conversation?.items.find((i) => i.id === "X");
			expect(xBefore?.kind === "assistant" && xBefore.markdown.endsWith("… truncated")).toBe(true);

			// Rehydrate omitting X — reconciliation removes the freeze for X.
			// Task 17 (v6 read path): a genuinely new incarnation is what actually
			// drops X here — the initial open above is a real v6 model (versioned:
			// true), so mergeHistory never removes an item on its own, and a
			// same-incarnation reread would only ADD "B" rather than dropping X.
			const omittingX2 = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t0", items: [userMessageItem("B", "base")] })],
				}),
				ALL_TRUE_CAPS,
				true,
			).conversation;
			service.readProjectionResult = {
				conversation: {
					...omittingX2,
					history: omittingX2.history && { ...omittingX2.history, incarnation: "inc-2" },
				},
				olderCursor: null,
			};
			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.items.find((i) => i.id === "X")).toBeUndefined();

			// Load a page bringing X back with SHORT content — must NOT be frozen.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [{ kind: "user", id: "X", text: "short-page" }],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			const reintroduced = store.getState().conversation?.items.find((i) => i.id === "X");
			expect(reintroduced).toBeDefined();
			expect(reintroduced?.kind).toBe("user");
		});
	});

	// --- what a page and the 500-row cap
	// do to a row that was over the byte limit.
	//
	// A page item is judged on the text it carries, never on what a row of the
	// same identity used to hold. When an appended row (item/started,
	// item/completed, a warning) pushes past the cap, the oldest row is trimmed
	// and a later re-introduction of that identity shows its own content.
	describe("a page against the 500-row cap", () => {
		async function openProjectedWithItems(
			items: ThreadItem[],
			options: { running?: boolean; versioned?: boolean } = {},
		): Promise<{
			store: ReturnType<typeof createConversationStore>;
			service: FakeConversationService;
		}> {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [makeTurn({ id: "t0", status: options.running ? "inProgress" : "completed", items })],
					...(options.running ? { evener: evenerWith({ activeTurnId: "t0" }) } : {}),
				}),
				options.versioned ?? false,
			);
			return { store, service };
		}

		it("commits exactly the reread's rows when an item's wire id and transcriptKey differ", async () => {
			const items: ThreadItem[] = [];
			for (let i = 0; i < 499; i++) items.push(userMessageItem(`u-${i}`, ""));
			items.push({
				...agentMessageItem("wire-x", "live", "inProgress"),
				transcriptKey: "stable-x",
			});
			const { store, service } = await openProjectedWithItems(items);
			expect(rows(store).filter((item) => item.transcriptKey === "stable-x")).toHaveLength(1);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								...agentMessageItem("wire-x", "live", "completed"),
								transcriptKey: "stable-x",
							},
							turnId: "t0",
						},
					],
				}),
			);
			// A reread whose snapshot ends before that item: the row goes with it,
			// exactly once, leaving nothing behind under either identity.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t0", items: items.slice(0, 499) })],
				}),
			);
			await store.getState().rehydrate(service);
			expect(rows(store).filter((item) => item.transcriptKey === "stable-x")).toEqual([]);
			expect(rowById(store, "wire-x")).toBeUndefined();
		});

		// A row the reread dropped can come back from an older page, judged on
		// its own content — nothing about the bounded version it had before
		// carries over. Until D23d moves the pages into the model, a page row is
		// history the model does not hold, so a live delta does not reach it; the
		// next snapshot that carries the item is what changes it.
		it("M1 omission: a page brings a dropped row back short, and a snapshot updates it", async () => {
			const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
			const { store, service } = await openProjectedWithItems([
				userMessageItem("B", "base"),
				agentMessageItem("X", oversized, "inProgress"),
			]);
			const xBefore = rowById(store, "X");
			expect(xBefore?.kind === "assistant" && xBefore.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);

			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t0", items: [userMessageItem("B", "base")] })],
				}),
			);
			await store.getState().rehydrate(service);
			expect(rowById(store, "X")).toBeUndefined();

			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [{ kind: "assistant", id: "X", markdown: "short-page", streaming: false }],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rowById(store, "X")).toMatchObject({ kind: "assistant", markdown: "short-page" });

			// The snapshot that carries X again is what moves it on.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							items: [userMessageItem("B", "base"), agentMessageItem("X", "short-page appended", "completed")],
						}),
					],
				}),
			);
			await store.getState().rehydrate(service);
			expect(rowById(store, "X")).toMatchObject({
				kind: "assistant",
				markdown: "short-page appended",
			});
		});

		// The cap drops the oldest rows as new ones arrive — whether the new row
		// comes from an item frame or from a warning — and a page that brings an
		// old row back shows it on its own content.
		it.each([
			[
				"an item frame",
				{
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 1,
						snapshot: { incarnation: "inc-1", length: 1 },
						items: [{ ...{ type: "userMessage", id: "new-item", text: "fresh" }, turnId: "t0" }],
					},
				} as AnyNotification,
				"new-item",
				"user",
			],
			[
				"a warning",
				{
					method: "warning",
					params: { threadId: "thread-1", ref: "ref-1", message: "test warning" },
				} as AnyNotification,
				undefined,
				"failure",
			],
		] as const)("M1 cap: the oldest row is trimmed when %s appends at 501", async (_label, frame, newId, newKind) => {
			const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
			const items: ThreadItem[] = [agentMessageItem("X", oversized, "completed")];
			for (let i = 1; i < 500; i++) items.push(userMessageItem(`u-${i}`, ""));
			// Versioned only for the item-frame case: its history/updated needs a
			// v6 model to merge at all (an unversioned one invalidates on
			// arrival), while the warning case is handled off the model entirely
			// (the transient-warning surface) and stays on the plain fixture.
			const { store, service } = await openProjectedWithItems(items, {
				running: true,
				versioned: _label === "an item frame",
			});
			expect(rows(store)).toHaveLength(500);
			const xBefore = rowById(store, "X");
			expect(xBefore?.kind === "assistant" && xBefore.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);
			expect(rows(store)[0]?.id).toBe("X");

			store.getState().applyNotification(frame);

			const items2 = rows(store);
			expect(items2).toHaveLength(500);
			expect(rowById(store, "X")).toBeUndefined();
			expect(items2[items2.length - 1]).toMatchObject({ kind: newKind });
			if (newId !== undefined) expect(items2[items2.length - 1]?.id).toBe(newId);
			expect(items2[0]?.id).toBe("u-1");

			// A page brings X back, short, with no bound carried over from the
			// oversized row the cap dropped. A tighter reread shrinks the model
			// window first — rows re-project from the model, so only the
			// reread's non-re-served items dropping makes room — and the page's
			// row then fits without the cap trimming it again.
			//
			// Task 17 (v6 read path): a genuinely new incarnation (a rebuild, or a
			// rollback) is what actually shrinks a real v6 model's window this way
			// — mergeHistory never drops an item on its own, so the item-frame
			// variant's earlier notification already made this model's held
			// history real (versioned: true in openProjectedWithItems above), and
			// a same-incarnation reread would only ever ADD the ten fresh rows
			// to, never shrink, the 500 it already holds.
			const newestTen: ThreadItem[] = [];
			for (let i = 490; i < 500; i++) newestTen.push(userMessageItem(`u-${i}`, ""));
			const tighter = makeReadProjectionResult(
				makeThread({ turns: [makeTurn({ id: "t0", items: newestTen })] }),
				ALL_TRUE_CAPS,
				true,
			).conversation;
			service.readProjectionResult = {
				conversation: {
					...tighter,
					history: tighter.history && { ...tighter.history, incarnation: "inc-2" },
				},
				olderCursor: "cursor-1",
			};
			await store.getState().rehydrate(service);
			expect(rows(store)).toHaveLength(10);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [{ kind: "assistant", id: "X", markdown: "short-page", streaming: false }],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			const xPage = rowById(store, "X");
			expect(xPage).toMatchObject({ kind: "assistant", markdown: "short-page" });
			expect(xPage?.kind === "assistant" && xPage.markdown.endsWith(TRUNCATION_MARKER)).toBe(false);
		});
	});

	describe("the cap and the bound on the same publish", () => {
		async function openProjectedWithItems(items: ThreadItem[]): Promise<{
			store: ReturnType<typeof createConversationStore>;
			service: FakeConversationService;
		}> {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread({ turns: [makeTurn({ id: "t0", items })] }));
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			return { store, service };
		}

		it("keeps a clustered member bounded when an unrelated item frame lands", async () => {
			const oversizedB = "b".repeat(MAX_ITEM_BYTES + 100);
			const { store } = await openProjectedWithItems([
				{
					type: "commandExecution",
					id: "wire-a",
					transcriptKey: "key-a",
					toolName: "shell",
					status: "completed",
					callId: "call-a",
					output: "short",
				},
				{
					type: "commandExecution",
					id: "wire-b",
					transcriptKey: "key-b",
					toolName: "shell",
					status: "completed",
					callId: "call-b",
					output: oversizedB,
				},
			]);
			const boundedBefore = (() => {
				const row = rowById(store, "wire-a");
				if (row?.kind !== "activity") throw new Error("expected a clustered activity");
				return row.members?.[1]?.detail.output;
			})();
			expect(boundedBefore?.endsWith(TRUNCATION_MARKER)).toBe(true);

			// An unrelated item frame republishes every row; the member is bounded
			// on its own content each time, not by anything remembered about it.
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...{ type: "userMessage", id: "unrelated", text: "hi" }, turnId: "t0" }],
				}),
			);

			const row = rowById(store, "wire-a");
			expect(row?.kind).toBe("activity");
			expect(row?.kind === "activity" && row.members?.[1]?.detail.output).toBe(boundedBefore);
		});
	});

	// --- sparse live items and clustered members as live targets ---

	describe("sparse live items inherit the active turn's status", () => {
		async function openWithActiveTurn(): Promise<ReturnType<typeof createConversationStore>> {
			const service = new FakeConversationService();
			service.openConv = makeConversation({}, true);
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			// turn/started's read-model replacement: a turn row's "running" status
			// for a status-less item now comes purely from the TURN's own status
			// (project.ts's isActiveItem), which history/updated carries directly.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", status: "inProgress", itemsView: "default", items: [] }],
				}),
			);
			return store;
		}

		function startItem(store: ReturnType<typeof createConversationStore>, item: ThreadItem): void {
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...item, turnId: "t1" }],
				}),
			);
		}

		it("projects a status-less tool item started in the active turn as running", async () => {
			const store = await openWithActiveTurn();
			startItem(store, {
				type: "commandExecution",
				id: "tool-1",
				turnId: "t1",
				toolName: "shell",
				callId: "call-1",
			});
			const row = store.getState().conversation?.items.find((i) => i.id === "tool-1");
			expect(row?.kind).toBe("activity");
			expect(row?.kind === "activity" && row.state).toBe("running");
		});

		// item/started's read-model replacement, history/updated, has no
		// equivalent guard: the package's mergeHistory (appwire-client's
		// reducer.ts) synthesizes a turn row for ANY turnId an incoming item
		// names — `{id: incoming.turnId, status: "inProgress", items: []}` when
		// none is held — unlike item/started's resolveInsertTurnId, which
		// required the named turn to already exist in the model or dropped the
		// item. This test asserted that stricter, now-superseded rule; deleted
		// rather than ported, since faking the drop back in would mean inventing
		// behavior the shared reducer no longer has.

		it("projects a status-less tool item naming no turn as running while a turn is active", async () => {
			const store = await openWithActiveTurn();
			startItem(store, {
				type: "commandExecution",
				id: "tool-1",
				toolName: "shell",
				callId: "call-1",
			});
			const row = store.getState().conversation?.items.find((i) => i.id === "tool-1");
			expect(row?.kind).toBe("activity");
			expect(row?.kind === "activity" && row.state).toBe("running");
		});

		it("projects a status-less reasoning item started in the active turn as running", async () => {
			const store = await openWithActiveTurn();
			startItem(store, {
				type: "reasoning",
				id: "reason-1",
				turnId: "t1",
				text: "thinking",
			});
			const row = store.getState().conversation?.items.find((i) => i.id === "reason-1");
			expect(row?.kind).toBe("activity");
			expect(row?.kind === "activity" && row.state).toBe("running");
		});

		it("marks a status-less assistant item started in the active turn as streaming", async () => {
			const store = await openWithActiveTurn();
			startItem(store, {
				type: "agentMessage",
				id: "assistant-1",
				turnId: "t1",
				text: "partial",
			});
			const row = store.getState().conversation?.items.find((i) => i.id === "assistant-1");
			expect(row?.kind).toBe("assistant");
			expect(row?.kind === "assistant" && row.streaming).toBe(true);
		});
	});

	describe("live updates resolve clustered members", () => {
		async function openProjectedWithItems(
			items: ThreadItem[],
			versioned = false,
		): Promise<{
			store: ReturnType<typeof createConversationStore>;
			service: FakeConversationService;
		}> {
			const { service, store } = await openProjectedThreadWith(
				makeThread({ turns: [makeTurn({ id: "t0", items })] }),
				versioned,
			);
			return { store, service };
		}

		function toolItem(id: string, transcriptKey: string, callId: string, output: string): ThreadItem {
			return {
				type: "commandExecution",
				id,
				transcriptKey,
				toolName: "shell",
				status: "completed",
				callId,
				output,
			};
		}

		function reasoningItem(
			id: string,
			transcriptKey: string,
			text: string,
			status: ThreadItem["status"] = "completed",
		): ThreadItem {
			return { type: "reasoning", id, transcriptKey, status, text };
		}

		function clusterMembers(store: ReturnType<typeof createConversationStore>, rowId: string): ActivityMember[] {
			const row = store.getState().conversation?.items.find((i) => i.id === rowId);
			expect(row?.kind).toBe("activity");
			if (row?.kind !== "activity") throw new Error("expected an activity row");
			expect(row.members?.length).toBe(2);
			return row.members ?? [];
		}

		// item/toolOutput/delta and item/reasoning/summaryTextDelta are gone —
		// Task 15 replaced the whole per-item lifecycle-notification family with
		// the overlay (design doc's "Running state": running output lives in the
		// overlay, laid over the in-progress history item — reducer.ts's
		// applyOverlayDelta/buildDisplayTurn). A live delta now arrives as
		// overlay/upserted (opens the entry) then overlay/delta (appends), keyed
		// "tool:<historyKey>"/"tool:<callId>" by the call item's own
		// transcriptKey, and only applies to a v6 (versioned) model — the
		// overlay is part of `history`, absent from a pre-v6 hydrate.
		function toolOverlayItem(turnId: string, callId: string, historyKey: string, output: string): OverlayItem {
			const key = `tool:${historyKey}`;
			return {
				key,
				kind: "tool",
				turnId,
				callId,
				historyKey,
				item: {
					type: "commandExecution",
					id: key,
					turnId,
					callId,
					output,
					status: "inProgress",
				},
			};
		}

		function upsertOverlay(store: ReturnType<typeof createConversationStore>, item: OverlayItem): void {
			store.getState().applyNotification({
				method: "overlay/upserted",
				params: { threadId: "thread-1", ref: "ref-1", item },
			} as AnyNotification);
		}

		function overlayDelta(
			store: ReturnType<typeof createConversationStore>,
			key: string,
			field: string,
			delta: string,
		): void {
			store.getState().applyNotification({
				method: "overlay/delta",
				params: { threadId: "thread-1", ref: "ref-1", key, field, delta },
			} as AnyNotification);
		}

		it("applies a tool-output delta aimed at a later clustered member in place", async () => {
			const { store } = await openProjectedWithItems(
				[toolItem("wire-a", "key-a", "call-a", "first"), toolItem("wire-b", "key-b", "call-b", "second")],
				true,
			);

			upsertOverlay(store, toolOverlayItem("t0", "call-b", "key-b", "second"));
			overlayDelta(store, "tool:key-b", "output", " MORE");

			const [memberA, memberB] = clusterMembers(store, "wire-a");
			expect(memberB?.detail.output).toBe("second MORE");
			// The other member keeps its own identity, output and position.
			expect(memberA?.detail.output).toBe("first");
			expect(memberA?.transcriptKey).toBe("key-a");
			expect(memberB?.transcriptKey).toBe("key-b");
		});

		it("applies a tool-output delta aimed at the first clustered member to that member, not only the cluster", async () => {
			const { store } = await openProjectedWithItems(
				[toolItem("wire-a", "key-a", "call-a", "first"), toolItem("wire-b", "key-b", "call-b", "second")],
				true,
			);

			upsertOverlay(store, toolOverlayItem("t0", "call-a", "key-a", "first"));
			overlayDelta(store, "tool:key-a", "output", " MORE");

			const [memberA, memberB] = clusterMembers(store, "wire-a");
			expect(memberA?.detail.output).toBe("first MORE");
			expect(memberB?.detail.output).toBe("second");
			const row = store.getState().conversation?.items.find((i) => i.id === "wire-a");
			expect(row?.kind === "activity" && row.detail.output).toBe("first MORE");
		});

		it("applies a reasoning delta aimed at a later clustered member in place", async () => {
			// item/reasoning/summaryTextDelta is gone with the rest of the per-item
			// lifecycle family (Task 15); the overlay that replaced it lays a tool
			// call's running output/status/images over its recorded item
			// (reducer.ts's layOverHistoryItem) but never its text — reasoning
			// content is never one of the fields an overlay carries over a held
			// item. A reasoning item already recorded but still running gets its
			// next chunk the same way any other held item does: a history/updated
			// carrying the item's own (higher-version-implicit, since neither side
			// stamps one here) text, still inProgress. Same delivery the sibling
			// "sparse completion" tests below use; this one keeps its own name and
			// running status to pin the still-running, one-member-only case.
			const { store } = await openProjectedWithItems(
				[reasoningItem("wire-a", "key-a", "first"), reasoningItem("wire-b", "key-b", "second", "inProgress")],
				true,
			);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								type: "reasoning",
								id: "wire-b",
								transcriptKey: "key-b",
								status: "inProgress",
								text: "second MORE",
							},
							turnId: "t0",
						},
					],
				}),
			);

			const [memberA, memberB] = clusterMembers(store, "wire-a");
			expect(memberB?.detail.output).toBe("second MORE");
			expect(memberA?.detail.output).toBe("first");
		});

		it("keeps a later clustered member bounded when a delta pushes it past the limit", async () => {
			const oversized = "b".repeat(MAX_ITEM_BYTES + 100);
			const { store } = await openProjectedWithItems(
				[toolItem("wire-a", "key-a", "call-a", "first"), toolItem("wire-b", "key-b", "call-b", oversized)],
				true,
			);
			const boundedOutput = clusterMembers(store, "wire-a")[1]?.detail.output;
			expect(boundedOutput?.endsWith(TRUNCATION_MARKER)).toBe(true);

			upsertOverlay(store, toolOverlayItem("t0", "call-b", "key-b", oversized));
			overlayDelta(store, "tool:key-b", "output", " MORE");

			// The row shows the same bounded prefix: what a reader sees is the
			// first MAX_ITEM_BYTES of the text, however much more streams in.
			expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(boundedOutput);
		});

		it("preserves a later clustered member's output when a sparse completion omits text", async () => {
			const { store } = await openProjectedWithItems([
				reasoningItem("wire-a", "key-a", "first"),
				reasoningItem("wire-b", "key-b", "accumulated"),
			]);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								type: "reasoning",
								id: "wire-b",
								transcriptKey: "key-b",
								status: "completed",
							},
							turnId: "t0",
						},
					],
				}),
			);

			const [memberA, memberB] = clusterMembers(store, "wire-a");
			expect(memberB?.detail.output).toBe("accumulated");
			expect(memberA?.detail.output).toBe("first");
		});

		it("preserves a clustered member's output when a sparse completion changes its wire id", async () => {
			const { store } = await openProjectedWithItems(
				[reasoningItem("wire-a", "key-a", "first"), reasoningItem("wire-b", "key-b", "accumulated")],
				true,
			);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								type: "reasoning",
								id: "wire-b2",
								transcriptKey: "key-b",
								status: "completed",
							},
							turnId: "t0",
						},
					],
				}),
			);

			const [memberA, memberB] = clusterMembers(store, "wire-a");
			// The member is replaced under its new wire id, keeping the output it
			// accumulated under the stable transcriptKey.
			expect(memberB?.id).toBe("wire-b2");
			expect(memberB?.detail.output).toBe("accumulated");
			expect(memberA?.detail.output).toBe("first");
		});
	});

	describe("paging dedupe spans every incoming identity", () => {
		function member(id: string, transcriptKey: string): ActivityMember {
			return {
				id,
				transcriptKey,
				label: "shell",
				family: "tool",
				state: "completed",
				detail: { output: transcriptKey, callId: `call-${transcriptKey}` },
			};
		}

		function cluster(id: string, transcriptKey: string, members: ActivityMember[]): MobileTimelineItem {
			return {
				kind: "activity",
				id,
				transcriptKey,
				label: "shell",
				family: "tool",
				state: "completed",
				detail: { output: transcriptKey, callId: `call-${transcriptKey}` },
				members,
			};
		}

		function memberIdentities(items: MobileTimelineItem[]): string[] {
			return items.flatMap((item) =>
				item.kind === "activity" ? (item.members ?? []).map((m) => m.transcriptKey ?? m.id) : [],
			);
		}

		async function pagedStore(
			current: MobileTimelineItem[],
			older: MobileTimelineItem[],
		): Promise<ReturnType<typeof createConversationStore>> {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation({ items: current }, true);
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = { items: older, nextCursor: "next" };
			await store.getState().loadOlder(service);
			return store;
		}

		it("rebuilds an incoming cluster whose member identity is already present under a new top-level id", async () => {
			const store = await pagedStore(
				[cluster("wire-X", "key-X", [member("wire-X", "key-X"), member("wire-A", "key-A")])],
				[
					cluster("wire-old", "key-old", [member("wire-old", "key-old"), member("wire-A2", "key-A")]),
					{ kind: "user", id: "older", text: "older" },
				],
			);

			const items = store.getState().conversation?.items ?? [];
			// Panel round 38 (Medium 1), superseding the whole-row skip this test
			// originally pinned: the member the live conversation already holds
			// dedupes per member, and the row's genuinely older first member
			// survives the merge rebuilt around the members nobody else holds —
			// a single survivor renders as the plain row, as retainedPageRow's
			// rebuild does on the rehydrate side.
			expect(items.map((i) => i.id)).toContain("wire-old");
			// The member is not duplicated across rows.
			expect(memberIdentities(items).filter((id) => id === "key-A")).toEqual(["key-A"]);
			// A genuinely new row from the same page is still admitted.
			expect(items.map((i) => i.id)).toContain("older");
		});

		it("admits an older attachment whose source row arrives in the same page", async () => {
			const store = await pagedStore(
				[{ kind: "user", id: "current", text: "current" }],
				[
					{ kind: "user", id: "wire-src", transcriptKey: "key-src", text: "older" },
					{
						kind: "attachments",
						id: "wire-src:attachments",
						sourceTranscriptKey: "key-src",
						// Input attachments travel inline on the wire (composer bytes).
						items: [{ id: "att-1", src: "data:image/png;base64,b2xk" }],
					},
				],
			);

			const items = store.getState().conversation?.items ?? [];
			expect(items.map((i) => i.id)).toContain("wire-src:attachments");
		});
	});

	// --- C1: Service-specific operation binding ---------------------------------------

	describe("C1: wrong service at entry => zero request/state change", () => {
		it("wrong-service loadOlder => zero service calls, zero state change", async () => {
			const serviceA = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().open(serviceA, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			const serviceB = new FakeConversationService();
			serviceB.olderItems = { items: [{ kind: "user", id: "old", text: "x" }] };
			const convBefore = store.getState().conversation;
			const cursorBefore = store.getState().olderCursor;
			await store.getState().loadOlder(serviceB);
			// C1: wrong service (B after A bound) => zero state change.
			expect(store.getState().conversation).toBe(convBefore);
			expect(store.getState().olderCursor).toBe(cursorBefore);
			expect(store.getState().loadingOlder).toBe(false);
		});

		it("correct-service loadOlder still works after binding", async () => {
			const service = new FakeConversationService();
			service.olderItems = { items: [{ kind: "user", id: "old", text: "x" }] };
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.items.some((i) => i.id === "old")).toBe(true);
		});
	});

	// The rows are a projection of the model, and the projection reads exactly one
	// of the model's own fields: `turns` — every turn, item and status the rows are
	// made of lives under it, and the answerable asks come from it too. Every other
	// field a frame moves — the status, the name, the queue, the jobs tree, the
	// wire's askPending, lastFrameAt, which moves on EVERY frame — changes no row,
	// so such a frame must publish the rows it already had, by reference:
	// re-projecting and re-bounding hundreds of rows for a status change is work
	// the reader never sees, and a fresh items array tells every list view the
	// transcript moved.
	describe("a frame that changes no projection input republishes the rows", () => {
		it("publishes the same rows for a thread-level status frame, and still advances the model", async () => {
			const { store } = await openRunningTurn([agentMessageItem("a1", "hello", "completed")]);
			const before = store.getState().conversation;
			expect(before?.items.length).toBeGreaterThan(0);

			store.getState().applyNotification(statusChanged({ type: "idle" }));

			const after = store.getState().conversation;
			// The model advanced: the frame is the authority on the thread's status.
			expect(after?.status).toEqual({ type: "idle" });
			expect(after).not.toBe(before);
			// The rows did not: same array, same row objects.
			expect(after?.items).toBe(before?.items);
		});
	});
});
