// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { AnyNotification, MutationReceipt } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { MobileConversation } from "../../projectedRows";
import { createConversationStore } from "./conversation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	VALID_ASK_ARGS,
	askUserItem,
	evenerWith,
	historyUpdated,
	makeControlledRead,
	makeConversation,
	makeReadProjectionResult,
	makeReceipt,
	makeThread,
	makeTurn,
	openProjectedThread,
	openRunningTurn,
	openedStore,
	readServing,
	refusal,
	resyncFrame,
	rowById,
	rows,
	statusChanged,
	textInput,
	userMessageItem,
	yieldMicrotask,
} from "./conversationTestUtils";

// A store opened through openProjected onto a fresh fake service whose read
// projection serves `conversation` with `olderCursor`: the opening the
// projected-path tests start with when they script the cursor directly.
async function openedProjectedStore(
	conversation: MobileConversation = makeConversation(),
	olderCursor: string | null = null,
): Promise<{
	service: FakeConversationService;
	store: ReturnType<typeof createConversationStore>;
}> {
	const service = new FakeConversationService();
	service.readProjectionResult = readServing(conversation, olderCursor);
	const store = createConversationStore();
	await store.getState().openProjected(service, "ref-1");
	return { service, store };
}

describe("ConversationStore", () => {
	// D18 B3 round 5: closes the class rounds 3-4 kept re-opening at different
	// sites — the store's own top-level olderCursor (a UI-only, intentionally
	// "is there another page to fetch" signal) and the
	// conversation's own ThreadModel olderCursor (the wire truth sessionTokens
	// reads) are two different values, and code kept collapsing one into the
	// other. Per-state table (a session with no thread-level cumulative usage,
	// so sessionTokens is always summing turns):
	//
	//   state                          | store cursor | conv cursor | turns   | scope
	//   initial (open)                 | null         | "cursor-1"  | [t2]    | loaded
	//   loadOlder (wire has more)      | "cursor-2"   | "cursor-2"  | [t1,t2] | loaded
	//   cap hit (wire still has more)  | "more"       | "more"      | [t1,t2] | loaded
	//   rehydrate, page history kept   | (unchanged)  | prior conv's| [t1,t2] | loaded
	//                                  |              | own cursor  |         |
	//   rehydrate, no page history     | fresh read's | fresh read's| fresh   | per fresh
	//                                  | own          | own         | only    | read
	//
	// "loadOlder" and "cap hit" (without a following rehydrate) are already
	// covered above by "merges the older page's turns..." and "keeps
	// conversation.olderCursor at the wire's cursor...". The remaining rows,
	// plus the two regressions the panel found, are below.
	describe("no presentation disclosure state in conversation store", () => {
		it("conversation store does not carry expandedToolKeys or setExpandedToolKeys", () => {
			const store = createConversationStore();
			const s = store.getState();
			// These presentation fields must NOT exist on the store.
			expect(s).not.toHaveProperty("expandedToolKeys");
			expect(typeof (s as unknown as Record<string, unknown>).setExpandedToolKeys).toBe("undefined");
		});
	});

	describe("projected binding epoch — stale work suppressed before effect", () => {
		it("queued rehydrate for A is suppressed after switch to B (zero serviceA reads)", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-A" }),
				olderCursor: null,
			};
			const store = createConversationStore();

			// Open A (completes).
			await store.getState().openProjected(serviceA, "ref-A");
			// Queue a rehydrate for A via resync — the scheduler captures the
			// binding (serviceA+refA+epoch) but defers to a microtask.
			store.getState().applyNotification(resyncFrame("thread-A", "ref-A"));
			// Now set up serviceB and switch to B before the scheduler fires.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-B" }),
				olderCursor: null,
			};
			await store.getState().openProjected(serviceB, "ref-B");
			const readsA = serviceA.readProjectionCalls.length;
			// M1: yield so the suppressed A effect's microtask fires and bails.
			await yieldMicrotask();
			// I1: zero serviceA reads after the switch.
			expect(serviceA.readProjectionCalls.length).toBe(readsA);
			// B should be the current conversation.
			expect(store.getState().conversation?.threadId).toBe("thread-B");
		});

		it("queued rehydrate for A is suppressed after close (no serviceA reads)", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-A" }),
				olderCursor: null,
			};
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			// Trigger a rehydrate then immediately close.
			store.getState().applyNotification(resyncFrame("thread-A", "ref-A"));
			store.getState().close();
			const readsA = serviceA.readProjectionCalls.length;
			await yieldMicrotask();
			// I1: zero additional serviceA reads after close.
			expect(serviceA.readProjectionCalls.length).toBe(readsA);
		});

		it("queued rehydrate for A is suppressed after reset (no serviceA reads)", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-A" }),
				olderCursor: null,
			};
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			store.getState().applyNotification(resyncFrame("thread-A", "ref-A"));
			store.getState().reset();
			const readsA = serviceA.readProjectionCalls.length;
			await yieldMicrotask();
			expect(serviceA.readProjectionCalls.length).toBe(readsA);
		});

		it("plain open clears projected bindings (no projected rehydrate after open)", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-A" }),
				olderCursor: null,
			};
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			// Queue a rehydrate via resync.
			store.getState().applyNotification(resyncFrame("thread-A", "ref-A"));
			// Plain open clears projected bindings.
			const serviceB = new FakeConversationService();
			serviceB.openConv = makeConversation({ threadId: "thread-B" });
			await store.getState().open(serviceB, "ref-B");
			const readsA = serviceA.readProjectionCalls.length;
			await yieldMicrotask();
			// I1: plain open cleared bindings — no projected rehydrate for A.
			expect(serviceA.readProjectionCalls.length).toBe(readsA);
			expect(store.getState().conversation?.threadId).toBe("thread-B");
		});

		it("rehydrate can never call serviceA with refB", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-1" }),
				olderCursor: null,
			};
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-1");
			// Queue a rehydrate for ref-1.
			store.getState().applyNotification(resyncFrame());
			// Before the scheduler fires, openProjected with a different service+ref.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-2" }),
				olderCursor: null,
			};
			await store.getState().openProjected(serviceB, "ref-2");
			const readsA = serviceA.readProjectionCalls.length;
			await yieldMicrotask();
			// I1: the queued rehydrate (captured with serviceA+ref-1) must NOT
			// have called serviceA.readProjection after the switch.
			expect(serviceA.readProjectionCalls.length).toBe(readsA);
		});
	});

	// A refused mutation requests the one coalesced reread; the error never waits
	// on it, and a newer mutation's own outcome is not disturbed by it.
	describe("a refused mutation and the reread scheduler", () => {
		it("surfaces the error while an earlier reread is still held; the refusal's reread runs after it", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			const readsBefore = service.readProjectionCalls.length;
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			service.sendShouldReject = refusal();
			await store.getState().send(service, textInput("x"));
			// The send settled and the error is visible with the first read still held.
			expect(store.getState().error).not.toBeNull();
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
			await ctrl.ready(1);
			ctrl.release();
			await ctrl.completed(1);
			await ctrl.ready(2);
			ctrl.release();
			await ctrl.completed(2);
			await yieldMicrotask();
			expect(service.readProjectionCalls.length).toBe(readsBefore + 2);
		});

		it("a newer mutation's outcome is not disturbed by the older refusal's reread", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			service.sendShouldReject = refusal();
			const ctrl = makeControlledRead(service);
			await store.getState().send(service, textInput("first"));
			await ctrl.started(1);
			// The refusal's reread is in flight; a second mutation succeeds meanwhile.
			service.sendShouldReject = null;
			await store.getState().send(service, textInput("second"));
			expect(store.getState().error).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			await ctrl.ready(1);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			expect(store.getState().error).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().conversation?.threadId).toBe("thread-1");
		});
	});

	describe("DrainScheduler: store-level invariants", () => {
		it("coalesces a synchronous pre-effect burst to one rehydrate", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), null);
			await store.getState().openProjected(service, "ref-1");
			const ctrl = makeControlledRead(service);
			const initialReads = service.readProjectionCalls.length;
			// Synchronous burst — 3 resync notifications coalesce to one rehydrate.
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());
			// M1: deterministic — await started, yield so orig resolves and the
			// release gate is pushed, set up completed, release.
			await ctrl.started();
			await yieldMicrotask();
			const completedP = ctrl.completed();
			ctrl.release();
			await completedP;
			expect(service.readProjectionCalls.length).toBe(initialReads + 1);
		});

		it("retains a request during an in-flight rehydrate and drains it", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), null);
			await store.getState().openProjected(service, "ref-1");
			// Hang every readProjection so we can control when each completes.
			const ctrl = makeControlledRead(service);
			const readsAfterOpen = service.readProjectionCalls.length;
			// Trigger a rehydrate (hangs on first readProjection).
			store.getState().applyNotification(resyncFrame());
			// M1: deterministic — await started so we know the rehydrate is in-flight.
			await ctrl.started();
			await yieldMicrotask(); // let orig resolve and push to releaseQueue
			// While in-flight, trigger another resync (trailing).
			store.getState().applyNotification(resyncFrame());
			// Release the first — the trailing must drain.
			const started2P = ctrl.started(2);
			const completed1P = ctrl.completed();
			ctrl.release();
			await started2P;
			await yieldMicrotask(); // let trailing orig resolve and push to releaseQueue
			const completed2P = ctrl.completed(2);
			ctrl.release();
			await completed1P;
			await completed2P;
			// One initial openProjected + one first rehydrate + one trailing = 3.
			expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 2);
		});

		it("scheduler drains all trailing work recursively (no test-only flush)", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), null);
			await store.getState().openProjected(service, "ref-1");
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			// M1: deterministic — await started so we know the rehydrate is in-flight.
			await ctrl.started();
			await yieldMicrotask(); // let orig resolve and push to releaseQueue
			// The rehydrate is hanging. Queue a trailing request while in-flight.
			// The trailing is retained and must drain after the first completes.
			const readsBeforeTrailing = service.readProjectionCalls.length;
			store.getState().applyNotification(resyncFrame());
			// Release the first — the trailing must drain recursively.
			const started2P = ctrl.started(2);
			const completed1P = ctrl.completed();
			ctrl.release();
			await started2P;
			await yieldMicrotask(); // let trailing orig resolve and push to releaseQueue
			const completed2P = ctrl.completed(2);
			ctrl.release();
			await completed1P;
			await completed2P;
			expect(service.readProjectionCalls.length).toBeGreaterThan(readsBeforeTrailing);
		});

		it("catches rehydrate errors without unhandled rejections and remains usable", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), null);
			await store.getState().openProjected(service, "ref-1");
			// Make readProjection throw on the first rehydrate call.
			const origRead = service.readProjection.bind(service);
			let throwOnce = true;
			service.readProjection = async (ref: string) => {
				if (throwOnce) {
					throwOnce = false;
					throw new Error("rehydrate boom");
				}
				return origRead(ref);
			};
			store.getState().applyNotification(resyncFrame());
			// M1: let the erroring effect run and bail. The error is caught by the
			// scheduler — yield so the microtask fires and the effect errors out.
			await yieldMicrotask();
			await yieldMicrotask();
			await yieldMicrotask();
			// The scheduler must remain usable — retrigger.
			const readsBefore = service.readProjectionCalls.length;
			store.getState().applyNotification(resyncFrame());
			// The second rehydrate's readProjection passes through (throwOnce=false).
			// Use controlled read to deterministically await completion.
			const ctrl = makeControlledRead(service);
			await ctrl.started();
			await yieldMicrotask();
			await yieldMicrotask(); // extra hop for throwOnce→origRead async chain
			const completedP = ctrl.completed();
			ctrl.release();
			await completedP;
			expect(service.readProjectionCalls.length).toBeGreaterThan(readsBefore);
		});
	});

	describe("which live frames owe one authoritative reread", () => {
		async function openProjectedFake() {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), null);
			await store.getState().openProjected(service, "ref-1");
			return service;
		}
		// A frame that merges an item into running turn t1 and sets the turn's
		// status: the reducer places it, so the store's own gap rule stays quiet.
		const turnFrame = (status: "inProgress" | "completed", threadId = "thread-1", ref = "ref-1") =>
			({
				method: "history/updated",
				params: {
					threadId,
					ref,
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 1 },
					turns: [{ id: "t1", itemsView: "default", status }],
					items: [{ type: "userMessage", id: "u2", text: "second", turnId: "t1" }],
				},
			}) as AnyNotification;

		it.each([
			[
				"a job finishing",
				{
					method: "evener/job/finished",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						job: { jobId: "job-1", jobType: "shell", status: "completed", outputBytes: 0 },
					},
				} as AnyNotification,
			],
			["a turn that is still running", turnFrame("inProgress")],
			["a settled turn on another thread", turnFrame("completed", "thread-other", "ref-other")],
		])("%s does not request a reread", async (_label, frame) => {
			const { service } = await openRunningTurn([], {}, true);
			const reads = service.readProjectionCalls.length;
			service.notificationHandler?.(frame);
			await yieldMicrotask();
			expect(service.readProjectionCalls.length).toBe(reads);
		});

		it("a jobs tree change requests a reread", async () => {
			const service = await openProjectedFake();
			const reads = service.readProjectionCalls.length;
			service.notificationHandler?.({
				method: "evener/jobs/treeUpdated",
				params: { threadId: "thread-1", ref: "ref-1" },
			} as AnyNotification);
			await yieldMicrotask();
			expect(service.readProjectionCalls.length).toBeGreaterThan(reads);
		});

		// The settling frame carries the turn's own usage, never the session
		// aggregate, so the canonical read is the only source of the totals.
		it("a running turn settling requests a reread", async () => {
			const { service } = await openRunningTurn([], {}, true);
			const reads = service.readProjectionCalls.length;
			service.notificationHandler?.(turnFrame("completed"));
			await yieldMicrotask();
			expect(service.readProjectionCalls.length).toBeGreaterThan(reads);
		});
	});

	// --- Residual: exact binding tuple, heterogeneous scheduler, no test API ---

	describe("Residual 1: same-epoch-like rebind with different objects suppresses stale queued effect", () => {
		it("rehydrate with a different service increments epoch — queued A effect suppressed", async () => {
			// Open A, queue a rehydrate via notification, then call rehydrate
			// directly with a different service object (same ref+gen). The
			// epoch increment suppresses the queued A effect — serviceA
			// is never called after the rebind.
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-1" }),
				olderCursor: null,
			};
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-1");

			// Queue a rehydrate for A via resync — scheduler captures binding
			// (serviceA+epoch).
			store.getState().applyNotification(resyncFrame());

			// Call rehydrate with a DIFFERENT service object (same ref+gen).
			// This increments bindingEpoch before assignment, so the queued A
			// effect is suppressed.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-1" }),
				olderCursor: null,
			};
			await store.getState().rehydrate(serviceB);

			const readsA = serviceA.readProjectionCalls.length;
			// Drain microtasks — the queued A effect must be suppressed.
			await yieldMicrotask();
			// I1: zero serviceA reads after the rebind.
			expect(serviceA.readProjectionCalls.length).toBe(readsA);
			// B's rehydrate should have run.
			expect(serviceB.readProjectionCalls.length).toBe(1);
		});

		it("RequestBinding is not exported from the module", () => {
			// RequestBinding is internal — it must not be importable.
			// We verify by checking that the store state does not expose it.
			const store = createConversationStore();
			const s = store.getState();
			expect(typeof (s as unknown as Record<string, unknown>).RequestBinding).toBe("undefined");
			expect(typeof (s as unknown as Record<string, unknown>).captureBinding).toBe("undefined");
		});
	});

	describe("Residual 4: no test-only mutable API on the production store", () => {
		it("store state does not expose flushScheduler", () => {
			const store = createConversationStore();
			const s = store.getState();
			expect(typeof (s as unknown as Record<string, unknown>).flushScheduler).toBe("undefined");
		});

		it("LiveConversationState type does not include flushScheduler", () => {
			// Type-level check: the interface must not declare flushScheduler.
			// We verify at runtime that the method is absent (cast to unknown
			// record since TS correctly rejects the property access).
			const store = createConversationStore();
			expect((store.getState() as unknown as Record<string, unknown>).flushScheduler).toBeUndefined();
		});
	});

	describe("same-key reread coalescing through public notifications", () => {
		it("multiple same-key reread requests during a blocker produce exactly one trailing read", async () => {
			const { service, store } = await openedProjectedStore(makeConversation({ threadId: "thread-1" }), null);
			const readCtrl = makeControlledRead(service);

			const readsAfterOpen = service.readProjectionCalls.length;

			// Emit multiple resync notifications (all same key "ref-1") while a
			// blocker is in flight. They must coalesce to exactly one trailing
			// read — not N reads.
			// First, start the initial rehydrate (held).
			store.getState().applyNotification(resyncFrame());
			await readCtrl.started(1); // 1st controlled read (openProjected was before)
			await yieldMicrotask(); // let the wrapper reach the release gate

			// While the first is held, emit 3 more same-key resync notifications.
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());

			// Release the first — the trailing coalesced read drains.
			readCtrl.release();
			await readCtrl.completed(1);
			await yieldMicrotask(); // let scheduler drain to trailing read

			// Exactly one trailing read started (coalesced from 3 same-key signals).
			await readCtrl.started(2);
			await yieldMicrotask(); // let the wrapper reach the release gate
			readCtrl.release();
			await readCtrl.completed(2);

			// Total reads: 1 (openProjected) + 1 (first rehydrate) + 1 (trailing) = 3.
			expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 2);
		});
	});

	// --- the deferred error-path drain ----------------------------------------
	//
	// The first effect (reread) exposes a started barrier, remains in flight,
	// then deterministically errors after release. While in flight, queue
	// same-key work (coalesces) and distinct heterogeneous work (cap refresh).
	// Prove: first error is caught (no unhandled rejection), every queued
	// effect/outcome drains exactly once, completion promises/state settle,
	// scheduler remains usable. No fixed yieldMicrotask counts — only
	// level-triggered started/completed barriers and a watchdog for RED.

	// Controlled error-read: patches readProjection so the first call hangs
	// until released (with rejection), and subsequent calls hang until
	// released (with normal resolution). Level-triggered started/completed.
	function makeControlledErrorRead(service: FakeConversationService): {
		started: (target?: number) => Promise<void>;
		releaseFirst: () => void;
		release: () => void;
		completed: (target?: number) => Promise<void>;
		getStartedCount: () => number;
		getDoneCount: () => number;
	} {
		let startedCount = 0;
		let doneCount = 0;
		let firstCallDone = false;
		const startedWaiters: Array<{ target: number; resolve: () => void }> = [];
		const doneWaiters: Array<{ target: number; resolve: () => void }> = [];
		// One release gate per call. First call rejects; subsequent resolve.
		const releaseQueue: Array<{
			resolve: () => void;
			reject: (e: Error) => void;
		}> = [];
		const orig = service.readProjection.bind(service);
		service.readProjection = async (ref: string) => {
			startedCount += 1;
			for (let i = startedWaiters.length - 1; i >= 0; i--) {
				const w = startedWaiters[i];
				if (w !== undefined && startedCount >= w.target) {
					w.resolve();
					startedWaiters.splice(i, 1);
				}
			}
			if (!firstCallDone) {
				// First call: hang until released, then reject.
				firstCallDone = true;
				await new Promise<void>((_resolve, reject) => {
					releaseQueue.push({ resolve: () => {}, reject });
				});
				throw new Error("first reread error");
			}
			// Subsequent calls: hang until released, then resolve normally.
			await new Promise<void>((resolve) => {
				releaseQueue.push({ resolve, reject: () => resolve() });
			});
			const result = await orig(ref);
			doneCount += 1;
			for (let i = doneWaiters.length - 1; i >= 0; i--) {
				const w = doneWaiters[i];
				if (w !== undefined && doneCount >= w.target) {
					w.resolve();
					doneWaiters.splice(i, 1);
				}
			}
			return result;
		};
		// Patch the doneCount increment for the first (error) call too, so
		// completed() can track it. We do this by wrapping the rejection path.
		// Actually, doneCount is NOT incremented for the first call (it throws).
		// So completed(1) would hang. We need to increment doneCount when the
		// first call's error is caught by the scheduler. The scheduler's
		// runOne .catch() swallows the error, then .then() resolves waiters and
		// drains. So the first call's "done" is when the scheduler catches the
		// error. We can't observe that from inside the patched method.
		//
		// Solution: track the first call's completion separately. The first
		// call rejects, but the scheduler catches it. The scheduler's .then()
		// runs after the catch. We increment doneCount in the subsequent calls.
		// For the first call, we need a separate signal. Since the first call
		// throws, the patched method never reaches the doneCount increment.
		// But the scheduler's runOne .catch().then() will fire, which means the
		// drain continues. The trailing reread (2nd call) will start — that's
		// our signal that the first call's error was caught.
		//
		// So completed(1) is NOT useful for the first (error) call. We use
		// started(2) as the signal that the first error was caught and the
		// drain continued.
		return {
			started: (target = 1) => {
				if (startedCount >= target) return Promise.resolve();
				return new Promise<void>((resolve) => {
					startedWaiters.push({ target, resolve });
				});
			},
			releaseFirst: () => {
				const r = releaseQueue.shift();
				if (r !== undefined) r.reject(new Error("first reread error"));
			},
			release: () => {
				const r = releaseQueue.shift();
				if (r !== undefined) r.resolve();
			},
			completed: (target = 1) => {
				if (doneCount >= target) return Promise.resolve();
				return new Promise<void>((resolve) => {
					doneWaiters.push({ target, resolve });
				});
			},
			getStartedCount: () => startedCount,
			getDoneCount: () => doneCount,
		};
	}

	describe("a reread that fails does not block the next reread", () => {
		it("the second resync rereads and commits after the first read threw", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			const orig = service.readProjection.bind(service);
			let failNext = true;
			service.readProjection = async (ref: string) => {
				if (!failNext) return orig(ref);
				failNext = false;
				service.readProjectionCalls.push({ ref });
				throw new Error("read failed");
			};
			const readsBefore = service.readProjectionCalls.length;
			const resync = {
				method: "evener/thread/resync",
				params: { threadId: "thread-1", ref: "ref-1" },
			} as AnyNotification;
			store.getState().applyNotification(resync);
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().error).toBe("read failed");
			service.readProjectionResult = makeReadProjectionResult(makeThread({ name: "after the failed read" }));
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resync);
			await ctrl.ready(1);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			expect(service.readProjectionCalls.length).toBe(readsBefore + 2);
			expect(store.getState().conversation?.name).toBe("after the failed read");
			expect(store.getState().error).toBeNull();
		});
	});

	describe("rehydrate commit-time ownership — page/mutation/error owner capture", () => {
		it("R pending→L success: R cannot delete page items or regress cursor after L owns page", async () => {
			// Rehydrate (R) is pending. A loadOlder (L) succeeds during the await,
			// prepending older items and advancing the cursor. When R completes, it
			// must NOT replace the conversation (deleting L's items) or regress the
			// cursor — L's page ownership is newer.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [{ kind: "user", id: "u1", text: "hi" } as never] as never,
						}),
					],
				}),
			);
			await store.getState().openProjected(service, "ref-1");
			// Set up an older cursor so loadOlder can run.
			store.setState({ olderCursor: "cursor-1" });
			// Start a rehydrate (R) that hangs.
			const ctrl = makeControlledRead(service);
			// Change the projection result so R would regress the cursor and
			// replace items.
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			service.readProjectionResult.olderCursor = null;
			store.getState().applyNotification(resyncFrame());
			await yieldMicrotask();
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, a loadOlder (L) succeeds — prepends items,
			// advances cursor.
			service.olderItems = {
				items: [{ kind: "user", id: "old-page-item", text: "older" }],
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);
			const itemsAfterL = store.getState().conversation?.items ?? [];
			const cursorAfterL = store.getState().olderCursor;
			expect(itemsAfterL.some((i) => i.id === "old-page-item")).toBe(true);
			expect(cursorAfterL).toBe("cursor-2");
			// Now release R — it must NOT replace the conversation or regress cursor.
			ctrl.release();
			await ctrl.completed(1);
			// R's stale domain write (replacing conversation) is discarded — L's
			// page items survive, cursor is NOT regressed.
			const itemsAfterR = store.getState().conversation?.items ?? [];
			expect(itemsAfterR.some((i) => i.id === "old-page-item")).toBe(true);
			expect(store.getState().olderCursor).toBe(cursorAfterL);
		});

		it("R→L failure: R success preserves page error set by L failure", async () => {
			// Rehydrate (R) is pending. A loadOlder (L) fails during the await,
			// setting a page error. When R succeeds, it must NOT clear that error.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			// Start R (hanging).
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await yieldMicrotask();
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
			// Release R — it must preserve the page error.
			ctrl.release();
			await ctrl.completed(1);
			expect(store.getState().error).toBe(errorAfterL);
		});

		it("R→new mutation failure: R failure cannot overwrite mutation error", async () => {
			// Rehydrate (R) is pending. A mutation fails during the await, setting
			// a mutation error. When R also fails, R's failure must NOT overwrite
			// the mutation's error.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					evener: {
						ref: "ref-1",
						capabilities: { ...ALL_TRUE_CAPS },
						queue: { revision: 0 },
					},
				}),
			);
			await store.getState().openProjected(service, "ref-1");
			// Start R (hanging).
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, a send fails — sets mutation error.
			service.sendShouldReject = new Error("mutation boom");
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBe("mutation boom");
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
			// Now make R fail — patch readProjection to reject.
			service.readProjection = async () => {
				throw new Error("rehydrate boom");
			};
			ctrl.release();
			await ctrl.completed(1).catch(() => {});
			// R's failure must NOT overwrite the mutation error.
			expect(store.getState().error).toBe("mutation boom");
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
		});
	});

	describe("generation/identity-stale loadOlder performs ZERO set calls", () => {
		it("does not rehydrate a stale cursor through the replacement binding", async () => {
			const serviceA = new FakeConversationService();
			const serviceB = new FakeConversationService();
			const store = createConversationStore();
			serviceA.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-A" }));
			serviceB.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-B" }));
			await store.getState().openProjected(serviceA, "ref-A");
			store.setState({ olderCursor: "cursor-A" });
			const readsBeforeLoad = serviceA.readProjectionCalls.length;

			let rejectA!: (error: Error) => void;
			serviceA.olderItems = new Promise<never>((_resolve, reject) => {
				rejectA = reject;
			}) as never;
			const loadA = store.getState().loadOlder(serviceA);
			await store.getState().openProjected(serviceB, "ref-B");

			rejectA(
				new WireError("stale transcript cursor", -32020, {
					evenerErrorInfo: "transcriptItemCursorStale",
				}),
			);
			await expect(loadA).resolves.toEqual({ status: "ignored" });

			expect(serviceA.readProjectionCalls).toHaveLength(readsBeforeLoad);
			expect(store.getState().conversation?.threadId).toBe("thread-B");
			expect(store.getState().ref).toBe("ref-B");
		});

		it("barrier: A pending, open/reset B, start B page, resolve A → subscriber sees zero stale writes", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-A" }));
			await store.getState().openProjected(service, "ref-A");
			store.setState({ olderCursor: "cursor-A" });
			// Start loadOlder A (hangs).
			let resolveA: (() => void) | null = null as (() => void) | null;
			const hangA = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((r) => {
				resolveA = () =>
					r({
						items: [{ kind: "user", id: "stale-A-item", text: "stale" }],
						nextCursor: "stale-cursor-A",
					});
			});
			service.olderItems = hangA as never;
			// Track all set() calls via subscriber.
			const setCalls: string[] = [];
			let snapshotConversation = store.getState().conversation;
			let snapshotLoadingOlder = store.getState().loadingOlder;
			let snapshotError = store.getState().error;
			let snapshotOlderCursor = store.getState().olderCursor;
			store.subscribe((s) => {
				if (s.conversation !== snapshotConversation) setCalls.push("conversation");
				if (s.loadingOlder !== snapshotLoadingOlder) setCalls.push("loadingOlder");
				if (s.error !== snapshotError) setCalls.push("error");
				if (s.olderCursor !== snapshotOlderCursor) setCalls.push("olderCursor");
				snapshotConversation = s.conversation;
				snapshotLoadingOlder = s.loadingOlder;
				snapshotError = s.error;
				snapshotOlderCursor = s.olderCursor;
			});
			const aP = store.getState().loadOlder(service);
			// While A is in-flight, open B (new conversation generation).
			service.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-B" }));
			await store.getState().openProjected(service, "ref-B");
			store.setState({ olderCursor: "cursor-B" });
			// Start and complete a B page operation.
			service.olderItems = {
				items: [{ kind: "user", id: "B-item", text: "B page" }],
				nextCursor: "cursor-B2",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.threadId).toBe("thread-B");
			// Clear tracked calls — from this point, only A's stale resolution
			// should produce writes. A must produce ZERO.
			setCalls.length = 0;
			snapshotConversation = store.getState().conversation;
			snapshotLoadingOlder = store.getState().loadingOlder;
			snapshotError = store.getState().error;
			snapshotOlderCursor = store.getState().olderCursor;
			// Now resolve the stale A — it must perform ZERO set calls.
			(resolveA as () => void)();
			await aP;
			// The subscriber must NOT have seen any stale writes from A.
			expect(setCalls.length).toBe(0);
			// B's state is intact.
			expect(store.getState().conversation?.threadId).toBe("thread-B");
			expect(store.getState().loadingOlder).toBe(false);
		});

		it("stale loadOlder failure after reset performs ZERO set calls", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			// Start loadOlder that hangs, then reset.
			let rejectA: ((e: Error) => void) | null = null as ((e: Error) => void) | null;
			const hangA = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((_r, reject) => {
				rejectA = reject;
			});
			service.olderItems = hangA as never;
			const aP = store.getState().loadOlder(service);
			store.getState().reset();
			// Track set calls AFTER reset — only A's stale resolution should be tracked.
			const setCalls: string[] = [];
			let snapLoading = store.getState().loadingOlder;
			let snapError = store.getState().error;
			let snapConv = store.getState().conversation;
			store.subscribe((s) => {
				if (s.loadingOlder !== snapLoading) setCalls.push("loadingOlder");
				if (s.error !== snapError) setCalls.push("error");
				if (s.conversation !== snapConv) setCalls.push("conversation");
				snapLoading = s.loadingOlder;
				snapError = s.error;
				snapConv = s.conversation;
			});
			// Resolve stale A with failure — must perform ZERO set calls.
			(rejectA as (e: Error) => void)(new Error("stale A failure"));
			await aP.catch(() => {});
			expect(setCalls.length).toBe(0);
			expect(store.getState().conversation).toBeNull();
			expect(store.getState().error).toBeNull();
			expect(store.getState().loadingOlder).toBe(false);
		});
	});

	describe("mutation starts atomically clear prior error", () => {
		it("send clears prior error atomically with new pending mutation", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation({
				capabilities: { ...ALL_TRUE_CAPS },
			});
			await store.getState().open(service, "ref-1");
			// Set an error via a failed send.
			service.sendShouldReject = new Error("first failure");
			await store.getState().send(service, textInput("first"));
			expect(store.getState().error).not.toBeNull();
			// Start a second send — it must atomically clear the error.
			service.sendShouldReject = null;
			let resolveSend: (() => void) | null = null as (() => void) | null;
			const hangSend = new Promise<MutationReceipt>((r) => {
				resolveSend = () => r(makeReceipt());
			});
			service.send = async () => hangSend;
			store.getState().send(service, textInput("second"));
			// Error must be cleared immediately (atomically with pending mutation).
			expect(store.getState().error).toBeNull();
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			(resolveSend as () => void)();
		});

		it("old failed mutation cannot republish error after newer mutation starts", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation({
				capabilities: { ...ALL_TRUE_CAPS },
			});
			await store.getState().open(service, "ref-1");
			// First send hangs then fails.
			let rejectFirst: ((e: Error) => void) | null = null as ((e: Error) => void) | null;
			const hangFirst = new Promise<MutationReceipt>((_r, reject) => {
				rejectFirst = reject;
			});
			service.sendShouldReject = null;
			service.send = async () => hangFirst;
			const send1P = store.getState().send(service, textInput("first"));
			// While first is in-flight, start a second send that succeeds.
			store.getState().setDraft("second");
			service.sendShouldReject = null;
			service.send = async () => makeReceipt();
			await store.getState().send(service, textInput("second"));
			expect(store.getState().error).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			// Now the first send fails — it must NOT republish error.
			(rejectFirst as (e: Error) => void)(new Error("first failed"));
			await send1P;
			expect(store.getState().error).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
		});
	});

	describe("explicit draft revision — type-then-delete is an edit", () => {
		it("failure restores draft snapshot only if user has not edited since clear", async () => {
			const service = new FakeConversationService();
			service.sendShouldReject = new Error("send failed");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("original draft");
			await store.getState().send(service, textInput("original draft"));
			expect(store.getState().draft).toBe("original draft");
		});

		it("type-then-delete after clear counts as edit — failure does NOT restore snapshot", async () => {
			const { service, store } = await openedStore();
			store.getState().setDraft("my draft text");
			let rejectSend: ((e: Error) => void) | null = null as ((e: Error) => void) | null;
			const hangSend = new Promise<MutationReceipt>((_r, reject) => {
				rejectSend = reject;
			});
			service.sendShouldReject = null;
			service.send = async () => hangSend;
			const sendP = store.getState().send(service, textInput("my draft text"));
			expect(store.getState().draft).toBe("");
			store.getState().setDraft("typed then deleted");
			store.getState().setDraft("");
			(rejectSend as (e: Error) => void)(new Error("send failed"));
			await sendP;
			expect(store.getState().draft).toBe("");
			expect(store.getState().error).not.toBeNull();
		});

		it("out-of-order mutation failure cannot alter newer mutation draft", async () => {
			const { service, store } = await openedStore();
			store.getState().setDraft("first");
			let rejectFirst: ((e: Error) => void) | null = null as ((e: Error) => void) | null;
			const hangFirst = new Promise<MutationReceipt>((_r, reject) => {
				rejectFirst = reject;
			});
			service.sendShouldReject = null;
			service.send = async () => hangFirst;
			const send1P = store.getState().send(service, textInput("first"));
			store.getState().setDraft("second");
			service.sendShouldReject = null;
			service.send = async () => makeReceipt();
			await store.getState().send(service, textInput("second"));
			expect(store.getState().draft).toBe("");
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			(rejectFirst as (e: Error) => void)(new Error("first failed"));
			await send1P;
			expect(store.getState().draft).toBe("");
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
		});
	});

	// The ask_user lifecycle, projected from the model the frames fold into:
	// the projector has the whole turn, so a settled ask becomes its question
	// row once the wire's own askPending says so, and a later user message
	// settles it — no reread in either direction. #1731 (piece A) round 4 made
	// ThreadModel.askPending the single source for "is anything pending": the
	// item scan that finds WHICH questions still needs the wire's flag to say
	// ANYTHING is pending at all before it looks.
	describe("the question lifecycle through the projection", () => {
		it("shows a completed parseable ask_user as a question row once askPending says so, with no reread", async () => {
			const { store, service } = await openRunningTurn([], {}, true);
			expect(store.getState().conversation?.askPending).toBe(false);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...askUserItem("ask-1", VALID_ASK_ARGS), turnId: "t1" }],
				}),
			);
			// The item alone is not the whole signal: the round that posts an
			// ask_user call ends its turn on the same boundary a status change
			// announces (session_lifecycle.go's askedThisRound), so the two frames
			// land together in production. Folding this one is still no reread.
			store.getState().applyNotification(statusChanged({ type: "active" }, { askPending: true }));
			await yieldMicrotask();
			expect(store.getState().conversation?.askPending).toBe(true);
			const conv = store.getState().conversation;
			const questionItem = conv?.items.find((i) => i.kind === "question");
			expect(questionItem).toBeDefined();
			if (questionItem?.kind === "question") {
				expect(questionItem.questions).toHaveLength(1);
				expect(questionItem.questions[0]?.question).toBe("Pick one");
				expect(questionItem.questions[0]?.options).toHaveLength(2);
			}
			expect(service.readProjectionCalls.length).toBe(initialReads);
		});

		// The wire's askPending is the hub's own thread-level signal and stays
		// exactly as the snapshot set it (the reducer's invariant), and it is not what
		// the phone answers from: the question ROWS are, and the projection builds one
		// only for an ask the package says is answerable now. Nothing derived is
		// written back into the model, so nothing latches.
		it("renders no question row for an ask the hub knows about but this window does not hold", async () => {
			// The ask's item is outside the loaded window: no question row can be
			// projected for it, and the wire flag is the only evidence there is.
			const store = await openProjectedThread(
				makeThread({
					evener: evenerWith({ askPending: true }),
					turns: [makeTurn({ id: "t1", items: [userMessageItem("u1", "hi")] })],
				}),
			);
			expect(rows(store).some((row) => row.kind === "question")).toBe(false);
			expect(store.getState().conversation?.askPending).toBe(true);
		});

		it("drops the question row once the answering message arrives, whatever the last snapshot said", async () => {
			const store = await openProjectedThread(
				makeThread({
					evener: evenerWith({ askPending: false, activeTurnId: "t2" }),
					turns: [
						makeTurn({ id: "t1", items: [askUserItem("ask-1", VALID_ASK_ARGS)] }),
						makeTurn({ id: "t2", items: [], status: "inProgress" }),
					],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("answer-1", "I choose A"), turnId: "t2" }],
				}),
			);
			// And it stays false as later frames fold: the flag is re-derived every
			// publish, never carried forward from the model the projection wrote.
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("answer-1", "I choose A"), turnId: "t2" }],
				}),
			);
			// The wire's own flag is untouched by any of it.
			expect(store.getState().conversation?.askPending).toBe(false);
		});

		// The hub's own clear, end to end (#1629): the snapshot said a question was
		// waiting, the user answers, and the status frame that follows carries
		// askPending: false — always stamped now, so false is a real clear rather
		// than "no update". Both halves of the derivation go quiet: the answering
		// user message takes the ask out of the live-ask window, and the frame takes
		// the wire flag down. No reread, no heuristic.
		it("drops the question row after the answer and the hub's own clear", async () => {
			const store = await openProjectedThread(
				makeThread({
					evener: evenerWith({ askPending: true, activeTurnId: "t1" }),
					turns: [makeTurn({ id: "t1", items: [askUserItem("ask-1", VALID_ASK_ARGS)], status: "inProgress" })],
				}),
			);

			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("answer-1", "I choose A"), turnId: "t1" }],
				}),
			);
			store.getState().applyNotification(statusChanged({ type: "active" }, { askPending: false }));

			expect(store.getState().conversation?.askPending).toBe(false);
			expect(rows(store).some((row) => row.kind === "question")).toBe(false);
		});

		// The reverse of the case above: an item completing is not the whole
		// signal either. Before any status frame carries askPending, the item
		// scan must not render a question row on its own — the class of bug
		// #1731 round 4 closed (a client re-deriving "is anything pending" from
		// transcript shape instead of the wire's own fact).
		it("renders no question row for a live ask until a status frame carries askPending", async () => {
			const { store } = await openRunningTurn([], {}, true);
			expect(store.getState().conversation?.askPending).toBe(false);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...askUserItem("ask-live", VALID_ASK_ARGS), turnId: "t1" }],
				}),
			);
			expect(rowById(store, "ask-live")).toMatchObject({ kind: "activity", family: "tool" });
			expect(store.getState().conversation?.askPending).toBe(false);

			// The status frame that follows (the same boundary that ends the round
			// which posted the ask, in production) carries the flag — no reread,
			// and the item the model already holds becomes its question row.
			store.getState().applyNotification(statusChanged({ type: "active" }, { askPending: true }));
			expect(rowById(store, "ask-live")).toMatchObject({ kind: "question" });
			expect(store.getState().conversation?.askPending).toBe(true);
		});

		it("settles the question when the answering user message arrives, with no reread", async () => {
			const askThread = makeThread({
				evener: evenerWith({ askPending: true, activeTurnId: "t2" }),
				turns: [
					makeTurn({
						id: "t1",
						items: [askUserItem("ask-1", VALID_ASK_ARGS)],
						status: "completed",
					}),
					makeTurn({ id: "t2", items: [], status: "inProgress" }),
				],
			});
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(askThread, ALL_TRUE_CAPS, true);
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().conversation?.askPending).toBe(true);
			expect(rows(store).some((item) => item.kind === "question")).toBe(true);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("answer-1", "I choose A"), turnId: "t2" }],
				}),
			);
			await yieldMicrotask();
			// The answered call is no longer answerable, so its question row is
			// gone and nothing is offered to answer.
			expect(rows(store).some((item) => item.kind === "question")).toBe(false);
			expect(rowById(store, "answer-1")).toMatchObject({ kind: "user" });
			expect(service.readProjectionCalls.length).toBe(initialReads);
		});

		it.each([
			["malformed", () => askUserItem("ask-bad", "not valid json {{{")],
			["still in flight", () => askUserItem("ask-incomplete", VALID_ASK_ARGS, "inProgress")],
		] as const)("projects a %s ask_user as its tool row, never a question row", async (_label, item) => {
			const { store, service } = await openRunningTurn([], {}, true);
			const initialReads = service.readProjectionCalls.length;
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...item(), turnId: "t1" }],
				}),
			);
			await yieldMicrotask();
			expect(rows(store).some((row) => row.kind === "question")).toBe(false);
			expect(rowById(store, item().id)).toMatchObject({ kind: "activity", family: "tool" });
			expect(store.getState().conversation?.askPending).toBe(false);
			expect(service.readProjectionCalls.length).toBe(initialReads);
		});
	});

	// --- Residual: R1 — monotonic mutation-owner and error-owner revisions ---

	describe("monotonic mutation-owner revision — ABA safe", () => {
		it("rehydrate success cannot clear error after mutation cleared+re-set same error (ABA)", async () => {
			// The error owner revision must increment even when the error string
			// is cleared then re-set to the same value. A stale rehydrate that
			// captured the old error revision must NOT clear the new error.
			// Real ABA: mutation1 fails (error="boom"), mutation2 starts (error
			// cleared via set), mutation2 fails (error="boom" again). The
			// error-owner revision incremented on each transition.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			// Start a rehydrate (R) that hangs.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, mutation1 fails — sets error "boom".
			service.sendShouldReject = new Error("boom");
			await store.getState().send(service, textInput("first"));
			expect(store.getState().error).toBe("boom");
			// Mutation2 starts — clears error via set (errorOwnerRev increments).
			// Then mutation2 also fails with the SAME error string — ABA.
			service.sendShouldReject = new Error("boom");
			await store.getState().send(service, textInput("second"));
			expect(store.getState().error).toBe("boom");
			// Release R — it must NOT clear the error (the error-owner revision
			// changed during the await, even though the string is the same).
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			// Clean up any trailing reread from the mutation owner change.
			ctrl.release();
			await yieldMicrotask();
			expect(store.getState().error).toBe("boom");
		});

		it("rehydrate success cannot publish projection after mutation ABA (same mutationId)", async () => {
			// The mutation-owner revision must increment when a mutation is set
			// then cleared then a new mutation set (even if the same marker
			// shape reappears). A stale rehydrate must not publish a predating
			// projection.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [{ kind: "user", id: "u1", text: "initial" } as never] as never,
						}),
					],
				}),
			);
			await store.getState().openProjected(service, "ref-1");
			// Start a rehydrate (R) that hangs — it captured the initial mutation
			// owner revision (no mutation pending → revision 0).
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, start a send (opens the click-to-commit
			// marker, incrementing the mutation-owner revision), then let it
			// succeed (clears the marker, incrementing the revision again).
			const sendP = store.getState().send(service, textInput("hello"));
			await sendP;
			// The marker is null again, but the mutation-owner revision
			// incremented twice (set + clear). R's captured revision (0) is stale.
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			// Change the projection result so R would publish a stale projection
			// (different items).
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [{ kind: "user", id: "stale-item", text: "stale" } as never] as never,
						}),
					],
				}),
			);
			// Release R — it must NOT replace the conversation because the
			// mutation-owner revision changed during the await.
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			// The conversation should NOT have been replaced by R's stale
			// projection. The items from the original open should remain.
			const items = store.getState().conversation?.items ?? [];
			expect(items.some((i) => i.id === "u1")).toBe(true);
			expect(items.some((i) => i.id === "stale-item")).toBe(false);
		});

		it("rehydrate failure installs rejection before controlled read captures it and proves true reject", async () => {
			// The failure path must capture the error-owner revision BEFORE the
			// read that rejects. If a newer error owner writes during the await,
			// R's failure must not overwrite it.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			// Start a rehydrate (R) that hangs — the read will reject.
			const ctrl = makeControlledErrorRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			// While R is in-flight, a mutation fails — sets a mutation error.
			service.sendShouldReject = new Error("mutation boom");
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBe("mutation boom");
			// Now release R's first call — it rejects.
			ctrl.releaseFirst();
			// R's failure must NOT overwrite the mutation error.
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().error).toBe("mutation boom");
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
			// Clean up the trailing reread if one was scheduled.
			ctrl.release();
			await yieldMicrotask();
		});

		it("if mutation owner changed during rehydrate, one bounded trailing reread is scheduled", async () => {
			// When the mutation owner changes during a rehydrate, the rehydrate
			// must not publish its projection. Instead, exactly one trailing
			// authoritative reread is scheduled through the scheduler (no loop,
			// no reentrant await). The trailing reread publishes the fresh
			// projection once the mutation has settled.
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Initial projection has no items.
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			// Start a rehydrate (R) that hangs.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await yieldMicrotask();
			const readsBeforeMutation = service.readProjectionCalls.length;
			// While R is in-flight, start a send that succeeds (mutation owner
			// revision increments: set then clear).
			const sendP = store.getState().send(service, textInput("hello"));
			await sendP;
			// Change the projection so the trailing reread returns fresh items.
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							items: [{ kind: "user", id: "fresh-item", text: "fresh" } as never] as never,
						}),
					],
				}),
			);
			// Release R — it must not publish, but must schedule one trailing reread.
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			// The trailing reread was scheduled via the scheduler. It needs to
			// start and complete. Since it goes through the same scheduler, it
			// hangs on the controlled read. Let it start and release it.
			await ctrl.started(2);
			await yieldMicrotask();
			ctrl.release();
			await ctrl.completed(2);
			await yieldMicrotask();
			// Exactly one trailing reread was scheduled (in addition to the
			// original rehydrate read).
			expect(service.readProjectionCalls.length).toBe(readsBeforeMutation + 1);
			// The trailing reread publishes the fresh projection.
			const items = store.getState().conversation?.items ?? [];
			expect(items.some((i) => i.id === "fresh-item")).toBe(true);
		});
	});
});
