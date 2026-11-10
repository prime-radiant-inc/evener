// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { AnyNotification, MutationReceipt, Thread, ThreadItem } from "@evener/appwire-client";
import { describe, expect, it, vi } from "vitest";
import type { MobileConversation, MobileTimelineItem } from "../../projectedRows";
import { projectConversation } from "../../projectedRows";
import { createConversationStore } from "./conversation";
import {
	ALL_TRUE_CAPS,
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
	readServing,
	resyncFrame,
	rowById,
	rows,
	target,
	textInput,
	turnsPage,
	userMessageItem,
	warningFrame,
	wireTurn,
	wireTurnFragment,
	yieldMicrotask,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	// --- Fix round 1: I1/I2/M1 rejection items ----------------------------------

	// --- the drain scheduler: per-key exact completion ------------------------

	// These tests prove the per-key completion promise resolves for the cap
	// key independently of unrelated rereads — the core invariant that the old
	// global-idle scheduler could not satisfy. All tests use level-triggered
	// deferred helpers (started/completed with target) and a watchdog to
	// detect hangs. No yieldMicrotask(count), setTimeout, or polling.

	// Watchdog: rejects if the promise does not settle within a timeout.
	// Catches hangs from a global-idle implementation that waits for all work.
	// The timer is cleared in finally so no open timer remains after the
	// guarded promise wins or the watchdog rejects.
	function withWatchdog<T>(label: string, p: Promise<T>, ms = 2000): Promise<T> {
		let timer: ReturnType<typeof setTimeout> | undefined;
		const watchdog = new Promise<never>((_resolve, reject) => {
			timer = setTimeout(() => reject(new Error(`watchdog: ${label} timed out after ${ms}ms`)), ms);
		});
		return Promise.race([p, watchdog]).finally(() => {
			if (timer !== undefined) clearTimeout(timer);
		});
	}

	// --- Residual: R2 — page-race must not drop authoritative outcome ---

	describe("page-race preserves authoritative outcome", () => {
		it("ask reread racing page success: authoritative question appears, page items/cursor preserved", async () => {
			// A rehydrate (R) is triggered by an ask_user notification. While R
			// is in-flight, a loadOlder (L) succeeds, prepending page items and
			// advancing the cursor. R's projection contains a question. When R
			// completes, the question must appear AND the page items/cursor must
			// be preserved (merged, not dropped).
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Initial: empty conversation.
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			// Set up R's projection to contain a question.
			const askThread = makeThread({
				evener: {
					ref: "ref-1",
					capabilities: { ...ALL_TRUE_CAPS },
					queue: { revision: 0 },
					askPending: true,
				},
				turns: [
					makeTurn({
						id: "t1",
						items: [askUserItem("ask-1", VALID_ASK_ARGS)],
						status: "completed",
					}),
				],
			});
			service.readProjectionResult = makeReadProjectionResult(askThread);
			// Trigger R via a resync — the authoritative refresh path — and hang it.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, L succeeds — prepends page items, advances cursor.
			service.olderItems = {
				items: [{ kind: "user", id: "old-page-item", text: "older" }],
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);
			const itemsAfterL = store.getState().conversation?.items ?? [];
			const cursorAfterL = store.getState().olderCursor;
			expect(itemsAfterL.some((i) => i.id === "old-page-item")).toBe(true);
			expect(cursorAfterL).toBe("cursor-2");
			// Release R — the question must appear AND page items/cursor preserved.
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			const items = store.getState().conversation?.items ?? [];
			// Question row from R's projection appears.
			expect(items.some((i) => i.kind === "question")).toBe(true);
			// Page items from L are preserved (not dropped).
			expect(items.some((i) => i.id === "old-page-item")).toBe(true);
			// Cursor from L is preserved.
			expect(store.getState().olderCursor).toBe("cursor-2");
		});

		it("preserves page-owned history when wire id differs from transcript key", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ items: [userMessageItem("base", "base")] })],
				}),
			);
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			const ctrl = makeControlledRead(service);
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							items: [userMessageItem("base", "base")],
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();

			service.olderItems = {
				items: [
					{
						kind: "user",
						id: "wire-page",
						transcriptKey: "stable-page",
						text: "older page",
					},
				],
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);

			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			const items = store.getState().conversation?.items ?? [];
			expect(items.filter((item) => item.transcriptKey === "stable-page")).toHaveLength(1);
			expect(items.find((item) => item.transcriptKey === "stable-page")?.id).toBe("wire-page");
			expect(store.getState().olderCursor).toBe("cursor-2");
		});

		it("dedupes page and reread items by transcript key when wire ids differ", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			const ctrl = makeControlledRead(service);
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							items: [
								{
									...userMessageItem("wire-reread", "canonical"),
									transcriptKey: "stable-page",
								},
							],
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			service.olderItems = {
				items: [
					{
						kind: "user",
						id: "wire-page",
						transcriptKey: "stable-page",
						text: "older",
					},
				],
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			const matches = (store.getState().conversation?.items ?? []).filter(
				(item) => item.transcriptKey === "stable-page",
			);
			expect(matches).toHaveLength(1);
			expect(matches[0]).toMatchObject({
				id: "wire-reread",
				text: "canonical",
			});
		});

		it("dedupes a live tail against a reread item by transcript key", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								...userMessageItem("wire-live", "live"),
								transcriptKey: "stable-live",
							},
							turnId: "t0",
						},
					],
				}),
			);
			const ctrl = makeControlledRead(service);
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							items: [
								{
									...userMessageItem("wire-reread", "canonical"),
									transcriptKey: "stable-live",
								},
							],
						}),
					],
				}),
			);
			// The live frame requested its own reread, which coalesces behind the
			// explicit one: both reads park, and the newer one commits.
			const reread = store.getState().rehydrate(service);
			await ctrl.ready(2);
			ctrl.release();
			ctrl.release();
			await reread;
			await ctrl.completed(2);
			await yieldMicrotask();
			const matches = (store.getState().conversation?.items ?? []).filter(
				(item) => item.transcriptKey === "stable-live",
			);
			expect(matches).toHaveLength(1);
			expect(matches[0]).toMatchObject({
				id: "wire-reread",
				text: "canonical",
			});
		});

		it("commits the reread's version when a live frame changed the wire id under the same transcript key", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							items: [
								{
									...userMessageItem("wire-live-old", "older-live"),
									transcriptKey: "stable-live",
								},
							],
						}),
					],
				}),
				ALL_TRUE_CAPS,
				true,
			);
			await store.getState().openProjected(service, "ref-1");
			const ctrl = makeControlledRead(service);
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t0",
							items: [
								{
									...userMessageItem("wire-reread", "canonical"),
									transcriptKey: "stable-live",
								},
							],
						}),
					],
				}),
			);
			const reread = store.getState().rehydrate(service);
			await ctrl.started(1);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								...userMessageItem("wire-live-new", "newer-live"),
								transcriptKey: "stable-live",
							},
							turnId: "t0",
						},
					],
				}),
			);
			ctrl.release();
			await reread;
			// One row under that transcript key, and it is the snapshot's — the
			// frame that renamed it is already accounted for at the cut.
			const matches = (store.getState().conversation?.items ?? []).filter(
				(item) => item.transcriptKey === "stable-live",
			);
			expect(matches).toHaveLength(1);
			expect(matches[0]).toMatchObject({
				id: "wire-reread",
				text: "canonical",
			});
		});

		it("activity reread racing page failure: authoritative activity appears, page error preserved", async () => {
			// A rehydrate (R) is triggered by an unknown item notification. While
			// R is in-flight, a loadOlder (L) fails, setting a page error. R's
			// projection contains an activity item. When R completes, the activity
			// must appear AND the page error must be preserved.
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Initial: empty conversation.
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			// Set up R's projection to contain an activity item.
			const activityThread = makeThread({
				turns: [
					makeTurn({
						id: "t1",
						items: [
							{
								...commandExecItem("tool-1", "bash", "completed"),
								argumentsJson: "{}",
								output: "done",
							} as ThreadItem,
						],
						status: "completed",
					}),
				],
			});
			service.readProjectionResult = makeReadProjectionResult(activityThread);
			// Trigger R via resync — it hangs.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, L fails — sets a page error.
			service.olderItems = Promise.reject(new Error("page load failed")) as never;
			await store
				.getState()
				.loadOlder(service)
				.catch(() => {});
			expect(store.getState().error).not.toBeNull();
			const errorAfterL = store.getState().error;
			// Release R — the activity must appear AND page error preserved.
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			const items = store.getState().conversation?.items ?? [];
			// Activity row from R's projection appears.
			expect(items.some((i) => i.kind === "activity" && i.id === "tool-1")).toBe(true);
			// Page error is preserved.
			expect(store.getState().error).toBe(errorAfterL);
		});
	});

	// --- Residual: R3 — real malformed/incomplete Thread fixtures through projectConversation ---

	describe("real malformed/incomplete Thread fixtures through projectConversation", () => {
		it("malformed completed ask_user in raw Thread: conservative activity rows, no question rows, askPending false", async () => {
			// A real raw Thread with a completed ask_user whose argumentsJson is
			// malformed must produce conservative activity rows (not question
			// rows), askPending false, and exactly one bounded reread.
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Initial: empty.
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			// The reread returns a Thread with a malformed completed ask_user.
			const malformedThread = makeThread({
				turns: [
					makeTurn({
						id: "t1",
						items: [askUserItem("ask-malformed", "{{not valid json")],
						status: "completed",
					}),
				],
			});
			service.readProjectionResult = makeReadProjectionResult(malformedThread);
			const initialReads = service.readProjectionCalls.length;
			// The snapshot is what settles an ask: drive one through a resync.
			store.getState().applyNotification(resyncFrame());
			const ctrl = makeControlledRead(service);
			await ctrl.started(1);
			await yieldMicrotask();
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			// Exactly one bounded reread.
			expect(service.readProjectionCalls.length).toBe(initialReads + 1);
			const conv = store.getState().conversation;
			expect(conv).not.toBeNull();
			// No question rows — malformed ask_user is conservative.
			const questionItem = conv?.items.find((i) => i.kind === "question");
			expect(questionItem).toBeUndefined();
			// askPending is false — malformed ask does not set pending.
			expect(conv?.askPending).toBe(false);
			// The ask_user is projected as a completed activity row (not a
			// question), since projectConversation's parseAskUserQuestions returns
			// undefined for malformed argumentsJson.
			const activityItem = conv?.items.find((i) => i.kind === "activity" && i.id === "ask-malformed");
			expect(activityItem).toBeDefined();
		});

		it("incomplete inProgress ask_user in raw Thread: conservative, no question rows, askPending false", async () => {
			// A real raw Thread with an inProgress ask_user must not produce
			// question rows or set askPending. The inProgress ask_user is
			// projected as a running activity row.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			// The reread returns a Thread with an inProgress ask_user.
			const incompleteThread = makeThread({
				turns: [
					makeTurn({
						id: "t1",
						items: [askUserItem("ask-incomplete", VALID_ASK_ARGS, "inProgress")],
						status: "running",
					}),
				],
			});
			service.readProjectionResult = makeReadProjectionResult(incompleteThread);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification(resyncFrame());
			const ctrl = makeControlledRead(service);
			await ctrl.started(1);
			await yieldMicrotask();
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			// Exactly one bounded reread.
			expect(service.readProjectionCalls.length).toBe(initialReads + 1);
			const conv = store.getState().conversation;
			expect(conv).not.toBeNull();
			// No question rows — inProgress ask is not pending.
			const questionItem = conv?.items.find((i) => i.kind === "question");
			expect(questionItem).toBeUndefined();
			// askPending is false — inProgress ask_user is not answerable.
			expect(conv?.askPending).toBe(false);
			// The inProgress ask_user is projected as a running activity row.
			const activityItem = conv?.items.find((i) => i.kind === "activity" && i.id === "ask-incomplete");
			expect(activityItem).toBeDefined();
			if (activityItem?.kind === "activity") {
				expect(activityItem.state).toBe("running");
			}
		});
	});

	// --- Fix round 1 (residual review): C1/I1/I2/I3 ---

	// C1: Trailing reread must capture the EXACT original RequestBinding at
	// schedule time (epoch/ref/gen/service), NOT recapture the current
	// binding inside the effect. If the store switched to serviceB during
	// the await, the trailing reread must NOT call serviceA. The effect
	// validates the captured snapshot — never recapture B then combine with
	// A's closure variables.
	describe("C1: trailing reread captures exact original binding snapshot", () => {
		it("trailing reread suppressed after switch to serviceB — zero serviceA reads", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-A" }));
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			// Start a rehydrate (R) on serviceA that hangs.
			const ctrl = makeControlledRead(serviceA);
			store.getState().applyNotification(resyncFrame("thread-A", "ref-A"));
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, start a send that succeeds — mutation owner
			// revision changes, triggering a trailing reread.
			const sendP = store.getState().send(serviceA, textInput("hello"));
			await sendP;
			// Switch to serviceB — new binding epoch.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-B" }));
			await store.getState().openProjected(serviceB, "ref-B");
			// Release R — it should detect mutation owner changed and schedule a
			// trailing reread. The trailing reread was captured with serviceA.
			// After the switch to serviceB, the trailing reread must be suppressed
			// (the captured binding is stale).
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			// Let the trailing reread effect run (it should be suppressed).
			await yieldMicrotask();
			// serviceA should NOT have been called for a trailing reread.
			// The original R read + openProjected read = 2 calls to serviceA.
			const aReads = serviceA.readProjectionCalls.length;
			// serviceB's openProjected read = 1 call to serviceB.
			const bReads = serviceB.readProjectionCalls.length;
			// No trailing reread on serviceA — its read count stays at 2.
			expect(aReads).toBe(2);
			expect(bReads).toBe(1);
		});
	});

	// I1: If mutation revision changed and mutation still pending, the trailing
	// reread must NOT queue/read yet. Store one binding+revision-owned trailing
	// request and drain exactly once only after that mutation settles (success
	// or failed terminal). Additional mutation revisions create at most one
	// later need. A switch to serviceB while the trailing reread is deferred
	// drops the deferred request entirely.
	describe("trailing reread deferred until mutation settles", () => {
		it("zero trailing reads while mutation pending, exactly one after settlement", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			// Start a rehydrate (R) that hangs.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, start a send that HANGS (mutation still pending).
			let resolveSend: (() => void) | null = null as (() => void) | null;
			const hangSend = new Promise<MutationReceipt>((r) => {
				resolveSend = () => r(makeReceipt());
			});
			service.send = async () => hangSend;
			store.getState().send(service, textInput("hello"));
			// Mutation is in flight — mutation owner revision changed.
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			// Release R — it detects mutation owner changed, schedules trailing
			// reread. But the mutation is STILL PENDING, so the trailing reread
			// must NOT start yet (not even call readProjection).
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			await yieldMicrotask();
			await yieldMicrotask();
			// Zero trailing reads while mutation is pending — startedCount stays
			// at 1 (only the original R read started).
			expect(ctrl.getStartedCount()).toBe(1);
			// Now settle the mutation (success).
			(resolveSend as () => void)();
			await yieldMicrotask();
			await yieldMicrotask();
			// After settlement, exactly one trailing reread should start.
			await ctrl.started(2);
			await yieldMicrotask();
			ctrl.release();
			await ctrl.completed(2);
			await yieldMicrotask();
			// Exactly one trailing read after settlement.
			expect(ctrl.getStartedCount()).toBe(2);
		});

		it("zero trailing reads while mutation pending (failed terminal), exactly one after", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			// Start a rehydrate (R) that hangs.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, start a send that HANGS, then will fail.
			let rejectSend: ((e: Error) => void) | null = null as ((e: Error) => void) | null;
			const hangSend = new Promise<MutationReceipt>((_r, reject) => {
				rejectSend = reject;
			});
			service.send = async () => hangSend;
			const sendP = store.getState().send(service, textInput("hello"));
			// Mutation is in flight.
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			// Release R — schedules trailing reread, but mutation pending.
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			await yieldMicrotask();
			await yieldMicrotask();
			// Zero trailing reads while mutation is pending.
			expect(ctrl.getStartedCount()).toBe(1);
			// Now fail the mutation (terminal failed state).
			(rejectSend as (e: Error) => void)(new Error("send failed"));
			await withWatchdog("send settle (failed)", sendP);
			await yieldMicrotask();
			await yieldMicrotask();
			// After settlement (failed), exactly one trailing reread.
			await ctrl.started(2);
			await yieldMicrotask();
			ctrl.release();
			await ctrl.completed(2);
			await yieldMicrotask();
			expect(ctrl.getStartedCount()).toBe(2);
		});

		it("switch to serviceB while trailing reread deferred drops A entirely", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-A" }));
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			// Start a rehydrate (R) on serviceA that hangs.
			const ctrlA = makeControlledRead(serviceA);
			store.getState().applyNotification(resyncFrame("thread-A", "ref-A"));
			await ctrlA.started(1);
			await yieldMicrotask();
			// While R is in-flight, start a send that hangs (mutation pending).
			let resolveSend: (() => void) | null = null as (() => void) | null;
			const hangSend = new Promise<MutationReceipt>((r) => {
				resolveSend = () => r(makeReceipt());
			});
			serviceA.send = async () => hangSend;
			store.getState().send(serviceA, textInput("hello"));
			// Release R — schedules deferred trailing reread, mutation pending.
			ctrlA.release();
			await ctrlA.completed(1);
			await yieldMicrotask();
			await yieldMicrotask();
			// Switch to serviceB while trailing reread is deferred.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-B" }));
			await store.getState().openProjected(serviceB, "ref-B");
			// Now settle the mutation on serviceA — the trailing reread was
			// captured with serviceA's binding. After the switch to serviceB,
			// the binding is stale, so the trailing reread must be suppressed.
			(resolveSend as () => void)();
			await yieldMicrotask();
			await yieldMicrotask();
			// Let any potential trailing reread drain.
			await yieldMicrotask();
			// serviceA should NOT have received a trailing reread (binding changed).
			const aReads = serviceA.readProjectionCalls.length;
			const bReads = serviceB.readProjectionCalls.length;
			// serviceA: 1 (openProjected) + 1 (R) = 2. No trailing reread.
			expect(aReads).toBe(2);
			// serviceB: 1 (openProjected) only.
			expect(bReads).toBe(1);
		});
	});

	// The reread's snapshot is authoritative over every thread-level frame that
	// preceded its response: AppWire orders the response at the snapshot cut,
	// so such a frame is already folded into the snapshot. These fixtures hand the store a snapshot that does NOT reflect
	// the frame — an ordering the protocol excludes — to pin that the store
	// does not second-guess the snapshot: no replay, no per-field ownership.
	async function rereadRacing(
		liveWrite: (store: ReturnType<typeof createConversationStore>) => void,
		snapshot: Thread,
		initial: Thread = makeThread(),
	) {
		const service = new FakeConversationService();
		service.readProjectionResult = makeReadProjectionResult(initial);
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		service.readProjectionResult = makeReadProjectionResult(snapshot);
		const ctrl = makeControlledRead(service);
		store.getState().applyNotification(resyncFrame());
		await ctrl.started(1);
		await yieldMicrotask();
		liveWrite(store);
		ctrl.release();
		await ctrl.completed(1);
		await yieldMicrotask();
		return store;
	}

	describe("the reread's snapshot is authoritative over pre-response capability writes", () => {
		it("normal resync changes caps — rehydrate commits projected capabilities", async () => {
			const conv = (
				await rereadRacing(
					() => {},
					makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: false } }) }),
					makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: true } }) }),
				)
			).getState().conversation;
			expect(conv?.capabilities.send).toBe(false);
		});

		it("a capability publication that preceded the response yields to the snapshot", async () => {
			// The publication turned queue off before the response arrived; the
			// snapshot (queue on, send off) is the newer truth and is what commits.
			const conv = (
				await rereadRacing(
					(store) =>
						store.getState().applyNotification({
							method: "thread/status/changed",
							params: {
								...target,
								status: { type: "ready" },
								capabilities: { ...ALL_TRUE_CAPS, send: false, queue: false },
							},
						} as AnyNotification),
					makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: false } }) }),
					makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: true } }) }),
				)
			).getState().conversation;
			expect(conv?.capabilities.send).toBe(false);
			expect(conv?.capabilities.queue).toBe(true);
		});
	});

	describe("the reread's snapshot is authoritative over frames that preceded it", () => {
		const liveEscalation = {
			...target,
			escalationId: "esc-live",
			mode: "workspace",
			tool: "exec",
			kind: "read",
			deniedPath: "/outside",
		};
		it.each<{
			name: string;
			method: string;
			params: Record<string, unknown>;
			snapshot: Thread;
			read: (conv: NonNullable<MobileConversation>) => unknown;
			expected: unknown;
		}>([
			{
				name: "a status change",
				method: "thread/status/changed",
				params: { status: { type: "active" } },
				snapshot: makeThread({ status: { type: "idle" } }),
				read: (conv) => conv.status,
				expected: { type: "idle" },
			},
			{
				name: "a queue change",
				method: "thread/queueChanged",
				params: { queue: { revision: 4, depth: 1, texts: ["later"] } },
				snapshot: makeThread(),
				read: (conv) => conv.queue,
				expected: { revision: 0 },
			},
			{
				name: "a rename",
				method: "evener/thread/name/changed",
				params: { name: "renamed before the response" },
				snapshot: makeThread({ name: "snapshot name" }),
				read: (conv) => conv.name,
				expected: "snapshot name",
			},
			{
				name: "a model change",
				method: "thread/model/changed",
				params: {
					modelProvider: "openai/gpt-x",
					model: "gpt-x",
					reasoningEffortLevels: ["low", "high"],
					supportsReasoning: true,
				},
				snapshot: makeThread({ modelProvider: "anthropic" }),
				read: (conv) => [conv.modelProvider, conv.reasoningEffortLevels],
				expected: ["anthropic", []],
			},
			{
				name: "a goal update",
				method: "evener/goal/updated",
				params: { goal: { objective: "before the response", status: "active", iterations: 2 } },
				snapshot: makeThread({
					evener: evenerWith({ goal: { objective: "snapshot", status: "active", iterations: 1 } }),
				}),
				read: (conv) => conv.goal,
				expected: { objective: "snapshot", status: "active", iterations: 1 },
			},
			{
				name: "a task update",
				method: "evener/task/updated",
				params: { total: 3, done: 2 },
				snapshot: makeThread({ evener: evenerWith({ tasks: { total: 3, done: 1 } }) }),
				read: (conv) => conv.tasks,
				expected: { total: 3, done: 1 },
			},
			{
				name: "a started turn",
				method: "history/updated",
				params: {
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 1 },
					turns: [{ id: "t-before", itemsView: "default", status: "inProgress", items: [] }],
				},
				snapshot: makeThread(),
				read: (conv) => [conv.activeTurnId, conv.turns.length],
				expected: [undefined, 0],
			},
			{
				name: "an approval request",
				method: "evener/sandbox/escalation/requested",
				params: liveEscalation,
				snapshot: makeThread(),
				read: (conv) => conv.pendingEscalations,
				expected: [],
			},
		])(
			"commits the snapshot over $name that preceded the response",
			async ({ method, params, snapshot, read, expected }) => {
				const conv = (
					await rereadRacing(
						(store) =>
							store.getState().applyNotification({
								method,
								params: { ...target, ...params },
							} as AnyNotification),
						snapshot,
					)
				).getState().conversation;
				if (!conv) throw new Error("conversation gone");
				expect(read(conv)).toEqual(expected);
			},
		);

		// The ordering the protocol does allow: a frame delivered after the
		// response lands on top of the committed snapshot.
		it("applies a frame that arrives after the response on top of the snapshot", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({ status: { type: "idle" }, name: "snapshot name" }),
			);
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification({
				method: "evener/thread/resync",
				params: target,
			} as AnyNotification);
			await ctrl.ready(1);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			store.getState().applyNotification({
				method: "thread/status/changed",
				params: { ...target, status: { type: "active" } },
			} as AnyNotification);
			expect(store.getState().conversation).toMatchObject({
				name: "snapshot name",
				status: { type: "active" },
			});
		});

		// The same rule for the frames that carry transcript CONTENT, which is the
		// half a reader would notice: a delta delivered after the response streams
		// on top of the snapshot's own text. The snapshot is what the hub had at
		// the cut — its materialized turn authority folds every delta into the item
		// as it streams (server/appwire_turns.go's NotifyAgentMessageDelta case,
		// `item.Text += params.Delta`), so a delta this store folded BEFORE the
		// response is already in the text the response carries, and one after it
		// appends to that text rather than being lost.

		// Two fences on the same rule for the frames where a second application
		// would not be idempotent: a turn the snapshot already carries keeps its
		// items and raises no duplicate-turn report, and a steering the snapshot
		// already carries is not appended twice, so the next live steering takes
		// the next index.
		it("does not replay a started turn the snapshot already carries", async () => {
			const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
			try {
				const conv = (
					await rereadRacing(
						(store) =>
							store.getState().applyNotification({
								method: "history/updated",
								params: {
									threadId: "thread-1",
									ref: "ref-1",
									bootGeneration: "1",
									epoch: 1,
									snapshot: { incarnation: "inc-1", length: 1 },
									turns: [{ id: "t-live", itemsView: "default", status: "inProgress" }],
								},
							} as AnyNotification),
						makeThread({
							turns: [
								makeTurn({
									id: "t-live",
									status: "inProgress",
									items: [userMessageItem("u-live", "started during the read")],
								}),
							],
							evener: evenerWith({ activeTurnId: "t-live" }),
						}),
					)
				).getState().conversation;
				expect(conv?.activeTurnId).toBe("t-live");
				expect(conv?.turns.map((turn) => [turn.id, turn.items.length])).toEqual([["t-live", 1]]);
				expect(consoleError).not.toHaveBeenCalled();
			} finally {
				consoleError.mockRestore();
			}
		});

		// A reread belongs to the conversation it was for: closing that
		// conversation and opening another must leave the next conversation's
		// frames applied and the dead read's snapshot never committed.
		it("never commits a dead read's snapshot after the conversation closes", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			service.readProjectionResult = makeReadProjectionResult(makeThread({ name: "old read's snapshot" }));
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification({
				method: "evener/thread/resync",
				params: target,
			} as AnyNotification);
			await ctrl.started(1);
			await yieldMicrotask();
			store.getState().close();

			const next = new FakeConversationService();
			next.openConv = makeConversation({ threadId: "thread-2", name: "second" });
			await store.getState().open(next, "ref-2");
			store.getState().applyNotification({
				method: "evener/thread/name/changed",
				params: { threadId: "thread-2", ref: "ref-2", name: "second, renamed live" },
			} as AnyNotification);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			expect(store.getState().conversation).toMatchObject({
				threadId: "thread-2",
				name: "second, renamed live",
			});
		});
	});

	// Every item frame folds into the package reducer's model, and the rows
	// are projected from it — so what the model carries is what the phone
	// shows: the streamed text of a settled item, an injected steer, the retry
	// indicator clearing on the model's own output.
	describe("the model under the item frames is what the rows show", () => {
		const withActiveTurn = (items: ThreadItem[]) =>
			makeThread({
				turns: [makeTurn({ id: "t1", status: "inProgress", items })],
				evener: evenerWith({ activeTurnId: "t1" }),
			});
		it("clears modelRetry when the model's own output item completes", async () => {
			const store = await openProjectedThread(withActiveTurn([agentMessageItem("a1", undefined, "inProgress")]));
			store.getState().applyNotification({
				method: "evener/thread/modelRetry",
				params: { ...target, attempt: 1, maxAttempts: 3, delayMs: 10, groupElapsedMs: 5, attemptCap: 3, turnId: "t1" },
			} as AnyNotification);
			expect(store.getState().conversation?.modelRetry).toMatchObject({ attempt: 1, maxAttempts: 3 });
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("a1", "done", "completed"), turnId: "t1" }],
				}),
			);
			expect(store.getState().conversation?.modelRetry).toBeUndefined();
		});

		// The live row must show the reducer-folded warning item's text/hint,
		// not just params.message — a title/hint-only frame (no top-level
		// message) folds to a non-blank text in the model (the hint), but the
		// row applier was building its failure row from params.message alone
		// and losing it.
		it("shows the reducer-folded hint text for a title/hint-only warning with an active turn", async () => {
			const store = await openProjectedThread(withActiveTurn([]));
			store.getState().applyNotification({
				method: "warning",
				params: { ...target, title: "Provider warning", hint: "slow down" },
			} as AnyNotification);
			const failureRow = store.getState().conversation?.items.find((row) => row.kind === "failure");
			expect(failureRow).toMatchObject({
				kind: "failure",
				title: "Provider warning",
				detail: "slow down",
			});
		});

		// A warning carrying both a message and a hint must show both, the same
		// as the web and TUI renderers — the live row must not drop the hint
		// just because a message is also present.
		it("composes both message and hint in the live row's detail", async () => {
			const store = await openProjectedThread(withActiveTurn([]));
			store.getState().applyNotification({
				method: "warning",
				params: { ...target, message: "rate limit approaching", hint: "slow down" },
			} as AnyNotification);
			const failureRow = store.getState().conversation?.items.find((row) => row.kind === "failure");
			expect(failureRow).toMatchObject({
				kind: "failure",
				detail: "rate limit approaching — slow down",
			});
		});

		// A whitespace-only stored title is not real content, the same reading
		// hasWarningText gives everywhere else — it must fall back to the
		// generic "Warning" label rather than rendering a blank chip.
		it("falls back to the generic Warning label for a whitespace-only title with an active turn", async () => {
			const store = await openProjectedThread(withActiveTurn([]));
			store.getState().applyNotification({
				method: "warning",
				params: { ...target, title: "   ", message: "careful" },
			} as AnyNotification);
			const failureRow = store.getState().conversation?.items.find((row) => row.kind === "failure");
			expect(failureRow).toMatchObject({ kind: "failure", title: "Warning" });
		});

		// The live row applier (case "warning" above) and the canonical projector
		// (projectConversation, applied on every reread) must produce the SAME row
		// for the same warning: an attention row — kind "failure", title as its own
		// field, message+hint joined as detail. The canonical projector used to
		// route type "warning" through its generic unknown-activity fallback, so
		// the row changed kind, lost its attention treatment, and regressed to a
		// collapsed "Activity" row labelled "unknown" the moment a reread replaced
		// the live one.
		it("projects the same warning to the same failure row live and canonically", async () => {
			const store = await openProjectedThread(withActiveTurn([]));
			const warning = {
				method: "warning",
				params: {
					...target,
					title: "Provider warning",
					message: "rate limit approaching",
					hint: "slow down",
				},
			} as AnyNotification;
			store.getState().applyNotification(warning);
			const live = store.getState().conversation;
			expect(live).not.toBeNull();
			const liveRow = live?.items.find((row) => row.kind === "failure");
			const canonicalRow = projectConversation(live!).items.find((row) => row.kind === "failure");
			const expected = {
				kind: "failure",
				title: "Provider warning",
				detail: "rate limit approaching — slow down",
			};
			expect(liveRow).toMatchObject(expected);
			expect(canonicalRow).toMatchObject(expected);
			expect(canonicalRow?.kind).toBe(liveRow?.kind);
			// One identity, not just one shape: timelineIdentity is
			// transcriptKey ?? id, and a reread dedupes on exactly that, so the two
			// rows must agree here or the warning survives the reread twice.
			const identity = (row: MobileTimelineItem | undefined) =>
				row === undefined ? undefined : (row.transcriptKey ?? row.id);
			expect(identity(canonicalRow)).toBe(identity(liveRow));
		});

		// The identity the live row shares with the canonical one is what keeps a
		// reread from showing the warning twice. The reread projection here is the
		// canonical projection of the same model (what a hub that carries the
		// warning frame in a snapshot — or a future projection path — serves); the
		// live-owned row must be recognized as that identity and dropped, leaving
		// exactly one failure row.
		it("merges a warning to one failure row across a reread", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(withActiveTurn([]));
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.getState().applyNotification({
				method: "warning",
				params: {
					...target,
					title: "Provider warning",
					message: "rate limit approaching",
					hint: "slow down",
				},
			} as AnyNotification);
			const liveRows = (store.getState().conversation?.items ?? []).filter((row) => row.kind === "failure");
			expect(liveRows).toHaveLength(1);
			const liveIdentity = liveRows[0]!.transcriptKey ?? liveRows[0]!.id;

			const reread = projectConversation(store.getState().conversation!);
			service.readProjectionResult = readServing(reread, null);
			await store.getState().rehydrate(service);

			const rows = (store.getState().conversation?.items ?? []).filter((row) => row.kind === "failure");
			expect(rows).toHaveLength(1);
			expect(rows[0]!.transcriptKey ?? rows[0]!.id).toBe(liveIdentity);
		});
	});

	// Page history and the snapshot: the rows a reread carries are the thread,
	// the page history in front of them is what this client paged in, and
	// everything else is gone — including a row a live frame inserted before
	// the response, which the snapshot has already accounted for.
	// Task 17 (v6 read path): as with the "#1919 follow-up" block above, this
	// block used to carry a family of tests pinning the pre-v6 merge's own
	// coverage-claim re-merge (a skeleton-free replay deciding whether a page's
	// cursor still counts as "covering" retained history), its per-field
	// supplier-set contribution tracking, and its call-id/fold-based tool
	// precedence — all mechanism the shared package deletes with
	// mergeTurnHistoryWithContext once mobile stops calling it. See the longer
	// note above "#1919 follow-up" for why the versioned merge (turn-id-keyed,
	// transcriptKey-stable) has no equivalent need: applyReadModel's own cursor
	// rule (keep the held older cursor exactly when the held history's earliest
	// position precedes the fresh window's) replaces the coverage re-merge, and
	// a turn's error/usage/status survive a covering refresh through ordinary
	// version supersession (mergeHistory's turn-scalar pass), not a page-vs-
	// snapshot ownership claim. The surviving tests below cover what still
	// applies under v6.
	describe("page history in front of the snapshot's rows", () => {
		// RoboRev #2213 round 1 (Medium): the reset above takes the row off the
		// screen, but the page ownership the removed item recorded used to
		// outlive the removal — and the wire reuses item ids across stream
		// restarts (the reset→started protocol), so the restarted live row
		// inherited a claim that described content the model no longer held and
		// survived an authoritative snapshot that omitted it.

		// The same seam on the completion side: a full turn/completed replaces
		// the turn's item set, so the items its stamp omits leave the model —
		// and the page ownership they recorded must leave with them, the
		// identities an omitted item's attachment row resolves its source by
		// included. The wire reuses ids across turns, so a stale claim would
		// resurrect a later live row an authoritative snapshot omits.

		// RoboRev round 2 on this change's own settlement: a full completion
		// can RE-KEY a survivor — the same transcript key under a new wire id —
		// and the combined either-spelling check used to keep the OLD wire id's
		// claim alive with the key's. The key's claim legitimately follows the
		// content that still backs it; the retired bare id's must retire with
		// the copy that left, or a later keyless reuse of that id inherits page
		// history nothing holds anymore.

		// RoboRev round 4 (panel M1): the frame settlement must reconcile
		// ownership through the merge's own identity rule, not a union of every
		// post-frame id. A completion can reissue a KEYLESS page item WITH its
		// transcript key — the content continues under the key, so the claim
		// must follow it there. The union kept the old bare id's claim but
		// never gained the key's, so the retention rules — which read the
		// key-first identity — treated the surviving page history as unowned
		// and an omitting rehydrate dropped it.
		it("migrates a page item's claim to the transcript key a completion reissues it under", async () => {
			const { service, store } = await openRunningTurn([], {});
			// The page fragment of the active turn carries a KEYLESS item: the
			// ownership record claims its bare wire id.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t1", [
							{
								...agentMessageItem("X", "page answer", "completed"),
								turnId: "t1",
								position: { entry: 10, item: 0 },
							} as ThreadItem,
						]),
					],
					undefined,
				),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rowById(store, "X")).toBeDefined();
			// The turn's full completion reissues the same content WITH its key.
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "full",
							status: "completed",
						},
					],
					items: [
						{
							...agentMessageItem("X", "page answer settled", "completed"),
							turnId: "t1",
							transcriptKey: "K",
						} as ThreadItem,
					].map((it) => ({ ...it, turnId: it.turnId ?? "t1" })),
				}),
			);
			expect(rowById(store, "X")).toBeDefined();
			// The authoritative snapshot omits the item: the reissued copy is
			// page history the model still holds, and the migrated claim — now
			// covering the key the survivor carries — must keep it.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t1", status: "completed", items: [] })],
				}),
			);
			await store.getState().rehydrate(service);
			expect(rows(store).some((row) => row.id === "X")).toBe(true);
		});

		// RoboRev round 4 (panel M1): the same id under a DIFFERENT key is a
		// conflicting replacement, not continuing content — the merge's own
		// identity rule (keys compared when both sides carry one) matches the
		// two to nothing, so both of the replaced item's spellings retire.
		// The union used to keep the stale bare-id claim alive while the
		// replacement held the id; every path that could read that bare-id
		// spelling passes a frame whose settlement retires it first (the
		// removal clears it; a keyed replacement is queried by its key), so
		// this pins the semantics end to end rather than a reachable failure:
		// the replacement's content is not page history, and once it leaves,
		// the next keyless holder of the bare id inherits nothing.

		// RoboRev round 4, review round 1: two items can share a bare wire id
		// under different keys — the merge's own identity rule calls them two
		// identities, so the model can hold both at once. The reconciliation's
		// ownership guard read BOTH spellings, so the unowned one found the
		// other's bare-id claim and a frame that preserved both promoted the
		// live item to page history; an omitting rehydrate then kept live
		// content the wire had withdrawn.
		it("keeps a same-id different-key item from adopting the bare-id claim another item owns", async () => {
			const { service, store } = await openRunningTurn([], {});
			// The page fragment of t1 carries a KEYED item: the claim records the
			// key and the bare wire id.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t1", [
							{
								...agentMessageItem("X", "page answer", "completed"),
								turnId: "t1",
								transcriptKey: "K1",
								position: { entry: 10, item: 0 },
							} as ThreadItem,
						]),
					],
					undefined,
				),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rowById(store, "X")).toBeDefined();
			// A live item takes the same bare id under a different key — a
			// second identity the package's own rule distinguishes.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...agentMessageItem("X", "live answer", "completed"),
							turnId: "t2",
							transcriptKey: "K2",
						},
					],
				}),
			);
			// t1's full completion preserves its own keyed item — the frame
			// removes nothing, yet the settlement walk runs and must not
			// promote the live copy: only the spelling an item's own identity
			// carries decides whether it owns a claim.
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "full",
							status: "completed",
						},
					],
					items: [
						{
							...agentMessageItem("X", "page answer", "completed"),
							turnId: "t1",
							transcriptKey: "K1",
						} as ThreadItem,
					].map((it) => ({ ...it, turnId: it.turnId ?? "t1" })),
				}),
			);
			// The authoritative snapshot omits both X items: the page-owned
			// copy stays as page history, and the live copy — unowned content
			// the wire withdrew — must go.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [userMessageItem("other", "kept")],
						}),
						makeTurn({ id: "t2", status: "completed", items: [] }),
					],
				}),
			);
			await store.getState().rehydrate(service);
			const xRows = rows(store).filter((row) => row.id === "X");
			expect(xRows).toHaveLength(1);
		});

		// RoboRev round 4, review round 1: the contribution replay compared
		// the item-merge spine, but a folded call's final fields come from the
		// tool fold's own selection — fresh results, fresh calls, then the
		// page's — so the spine's rank-promoted status can be one the rewrite
		// immediately overrides. A page whose every field the retained side
		// already supplies must contribute nothing: no progress keys, and no
		// page ownership for content the merge did not keep.

		// RoboRev round 5 (panel Medium): the contribution answer must come
		// from the fold's own keep-decisions, recorded where the fold made
		// them. The replay re-derived those decisions after the fact, and its
		// re-derivation was not the fold's: an identity-folded RESULT fed the
		// same candidate groups the fold classifies by kind — and a same-callId
		// call the fold's field selection reads but no identity fold joined was
		// invisible to it — so a page result the fold's call precedence
		// superseded still read as the merged call's contributor: the page
		// claimed ownership and reported progress keys for content the fold
		// did not keep.

		// RoboRev round 5, review round 1: an item whose content arrives only
		// from the page — here as two identical copies a reissued fragment
		// carries, folding into an identity skeleton the retained window
		// remembers — is page-carried no matter what identical-input
		// deduplication does to the field-level keep-decisions. The second
		// identical fold erased the first copy's recorded supply, so a
		// brand-new page item read as contributing nothing and an omitting
		// rehydrate dropped history the page alone supplied.

		// RoboRev round 5, review round 1: a supplier whose content a LATER
		// fold replaced must not ride the non-tool set forward. The page
		// supplies the text, an earlier retained fragment the startedAt, and a
		// later retained fragment replaces the text: the startedAt survives
		// the chain but the page's content does not, so the page contributed
		// nothing and must claim nothing.

		// RoboRev round 5, review round 1: the rewrite's selection, not the
		// base's history, names a tool field's supplier. A page result whose
		// output equals the later retained call's still loses the field to
		// that call — the selection scanned it — so the page must not keep the
		// supply the base's earlier fold recorded.

		// A replayed input image reaches a page with no bytes and no stamped
		// url, only its content sha. D23d: the store's own merge hydrates the
		// page against the merged MODEL's serving session — the route the hub
		// can actually serve — never a placeholder id (the old service-side
		// projection hydrated a fake Thread under the thread id for this).
		it("routes an older page's sha-only image through the merged model's session", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			const sha = "a".repeat(64);
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{
								...userMessageItem("u-older", "see attached"),
								turnId: "t0",
								images: [{ type: "image", name: "shot.png", metadata: { sha } }],
								position: { entry: 1, item: 0 },
							} as ThreadItem,
						]),
					],
					undefined,
				),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).find((row) => row.id === "u-older:attachments")).toMatchObject({
				kind: "attachments",
				items: [{ id: "u-older:0", src: `/s/session-1/images/${sha}`, name: "shot.png" }],
			});
		});

		it("a paged running item settles with the snapshot's completed turn", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "inProgress",
							items: [],
						}),
					],
					evener: evenerWith({ activeTurnId: "t1" }),
				}),
			);
			// The older page carries the item's row before the live window
			// holds it: page ownership registers (RoboRev round 17), and a
			// live frame then brings the same item into the active turn —
			// page-owned history AND the running row.
			await loadFillerPage(store, service);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("item-a", "Hello", "inProgress"), turnId: "t1" }],
				}),
			);

			// The reread's turn completed; its item carries no status of its
			// own: the snapshot settles it, and the page-owned copy must settle
			// with it — not project "Writing…" forever after.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [{ id: "item-a", turnId: "t1", type: "agentMessage", text: "Hello" } as ThreadItem],
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect((rowById(store, "item-a") as { streaming?: boolean } | undefined)?.streaming).toBe(false);
		});

		it("a full turn completion withdraws a page-loaded item it omits", async () => {
			const { service, store } = await openRunningTurn([], {}, true);
			await loadFillerPage(store, service);

			// A live frame brings the paged item into the active turn.
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("pagey", "paged content"), turnId: "t1" }],
				}),
			);
			expect(rowById(store, "pagey")).toBeDefined();

			// The turn's full completion omits the item: the reducer withdraws
			// it, and the page-history layer must not resurrect the row (RoboRev
			// round 18).
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "full",
							status: "completed",
						},
					],
					items: [userMessageItem("other", "kept")].map((it) => ({ ...it, turnId: it.turnId ?? "t1" })),
				}),
			);
			expect(rowById(store, "pagey")).toBeUndefined();

			// And no later frame restores it either.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(rowById(store, "pagey")).toBeUndefined();
		});

		it("a full turn completion with no items list withdraws them all", async () => {
			const { service, store } = await openRunningTurn([], {}, true);
			await loadFillerPage(store, service);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("pagey", "paged content"), turnId: "t1" }],
				}),
			);
			expect(rowById(store, "pagey")).toBeDefined();

			// The stamp's item list is OMITTED, not empty-bracketed — Go's
			// omitempty drops an empty list, and the reducer reads it as one:
			// every item is withdrawn (RoboRev round 19).
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "full",
							status: "completed",
						},
					],
				}),
			);
			expect(rowById(store, "pagey")).toBeUndefined();

			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(rowById(store, "pagey")).toBeUndefined();
		});

		it("a missed completion's stale active flag does not keep the discarded items", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [makeTurn({ id: "tA", status: "inProgress", items: [] })],
					evener: evenerWith({ activeTurnId: "tA" }),
				}),
			);
			await loadFillerPage(store, service);
			for (const item of [
				{ id: "p-1", turnId: "tA", type: "userMessage", text: "paged" } as ThreadItem,
				{ id: "live-1", turnId: "tA", type: "agentMessage", text: "stale live", status: "inProgress" } as ThreadItem,
			]) {
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...item, turnId: "tA" }],
					}),
				);
			}
			// The turn's completion is MISSED: the local model still names tA
			// active.

			// The reread omits the formerly active mixed turn and names no
			// active turn: the snapshot's word is the authority for which turn
			// is still the live working set (RoboRev round 22).
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t9", status: "completed", items: [userMessageItem("later", "fresh")] })],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(rowById(store, "live-1")).toBeUndefined();

			// And no later row-changing frame projects it back.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(rowById(store, "live-1")).toBeUndefined();
		});

		it("a reread that drops a turn's error does not resurrect the failure", async () => {
			const { service, store } = await openRunningTurn([], {}, true);
			await loadFillerPage(store, service);

			// The turn fails locally: its error projects a failure row.
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "",
							status: "failed",
							error: { message: "boom" },
						},
					],
				}),
			);
			const failureRows = () => rows(store).filter((row) => row.kind === "failure");
			expect(failureRows()).not.toHaveLength(0);

			// The reread's turn completed cleanly — no error: the snapshot's
			// copy of the turn is authoritative, and the retained error must
			// not ride the merge back (RoboRev round 23).
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [userMessageItem("done", "finished")],
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(failureRows()).toHaveLength(0);

			// And no later row-changing frame resurrects it.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(failureRows()).toHaveLength(0);
		});

		it("a paged fragment of a turn does not shield its live-acquired failure from a clean reread", async () => {
			const { service, store } = await openRunningTurn([], {}, true);
			store.setState({ olderCursor: "cursor-1" });
			// The page owns a bare usage FRAGMENT of the live turn — no failure
			// content among it.
			service.olderItems = {
				turnsPage: turnsPage([wireTurn("t1", 500, 20)], undefined),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);

			// The turn fails live: its error projects a failure row.
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "",
							status: "failed",
							error: { message: "boom" },
						},
					],
				}),
			);
			const failureRows = () => rows(store).filter((row) => row.kind === "failure");
			expect(failureRows()).not.toHaveLength(0);

			// The reread's turn completed cleanly — no error: the snapshot's
			// copy of the turn is authoritative, and the page owned no failure
			// CONTENT for the turn — only a usage fragment — so the error must
			// not survive the merge (RoboRev round 24: ownership of the turn
			// is not ownership of its failure).
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [userMessageItem("done", "finished")],
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(failureRows()).toHaveLength(0);

			// And no later row-changing frame resurrects it.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(failureRows()).toHaveLength(0);
		});

		it("a settled snapshot withdraws a matched item's retained failure status", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "inProgress",
							items: [
								{
									...commandExecItem("act", "shell", "inProgress"),
									turnId: "t1",
									callId: "c1",
								} as ThreadItem,
							],
						}),
					],
					evener: evenerWith({ activeTurnId: "t1" }),
				}),
				true,
			);
			await loadFillerPage(store, service);

			// The call fails locally: its item settles "failed" and the row
			// shows it.
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...commandExecItem("act", "shell", "failed"), turnId: "t1", callId: "c1" }],
				}),
			);
			expect((rowById(store, "act") as { state?: string }).state).toBe("failed");

			// The reread's turn completed; its copy of the item carries NO
			// status of its own: the snapshot reads it settled-clean, and the
			// retained "failed" must not outlive that copy (RoboRev round 24:
			// the nullish fallback kept the failure, and the next row-changing
			// frame reprojected the completed activity as failed again).
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [
								{
									id: "act",
									turnId: "t1",
									type: "commandExecution",
									toolName: "shell",
									callId: "c1",
								} as ThreadItem,
							],
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect((rowById(store, "act") as { state?: string }).state).toBe("completed");
			expect(
				store
					.getState()
					.conversation?.turns.flatMap((turn) => turn.items)
					.find((item) => item.id === "act")?.status,
			).toBeUndefined();

			// And the clean state survives an unrelated row-changing frame.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect((rowById(store, "act") as { state?: string }).state).toBe("completed");
		});

		// RoboRev round 31, fold-rename collision: the survivor of a fold can
		// carry its OWN error — the snapshot then projects failure:<survivor>
		// while the page-owned failure:<older> row renames onto the SAME
		// identity, leaving two display rows with one list key. The snapshot's
		// row is the survivor's authoritative failure; the renamed history row
		// must reconcile away instead of duplicating.

		// RoboRev round 38 (panel Medium 2): the rehydrate failure-fold marked
		// every fold survivor page-owned unconditionally, but the row rewrite
		// below drops the renamed row whenever the snapshot carries its own
		// failure:<survivor> row (nativeFailureIds wins). The survivor was
		// marked page-owned even though the page's content did not survive,
		// so the next snapshot to resolve or retry the failure read the
		// snapshot's own row as retained history and kept the resolved failure
		// on screen indefinitely. The loadOlder path adds the survivor only
		// when the renamed row actually survived (seenRenamedIds); the
		// rehydrate fold must mirror that guard.

		// RoboRev round 32, pagination-side fold: loadOlder's merge folds a
		// failed page turn into a retained turn that shares its item — the
		// merged model carries the error under the RETAINED turn, but the
		// committed page row still said failure:<page turn>, so the next
		// row-changing frame projected the carrier's failure beside it: the
		// same failure twice. The row and its page ownership must migrate with
		// the fold at the pagination commit too (rounds 30-31 covered the
		// rehydrate side), with the same one-row-per-identity reconciliation.
		it("migrates a failed page turn's failure row when the pagination merge folds it into a retained turn", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [
								{
									...agentMessageItem("k1", "retained text", "completed"),
									transcriptKey: "k1",
									turnId: "t1",
								} as ThreadItem,
							],
						}),
					],
				}),
			);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						{
							id: "t0",
							itemsView: "fragment",
							status: "failed",
							error: { message: "page boom" },
							usage: { inputTokens: 500, outputTokens: 20 },
							items: [
								{
									...agentMessageItem("k1", "page text", "completed"),
									transcriptKey: "k1",
									turnId: "t0",
								} as ThreadItem,
							],
						},
					],
					"c9",
				),
				nextCursor: "c9",
			};
			await store.getState().loadOlder(service);
			const failureRows = () => rows(store).filter((row) => row.kind === "failure");
			// The fold moved the failure onto the retained turn — the committed
			// row must carry the carrier's identity, not the folded page turn's.
			expect(failureRows()).toHaveLength(1);
			expect(failureRows()[0]?.id).toBe("failure:t1");

			// A row-changing frame projects the carrier's failure from the
			// model — still one row, same identity.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(failureRows()).toHaveLength(1);
			expect(failureRows()[0]?.id).toBe("failure:t1");
		});

		// RoboRev review round 1, claim migration: a paged failed turn folds
		// into a fresh survivor that carries NO error of its own — the error
		// the merged model carries is the PAGE's, so the failure claim must
		// migrate to the survivor's identity. The retiring pass used to read
		// the claim it had just retired (the successor walk runs against the
		// live set), so the claim never migrated — the error rode this merge's
		// model, and the next covering refresh, whose copy omits the error,
		// stripped the page-supplied failure as unowned.

		// RoboRev review round 1, result-only paging: a page that carries only a
		// call's RESULT folds it into the call the window already holds — the
		// package records the absorbed result in the tool-result folds, which
		// only retention consumers read, not the item folds — so the merged call
		// is page history (the page supplied its output) and a later sparse
		// covering refresh must not strip that output as unowned, and the load
		// must report the contribution it added to the window.

		// RoboRev round 4 (panel M2): the contribution set must be the merge's
		// own answer, not a hand-rolled field list. Status merges by RANK — a
		// page's completed copy outranks the retained inProgress one while both
		// sides still carry a status — so the merged item carries the page's
		// status while the presence check read two non-null statuses as "the
		// page brought nothing": the claim never reached pageItemIds and an
		// omitting rehydrate dropped the surviving history.

		// The #2213 final-verdict Medium: contribution tracking attributed
		// text suppliers by strict value equality, so a page item whose text is
		// explicitly provided-empty and a retained sparse item whose text the
		// wire omitted — both hydrating to "" — recorded as sharing the text
		// supplier. The page's text settle then read as contributing nothing,
		// the load reported no item keys, and an omitting rehydrate dropped the
		// loaded historical row. The attribution must follow the merge's own
		// presence rule: the side that actually supplied text keeps the claim.

		// RoboRev round 4 (panel M2): startedAt and completedAt fold by nullish
		// fallback — the merged item carries the page's copy when the retained
		// side has none — but the hand-rolled field list never named them, so a
		// page item supplying nothing else read as contributing nothing and the
		// load reported no item keys for content the merge actually took.

		// RoboRev review round 3: itemKeys is the reader's no-progress signal —
		// nonempty clears its retry guard — so it must report what THIS page
		// brought, not the page-owned history a prior page already owns. A page
		// that only re-serves retained content (a duplicate under the next
		// cursor) adds nothing and must report empty, or the guard clears and a
		// stale-cursor loop can keep re-requesting.
		it("a page that only re-serves retained content reports no item keys", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({ turns: [makeTurn({ id: "t1", status: "completed", items: [] })] }),
			);
			store.setState({ olderCursor: "cursor-1" });
			const olderFragment = (turnId: string): ThreadItem[] => [
				{
					id: "older-a",
					turnId,
					type: "userMessage",
					text: "older text",
					status: "completed",
				} as ThreadItem,
			];
			service.olderItems = {
				turnsPage: turnsPage([wireTurnFragment("t0", olderFragment("t0"))], "cursor-2"),
				nextCursor: "cursor-2",
			};
			const first = await store.getState().loadOlder(service);
			expect(first.status).toBe("loaded");
			expect(first.status === "loaded" ? first.itemKeys : []).toContain("older-a");

			// The next page re-serves the same row under the next cursor: nothing
			// new reaches the window, so the load must report no item keys.
			store.setState({ olderCursor: "cursor-2" });
			service.olderItems = {
				turnsPage: turnsPage([wireTurnFragment("t0-again", olderFragment("t0-again"))], "cursor-3"),
				nextCursor: "cursor-3",
			};
			const second = await store.getState().loadOlder(service);
			expect(second.status).toBe("loaded");
			expect(second.status === "loaded" ? second.itemKeys : []).toEqual([]);
		});

		// RoboRev review round 4: text is not a plain nullable in the
		// contribution check — hydration spells OMITTED text as an empty
		// string plus a presence marker, so a retained sparse item's ""
		// reads as a payload the page's text does not dominate under the
		// field-null rule. A page supplying ONLY the missing text still owns
		// the restored row: it is this page's contribution (itemKeys) and
		// page history, so the next refresh whose window omits the item
		// keeps it.

		// RoboRev review round 4: a seated notice consumes a cap slot like
		// any row. A window the cap fills with 499 rows plus a notice trims
		// when the next page's row would displace the notice, and paging goes
		// on from the oldest row kept.
		it("keeps paging when a page's row would displace a seated warning", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			// The warning arrives on an empty window: it seats before
			// everything, as the oldest row the window can evict.
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store)).toHaveLength(1);
			// A resync reread fills the window: 499 model rows plus the
			// seated notice is exactly the retained cap.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: Array.from({ length: 499 }, (_, i) => ({
								type: "userMessage" as const,
								id: `row-${i}`,
								turnId: "t1",
								text: `message ${i}`,
								position: { entry: 100 + i, item: 0 },
								status: "completed" as const,
							})),
						}),
					],
				}),
			);
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
			expect(rows(store)).toHaveLength(500);
			expect(rows(store)[0]?.kind).toBe("failure");
			// One older row arrives below the window: seated, the merged
			// window is 501 rows and the cap trims a row the entry window
			// held.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{
								...userMessageItem("older-row", "older"),
								turnId: "t0",
								position: { entry: 1, item: 0 },
								status: "completed",
							} as ThreadItem,
						]),
					],
					"more",
				),
				nextCursor: "more",
			};
			const loaded = await store.getState().loadOlder(service);
			expect(loaded.status).toBe("loaded");
			expect(store.getState().olderCursor).toBe("more");
			expect(store.getState().trimmedAbove).toBe(true);
			// The page's own row stays in the window: the notice took the
			// eviction, so this page was not discarded.
			expect(rows(store).some((row) => row.id === "older-row")).toBe(true);
			expect(rows(store)).toHaveLength(500);
		});

		// RoboRev review round 5, progress reporting: itemKeys must name
		// every identity this page contributed that the window retains —
		// including a failed turn's failure row, which no page item backs.

		// RoboRev review round 1, key acquisition: a refresh can hand a paged
		// item the transcript key it never carried — the merge folds the keyed
		// reissue into the keyless page item, and the identity the retention
		// rules then query is the KEY. Page ownership must follow the fold, or
		// the next bounded refresh, whose window omits the item, drops loaded
		// page history.
		it("keeps a paged item's page-ness when a refresh lends it a transcript key", async () => {
			const { service, store } = await openRunningTurn([], {});
			store.setState({ olderCursor: "cursor-1" });
			// The page's item is keyless (an older producer that predates keys)
			// and otherwise exactly what the reread will re-serve — no position,
			// no payload the keyed reissue lacks — so the merge's no-op path
			// carries it: the fresh items return by reference, with no fold
			// provenance to read ownership from.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{ ...agentMessageItem("u", "page text", "completed"), turnId: "t0" } as ThreadItem,
						]),
					],
					undefined,
				),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).some((row) => row.id === "u")).toBe(true);

			const awaitRehydrateRead = async (count: number): Promise<void> => {
				for (let i = 0; i < 40 && service.readProjectionCalls.length < count; i++) {
					await Promise.resolve();
				}
				await Promise.resolve();
				await Promise.resolve();
				await Promise.resolve();
			};

			// The reread re-serves the same wire id, now carrying its key: the
			// fold lends the merged item the key the retention rules will read.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t9",
							status: "completed",
							items: [
								{
									...agentMessageItem("u", "page text", "completed"),
									transcriptKey: "k",
									turnId: "t9",
								} as ThreadItem,
							],
						}),
						makeTurn({ id: "t1", status: "inProgress", items: [] }),
					],
					evener: evenerWith({ activeTurnId: "t1" }),
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await awaitRehydrateRead(2);
			expect(rows(store).some((row) => row.id === "u")).toBe(true);

			// The next refresh's window omits the item entirely: page history
			// nobody else holds must survive it.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t1", status: "inProgress", items: [] })],
					evener: evenerWith({ activeTurnId: "t1" }),
				}),
			);
			store.getState().applyNotification(resyncFrame());
			await awaitRehydrateRead(3);
			expect(rows(store).some((row) => row.id === "u")).toBe(true);
		});

		// RoboRev round 33, self-rename poison: the rehydrate rename loop used
		// to mutate page ownership WHILE collecting migrations, so a fold
		// coalescing two retained turns under one survivor made the survivor
		// look newly page-owned: processing the first member added
		// failure:<survivor>, processing the second member then recorded a
		// SELF-rename, and the reconciliation read the snapshot's own failure
		// row as history — keeping the stale page failure and dropping the
		// snapshot's authoritative error.

		// RoboRev round 33, pagination bridge: a second page can BRIDGE two
		// retained turns — its items chain a previously paged failed turn to
		// another retained turn, and the merge names both as newer-side
		// members under the surviving carrier. The paged turn's failure row
		// and ownership must migrate to the carrier through newerTurnFolds,
		// or the next row-changing frame projects the carrier's failure
		// beside the obsolete row: the same failure twice.
		it("migrates a previously paged failure when a later page bridges its turn with another retained turn", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [
								{
									...agentMessageItem("k2", "retained t1 text", "completed"),
									transcriptKey: "k2",
									turnId: "t1",
								} as ThreadItem,
							],
						}),
					],
				}),
			);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						{
							id: "t0",
							itemsView: "fragment",
							status: "failed",
							error: { message: "page boom" },
							usage: { inputTokens: 500, outputTokens: 20 },
							items: [
								{
									...agentMessageItem("k1", "page text", "completed"),
									transcriptKey: "k1",
									turnId: "t0",
								} as ThreadItem,
							],
						},
					],
					"c9",
				),
				nextCursor: "c9",
			};
			await store.getState().loadOlder(service);
			const failureRows = () => rows(store).filter((row) => row.kind === "failure");
			expect(failureRows()).toHaveLength(1);
			expect(failureRows()[0]?.id).toBe("failure:t0");

			// The second page's turn bridges the paged t0 (k1) with the
			// retained t1 (k2): the merge coalesces both under t1, and t1 now
			// carries the failure — the committed row and its page ownership
			// must follow.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						{
							id: "tp",
							itemsView: "fragment",
							status: "completed",
							usage: { inputTokens: 10, outputTokens: 1 },
							items: [
								{
									...agentMessageItem("k1", "page text", "completed"),
									transcriptKey: "k1",
									turnId: "tp",
								} as ThreadItem,
								{
									...agentMessageItem("k2", "retained t1 text", "completed"),
									transcriptKey: "k2",
									turnId: "tp",
								} as ThreadItem,
							],
						},
					],
					undefined,
				),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(failureRows()).toHaveLength(1);
			expect(failureRows()[0]?.id).toBe("failure:t1");

			// A row-changing frame projects the carrier's failure from the
			// model — still one row, same identity.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(failureRows()).toHaveLength(1);
			expect(failureRows()[0]?.id).toBe("failure:t1");
		});

		it("keeps a page-owned attachment when the snapshot's source row omits it", async () => {
			// The page-owned attachment's own identity is "wire-Z:attachments";
			// its source is "key-Z". A later notification adds a fresh row that
			// shares "key-Z" (the source) but carries no attachment of its own —
			// the snapshot merely mentioning the source must not read as "this
			// attachment is already there."
			const { store, service } = await openRunningTurn([], {}, true);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [
					{
						kind: "activity",
						id: "wire-Z",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "done" },
						transcriptKey: "key-Z",
					},
					{
						kind: "attachments",
						id: "wire-Z:attachments",
						items: [{ id: "att-Z", src: "https://example.com/z.png" }],
						sourceTranscriptKey: "key-Z",
					},
				],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).some((item) => item.id === "wire-Z:attachments")).toBe(true);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("wire-Z-source", "shell", "completed"),
							transcriptKey: "key-Z",
							output: "done",
							turnId: "t1",
						},
					],
				}),
			);

			// The merge keeps the page's images (absent images mean unchanged),
			// while the source's identity follows the reissue — the attachment
			// row re-keys with its source and survives under the new id.
			expect(rows(store).some((item) => item.id === "wire-Z-source:attachments")).toBe(true);
		});

		// The RoboRev finding this pins: an authoritative outputImages: [] (the
		// wire's only way to say "these are gone" — appwire's nil/non-nil-empty/
		// non-empty rule, reducer.ts's outputImagesToItemImages comment) removes
		// the live attachment row, but the merge above only ever treated a
		// source as superseded once a REPLACEMENT attachment row existed, so a
		// page-owned attachment for that source was reattached even after an
		// explicit clear. Distinct from the "omits it" case above: there, the
		// snapshot says nothing about the source's images (unchanged); here it
		// says there are none (removed).
		it("drops a page-owned attachment once its source explicitly clears its output images", async () => {
			const { store, service } = await openRunningTurn([], {}, true);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [
					{
						kind: "activity",
						id: "wire-Z",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "done" },
						transcriptKey: "key-Z",
					},
					{
						kind: "attachments",
						id: "wire-Z:attachments",
						items: [{ id: "att-Z", src: "https://example.com/z.png" }],
						sourceTranscriptKey: "key-Z",
					},
				],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).some((item) => item.id === "wire-Z:attachments")).toBe(true);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("wire-Z-source", "shell", "completed"),
							transcriptKey: "key-Z",
							output: "done",
							outputImages: [],
							turnId: "t1",
						},
					],
				}),
			);

			expect(rows(store).some((item) => item.id === "wire-Z:attachments")).toBe(false);
		});

		it("keeps a retained page-owned attachment beside its source, not wherever it sat in the page", async () => {
			// Placement: a page [P1, P2, P3+attach(P3), P4, P5]. A live frame
			// reissues P3 under a new wire id with no output images of its own.
			// The identity merge folds the reissue into the retained page item
			// where it sits (the page's images stay — absent means unchanged),
			// so the attachment row re-keys with its source and stays directly
			// beside it — never orphaned wherever it sat in the page.
			const { store, service } = await openRunningTurn([], {}, true);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [
					{ kind: "user", id: "p1", text: "one" },
					{ kind: "user", id: "p2", text: "two" },
					{
						kind: "activity",
						id: "wire-p3",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "done" },
						transcriptKey: "key-p3",
					},
					{
						kind: "attachments",
						id: "wire-p3:attachments",
						items: [{ id: "att-p3", src: "https://example.com/p3.png" }],
						sourceTranscriptKey: "key-p3",
					},
					{ kind: "user", id: "p4", text: "four" },
					{ kind: "user", id: "p5", text: "five" },
				],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).map((item) => item.id)).toEqual(["p1", "p2", "wire-p3", "wire-p3:attachments", "p4", "p5"]);

			// The snapshot re-emits key-p3 under a new wire id, no outputImages.
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("wire-p3-new", "shell", "completed"),
							transcriptKey: "key-p3",
							output: "done",
							turnId: "t1",
						},
					],
				}),
			);

			const ids = rows(store).map((item) => item.id);
			expect(ids).toEqual(["p1", "p2", "wire-p3-new", "wire-p3-new:attachments", "p4", "p5"]);
		});

		it("drops a page-owned attachment once the snapshot re-emits its source's attachment under the same wire id", async () => {
			// Case (c) of the state table below: the snapshot's attachment row has
			// the SAME own identity as the page's, so ownTimelineIdentities alone
			// already catches it — this pins that the existing behavior holds once
			// (d), below, adds a second path to the same drop.
			const { store, service } = await openRunningTurn([]);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [
					{
						kind: "activity",
						id: "wire-Z",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "done" },
						transcriptKey: "key-Z",
					},
					{
						kind: "attachments",
						id: "wire-Z:attachments",
						items: [{ id: "att-Z", src: "https://example.com/z.png" }],
						sourceTranscriptKey: "key-Z",
					},
				],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).some((item) => item.id === "wire-Z:attachments")).toBe(true);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("wire-Z", "shell", "completed"),
							transcriptKey: "key-Z",
							output: "done",
							outputImages: [{ source: "z", url: "https://example.com/z.png" }],
							turnId: "t1",
						},
					],
				}),
			);

			// The snapshot's own "wire-Z:attachments" supersedes the page's.
			expect(rows(store).filter((item) => item.id === "wire-Z:attachments")).toHaveLength(1);
		});

		it("drops a page-owned attachment once the snapshot re-emits its source's attachment under a new wire id", async () => {
			// Case (d): the hub reissues the source's wire id while its transcript
			// key stands (loadOlder's own admission rule, F10 above, dedupes
			// exactly this). The page's "old-wire-Z:attachments" and the snapshot's
			// "new-wire-Z:attachments" share source "key-Z" but no own identity, so
			// the own-identity check alone would keep both — one page-owned image
			// row and one fresh one, for the same message.
			const { store, service } = await openRunningTurn([], {}, true);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				items: [
					{
						kind: "activity",
						id: "old-wire-Z",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "done" },
						transcriptKey: "key-Z",
					},
					{
						kind: "attachments",
						id: "old-wire-Z:attachments",
						items: [{ id: "att-old", src: "https://example.com/old.png" }],
						sourceTranscriptKey: "key-Z",
					},
				],
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).some((item) => item.id === "old-wire-Z:attachments")).toBe(true);

			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("new-wire-Z", "shell", "completed"),
							transcriptKey: "key-Z",
							output: "done",
							outputImages: [{ source: "new", url: "https://example.com/new.png" }],
							turnId: "t1",
						},
					],
				}),
			);

			const ids = rows(store).map((item) => item.id);
			// The page's superseded copy is gone, not kept alongside the fresh one.
			expect(ids).not.toContain("old-wire-Z:attachments");
			expect(ids).toContain("new-wire-Z:attachments");
		});

		// RoboRev round 2 (panel Medium 1): the rehydrate's retained-turn bound
		// must read the same window the publish will show. The publish seats the
		// transient warnings before the cap, and a seated notice consumes a cap
		// slot — an unseated bound keeps a page turn's full payloads alive for a
		// row the seated cap just evicted, so the model would carry page history
		// the actual 500-row display window no longer holds.
		it("bounds the rehydrate's retained page payloads against the seated window the publish shows", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t-live",
							status: "completed",
							items: [
								{
									...userMessageItem("live-1", "live row"),
									position: { entry: 50, item: 0 },
								},
							],
						}),
					],
				}),
			);
			// One older page loads a single page-owned row below the live row.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("tp", [
							{
								...userMessageItem("page-row", "page text"),
								turnId: "tp",
								position: { entry: 1, item: 0 },
								status: "completed",
							} as ThreadItem,
						]),
					],
					"cursor-2",
				),
				nextCursor: "cursor-2",
			};
			const loaded = await store.getState().loadOlder(service);
			expect(loaded.status).toBe("loaded");
			expect(rows(store).map((row) => row.id)).toEqual(["page-row", "live-1"]);
			// The warning arrives while the live row is the newest row the window
			// holds, so it anchors there and seats directly after it.
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store)).toHaveLength(3);
			expect(rows(store)[2]?.kind).toBe("failure");
			// A resync reread fills the window: the page row, the live row, and
			// 498 fresh rows project to exactly the retained cap, and the seated
			// notice makes the publish's window one row larger — the cap's
			// eviction falls on the page row.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t-live",
							status: "completed",
							items: [
								{
									...userMessageItem("live-1", "live row"),
									position: { entry: 50, item: 0 },
								},
							],
						}),
						makeTurn({
							id: "t-fresh",
							status: "completed",
							items: Array.from({ length: 498 }, (_, i) => ({
								type: "userMessage" as const,
								id: `fresh-${i}`,
								turnId: "t-fresh",
								text: `fresh ${i}`,
								position: { entry: 100 + i, item: 0 },
								status: "completed" as const,
							})),
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			const awaitRehydrateRead = async (count: number): Promise<void> => {
				for (let i = 0; i < 40 && service.readProjectionCalls.length < count; i += 1) {
					await Promise.resolve();
				}
				await Promise.resolve();
				await Promise.resolve();
				await Promise.resolve();
			};
			await awaitRehydrateRead(2);
			// The publish seats the notice and the cap evicts the page row: the
			// seated window the display shows — the live row, the seated notice,
			// and the fresh rows — no longer holds the page row.
			expect(rows(store)).toHaveLength(500);
			expect(rows(store).some((row) => row.id === "page-row")).toBe(false);
			expect(rows(store).filter((row) => row.kind === "failure")).toHaveLength(1);
			// The retained-turn bound must read that same seated window: the page
			// turn's payloads left the keep-window, so the model trims the turn to
			// its compact identity — full payloads for rows the display window no
			// longer holds would keep re-projecting on every later merge.
			const pageTurn = store.getState().conversation?.turns.find((turn) => turn.id === "tp");
			expect(pageTurn?.items.length).toBe(0);
		});

		// RoboRev local round 1 (Medium): the publish must show the same seated
		// window the retained-turn bound trims against. A notice that anchors
		// to a page row the seated cap evicts still SURVIVES that cap — but
		// re-projecting the rows from the trimmed turns drops the anchor row
		// entirely, the re-seat at the publish then finds no anchor, and the
		// prune retires a notice the window kept.
		it("keeps a seated warning through a rehydrate whose bound trims the warning's anchor row", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			// One older page loads a single page-owned row below the empty window.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("tp", [
							{
								...userMessageItem("page-row", "page text"),
								turnId: "tp",
								position: { entry: 1, item: 0 },
								status: "completed",
							} as ThreadItem,
						]),
					],
					"cursor-2",
				),
				nextCursor: "cursor-2",
			};
			const loaded = await store.getState().loadOlder(service);
			expect(loaded.status).toBe("loaded");
			expect(rows(store).map((row) => row.id)).toEqual(["page-row"]);
			// The warning arrives on a window whose only row is the page row, so
			// it anchors there and seats directly after it.
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			expect(rows(store)).toHaveLength(2);
			expect(rows(store)[1]?.kind).toBe("failure");
			// A resync reread fills the window: the page row plus 499 fresh rows
			// project to exactly the retained cap, and the seated notice makes
			// the window one row larger — the cap's eviction falls on the page
			// row, the notice's own anchor.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t-fresh",
							status: "completed",
							items: Array.from({ length: 499 }, (_, i) => ({
								type: "userMessage" as const,
								id: `fresh-${i}`,
								turnId: "t-fresh",
								text: `fresh ${i}`,
								position: { entry: 100 + i, item: 0 },
								status: "completed" as const,
							})),
						}),
					],
				}),
			);
			store.getState().applyNotification(resyncFrame());
			const awaitRehydrateRead = async (count: number): Promise<void> => {
				for (let i = 0; i < 40 && service.readProjectionCalls.length < count; i += 1) {
					await Promise.resolve();
				}
				await Promise.resolve();
				await Promise.resolve();
				await Promise.resolve();
			};
			await awaitRehydrateRead(2);
			// The seated window the bound read kept the notice — the publish must
			// show exactly it: the notice stays seated at the front of the window
			// even though the bound trimmed the page turn its anchor named.
			expect(rows(store)).toHaveLength(500);
			expect(rows(store)[0]?.kind).toBe("failure");
			expect(rows(store).some((row) => row.id === "page-row")).toBe(false);
			const pageTurn = store.getState().conversation?.turns.find((turn) => turn.id === "tp");
			expect(pageTurn?.items.length).toBe(0);
		});
	});
});
