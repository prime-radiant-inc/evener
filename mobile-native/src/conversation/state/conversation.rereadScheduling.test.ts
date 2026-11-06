// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { AnyNotification, MutationReceipt, ThreadItem } from "@evener/appwire-client";
import { hydrateThread, liveAskQuestions, makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { MobileTimelineItem } from "../../projectedRows";
import { projectConversation } from "../../projectedRows";
import { createConversationStore, MAX_ITEM_BYTES, TRUNCATION_MARKER } from "./conversation";
import {
	FakeConversationService,
	VALID_ASK_ARGS,
	agentMessageItem,
	askUserItem,
	commandExecItem,
	evenerWith,
	historyUpdated,
	loadFillerPage,
	makeControlledRead,
	makeConversation,
	makeReadProjectionResult,
	makeReceipt,
	makeThread,
	makeTurn,
	openProjectedThread,
	openProjectedThreadWith,
	openRunningTurn,
	openedStore,
	readServing,
	resyncFrame,
	rowById,
	rows,
	runningTurnThread,
	statusChanged,
	textInput,
	turnsPage,
	userMessageItem,
	warningFrame,
	wireTurnFragment,
	yieldMicrotask,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	// --- Task 2A reslice: F3-F11 tests -------------------------------------------

	describe("LiveConversationState is a required interface", () => {
		it("createConversationStore returns state with required live methods", () => {
			const store = createConversationStore();
			const s = store.getState();
			// These must be functions on the production store (not optional).
			expect(typeof s.openProjected).toBe("function");
			expect(typeof s.rehydrate).toBe("function");
			// F2: setCoalescer is removed — openProjected binds the coalescer
			// internally.
			expect(typeof (s as unknown as Record<string, unknown>).setCoalescer).toBe("undefined");
		});
	});

	describe("mutation object identity / private mutation ID", () => {
		it("out-of-order completion cannot clear a newer mutation (send)", async () => {
			const { service, store } = await openedStore();
			store.getState().setDraft("first");

			// Start a send that hangs.
			let resolve1: ((r: MutationReceipt) => void) | null = null as ((r: MutationReceipt) => void) | null;
			const hang1 = new Promise<MutationReceipt>((r) => {
				resolve1 = r;
			});
			service.send = async () => hang1;
			const p1 = store.getState().send(service, textInput("first"));

			// While mutation 1 is in flight, start mutation 2.
			store.getState().setDraft("second");
			let resolve2: ((r: MutationReceipt) => void) | null = null as ((r: MutationReceipt) => void) | null;
			const hang2 = new Promise<MutationReceipt>((r) => {
				resolve2 = r;
			});
			service.send = async () => hang2;
			const p2 = store.getState().send(service, textInput("second"));

			// Resolve mutation 2 first (newer).
			resolve2?.(makeReceipt());
			await p2;
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();

			// Now resolve mutation 1 (older, stale). It must NOT clear the
			// already-resolved state or set a newer mutation to null.
			resolve1?.(makeReceipt());
			await p1;
			// The mutation was already cleared by mutation 2; stale completion
			// must not re-introduce pending state.
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
		});

		it("out-of-order failure cannot overwrite a newer mutation's error", async () => {
			const { service, store } = await openedStore();
			store.getState().setDraft("first");

			// Start mutation 1 that hangs, then fails.
			let resolveFail1: (() => void) | null = null as (() => void) | null;
			const hangFail1 = new Promise<MutationReceipt>((_resolve, reject) => {
				resolveFail1 = () => reject(new Error("mutation 1 error"));
			});
			service.send = async () => hangFail1;
			const p1 = store.getState().send(service, textInput("first"));

			// Start mutation 2 that succeeds.
			store.getState().setDraft("second");
			service.send = async () => Promise.resolve(makeReceipt());
			const p2 = store.getState().send(service, textInput("second"));
			await p2;
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().error).toBeNull();

			// Now mutation 1 fails (stale). It must NOT set error or the markers.
			resolveFail1?.();
			await p1.catch(() => {}); // swallow the rejection
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().error).toBeNull();
		});

		it("draft restore only happens if user has not typed since submit", async () => {
			const { service, store } = await openedStore();
			store.getState().setDraft("original draft");
			// Start a send that fails.
			service.sendShouldReject = new Error("conflict");
			const p = store.getState().send(service, textInput("original draft"));
			// While in-flight, the user types new text.
			store.getState().setDraft("new text typed during send");
			await p;
			// Since the user typed new text, the draft should NOT be restored.
			expect(store.getState().draft).toBe("new text typed during send");
		});

		it("interrupt does not clear draft while in flight", async () => {
			const { service, store } = await openedStore(makeConversation({ status: { type: "active" } }));
			store.getState().setDraft("my draft text");
			let resolveFn: (() => void) | null = null as (() => void) | null;
			const hang = new Promise<MutationReceipt>((r) => {
				resolveFn = () => r(makeReceipt());
			});
			service.interrupt = async () => hang;
			const p = store.getState().interrupt(service);
			const pending = store.getState().pendingSubmission;
			expect(pending).toEqual({ kind: "interrupt" });
			// Draft should not be cleared by interrupt.
			expect(store.getState().draft).toBe("my draft text");
			resolveFn?.();
			await p;
		});
	});

	describe("AuthoritativeRereadScheduler.request(key, effect)", () => {
		it("coalesces multiple signals to one readProjection", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), "cursor-1");
			// F2: openProjected binds the coalescer internally.
			await store.getState().openProjected(service, "ref-1");

			// The internal coalescer coalesces multiple requestRehydrate calls
			// into one actual readProjection call.
			const ctrl = makeControlledRead(service);

			// Emit multiple resync notifications.
			service.readProjectionCalls = []; // reset count
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());

			// M1: deterministic barrier — await started, yield so orig resolves and
			// the release gate is pushed, then set up completed and release.
			await ctrl.started();
			await yieldMicrotask(); // let orig(ref) resolve and push to releaseQueue
			const completedP = ctrl.completed();
			ctrl.release();
			await completedP;

			// Multiple signals should result in only 1 readProjection call.
			expect(service.readProjectionCalls.length).toBe(1);
		});
	});

	// What a live item frame resolves locally, and what still needs the
	// canonical read. The projector has the whole turn in hand — the pending
	// ask_user set included — so the frames that once had to ask for a reread
	// now settle in place; evener/thread/resync stays the authoritative
	// refresh path.
	describe("item frames settle locally; resync is the reread path", () => {
		it("projects a started ask_user as its question row, without a reread", async () => {
			const { store, service } = await openRunningTurn([], {}, true);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...askUserItem("ask-1", VALID_ASK_ARGS, "inProgress"), turnId: "t1" }],
				}),
			);
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			// An ask_user still in flight is not answerable yet: it is the tool
			// call it is, and the question row appears when it settles.
			expect(rowById(store, "ask-1")).toMatchObject({ kind: "activity", family: "tool" });
			expect(service.readProjectionCalls.length).toBe(initialReads);
		});

		it("projects a completed ask_user as a question row once the status frame carries the pending flag, without a reread", async () => {
			const { store, service } = await openRunningTurn([], {}, true);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...askUserItem("ask-1", VALID_ASK_ARGS, "completed"), turnId: "t1" }],
				}),
			);
			// The item alone is not the whole signal (#1731 round 4): the status
			// frame that lands on the same boundary carries askPending, and that
			// is what liveAskQuestions gates the question row on.
			store.getState().applyNotification(statusChanged({ type: "active" }, { askPending: true }));
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(rowById(store, "ask-1")).toMatchObject({ kind: "question" });
			expect(service.readProjectionCalls.length).toBe(initialReads);
		});

		// capAndTruncate bounds every published row's text for display
		// (truncateItem's "question" case, the row module's boundQuestion) — the
		// existing "bounds a question row's prose" test above pins that for the
		// hydrate/openProjected path. The answer path is unaffected because it
		// reads liveAskQuestions' canonical, uncut model — not the bounded row —
		// and this must hold for the LIVE incremental path too, not just hydrate:
		// a bounded option label could otherwise collide with another bounded
		// label, or read as a choice the agent never actually offered under its
		// real name (boundQuestion's own contract).
		it("bounds a live question row's option label but leaves liveAskQuestions' canonical copy whole", async () => {
			const { store } = await openRunningTurn([], {}, true);
			const oversizedLabel = "x".repeat(MAX_ITEM_BYTES + 100);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								type: "commandExecution",
								id: "ask-oversized",
								toolName: "ask_user",
								status: "completed",
								argumentsJson: JSON.stringify({
									questions: [
										{
											header: "Choose",
											question: "Pick one",
											options: [
												{ label: oversizedLabel, detail: "da" },
												{ label: "B", detail: "db" },
											],
											multi_select: false,
										},
									],
								}),
							},
							turnId: "t1",
						},
					],
				}),
			);
			store.getState().applyNotification(statusChanged({ type: "active" }, { askPending: true }));
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			const row = rowById(store, "ask-oversized");
			if (row?.kind !== "question") throw new Error("expected a projected question row");
			const boundLabel = row.questions[0]?.options[0]?.label ?? "";
			expect(new TextEncoder().encode(boundLabel).length).toBeLessThanOrEqual(MAX_ITEM_BYTES);
			expect(boundLabel.endsWith(TRUNCATION_MARKER)).toBe(true);

			const conversation = store.getState().conversation;
			if (conversation === null) throw new Error("conversation gone");
			const canonical = liveAskQuestions(conversation)[0];
			expect(canonical?.options[0]?.label).toBe(oversizedLabel);
		});

		it.each([
			[
				"a history update naming a held item",
				{
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 1,
						snapshot: { incarnation: "inc-1", length: 1 },
						items: [{ type: "agentMessage", id: "r-1", turnId: "t1", text: "thinking" }],
					},
				},
			],
			[
				"a known thread-level frame",
				{
					method: "thread/status/changed",
					params: { threadId: "thread-1", ref: "ref-1", status: { type: "running" } },
				},
			],
		] as const)("does not reread for %s", async (_label, frame) => {
			// Versioned: an unversioned model has no epoch/generation to compare
			// a history/updated frame against, so classifySignal always reads
			// "replace" (bootGeneration "" never parses as a counter) and
			// invalidates on the very first one — a real gap, correctly, not the
			// one this case means to pin (a v6 model merging a frame that names
			// an item it already holds, entirely in place, no reread needed).
			const { store, service } = await openRunningTurn([agentMessageItem("r-1", "", "inProgress")], {}, true);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification(frame as AnyNotification);
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBe(initialReads);
		});

		// The same gap detection covers a frame that is not an item frame at all: a
		// warning, or a settle whose active turn lies outside the window this
		// client loaded, has nowhere to land, and the model says so by handing
		// back the turns it already had. evener/steering/injected's old separate
		// coverage here collapsed into history/updated's — a steering item now
		// arrives the same way any other item does.
		it.each([
			[
				"a warning",
				{
					method: "warning",
					params: { threadId: "thread-1", ref: "ref-1", message: "careful" },
				},
			],
			[
				"a turn settle",
				{
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 1,
						snapshot: { incarnation: "inc-1", length: 1 },
						turns: [{ id: "t-outside-window", status: "completed", itemsView: "" }],
					},
				},
			],
		] as const)("rereads when %s names an active turn this window does not hold", async (_label, frame) => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({ evener: evenerWith({ activeTurnId: "t-outside-window" }) }),
			);
			expect(store.getState().conversation?.activeTurnId).toBe("t-outside-window");
			const initialReads = service.readProjectionCalls.length;

			store.getState().applyNotification(frame as AnyNotification);
			// Three ticks to drain the scheduler's microtask hop, runOne's own
			// Promise.resolve().then(effect), and the effect's own
			// await rehydrate() before readProjection is actually called — the
			// same count "rereads for an item frame the model has no rule for"
			// (above) already needs for the identical requestRehydrate path.
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
		});

		// A warning that arrives while no turn is active is dropped by the wire's
		// own rule, not by a gap: warnings are never transcript-persisted
		// (reducer.ts's "warning" case cites internal/apptranscript having no
		// warning-item conversion), so the canonical read cannot carry it either
		// and asking for one buys nothing.
		it("does not reread for a warning the wire drops because no turn is active", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().conversation?.activeTurnId).toBeUndefined();
			const initialReads = service.readProjectionCalls.length;

			store.getState().applyNotification(warningFrame({ message: "careful" }));
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBe(initialReads);
		});

		// The transient-warning settle finding (RoboRev, Sep-18, on this piece's
		// pre-restack branch): a warning folds into the active turn's items, the
		// bare turn/completed settles the turn — and the folded warning item
		// survives the settle in the model, so the projected warning row had
		// nothing in flight to clear it. The wire never persists warnings, so
		// the canonical read is the one honest way to drop them: settling a turn
		// that still holds warning items must request that read.
		it("rereads when a bare turn/completed settles a turn still holding warning items, and the read drops them", async () => {
			const { service, store } = await openRunningTurn([], {});
			store.getState().applyNotification(warningFrame({ message: "careful" }));
			expect(store.getState().conversation?.items.filter((row) => row.kind === "failure")).toHaveLength(1);
			const initialReads = service.readProjectionCalls.length;

			// The hub's transcript never carries the warning, so the canonical
			// snapshot this reread serves has no warning item anywhere.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({ turns: [makeTurn({ id: "t1", status: "completed", items: [] })] }),
			);
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "", status: "completed" }],
				}),
			);
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBe(initialReads + 1);
			// The settle's own row projection ran too: the completed turn stays,
			// and the warning row the snapshot does not carry is gone.
			expect(store.getState().conversation?.turns.map((turn) => turn.status)).toEqual(["completed"]);
			expect(store.getState().conversation?.items.filter((row) => row.kind === "failure")).toHaveLength(0);
		});

		// RoboRev finding on the restack (round 2): once an older page's turns
		// are loaded, the rehydrate's turn-history merge folded the settled
		// turn's live warning item back into the committed model — the settle
		// reread cleared the ROW, but the next row-changing frame reprojected
		// the model and the transient warning returned. The rehydrate must keep
		// live-only warnings out of the retained turn history it merges.
		it("the settle reread's warning cleanup sticks across later frames once page turns are loaded", async () => {
			const { service, store } = await openRunningTurn([], {});
			await loadFillerPage(store, service);
			expect(store.getState().conversation?.turns.map((turn) => turn.id)).toEqual(["t0", "t1"]);

			store.getState().applyNotification(warningFrame({ message: "careful" }));
			expect(store.getState().conversation?.items.filter((row) => row.kind === "failure")).toHaveLength(1);

			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t1", status: "completed", items: [] })],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "", status: "completed" }],
				}),
			);
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(store.getState().conversation?.items.filter((row) => row.kind === "failure")).toHaveLength(0);
			// A later row-changing frame reprojects from the committed model:
			// the transient warning must stay gone, not come back through the
			// turn history the rehydrate retained.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(store.getState().conversation?.items.filter((row) => row.kind === "failure")).toHaveLength(0);
		});

		it("a settle reread whose fresh window re-issues the turn's content under a new id still drops the transient warning", async () => {
			const { service, store } = await openRunningTurn([], {});
			await loadFillerPage(store, service);

			// The warned turn also carries real content the transcript persists:
			// the turn stays inside the keep-window through it, so compaction
			// alone cannot be what clears the warning (RoboRev round 3).
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("real-1", "the real message"), transcriptEntryIndex: 3, turnId: "t1" }],
				}),
			);
			store.getState().applyNotification(warningFrame({ message: "careful" }));

			// The settle's reread serves the turn's transcript content under a
			// NEW bare id, carrying the real message the transcript persisted:
			// the retained turn folds into it by item identity, and only the
			// transient warning is unmatched. The merge must not keep it.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1-fresh",
							status: "completed",
							items: [userMessageItem("real-1", "the real message")],
						}),
					],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "", status: "completed" }],
				}),
			);
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(store.getState().conversation?.items.filter((row) => row.kind === "failure")).toHaveLength(0);
			// Even the committed MODEL keeps no trace of the transient warning.
			expect(
				store
					.getState()
					.conversation?.turns.flatMap((turn) => turn.items)
					.filter((item) => item.type === "warning"),
			).toHaveLength(0);

			// And a later row-changing frame reprojects from that model: the
			// real message stays, the transient warning never returns.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(store.getState().conversation?.items.filter((row) => row.kind === "failure")).toHaveLength(0);
		});

		// A burst of unplaceable frames is one read, not a storm: requestRehydrate
		// goes through the drain scheduler keyed by the thread ref, which coalesces
		// same-key requests (overwriting the effect, merging waiters) and runs one
		// effect at a time — at most one read in flight plus one queued, however
		// many frames land.

		// An item frame the reducer has no case for cannot be projected, so the
		// canonical read is still the recovery.
		it("rereads for an item frame the model has no rule for", async () => {
			const { store, service } = await openRunningTurn([], {}, true);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification({
				method: "item/somethingNew/delta",
				params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "x", delta: "y" },
			} as unknown as AnyNotification);
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
		});

		// The read model's transcript frame (history/updated) takes the same gap
		// rule as item/turn frames: the package's applyHistoryUpdated hands back
		// `turns` by reference for a stale replay or an invalidating epoch, and
		// that identity-unchanged case is what requests the canonical read.
		describe("history/updated gap rule (v6)", () => {
			// A store opened on a v6-hydrated thread (a snapshot on the read
			// response), so applyNotification's package call takes
			// applyHistoryUpdated's branch instead of the pre-v6 fallback.
			async function openV6RunningTurn(): Promise<{
				store: ReturnType<typeof createConversationStore>;
				service: FakeConversationService;
			}> {
				const thread = runningTurnThread();
				const service = new FakeConversationService();
				service.readProjectionResult = {
					conversation: projectConversation(
						hydrateThread(
							{
								thread,
								bootGeneration: "1",
								epoch: 1,
								snapshot: { incarnation: "inc-1", length: 1 },
							},
							thread.evener.ref,
							0,
						),
					),
					olderCursor: null,
				};
				const store = createConversationStore();
				await store.getState().openProjected(service, "ref-1");
				return { store, service };
			}

			it("rereads when history/updated names a newer epoch than held", async () => {
				const { store, service } = await openV6RunningTurn();
				expect(store.getState().conversation?.history?.epoch).toBe(1);
				const initialReads = service.readProjectionCalls.length;
				store.getState().applyNotification({
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 2,
						snapshot: { incarnation: "inc-1", length: 1 },
					},
				} as unknown as AnyNotification);
				await Promise.resolve();
				await Promise.resolve();
				await Promise.resolve();
				expect(service.readProjectionCalls.length).toBe(initialReads + 1);
			});

			it("does not reread for a history/updated at the same epoch carrying new content", async () => {
				const { store, service } = await openV6RunningTurn();
				const initialReads = service.readProjectionCalls.length;
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...agentMessageItem("m1", "hi"), turnId: "t1", version: 1 }],
						turns: [{ id: "t1", status: "inProgress", itemsView: "default", items: [] }],
						snapshot: { incarnation: "inc-1", length: 2 },
					}),
				);
				await Promise.resolve();
				await Promise.resolve();
				await Promise.resolve();
				expect(service.readProjectionCalls.length).toBe(initialReads);
				expect(
					store
						.getState()
						.conversation?.turns.flatMap((turn) => turn.items)
						.some((item) => item.id === "m1"),
				).toBe(true);
			});

			it("rereads for a history/updated that names no item or turn this model can place", async () => {
				const { store, service } = await openV6RunningTurn();
				const initialReads = service.readProjectionCalls.length;
				// Same signal identity as held (no invalidation) but no turns/items:
				// applyHistoryUpdated's merge is then a no-op and `turns` comes back
				// by reference — the same "nothing matched" gap an unplaceable item
				// frame produces.
				store.getState().applyNotification(historyUpdated({}));
				await Promise.resolve();
				await Promise.resolve();
				await Promise.resolve();
				expect(service.readProjectionCalls.length).toBe(initialReads + 1);
			});
		});

		it("warning insertion obeys item cap", async () => {
			// 500 user messages in the settled turn, at the cap, plus a running
			// turn for the warning to land in.
			const older: ThreadItem[] = [];
			for (let i = 0; i < 500; i++) older.push(userMessageItem(`u${i}`, ""));
			const { store } = await openProjectedThreadPair(older);
			expect(rows(store)).toHaveLength(500);
			store.getState().applyNotification(warningFrame({ message: "test warning" }));
			const items = rows(store);
			expect(items.length).toBeLessThanOrEqual(500);
			// The newest rows are retained: the warning is the last one. Main's
			// landed warnings projection renders a warning as an attention row
			// (kind "failure", title its own field, message as detail) — the row
			// old-C-era tests here called a notice.
			expect(items[items.length - 1]).toMatchObject({
				kind: "failure",
				title: "Warning",
				detail: "test warning",
			});
		});

		it("keeps repeated warning identities stable through a later item update", async () => {
			const { store } = await openRunningTurn([userMessageItem("user-1", "input")]);
			const warning = {
				method: "warning",
				params: { threadId: "thread-1", ref: "ref-1", title: "Provider warning", message: "Retrying" },
			} as AnyNotification;
			store.getState().applyNotification(warning);
			store.getState().applyNotification(warning);
			const warningIdsBefore = rows(store)
				.filter((item) => item.kind === "failure")
				.map((item) => item.id);

			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("user-1", "updated input"), transcriptEntryIndex: 4, turnId: "t1" }],
				}),
			);

			const warningIdsAfter = rows(store)
				.filter((item) => item.kind === "failure")
				.map((item) => item.id);
			expect(warningIdsBefore).toHaveLength(2);
			expect(new Set(warningIdsBefore).size).toBe(2);
			expect(warningIdsAfter).toEqual(warningIdsBefore);
		});

		// RoboRev round 34: a warning that arrives with no active turn is a
		// real server diagnostic (the projector emits EventWarning
		// unconditionally; a prompt-render failure on a model change lands
		// exactly here, while idle). The wire drops it and never persists it,
		// so no read can recover it either — the store displays it itself, as
		// the attention row the canonical projection builds for a model
		// warning item (the row module's warningItem), held in transient display
		// state that every timeline rebuild re-appends and every conversation
		// transition clears, the lifetime main's live-owned rows gave it.
		it("shows a warning that arrives with no active turn, and keeps it through rebuilds and transitions", async () => {
			const store = await openProjectedThread(makeThread());
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			// Displayed at once: title as its own field, the message as detail.
			expect(rows(store)).toEqual([
				expect.objectContaining({
					kind: "failure",
					title: "Provider",
					detail: "careful",
				}),
			]);
			// A later row-changing frame rebuilds the timeline from the
			// projection — the notice is not a model row, yet survives.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "default", status: "running" }],
				}),
			);
			expect(rows(store).some((row) => row.kind === "failure" && row.title === "Provider")).toBe(true);
			// A resync reread publishes a snapshot that does not carry the
			// warning (warnings are never persisted) — the notice survives that
			// rebuild too, from the transient surface, not the transcript.
			store.getState().applyNotification(resyncFrame());
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(rows(store).some((row) => row.kind === "failure" && row.title === "Provider")).toBe(true);
			// The conversation transition clears it: reopened, the conversation
			// does not resurrect a notice from before it.
			store.getState().reset();
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			expect(rows(store).filter((row) => row.kind === "failure")).toEqual([]);
		});

		// RoboRev round 35: a transient notice is evidence of a moment, not
		// the newest row forever. Re-appended at the tail, an old diagnostic
		// would sink below every response that arrived later, and the
		// retained window could never evict it — accumulated notices crowd
		// out real messages. The notice keeps the position it arrived at and
		// leaves with that position when the window slides past it.
		it("seats an idle warning at its arrival position, above later messages, and evicts it with that position", async () => {
			const store = await openProjectedThread(
				makeThread({
					turns: [
						makeTurn({
							items: [userMessageItem("u1", "first"), agentMessageItem("a1", "second")],
						}),
					],
				}),
				true,
			);
			expect(rows(store).map((row) => row.id)).toEqual(["u1", "a1"]);
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			// Newer messages arrive after the notice did.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("u2", "newer"), turnId: "t2" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("a2", "newer reply"), turnId: "t2" }],
				}),
			);
			// The notice sits where it arrived: after a1, above u2 and a2.
			expect(rows(store).map((row) => row.kind)).toEqual(["user", "assistant", "failure", "user", "assistant"]);
			expect(rows(store).map((row) => row.id)[2]).toMatch(/^warning:/);
			// Grow the transcript past the retained cap: the window keeps the
			// newest 500 rows, and the arrival position — with the notice —
			// leaves it.
			for (let i = 3; i <= 502; i++) {
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...agentMessageItem(`a${i}`, `message ${i}`), turnId: "t2" }],
					}),
				);
			}
			const after = rows(store);
			expect(after.filter((row) => row.kind === "failure")).toEqual([]);
			expect(after).toHaveLength(500);
			expect(after[0]?.id).toBe("a3");
			expect(after[after.length - 1]?.id).toBe("a502");
		});

		it("seats consecutive idle warnings at one arrival position, in arrival order, and evicts them with that position", async () => {
			const store = await openProjectedThread(
				makeThread({
					turns: [
						makeTurn({
							items: [userMessageItem("u1", "first"), agentMessageItem("a1", "second")],
						}),
					],
				}),
				true,
			);
			expect(rows(store).map((row) => row.id)).toEqual(["u1", "a1"]);
			// Two notices land back-to-back, with no model row between them.
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "first warning" }));
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "second warning" }));
			// Both notices hold the arrival position — after a1, in the order
			// they arrived.
			expect(rows(store).map((row) => row.id)).toEqual(["u1", "a1", "warning:1", "warning:2"]);
			// A rebuild reseats both at that position, above newer messages.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("u2", "newer"), turnId: "t2" }],
				}),
			);
			expect(rows(store).map((row) => row.id)).toEqual(["u1", "a1", "warning:1", "warning:2", "u2"]);
			// Grow the transcript past the retained cap: the window keeps the
			// newest 500 rows, and the arrival position — with both notices —
			// leaves it together.
			for (let i = 3; i <= 502; i++) {
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...agentMessageItem(`a${i}`, `message ${i}`), turnId: "t2" }],
					}),
				);
			}
			const after = rows(store);
			expect(after.filter((row) => row.kind === "failure")).toEqual([]);
			expect(after).toHaveLength(500);
			expect(after[0]?.id).toBe("a3");
			expect(after[after.length - 1]?.id).toBe("a502");
		});

		// RoboRev round 37: the wire can name an active turn the loaded window
		// does not hold — a resumed old turn, with the window showing only
		// newer history. The reducer's fold then finds no turn to attach the
		// warning to, the round-34 fallback required an ABSENT active id, and
		// the gap reread the frame requests can never carry the warning back
		// (the wire never persists warnings): the diagnostic vanished. It
		// belongs on the transient surface like any warning the reducer could
		// not place.
		it("shows a warning whose active turn lies outside the loaded window, and keeps it through the gap reread", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [userMessageItem("u1", "first"), agentMessageItem("a1", "second")],
						}),
					],
					evener: evenerWith({ activeTurnId: "t-outside" }),
				}),
			);
			expect(store.getState().conversation?.activeTurnId).toBe("t-outside");
			const readsBefore = service.readProjectionCalls.length;
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			// Displayed at once, though the reducer could not place it: the
			// notice seats after the last model row, from the transient surface.
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["user", "u1"],
				["assistant", "a1"],
				["failure", "warning:1"],
			]);
			// The frame is still a gap — the loaded window misses the active
			// turn it names — so the canonical read fires; the wire never
			// persists warnings, so the read cannot carry the diagnostic, and
			// the notice survives the reread's rebuild from the transient
			// surface.
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBe(readsBefore + 1);
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["user", "u1"],
				["assistant", "a1"],
				["failure", "warning:1"],
			]);
			// The model itself keeps no trace of the warning — there was nowhere
			// to fold one, and the read never carried it back.
			expect(
				store
					.getState()
					.conversation?.turns.flatMap((turn) => turn.items)
					.filter((item) => item.type === "warning"),
			).toEqual([]);
		});

		// RoboRev round 37, second finding: anchors matched only top-level row
		// identities, but pagination can seat an older tool directly beside the
		// tool a notice anchored to, and the next projection then clusters the
		// two under the OLDER tool's identity — the anchor row remains visible
		// as a cluster member while its identity stops matching, so the prune
		// deleted the notice outright. Anchors must resolve through the
		// identities a row OWNS, members included.
		it("reseats a warning through a cluster that absorbed its anchor row", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [commandExecItem("a-tool", "shell", "completed")],
						}),
					],
				}),
				true,
			);
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([["activity", "a-tool"]]);
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["activity", "a-tool"],
				["failure", "warning:1"],
			]);
			// An older page seats tool p-tool directly before a-tool in the
			// window and the model: the page serves the wire turn the model
			// merges (rows re-project from it, D23d).
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage([wireTurnFragment("t0", [commandExecItem("p-tool", "shell", "completed")])]),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.map((turn) => turn.id)).toEqual(["t0", "t1"]);
			// Rows re-project from the merged model, so the two adjacent
			// same-family tools cluster under p-tool's identity at the page
			// merge itself — a-tool rides as a member — and the notice keeps
			// its seat beside the row that now carries its anchor.
			const paged = rows(store);
			expect(paged.map((row) => [row.kind, row.id])).toEqual([
				["activity", "p-tool"],
				["failure", "warning:1"],
			]);
			expect(paged[0]?.kind === "activity" ? paged[0].members?.map((member) => member.id) : undefined).toEqual([
				"p-tool",
				"a-tool",
			]);
			// A row-changing frame reprojects from the model: the cluster keeps
			// its members, and the notice keeps its position beside the row
			// that carries its anchor.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("u2", "newer"), turnId: "t2" }],
				}),
			);
			const after = rows(store);
			expect(after.map((row) => [row.kind, row.id])).toEqual([
				["activity", "p-tool"],
				["failure", "warning:1"],
				["user", "u2"],
			]);
			const activities = after.filter(
				(row): row is Extract<MobileTimelineItem, { kind: "activity" }> => row.kind === "activity",
			);
			expect(activities).toHaveLength(1);
			expect(activities[0]?.members?.map((member) => member.id)).toEqual(["p-tool", "a-tool"]);
		});

		// RoboRev panel R38: a cluster can GROW past the row a notice anchored
		// to. A later same-family activity joins the run that absorbed the
		// anchored row, and the seating resolves the anchor through the
		// cluster's owned identities and seats the notice after the WHOLE
		// cluster — below the member that arrived after the notice. The
		// cluster splits at the anchored member instead, so the notice keeps
		// the position it arrived at, above the rows that arrived later.
		it("seats a warning above later members when a cluster grows past its anchor row", async () => {
			const { store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [commandExecItem("a-tool", "shell", "completed")],
						}),
					],
				}),
				true,
			);
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([["activity", "a-tool"]]);
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["activity", "a-tool"],
				["failure", "warning:1"],
			]);
			// A newer same-family activity arrives AFTER the notice: the
			// projection clusters the two consecutive shell rows, so the run
			// that absorbed the anchored row now also carries a member that
			// arrived after the notice did.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...commandExecItem("b-tool", "shell", "completed"), turnId: "t2" }],
				}),
			);
			const after = rows(store);
			expect(after.map((row) => [row.kind, row.id])).toEqual([
				["activity", "a-tool"],
				["failure", "warning:1"],
				["activity", "b-tool"],
			]);
		});

		// The summary-only ruling meets the R38 split: a cluster of summarized
		// rows re-projects its sub-runs through activityRunRow when a notice
		// splits it, and the rebuilt top-level rows must stay summary-only —
		// a run of one loses the marker exactly when the split reduced it to
		// its first member's fields.
		it("keeps a split cluster's sub-run rows summary-only at the user's level", async () => {
			const thread = makeThread({
				turns: [
					makeTurn({
						id: "t1",
						status: "completed",
						items: [
							{
								...commandExecItem("a-tool", "shell", "completed"),
								description: "first audit",
							} as ThreadItem,
							{
								...commandExecItem("b-tool", "shell", "completed"),
								description: "second audit",
							} as ThreadItem,
						],
					}),
				],
			});
			// Versioned: turn/started and item/completed are gone with the rest
			// of the per-item lifecycle family (Task 15) — a new turn and its
			// first item now arrive together as one history/updated frame, and
			// that frame only merges against a v6 (versioned) model.
			const store = await openProjectedThread(thread, true);
			store.getState().setDisplayConfig(makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }));
			// The two completed calls cluster into one summary-only row at
			// intent (the operator's summary-only ruling).
			const clustered = rows(store).find((row) => row.id === "a-tool");
			expect(clustered).toMatchObject({
				kind: "activity",
				summaryOnly: true,
				detail: { description: "first audit" },
			});
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			// A newer same-family activity arrives AFTER the notice, on a new
			// turn: the run splits at the anchored member, and BOTH sub-runs
			// re-project.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
					items: [
						{
							...commandExecItem("c-tool", "shell", "completed"),
							description: "third audit",
							turnId: "t2",
						} as ThreadItem,
					],
				}),
			);
			// The split keeps the members displayed at arrival above the notice
			// (the [a, b] run) and seats the member that arrived later below it.
			const split = rows(store).filter((row) => row.kind === "activity");
			expect(split.map((row) => row.id)).toEqual(["a-tool", "c-tool"]);
			for (const row of split) {
				if (row.kind !== "activity") continue;
				expect(row.summaryOnly, `row ${row.id}`).toBe(true);
				expect(row.detail.output, `row ${row.id} output`).toBeUndefined();
			}
			const above = split[0];
			expect(above?.detail.description).toBe("first audit");
			expect(above?.members?.map((member) => `${member.id}:${member.summaryOnly === true}`)).toEqual([
				"a-tool:true",
				"b-tool:true",
			]);
			const below = split[1];
			expect(below?.detail.description).toBe("third audit");
			expect(below?.members).toBeUndefined();
		});

		// The R38 split re-projects each sub-run through activityRunRow, which
		// rebuilds the row from the run's first member. A tail sub-run of one
		// member comes back as that member's own row, so it must keep the
		// member's turnId: the reader's transcript folds runs and finds the live
		// run by turn (sessionRows, liveRunId, openingTarget). Before the fix the
		// rebuilt row dropped the id, so the run had no turn.
		it("keeps a split cluster's single-member tail row's turn id", async () => {
			const thread = makeThread({
				turns: [
					makeTurn({
						id: "t1",
						status: "completed",
						items: [commandExecItem("a-tool", "shell", "completed"), commandExecItem("b-tool", "shell", "completed")],
					}),
				],
			});
			const store = await openProjectedThread(thread, true);
			// The two completed calls cluster into one row whose members are [a, b].
			const clustered = rows(store).find((row) => row.id === "a-tool");
			expect(clustered?.kind === "activity" ? clustered.members?.map((member) => member.id) : undefined).toEqual([
				"a-tool",
				"b-tool",
			]);
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			// A newer same-family activity arrives AFTER the notice, on a new turn:
			// the run splits at the anchored member (b-tool), leaving the tail as a
			// sub-run of the single new member.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
					items: [{ ...commandExecItem("c-tool", "shell", "completed"), turnId: "t2" } as ThreadItem],
				}),
			);
			const split = rows(store).filter((row) => row.kind === "activity");
			expect(split.map((row) => row.id)).toEqual(["a-tool", "c-tool"]);
			const tail = split[1];
			expect(tail?.members).toBeUndefined();
			expect(tail?.turnId).toBe("t2");
		});

		// RoboRev review round 1: the anchor a notice records when it arrives
		// over an ALREADY-CLUSTERED row. The cluster's top-level identity
		// belongs to its FIRST member, so a notice that arrived after the
		// whole displayed run anchored there — and the R38 split then lifted
		// it above the run's later members, the same reorder the split exists
		// to prevent, one arrival earlier. The anchor is the cluster's LAST
		// member: every member was on screen when the notice landed, so the
		// whole run stays above it and only members that join LATER split
		// below it.
		it("seats a warning after the whole cluster that was displayed when it arrived", async () => {
			const { store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [commandExecItem("a-tool", "shell", "completed"), commandExecItem("b-tool", "shell", "completed")],
						}),
					],
				}),
				true,
			);
			const opened = rows(store);
			expect(opened[0]?.kind === "activity" ? opened[0].members?.map((member) => member.id) : undefined).toEqual([
				"a-tool",
				"b-tool",
			]);
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["activity", "a-tool"],
				["failure", "warning:1"],
			]);
			// A third same-family activity arrives AFTER the notice: the run
			// grows past the members that were displayed at arrival.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...commandExecItem("c-tool", "shell", "completed"), turnId: "t2" }],
				}),
			);
			const after = rows(store);
			expect(after.map((row) => [row.kind, row.id])).toEqual([
				["activity", "a-tool"],
				["failure", "warning:1"],
				["activity", "c-tool"],
			]);
			expect(after[0]?.kind === "activity" ? after[0].members?.map((member) => member.id) : undefined).toEqual([
				"a-tool",
				"b-tool",
			]);
		});

		// RoboRev panel round 2: an attachment row's own identity is GENERATED
		// from its source's wire id (`<id>:attachments`), so a notice that
		// arrives after one anchors to that generated id. The hub can reissue
		// the source item under a NEW wire id while its transcript key stands:
		// the projection re-keys the attachment row to the new id, the anchor
		// matches nothing the seating walk can resolve, and the next rebuild
		// pruned the notice — a silent diagnostic loss. Attachment rows must
		// anchor through their stable source identity, which the reissue keeps.
		it("keeps a warning seated across a reissue of the source item its attachment row was keyed to", async () => {
			const store = await openProjectedThread(
				makeThread({
					turns: [
						makeTurn({
							items: [
								{
									type: "userMessage",
									id: "wire-old",
									transcriptKey: "stable-message",
									text: "old",
									images: [{ type: "image", url: "https://hub.test/old" }],
								},
							],
						}),
					],
				}),
				true,
			);
			// The user row, then its attachments row keyed to the wire id.
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["user", "wire-old"],
				["attachments", "wire-old:attachments"],
			]);
			// An idle warning lands while the attachments row is the nearest
			// model row: the notice is displayed at once.
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store).some((row) => row.kind === "failure" && row.title === "Provider")).toBe(true);
			// The source is reissued under a new wire id with the same transcript
			// key and says nothing about images: the reducer folds it by identity,
			// the projection re-keys both the user row and the attachments row,
			// and the notice must stay seated.
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("wire-new", "reissued"), transcriptKey: "stable-message", turnId: "t1" }],
				}),
			);
			const reissued = rows(store);
			expect(reissued.filter((row) => row.kind === "attachments").map((row) => row.id)).toEqual([
				"wire-new:attachments",
			]);
			// The notice seats through the source identity the SOURCE row owns,
			// after the attachments that follow the source — the position it
			// arrived at, with the source/attachment pair left adjacent (the
			// cap cut drops orphans by that adjacency).
			expect(reissued.map((row) => [row.kind, row.id])).toEqual([
				["user", "wire-new"],
				["attachments", "wire-new:attachments"],
				["failure", "warning:1"],
			]);
			// A later row-changing rebuild keeps the notice seated.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("u2", "newer"), turnId: "t2" }],
				}),
			);
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["user", "wire-new"],
				["attachments", "wire-new:attachments"],
				["failure", "warning:1"],
				["user", "u2"],
			]);
		});

		// RoboRev follow-up to the panel round 2 fix: a notice anchored
		// through an attachment row's source identity must not seat BETWEEN
		// the source and its attachments. capItems' cap cut drops a leading
		// attachment whose source fell off the cut (an orphaned attachment's
		// lingering source identity makes loadOlder's F10 admission rule
		// refuse a genuine older page copy of that source, forever) — and it
		// only scans a LEADING RUN of attachments: a notice seated between
		// the pair became the first retained row at the cut, the scan stopped
		// before reaching the attachment, and the orphan survived.
		it("drops an attachment at the cap cut even when a warning is seated at its source's position", async () => {
			const store = await openProjectedThread(
				makeThread({
					turns: [
						makeTurn({
							items: [
								{
									type: "userMessage",
									id: "wire-old",
									transcriptKey: "stable-message",
									text: "old",
									images: [{ type: "image", url: "https://hub.test/old" }],
								},
							],
						}),
					],
				}),
				true,
			);
			expect(rows(store).map((row) => row.kind)).toEqual(["user", "attachments"]);
			// An idle warning lands while the attachments row is the nearest
			// model row.
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store)).toHaveLength(3);
			// Grow the transcript past the retained cap so the cut lands right
			// after the source row: [user, notice, attachments, a1..a498] is
			// 501 rows and the cap keeps the newest 500.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			for (let i = 1; i <= 498; i++) {
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...agentMessageItem(`a${i}`, `message ${i}`), turnId: "t2" }],
					}),
				);
			}
			const after = rows(store);
			// The source fell off the cut; its attachment left with it, and the
			// notice kept its seat.
			expect(after.some((row) => row.id === "wire-old")).toBe(false);
			expect(after.some((row) => row.kind === "attachments")).toBe(false);
			expect(after.filter((row) => row.kind === "failure")).toHaveLength(1);
			expect(after).toHaveLength(499);
		});

		// RoboRev round 2 of the panel follow-up: a clustered activity can
		// carry attachments from MORE THAN ONE member — [cluster(A, B),
		// A:attachments, B:attachments] — and a notice anchored through a
		// member's source identity seats after the cluster's WHOLE attachments
		// run, never between the cluster and the run. Seating it right after
		// the cluster (stopping the scan at A's attachment, whose source is
		// the other member) moved the notice off its arrival position and made
		// it the first retained row at a cap cut that fell just past the
		// cluster, so BOTH attachments survived orphaned — their lingering
		// source identities then make loadOlder's F10 admission rule refuse
		// genuine older page copies of those sources, forever.
		it("seats a warning anchored to a later cluster member after the cluster's whole attachments run", async () => {
			const store = await openProjectedThread(
				makeThread({
					turns: [
						makeTurn({
							items: [
								{
									type: "commandExecution",
									id: "call-a",
									toolName: "shell",
									status: "completed",
									outputImages: [{ source: "a", url: "https://hub.test/a" }],
								},
								{
									type: "commandExecution",
									id: "call-b",
									toolName: "shell",
									status: "completed",
									outputImages: [{ source: "b", url: "https://hub.test/b" }],
								},
							],
						}),
					],
				}),
				true,
			);
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["activity", "call-a"],
				["attachments", "call-a:attachments"],
				["attachments", "call-b:attachments"],
			]);
			// An idle warning lands while B's attachments row is the nearest
			// model row: it anchors through B's stable source identity and
			// seats after the whole run, at the position it arrived at.
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store).map((row) => [row.kind, row.id])).toEqual([
				["activity", "call-a"],
				["attachments", "call-a:attachments"],
				["attachments", "call-b:attachments"],
				["failure", "warning:1"],
			]);
			// Grow the transcript past the retained cap so the cut lands right
			// after the cluster: [cluster, A:att, B:att, notice, a1..a497] is
			// 501 rows and the cap keeps the newest 500.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "running" }],
				}),
			);
			for (let i = 1; i <= 497; i++) {
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...agentMessageItem(`a${i}`, `message ${i}`), turnId: "t2" }],
					}),
				);
			}
			const after = rows(store);
			// The cluster fell off the cut; the orphan scan reached its
			// attachments through the notice and dropped them with it.
			expect(after.some((row) => row.id === "call-a")).toBe(false);
			expect(after.some((row) => row.kind === "attachments")).toBe(false);
			expect(after.filter((row) => row.kind === "failure")).toHaveLength(1);
			expect(after).toHaveLength(498);
		});

		it("preserves a command description through live item projection", async () => {
			const { store } = await openRunningTurn([], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("command-1", "shell", "completed"),
							description: "Inspect the source tree",
							turnId: "t1",
						},
					],
				}),
			);
			expect(rowById(store, "command-1")).toMatchObject({
				kind: "activity",
				detail: { description: "Inspect the source tree" },
			});
		});

		it("preserves a user transcript entry index through live item projection", async () => {
			const { store } = await openRunningTurn([], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...userMessageItem("fork-source", "fork from this input"),
							transcriptEntryIndex: 8,
							turnId: "t1",
						},
					],
				}),
			);
			expect(rowById(store, "fork-source")).toMatchObject({
				kind: "user",
				transcriptEntryIndex: 8,
			});
		});
	});

	// A settled turn of `older` items followed by a running turn t1 — the shape
	// for the rows that are already history when a live frame lands.
	async function openProjectedThreadPair(older: ThreadItem[]) {
		const thread = makeThread({
			turns: [
				makeTurn({ id: "t0", status: "completed", items: older }),
				makeTurn({ id: "t1", status: "inProgress", items: [] }),
			],
			evener: evenerWith({ activeTurnId: "t1" }),
		});
		const service = new FakeConversationService();
		service.readProjectionResult = makeReadProjectionResult(thread);
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		return { store, service, thread };
	}
});
