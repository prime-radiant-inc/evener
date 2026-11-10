// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { AnyNotification, ThreadItem } from "@evener/appwire-client";
import { liveAskQuestions, sessionControls, TURN_RUNNING } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { MobileTimelineItem } from "../../projectedRows";
import { projectNativeTranscript } from "../../transcriptPresentation";
import type { ConversationReadProjection } from "../services/conversation";
import { createConversationStore, MAX_ITEM_BYTES, TRUNCATION_MARKER } from "./conversation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	agentMessageItem,
	askUserItem,
	commandExecItem,
	evenerWith,
	historyUpdated,
	makeConversation,
	makeReadProjectionResult,
	makeThread,
	openProjectedThreadWith,
	openRunningTurn,
	openedStore,
	readServing,
	reasoningItem,
	resyncFrame,
	rowById,
	rows,
	runningTurnThread,
	statusChanged,
	textInput,
	userMessageItem,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	describe("applyNotification", () => {
		it.each(["", "off", "provider/model"])(
			"updates the vision model %j for the matching session",
			async (visionModel) => {
				const store = createConversationStore();
				const service = new FakeConversationService();
				await store.getState().open(service, "ref-1");
				store.getState().applyNotification({
					method: "thread/vision-model/changed",
					params: { threadId: "thread-1", ref: "ref-1", visionModel },
				} as AnyNotification);
				expect(store.getState().conversation?.visionModel).toBe(visionModel);
			},
		);

		// The package reducer's routing: a frame names its thread by ref when it
		// carries one, else by threadId; a frame naming neither is not about this
		// thread.
		it("drops vision changes that name another session or thread", async () => {
			const store = createConversationStore();
			const service = new FakeConversationService();
			await store.getState().open(service, "ref-1");
			const original = store.getState().conversation;
			for (const params of [{ threadId: "thread-1", ref: "other" }, { threadId: "other" }, {}]) {
				store.getState().applyNotification({
					method: "thread/vision-model/changed",
					params: { ...params, visionModel: "off" },
				} as AnyNotification);
			}
			expect(store.getState().conversation).toBe(original);
		});

		it("routes a vision change by its ref when it carries one", async () => {
			const store = createConversationStore();
			const service = new FakeConversationService();
			await store.getState().open(service, "ref-1");
			store.getState().applyNotification({
				method: "thread/vision-model/changed",
				params: { threadId: "other", ref: "ref-1", visionModel: "off" },
			} as AnyNotification);
			expect(store.getState().conversation?.visionModel).toBe("off");
		});
		it("drops notifications that don't match current ref", async () => {
			const { store } = await openedStore();
			const originalConv = store.getState().conversation;
			store
				.getState()
				.applyNotification(statusChanged({ type: "running" }, { threadId: "thread-other", ref: "ref-other" }));
			expect(store.getState().conversation).toBe(originalConv);
		});

		it("updates status on matching thread/status/changed", async () => {
			const { store } = await openedStore();
			store.getState().applyNotification(statusChanged({ type: "running" }));
			expect(store.getState().conversation?.status.type).toBe("running");
		});

		it("updates name on matching evener/thread/name/changed", async () => {
			const { store } = await openedStore();
			store.getState().applyNotification({
				method: "evener/thread/name/changed",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					name: "New Name",
				},
			} as AnyNotification);
			expect(store.getState().conversation?.name).toBe("New Name");
		});

		it("updates queue on matching thread/queueChanged", async () => {
			const { store } = await openedStore();
			store.getState().applyNotification({
				method: "thread/queueChanged",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					queue: {
						revision: 7,
						depth: 2,
						ids: ["entry-a", "entry-b"],
						texts: ["full first", "full second"],
						clientMutationIds: ["send-a", "send-b"],
						preview: ["first", "second"],
					},
				},
			} as AnyNotification);
			expect(store.getState().conversation?.queue?.depth).toBe(2);
			expect(store.getState().conversation?.queue?.revision).toBe(7);
			expect(store.getState().conversation?.queue?.ids).toEqual(["entry-a", "entry-b"]);
			expect(store.getState().conversation?.queue?.texts).toEqual(["full first", "full second"]);
			expect(store.getState().conversation?.queue?.clientMutationIds).toEqual(["send-a", "send-b"]);
			expect(store.getState().conversation?.queue?.preview).toEqual(["first", "second"]);
		});

		it("marks running on turn/started", async () => {
			const { store } = await openedStore(makeConversation({ status: { type: "idle" } }, true));
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "default", status: "inProgress" }],
				}),
			);
			// The status is thread/status/changed's, never turn/started's (the
			// status frame rides right behind it); the turn id is this frame's.
			expect(store.getState().conversation?.status.type).toBe("idle");
			expect(store.getState().conversation?.activeTurnId).toBe("t1");
		});

		it("leaves the status to the status frame on turn/completed", async () => {
			const { store } = await openedStore(makeConversation({ status: { type: "active" } }));
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "default", status: "running" }],
				}),
			);
			expect(store.getState().conversation?.status.type).toBe("active");
			// Now complete
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "default",
							status: "completed",
							usage: { totalTokens: 100 },
						},
					],
				}),
			);
			// A completed turn is followed by its status frame (idle at session end,
			// active at an inline boundary); this frame leaves the status alone.
			expect(store.getState().conversation?.status.type).toBe("active");
		});

		// The inline turn boundary through the store: turn/completed(previous),
		// turn/started(next), status(active), one frame each, and the controls
		// the SDK derives from the store's status never blink (the web's and
		// TUI's #1330).
		it("keeps steer and stop through an inline turn boundary", async () => {
			const { store } = await openedStore(makeConversation({ status: { type: "active" }, activeTurnId: "t1" }));
			const frames: AnyNotification[] = [
				{
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 1,
						snapshot: { incarnation: "inc-1", length: 1 },
						turns: [{ id: "t1", itemsView: "", status: "completed" }],
					},
				} as AnyNotification,
				{
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 1,
						snapshot: { incarnation: "inc-1", length: 1 },
						turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
					},
				} as AnyNotification,
				{
					method: "thread/status/changed",
					params: { threadId: "thread-1", ref: "ref-1", status: { type: "active" } },
				} as AnyNotification,
			];
			for (const frame of frames) {
				store.getState().applyNotification(frame);
				const conv = store.getState().conversation;
				if (conv === null) throw new Error("conversation gone");
				const controls = sessionControls(conv.status.type, conv.capabilities, conv.queue?.depth ?? 0);
				expect({ frame: frame.method, steer: controls.steer, stop: controls.stop }).toEqual({
					frame: frame.method,
					steer: true,
					stop: true,
				});
			}
		});

		// A genuine failure ends as turn/completed{status: "failed"} followed by
		// its own thread/status/changed(idle) frame (the agent's failure exit,
		// agent/session_lifecycle.go endInputAtTurnFailure, kata hen0); the status
		// frame owns the settle, exactly as it does for a completed turn.
		it("settles idle on the status frame when the active turn fails", async () => {
			const { store } = await openedStore(makeConversation({ status: { type: "active" }, activeTurnId: "t1" }, true));
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "", status: "failed", error: { message: "rate limited" } }],
				}),
			);
			// The failed completion ends the turn; its status frame has not arrived.
			expect(store.getState().conversation?.status.type).toBe("active");
			expect(store.getState().conversation?.activeTurnId).toBeUndefined();
			store.getState().applyNotification(statusChanged({ type: "idle" }));
			expect(store.getState().conversation?.status.type).toBe("idle");
		});

		// A failure that leaves a message queued is not a rest: the daemon resumes
		// the queued message and its turn_failed input-end emission reports the
		// session active (WireState reads active while pendingQueueDepth > 0), not
		// the idle a settled turn would reach. The client must hold the session
		// active at the failed frame and at the authoritative thread/status/changed
		// frame behind it -- so Send stays closed and the queued entry is still
		// there to run -- and follow the hand-off as the queued message becomes the
		// next turn and leaves the queue.
		it("keeps active through a failed turn that resumes a queued message", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation(
				{
					status: { type: "active" },
					activeTurnId: "t1",
					queue: {
						revision: 1,
						depth: 1,
						preview: ["queued"],
						ids: ["q1"],
						texts: ["queued"],
					},
				},
				true,
			);
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");

			// t1 fails; its queued message has not started yet.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "", status: "failed", error: { message: "rate limited" } }],
				}),
			);
			const failed = store.getState().conversation;
			if (failed === null) throw new Error("conversation gone");
			expect(failed.status.type).toBe("active");
			const controls = sessionControls(failed.status.type, failed.capabilities, failed.queue?.depth ?? 0);
			expect({ send: controls.send, queue: controls.queue }).toEqual({ send: false, queue: true });
			expect(failed.queue?.depth).toBe(1);
			expect(failed.queue?.texts).toEqual(["queued"]);

			// The failure exit's own status frame is the authority. With work still
			// queued it announces active, so the session stays active and Send stays
			// closed with the entry still waiting to run.
			store.getState().applyNotification(statusChanged({ type: "active" }));
			const announced = store.getState().conversation;
			if (announced === null) throw new Error("conversation gone");
			expect(announced.status.type).toBe("active");
			expect(announced.queue?.depth).toBe(1);
			expect(announced.queue?.texts).toEqual(["queued"]);
			const afterStatus = sessionControls(announced.status.type, announced.capabilities, announced.queue?.depth ?? 0);
			expect(afterStatus.send).toBe(false);

			// The queue drains before the queued turn opens: the drain claims the
			// entry and emits thread/queueChanged (agent/session_queue.go
			// popQueueHead -> reflectDurableInputQueue), and the next iteration's
			// EventUserInput opens the turn. Assert each boundary rather than only
			// the final state, so an ordering regression is caught.
			store.getState().applyNotification({
				method: "thread/queueChanged",
				params: { threadId: "thread-1", ref: "ref-1", queue: { revision: 2, depth: 0 } },
			} as AnyNotification);
			const drained = store.getState().conversation;
			expect(drained?.status.type).toBe("active");
			expect(drained?.queue?.depth).toBe(0);
			expect(drained?.activeTurnId).toBeUndefined();

			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			const resumed = store.getState().conversation;
			expect(resumed?.status.type).toBe("active");
			expect(resumed?.activeTurnId).toBe("t2");
			expect(resumed?.queue?.depth).toBe(0);
		});

		// The status is authoritative and the turn id can be absent while the
		// session is active (a read cut between turns); the failed completion's
		// own status frame settles idle, while one for a superseded turn is left
		// alone.
		it("settles idle on a failed completion's status frame, not on a superseded turn", async () => {
			const { store } = await openedStore(makeConversation({ status: { type: "active" }, activeTurnId: undefined }));
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t-x", itemsView: "", status: "failed", error: { message: "boom" } }],
				}),
			);
			expect(store.getState().conversation?.status.type).toBe("active");
			store.getState().applyNotification(statusChanged({ type: "idle" }));
			expect(store.getState().conversation?.status.type).toBe("idle");

			const other = new FakeConversationService();
			other.openConv = makeConversation({ status: { type: "active" }, activeTurnId: "t2" });
			const store2 = createConversationStore();
			await store2.getState().open(other, "ref-1");
			store2.getState().applyNotification({
				method: "history/updated",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 1 },
					turns: [{ id: "t1", itemsView: "", status: "failed", error: { message: "late" } }],
				},
			} as AnyNotification);
			expect(store2.getState().conversation?.status.type).toBe("active");
			expect(store2.getState().conversation?.activeTurnId).toBe("t2");
		});

		// The control is re-evaluated at the mutation boundary: a Stop the
		// screen offered while active is refused if the status flipped idle
		// before the submit reached the store.
		it("refuses a stop submitted after the status flipped idle", async () => {
			const { service, store } = await openedStore();
			store.getState().applyNotification(statusChanged({ type: "idle" }));
			await expect(store.getState().interrupt(service)).rejects.toThrow(/no active turn/);
			expect(service.interruptCallCount).toBe(0);
		});

		// Send is a control like the others: while a turn runs the session queues,
		// it does not send. The store refuses with the control's reason so the
		// fake service (which gates on nothing) cannot accept what the hub would
		// not.
		it("refuses a send while a turn is running, with the control's reason", async () => {
			const { service, store } = await openedStore(makeConversation({ status: { type: "active" } }));
			await expect(store.getState().send(service, textInput("x"))).rejects.toThrow(TURN_RUNNING);
			expect(service.sendCallCount).toBe(0);
		});

		it("does not merge a completed turn's usage into the cumulative conversation usage", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({
				usage: { totalTokens: 500, inputTokens: 300 },
			});
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			// Two turns complete, each carrying its own (much smaller) per-turn
			// usage. The cumulative conversation usage set by the projection must
			// survive both — it is not the sum or replacement of per-turn totals.
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t1",
							itemsView: "default",
							status: "completed",
							usage: { totalTokens: 40, inputTokens: 10 },
						},
					],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "t2",
							itemsView: "default",
							status: "completed",
							usage: { totalTokens: 55, inputTokens: 15 },
						},
					],
				}),
			);
			expect(store.getState().conversation?.usage?.totalTokens).toBe(500);
			expect(store.getState().conversation?.usage?.inputTokens).toBe(300);
		});
	});

	describe("close", () => {
		it("transitions to closed and clears conversation", async () => {
			const { store } = await openedStore();
			store.getState().close();
			const s = store.getState();
			expect(s.status).toBe("closed");
			expect(s.conversation).toBeNull();
			expect(s.ref).toBeNull();
		});
	});

	describe("reset", () => {
		it("returns to idle state", async () => {
			const { store } = await openedStore();
			store.getState().setDraft("hello");
			store.getState().reset();
			const s = store.getState();
			expect(s.status).toBe("idle");
			expect(s.conversation).toBeNull();
			expect(s.ref).toBeNull();
			expect(s.draft).toBe("");
			expect(s.error).toBeNull();
		});
	});

	describe("generation safety", () => {
		it("late open from older generation does not overwrite newer conversation", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Open ref-1 (gen 1)
			await store.getState().open(service, "ref-1");
			// Reset (back to idle)
			store.getState().reset();
			// Open ref-2 (gen 2)
			service.openConv = makeConversation({ threadId: "thread-2", status: { type: "active" } });
			await store.getState().open(service, "ref-2");
			expect(store.getState().conversation?.threadId).toBe("thread-2");
			// A stale notification for ref-1 should be dropped: ref-2 keeps the
			// status it opened with rather than taking ref-1's idle.
			store.getState().applyNotification(statusChanged({ type: "idle" }));
			expect(store.getState().conversation?.threadId).toBe("thread-2");
			expect(store.getState().conversation?.status.type).toBe("active");
		});
	});

	// --- item lifecycle and delta notification tests (Step 2) -------------------

	describe("item/started inserts/replaces authoritative item", () => {
		it("inserts a new assistant item from item/started", async () => {
			const { store } = await openRunningTurn([], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("item-a", "Hello", "inProgress"), turnId: "t1" }],
				}),
			);
			expect(rowById(store, "item-a")).toMatchObject({ kind: "assistant", markdown: "Hello" });
		});

		it("replaces an existing item when item/started carries the same id", async () => {
			const { store } = await openRunningTurn([agentMessageItem("item-a", "old", "inProgress")], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("item-a", "new text", "inProgress"), turnId: "t1" }],
				}),
			);
			expect(rowById(store, "item-a")).toMatchObject({ kind: "assistant", markdown: "new text" });
		});

		// The replacement carries the transcript key, so it IS the same message
		// under a new wire id — and it says nothing about images, so the ones
		// already known stay with it (mergeItemImages, the hub's own rule: an
		// absent or empty input-images list is not a removal signal).
		//
		// The surviving row MUST be the one folded from conv.turns (the model
		// item, which retains the image) and re-keyed to the new wire id — not the
		// pre-existing companion row still sitting under the old id. The two
		// sources carry deliberately different images so the assertion can tell
		// them apart: a regression that leaves the stale row in place, or that
		// rebuilds the row from the raw wire item instead of the folded model item
		// (findFoldedItem/itemAttachments), fails here.
		it("replaces the same transcriptKey across wire IDs, folding images onto the new id", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.openConv = makeConversation(
				{
					turns: [
						{
							id: "t1",
							status: "inProgress",
							items: [
								{
									id: "wire-old",
									turnId: "t1",
									type: "userMessage",
									transcriptKey: "stable-message",
									text: "old",
									images: [{ src: "https://hub.test/folded" }],
								},
							],
						},
					],
					items: [
						{
							kind: "user",
							id: "wire-old",
							transcriptKey: "stable-message",
							text: "old",
						},
						{
							kind: "attachments",
							id: "wire-old:attachments",
							sourceTranscriptKey: "stable-message",
							items: [{ id: "image", src: "https://hub.test/stale" }],
						},
					],
				},
				true,
			);
			await store.getState().open(service, "ref-1");
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...userMessageItem("wire-new", "new"), transcriptKey: "stable-message", turnId: "t1" }],
				}),
			);
			const items = rows(store);
			expect(items.filter((item) => item.transcriptKey === "stable-message")).toHaveLength(1);
			expect(items.find((item) => item.transcriptKey === "stable-message")?.id).toBe("wire-new");
			// One attachment row, re-keyed to the replacement and carrying the
			// image the fold retained ("folded"), never the stale row's image.
			const attachments = items.filter((item) => item.kind === "attachments");
			expect(attachments).toHaveLength(1);
			expect(attachments[0]).toMatchObject({
				id: "wire-new:attachments",
				sourceTranscriptKey: "stable-message",
				items: [{ id: "wire-new:0", src: "https://hub.test/folded" }],
			});
		});
	});

	describe("item/completed settles item", () => {
		// Two shell calls in one running turn: consecutive tool items of the same
		// family, which the projector renders as one clustered row.
		const clusterPair = (over: Partial<ThreadItem> = {}): ThreadItem[] => [
			{
				...commandExecItem("call-first", "shell", "inProgress"),
				outputImages: [{ source: "old", url: "https://hub.test/old" }],
			} as ThreadItem,
			{
				type: "commandExecution",
				id: "call-later",
				toolName: "shell",
				status: "inProgress",
				outputImages: [{ source: "later", url: "https://hub.test/later" }],
				...over,
			} as ThreadItem,
		];

		it("updates a first clustered member without losing later members or attachments", async () => {
			const { store } = await openRunningTurn(clusterPair(), {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("call-first", "shell", "completed"),
							output: "updated",
							outputImages: [{ source: "new", url: "https://hub.test/new" }],
							turnId: "t1",
						},
					],
				}),
			);
			const items = rows(store);
			const cluster = items.find((item) => item.kind === "activity" && item.id === "call-first");
			expect(cluster?.kind).toBe("activity");
			expect(cluster && "members" in cluster ? cluster.members : []).toHaveLength(2);
			expect(items.filter((item) => item.id === "call-first")).toHaveLength(1);
			expect(
				items.find(
					(item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>
						item.kind === "attachments" && item.id === "call-first:attachments",
				)?.items,
			).toMatchObject([{ id: "call-first:out:0", src: "https://hub.test/new" }]);
			expect(items.find((item) => item.id === "call-later:attachments")).toBeDefined();
		});

		it("uses transcript identity for a later member and keeps its attachment beside the cluster", async () => {
			const { store } = await openRunningTurn(
				[
					{
						...commandExecItem("wire-first", "shell", "completed"),
						transcriptKey: "first",
						outputImages: [{ source: "first", url: "https://hub.test/first" }],
					} as ThreadItem,
					{
						...commandExecItem("wire-later", "shell", "inProgress"),
						transcriptKey: "later",
						outputImages: [{ source: "old", url: "https://hub.test/old" }],
					} as ThreadItem,
				],
				{},
				true,
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("new-wire-later", "shell", "inProgress"),
							transcriptKey: "later",
							outputImages: [{ source: "new", url: "https://hub.test/new" }],
							turnId: "t1",
						},
					],
				}),
			);
			const items = rows(store);
			const activities = items.filter((item) => item.kind === "activity");
			expect(activities).toHaveLength(1);
			expect(activities[0]?.kind === "activity" && activities[0].members).toHaveLength(2);
			expect(items.map((item) => item.id)).toEqual([
				"wire-first",
				"wire-first:attachments",
				"new-wire-later:attachments",
			]);
			expect(items[1]).toMatchObject({
				kind: "attachments",
				sourceTranscriptKey: "first",
				items: [{ id: "wire-first:out:0", src: "https://hub.test/first" }],
			});
			expect(items[2]).toMatchObject({
				kind: "attachments",
				sourceTranscriptKey: "later",
				items: [{ id: "new-wire-later:out:0", src: "https://hub.test/new" }],
			});
			expect(items).not.toContainEqual(expect.objectContaining({ id: "wire-later:attachments" }));
		});

		// Decision 2: the read response is ordered at the snapshot cut, so a frame
		// this store folded while the read was in flight is already in the
		// snapshot. A snapshot without the cluster is a thread without it.
		it("takes the reread's snapshot over a cluster the live frames built", async () => {
			const { store, service, release, rehydratePromise } = await beginHeldClusterRehydrate();
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("new-wire-later", "shell", "completed"),
							transcriptKey: "later",
							output: "updated",
							turnId: "t1",
						},
					],
				}),
			);
			// Task 17 (v6 read path): a plain V6_HISTORY_BASELINE release (length 0,
			// same incarnation as the notification just above, which already
			// reported length 1) would be DISCARDED outright — mergeHistory never
			// shrinks, and applyReadModel's own generation state machine (the same
			// one applyReadResponse uses in production) reads a same-incarnation
			// read reporting a SHORTER length as stale, not authoritative, exactly
			// to protect against an out-of-order response undoing real content.
			// The scenario this test actually wants — a snapshot arriving at a cut
			// that predates everything the held cluster's live frames built —
			// is a genuinely NEW incarnation in real v6 (a rebuild/rollback), which
			// replaces wholesale regardless of length; that is what a bare `inc-2`
			// here exercises, honestly, instead of a same-incarnation length that
			// could never really arrive after a longer one.
			const empty = makeReadProjectionResult(makeThread(), ALL_TRUE_CAPS, true);
			release({
				...empty,
				conversation: {
					...empty.conversation,
					history: empty.conversation.history && { ...empty.conversation.history, incarnation: "inc-2" },
				},
			});
			await rehydratePromise;
			expect(rows(store)).toEqual([]);
			expect(service.readProjectionCalls.length).toBeGreaterThan(0);
		});

		it.each([false, true])(
			"commits the reread's own members, page history first, with page history %s",
			async (withPageHistory) => {
				const { store, service, release, rehydratePromise } = await beginHeldClusterRehydrate();
				if (withPageHistory) {
					service.olderItems = { items: [{ kind: "user", id: "older", text: "older" }], nextCursor: undefined };
					store.setState({ olderCursor: "older-cursor" });
					await store.getState().loadOlder(service);
				}
				store.getState().applyNotification(
					historyUpdated({
						items: [
							{
								...commandExecItem("new-wire-later", "shell", "failed"),
								transcriptKey: "later",
								output: "failed",
								error: "boom",
								turnId: "t1",
							},
						],
					}),
				);
				// The snapshot carries both members: the first settled clean, the second
				// failed — a failed member is never clustered with a clean one.
				release(
					makeReadProjectionResult(
						runningTurnThread([
							{
								...commandExecItem("wire-first", "shell", "completed"),
								transcriptKey: "first",
								output: "authoritative first",
							} as ThreadItem,
							{
								...commandExecItem("wire-later", "shell", "completed"),
								transcriptKey: "later",
								output: "failed",
								error: "boom",
							} as ThreadItem,
						]),
						ALL_TRUE_CAPS,
						true,
					),
				);
				await rehydratePromise;
				const activities = rows(store).filter((item) => item.kind === "activity");
				expect(activities).toHaveLength(2);
				expect(activities.map((item) => item.transcriptKey)).toEqual(["first", "later"]);
				expect(activities[0]).toMatchObject({ state: "completed", detail: { output: "authoritative first" } });
				expect(activities[1]).toMatchObject({ state: "failed", detail: { output: "failed" } });
				expect(activities.every((item) => !item.members)).toBe(true);
				if (withPageHistory) expect(rows(store)[0]?.id).toBe("older");
			},
		);

		// Page history lives in the model (D23d), and a paged row can carry more
		// than one identity: a clustered activity row IS its members. When the
		// reread's snapshot has grown to include ONE of those members, the merge
		// folds that member by the package's own identity rule — the snapshot's
		// payload wins, and one identity stays one row member — while the other
		// members are still history nobody else holds, and the projection keeps
		// them clustered in the rebuilt row.
		it("keeps the paged cluster's other members when the snapshot holds one of them", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(runningTurnThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");

			// An older page whose one row is a cluster of three shell calls.
			const member = (id: string) => ({
				id,
				label: "shell",
				family: "tool" as const,
				state: "completed" as const,
				detail: { output: `${id} output` },
			});
			service.olderItems = {
				items: [
					{
						kind: "activity",
						id: "m1",
						label: "shell",
						family: "tool",
						state: "completed",
						detail: { output: "m1 output" },
						members: [member("m1"), member("m2"), member("m3")],
					},
				],
				nextCursor: undefined,
			};
			store.setState({ olderCursor: "older-cursor" });
			await store.getState().loadOlder(service);
			expect(rows(store)[0]).toMatchObject({ kind: "activity", id: "m1" });

			// The reread's snapshot has caught up with the middle member only.
			service.readProjectionResult = makeReadProjectionResult(
				runningTurnThread([
					{ ...commandExecItem("m2", "shell", "completed"), output: "m2 authoritative" } as ThreadItem,
				]),
			);
			await store.getState().rehydrate(service);

			// The snapshot owns m2's content: the fold kept exactly one m2 — the
			// authoritative one — inside the rebuilt cluster. The page still owns
			// m1 and m3: history nobody else holds survives the fold.
			const paged = rows(store).find((row) => row.kind === "activity" && row.id === "m1");
			expect(paged).toBeDefined();
			const memberIds = paged?.kind === "activity" ? paged.members?.map((m) => m.id) : undefined;
			expect(memberIds).toEqual(["m1", "m2", "m3"]);
			const foldedM2 = paged?.kind === "activity" ? paged.members?.find((m) => m.id === "m2") : undefined;
			expect(foldedM2?.state).toBe("completed");
			expect(foldedM2?.detail.output).toBe("m2 authoritative");
			// No duplicate row for the identity the snapshot re-served.
			expect(rows(store).filter((row) => row.id === "m2")).toHaveLength(0);
		});

		// A paged row's transcriptKey is the FIRST member's. When the snapshot
		// catches that member, the merge folds the keyed copies into one identity
		// (the snapshot's payload authoritative) and the rebuilt row keeps the
		// key — it names the identity the row's first member now carries, not a
		// phantom. The next member has no key of its own: it survives the fold
		// and the next publish as history nobody else holds, never reading as a
		// duplicate of the keyed member beside it.
		it("keeps a paged cluster's keyless member when the snapshot catches the keyed one", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(runningTurnThread());
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");

			const member = (id: string, over: { transcriptKey?: string } = {}) => ({
				id,
				label: `shell ${id}`,
				family: "tool" as const,
				state: "completed" as const,
				detail: { output: `${id} output` },
				...over,
			});
			service.olderItems = {
				items: [
					{
						kind: "activity",
						id: "keyed-first",
						transcriptKey: "key-first",
						label: "shell keyed-first",
						family: "tool",
						state: "completed",
						detail: { output: "keyed-first output" },
						members: [member("keyed-first", { transcriptKey: "key-first" }), member("unkeyed-second")],
					},
				],
				nextCursor: undefined,
			};
			store.setState({ olderCursor: "older-cursor" });
			await store.getState().loadOlder(service);

			// The reread's snapshot holds the keyed first member.
			service.readProjectionResult = makeReadProjectionResult(
				runningTurnThread([
					{
						...commandExecItem("wire-first", "shell", "completed"),
						transcriptKey: "key-first",
						output: "authoritative",
					} as ThreadItem,
				]),
			);
			await store.getState().rehydrate(service);

			const rebuilt = rows(store).find((row) => row.kind === "activity" && row.id === "wire-first");
			expect(rebuilt).toBeDefined();
			const members = rebuilt?.kind === "activity" ? rebuilt.members : undefined;
			expect(members?.map((member) => member.id)).toEqual(["wire-first", "unkeyed-second"]);
			expect(members?.[0]?.detail.output).toBe("authoritative");
			expect(rebuilt?.transcriptKey).toBe("key-first");

			// And the keyless member survives the next publish rather than reading
			// as a duplicate.
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...{ type: "userMessage", id: "u-later", turnId: "t1", text: "later" } }],
				}),
			);
			expect(
				rows(store).some(
					(row) => row.kind === "activity" && row.members?.some((member) => member.id === "unkeyed-second"),
				),
			).toBe(true);
		});

		it("does not resurrect a removed image when an attachment wire ID changes", async () => {
			const { store, service } = await openRunningTurn(
				[
					{
						...commandExecItem("old-wire", "shell", "inProgress"),
						transcriptKey: "later",
						outputImages: [{ source: "old", url: "old" }],
					} as ThreadItem,
				],
				{},
				true,
			);
			expect(rows(store).some((item) => item.kind === "attachments")).toBe(true);
			let release!: (value: ConversationReadProjection) => void;
			service.readProjectionBlock = new Promise((resolve) => {
				release = resolve;
			});
			const rehydratePromise = store.getState().rehydrate(service);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("new-wire-later", "shell", "completed"),
							transcriptKey: "later",
							output: "done",
							turnId: "t1",
						},
					],
				}),
			);
			// Task 17 (v6 read path): mergeReplacedItem (reducer.ts) follows the
			// wire's own nil/non-nil-empty/non-empty convention — an OMITTED list
			// field means "this entry said nothing new," an explicit EMPTY one
			// means "withdrawn" — so the read must spell the removal explicitly,
			// the same convention a real completion carrying no images actually
			// sends (imagesToItemImagesForSession/outputImagesToItemImages).
			release(
				makeReadProjectionResult(
					runningTurnThread([
						{
							...commandExecItem("new-wire-later", "shell", "completed"),
							transcriptKey: "later",
							output: "done",
							outputImages: [],
						} as ThreadItem,
					]),
				),
			);
			await rehydratePromise;
			expect(rows(store).some((item) => item.kind === "attachments")).toBe(false);
		});

		it("carries a later member's completion into the cluster row", async () => {
			const { store } = await openRunningTurn(
				[
					{ ...commandExecItem("wire-first", "shell", "completed"), transcriptKey: "first" } as ThreadItem,
					{
						...commandExecItem("wire-later", "shell", "inProgress"),
						transcriptKey: "later",
					} as ThreadItem,
				],
				{},
				true,
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("new-wire-later", "shell", "completed"),
							transcriptKey: "later",
							output: "updated",
							outputImages: [{ source: "new", url: "new" }],
							turnId: "t1",
						},
					],
				}),
			);
			const items = rows(store);
			const activity = items.find((item) => item.kind === "activity");
			expect(
				activity?.kind === "activity"
					? activity.members?.find((member) => member.transcriptKey === "later")?.detail.output
					: undefined,
			).toBe("updated");
			expect(
				items.find(
					(item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>
						item.kind === "attachments" && item.sourceTranscriptKey === "later",
				)?.items,
			).toMatchObject([{ id: "new-wire-later:out:0", src: "new" }]);
		});

		it("splits a failed member out of a hydrated cluster", async () => {
			const { store } = await openRunningTurn(
				[
					{ type: "commandExecution", id: "call-first", toolName: "shell", status: "inProgress" } as ThreadItem,
					{ type: "commandExecution", id: "call-later", toolName: "shell", status: "inProgress" } as ThreadItem,
				],
				{},
				true,
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...commandExecItem("call-later", "shell", "completed"), error: "boom", turnId: "t1" }],
				}),
			);
			const activities = rows(store).filter((item) => item.kind === "activity");
			expect(activities).toHaveLength(2);
			expect(
				activities.some((item) => item.kind === "activity" && item.id === "call-later" && item.state === "failed"),
			).toBe(true);
			expect(activities.filter((item) => item.kind === "activity" && item.id === "call-later")).toHaveLength(1);
			const flattened = projectNativeTranscript(store.getState().conversation, null).items;
			expect(flattened.filter((item) => item.kind === "activity").map((item) => item.id)).toEqual([
				"call-first",
				"call-later",
			]);
		});

		// Decision 2: an item's own status is the per-item liveness signal, as the
		// web reads it (TurnBlock.tsx's isItemLive, `item.status === "inProgress"`).
		// A settled assistant message stops saying "Writing…" the moment it
		// settles, whether or not the turn it sits in is still running.
		it("stops streaming when the assistant item settles inside a running turn", async () => {
			const { store } = await openRunningTurn([agentMessageItem("item-a", "partial", "inProgress")], {}, true);
			expect(rowById(store, "item-a")).toMatchObject({ streaming: true });
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("item-a", "all of it", "completed"), turnId: "t1" }],
				}),
			);
			expect(store.getState().conversation?.activeTurnId).toBe("t1");
			expect(rowById(store, "item-a")).toMatchObject({
				kind: "assistant",
				markdown: "all of it",
				streaming: false,
			});
		});

		it("marks an assistant item as not streaming on item/completed", async () => {
			const { store } = await openRunningTurn([agentMessageItem("item-a", "streaming text", "inProgress")], {}, true);
			expect(rowById(store, "item-a")).toMatchObject({ streaming: true });
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("item-a", "streaming text", "completed"), turnId: "t1" }],
				}),
			);
			// The turn is still running, so the row settles when the turn does —
			// the projector reads streaming from the turn, as the web does.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t1", itemsView: "", status: "completed" }],
				}),
			);
			expect(rowById(store, "item-a")).toMatchObject({
				kind: "assistant",
				streaming: false,
			});
		});

		it.each([
			["completed", undefined, undefined, "completed"],
			// A nonzero exit alone is not a failure: it shows as a small inline
			// mark on the call, not a failed row (Jesse, 2026-10-16).
			["completed on a nonzero exit code and no error", 1, undefined, "completed"],
			["completed on a zero exit code and no error", 0, undefined, "completed"],
			["failed with an error and no exit code", undefined, "boom", "failed"],
		] as const)("marks an activity item as %s on item/completed", async (_label, exitCode, error, expected) => {
			const { store } = await openRunningTurn(
				[{ type: "commandExecution", id: "tool-1", toolName: "shell", status: "inProgress" } as ThreadItem],
				{},
				true,
			);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								type: "commandExecution",
								id: "tool-1",
								toolName: "shell",
								status: "completed",
								output: "done",
								...(exitCode === undefined ? {} : { exitCode }),
								...(error === undefined ? {} : { error }),
							},
							turnId: "t1",
						},
					],
				}),
			);
			expect(rowById(store, "tool-1")).toMatchObject({
				kind: "activity",
				state: expected,
			});
		});

		it("C6: upserts completed item even when start was missed", async () => {
			// The turn is open but empty — the item/started was missed.
			const { store } = await openRunningTurn([], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...commandExecItem("tool-missed", "shell", "completed"), output: "done", turnId: "t1" }],
				}),
			);
			// The completed item is inserted (UPSERT), not lost.
			expect(rowById(store, "tool-missed")).toMatchObject({ kind: "activity" });
		});

		// The durable activity family comes from the wire type, independent of the
		// label. A commandExecution whose toolName is "Reasoning" stays family
		// "tool" and preserves callId exactly; a reasoning item is family
		// "reasoning".
		it("a commandExecution named 'Reasoning' is family 'tool' with exact callId", async () => {
			const { store } = await openRunningTurn([], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...commandExecItem("tool-reasoning-1", "Reasoning", "completed"),
							callId: "call-reasoning-1",
							output: "thoughts",
							argumentsJson: '{"summary":"thinking"}',
							turnId: "t1",
						},
					],
				}),
			);
			expect(rowById(store, "tool-reasoning-1")).toMatchObject({
				kind: "activity",
				// Family follows the wire type (commandExecution), not the toolName;
				// the label is the toolName verbatim (display only); callId is
				// preserved exactly for diagnostics disclosure.
				family: "tool",
				label: "Reasoning",
				detail: { callId: "call-reasoning-1" },
			});
		});

		it("a reasoning item is family 'reasoning'", async () => {
			const { store } = await openRunningTurn([], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...reasoningItem("reason-notify-1", "analyzing", "inProgress"), turnId: "t1" }],
				}),
			);
			expect(rowById(store, "reason-notify-1")).toMatchObject({
				kind: "activity",
				family: "reasoning",
				label: "Reasoning",
			});
		});

		// reducer.ts's wireItemToModel seeds reasoningSummaries from ANY non-empty
		// initial wire text (item/started here), and mergeReasoning keeps that
		// seeded summary across later merges once it's set — but mergeCompletedText
		// still treats the completion's own explicit text as authoritative
		// (the row module's reasoningText: a settled item's non-blank text wins over a
		// stale seeded summary; see project.test.ts's "shows the completion's
		// authoritative text over a stale seeded reasoningSummaries entry" for the
		// same invariant through the canonical projector directly). Main's landed
		// text-presence rule reads the empty string as EXPLICIT wire text —
		// authoritative like any other — while an omitted text field is not a
		// replacement and the seeded summary stands.
		it.each([
			["a later text", "final reasoning", "final reasoning"],
			["explicit empty text", "", ""],
			["omitted text", undefined, "old reasoning"],
		] as const)(
			"shows the completion's text over the seeded summary when the completion carries %s",
			async (_label, text, expected) => {
				const { store } = await openRunningTurn([], {}, true);
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...reasoningItem("reason-authoritative-1", "old reasoning", "inProgress"), turnId: "t1" }],
					}),
				);
				store.getState().applyNotification(
					historyUpdated({
						items: [
							{
								...{
									type: "reasoning",
									id: "reason-authoritative-1",
									status: "completed",
									...(text === undefined ? {} : { text }),
								},
								turnId: "t1",
							},
						],
					}),
				);
				expect(rowById(store, "reason-authoritative-1")).toMatchObject({
					kind: "activity",
					detail: { output: expected },
				});
			},
		);

		// A reasoning item the model never streamed into shows the text the wire
		// settled it with.
		it("shows the settled text of a reasoning item that streamed nothing", async () => {
			const { store } = await openRunningTurn([], {}, true);
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...reasoningItem("reason-settled-1", "final reasoning", "completed"), turnId: "t1" }],
				}),
			);
			expect(rowById(store, "reason-settled-1")).toMatchObject({
				kind: "activity",
				detail: { output: "final reasoning" },
			});
		});

		it("preserves existing attachments when item/completed for the same user message carries no images field", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({
				turns: [
					{
						id: "t1",
						status: "inProgress",
						items: [
							{
								id: "msg-1",
								turnId: "t1",
								type: "userMessage",
								text: "hello",
								images: [{ src: "https://hub.test/image" }],
							},
						],
					},
				],
				items: [
					{ kind: "user", id: "msg-1", text: "hello" },
					{
						kind: "attachments",
						id: "msg-1:attachments",
						items: [{ id: "image", src: "https://hub.test/image" }],
					},
				],
			});
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().applyNotification(
				historyUpdated({
					items: [
						{
							...{
								type: "userMessage",
								id: "msg-1",
								text: "hello",
								status: "completed",
								// No images field: an absent input-image list means "unchanged",
								// not "removed" (the same rule the package reducer's
								// mergeItemImages already applies).
							},
							turnId: "t1",
						},
					],
				}),
			);
			const items = store.getState().conversation?.items ?? [];
			expect(
				items.find(
					(item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>
						item.kind === "attachments" && item.id === "msg-1:attachments",
				)?.items,
			).toEqual([{ id: "msg-1:0", src: "https://hub.test/image" }]);
		});
	});

	describe("assistant delta appends to item", () => {
		// A frame naming an item the model does not hold could not be applied:
		// this client missed the item/started that would have made it placeable,
		// so the rows have a gap in them. Nothing changes on screen from the
		// frame itself, and the canonical read is asked for at once — the phone
		// refreshes itself rather than waiting for the next resync.

		it.each([
			[
				"a completion for a turn the model does not hold",
				{
					method: "history/updated",
					params: {
						threadId: "thread-1",
						ref: "ref-1",
						bootGeneration: "1",
						epoch: 1,
						snapshot: { incarnation: "inc-1", length: 1 },
						items: [{ type: "userMessage", id: "u9", turnId: "t-unknown", text: "hi" }],
					},
				},
			],
		])("asks for a reread when %s cannot be placed", async (_label, frame) => {
			const { store, service } = await openRunningTurn([agentMessageItem("a1", "hello", "inProgress")]);
			const initialReads = service.readProjectionCalls.length;
			const before = rows(store);
			store.getState().applyNotification(frame as AnyNotification);
			expect(rows(store)).toEqual(before);
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
		});
	});

	// Every row kind that carries text a reader scrolls past is bounded, not just
	// the two that happen to stream: a pasted user message can be as large as any
	// assistant reply, a tool failure's detail carries a stack, and a notice
	// carries whatever the daemon said.
	describe("the display bound covers every text-bearing row kind", () => {
		const oversized = "x".repeat(MAX_ITEM_BYTES + 5_000);
		// Bounded means all three things, so every row kind below is held to them: it
		// fits the byte limit, it says it was cut, and it says so exactly once (a row
		// re-bound per publish must not accumulate markers).
		const bounded = (text: string): boolean =>
			new TextEncoder().encode(text).length <= MAX_ITEM_BYTES &&
			text.endsWith(TRUNCATION_MARKER) &&
			text.split(TRUNCATION_MARKER).length - 1 === 1;

		it.each([
			[
				"user",
				{ kind: "user", id: "r", text: oversized },
				(row: MobileTimelineItem) => (row.kind === "user" ? [row.text] : []),
			],
			[
				"note",
				{ kind: "note", id: "r", text: oversized },
				(row: MobileTimelineItem) => (row.kind === "note" ? [row.text] : []),
			],
			[
				"assistant",
				{ kind: "assistant", id: "r", markdown: oversized, streaming: false },
				(row: MobileTimelineItem) => (row.kind === "assistant" ? [row.markdown] : []),
			],
			[
				"notice",
				{
					kind: "notice",
					id: "r",
					origin: "system",
					family: "warning",
					tone: "warning",
					text: oversized,
				},
				(row: MobileTimelineItem) => (row.kind === "notice" ? [row.text] : []),
			],
			[
				"failure",
				{ kind: "failure", id: "r", title: oversized, detail: oversized },
				(row: MobileTimelineItem) => (row.kind === "failure" ? [row.title, row.detail] : []),
			],
			[
				"activity",
				{
					kind: "activity",
					id: "r",
					label: "shell",
					family: "tool",
					state: "completed",
					detail: { output: oversized, arguments: oversized, error: oversized },
				},
				(row: MobileTimelineItem) =>
					row.kind === "activity"
						? [row.detail.output, row.detail.arguments, row.detail.error].filter(
								(text): text is string => text !== undefined,
							)
						: [],
			],
		] as const)("bounds a %s row", async (_kind, row, read) => {
			const { store } = await openedStore(makeConversation({ items: [row as unknown as MobileTimelineItem] }));
			const published = store.getState().conversation?.items.find((item) => item.id === "r");
			if (published === undefined) throw new Error("row not published");
			const texts = read(published);
			expect(texts.length).toBeGreaterThan(0);
			for (const text of texts) expect(bounded(text)).toBe(true);
		});

		// A question row's own prose is bounded in place, like every other kind, and
		// the fixture is built the way the app builds one: an ask_user item whose
		// arguments carry the oversized text, projected through the model. The answer
		// path is unaffected because it does not read these rows — it asks the model
		// through the package's own rule, so it still names the uncut values.
		it("bounds a question row's prose and leaves the model's asks whole", async () => {
			const bigAsk = JSON.stringify({
				questions: [
					{
						header: oversized,
						question: oversized,
						why: oversized,
						if_unanswered: oversized,
						options: [{ label: oversized, detail: oversized }],
						multi_select: false,
					},
				],
			});
			const { store } = await openProjectedThreadWith(
				runningTurnThread([askUserItem("ask-1", bigAsk)], {
					evener: evenerWith({ activeTurnId: "t1", askPending: true }),
				}),
			);

			const row = rows(store).find((item) => item.kind === "question");
			if (row === undefined || row.kind !== "question") throw new Error("no question row");
			const shown = row.questions[0];
			if (shown === undefined) throw new Error("no question");
			const option = shown.options[0];
			for (const text of [shown.header, shown.question, shown.why, shown.ifUnanswered, option?.label, option?.detail]) {
				if (text === undefined) throw new Error("prose field missing");
				expect(bounded(text)).toBe(true);
			}

			// What the composer answers with comes from the model, uncut: a bounded
			// label would name a choice the agent never offered.
			const conversation = store.getState().conversation;
			if (conversation === null) throw new Error("conversation gone");
			const canonical = liveAskQuestions(conversation)[0];
			expect(canonical?.header).toBe(oversized);
			expect(canonical?.options[0]?.label).toBe(oversized);
			expect(canonical?.ifUnanswered).toBe(oversized);
		});

		// Every text an activity row renders, top-level and per member: the label
		// (the row's disclosure line and its accessibility label) and the
		// description (the summary line a collapsed row shows) are read exactly as
		// the arguments and output are.
		it.each([
			["top-level", false],
			["clustered member", true],
		])("bounds an activity's label and description on a %s row", async (_where, clustered) => {
			const activity = {
				kind: "activity" as const,
				id: "r",
				label: oversized,
				family: "tool" as const,
				state: "completed" as const,
				detail: { description: oversized, output: "small" },
			};
			const row = clustered
				? {
						...activity,
						members: [
							{ ...activity, id: "r:0" },
							{ ...activity, id: "r:1" },
						],
					}
				: activity;
			const { store } = await openedStore(makeConversation({ items: [row as unknown as MobileTimelineItem] }));
			// A clustered row is model-backed (D23d): the projection names it by
			// its first member's id.
			const published = store.getState().conversation?.items.find((item) => item.id === (clustered ? "r:0" : "r"));
			if (published === undefined || published.kind !== "activity") throw new Error("row not published");
			const texts = [
				published.label,
				published.detail.description,
				...(published.members ?? []).flatMap((member) => [member.label, member.detail.description]),
			].filter((text): text is string => text !== undefined);
			expect(texts.length).toBe(clustered ? 6 : 2);
			for (const text of texts) expect(bounded(text)).toBe(true);
		});

		// An attachment's src is the image itself (a data: URI for composer bytes),
		// not prose a reader scrolls: cutting it mid-payload yields an image that
		// cannot decode, so it is left whole. The wire bounds image payloads at the
		// source instead.
		it("leaves an attachment's data URI whole", async () => {
			const src = `data:image/png;base64,${"A".repeat(MAX_ITEM_BYTES + 5_000)}`;
			const service = new FakeConversationService();
			// Model-backed (D23d): an attachments row converts through the wire
			// item of the source row it follows — the fixture must script that
			// source row, or the conversion refuses it.
			service.openConv = makeConversation({
				items: [
					{ kind: "user", id: "r", text: "source row" },
					{ kind: "attachments", id: "r:attachments", items: [{ id: "r:0", src }] },
				],
			});
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			const published = store.getState().conversation?.items.find((item) => item.id === "r:attachments");
			expect(published?.kind === "attachments" ? published.items[0]?.src : undefined).toBe(src);
		});
	});

	describe("arguments/output stop at 64 KiB UTF-8 and end with truncation marker", () => {
		it("truncates multibyte text at 64 KiB UTF-8 boundary without splitting surrogates", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Each 'é' is 2 bytes in UTF-8. 35,000 é chars = 70,000 bytes > 64 KiB.
			const largeMultibyte = "é".repeat(35_000);
			service.openConv = makeConversation({
				items: [
					{
						kind: "assistant",
						id: "item-multi",
						markdown: largeMultibyte,
						streaming: true,
					},
				],
			});
			await store.getState().open(service, "ref-1");
			const conv = store.getState().conversation;
			const item = conv?.items.find((i) => i.id === "item-multi");
			if (item?.kind === "assistant") {
				const encoder = new TextEncoder();
				const byteLen = encoder.encode(item.markdown).length;
				expect(byteLen).toBeLessThanOrEqual(65536);
				// The result must be valid Unicode (no split surrogates)
				const decoded = new TextDecoder().decode(encoder.encode(item.markdown));
				expect(decoded).toBe(item.markdown);
				expect(item.markdown.endsWith("… truncated")).toBe(true);
			}
		});
	});

	describe("attachment names stop at 64 KiB UTF-8 while src passes through", () => {
		it("bounds an oversized attachment name and leaves src byte-for-byte intact", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const largeName = "n".repeat(MAX_ITEM_BYTES + 100);
			// src is not display text: cutting a data URI (or a fetch URL) yields
			// something that cannot decode, so it must survive verbatim.
			const src = `data:image/png;base64,${"A".repeat(200)}`;
			// Model-backed (D23d): the attachments row converts through the wire
			// item of the source row it names — the fixture must script that
			// source row, or the conversion refuses it.
			service.openConv = makeConversation({
				items: [
					{ kind: "user", id: "msg-1", text: "source row" },
					{
						kind: "attachments",
						id: "msg-1:attachments",
						sourceTranscriptKey: "msg-1",
						items: [{ id: "image-1", src, name: largeName }],
					},
				],
			});
			await store.getState().open(service, "ref-1");
			const item = store.getState().conversation?.items.find((candidate) => candidate.id === "msg-1:attachments");
			expect(item?.kind).toBe("attachments");
			if (item?.kind !== "attachments") return;
			const attachment = item.items[0];
			expect(attachment).toBeDefined();
			if (!attachment) return;
			const encoder = new TextEncoder();
			expect(encoder.encode(attachment.name ?? "").length).toBeLessThanOrEqual(MAX_ITEM_BYTES);
			expect(attachment.name?.endsWith("… truncated")).toBe(true);
			const markerCount = (attachment.name?.split("… truncated").length ?? 1) - 1;
			expect(markerCount).toBe(1);
			// src is never bounded — byte-for-byte identical to the input.
			expect(attachment.src).toBe(src);
			expect(encoder.encode(attachment.src).length).toBe(encoder.encode(src).length);
		});

		it("bounds an oversized attachment name arriving on a live item/started", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// The frame's turn must be one the model holds: under the cutover the
			// rows are a projection of the model, so a frame naming a turn this
			// window does not hold is a gap (a reread request), not a row.
			service.openConv = makeConversation(
				{
					turns: [{ id: "t1", status: "inProgress", items: [] }],
				},
				true,
			);
			await store.getState().open(service, "ref-1");
			const largeName = "n".repeat(MAX_ITEM_BYTES + 100);
			const src = "https://hub.test/live.png";
			store.getState().applyNotification({
				method: "history/updated",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 1 },
					items: [{ ...userMessageItem("msg-live", "hi"), images: [{ url: src, name: largeName }], turnId: "t1" }],
				},
			} as AnyNotification);
			const item = store.getState().conversation?.items.find((candidate) => candidate.id === "msg-live:attachments");
			expect(item?.kind).toBe("attachments");
			if (item?.kind !== "attachments") return;
			const attachment = item.items[0];
			expect(attachment).toBeDefined();
			if (!attachment) return;
			const encoder = new TextEncoder();
			expect(encoder.encode(attachment.name ?? "").length).toBeLessThanOrEqual(MAX_ITEM_BYTES);
			expect(attachment.name?.endsWith("… truncated")).toBe(true);
			// src is never bounded — byte-for-byte identical to the input.
			expect(attachment.src).toBe(src);
		});
	});

	describe("resync coalesces to one rehydrate via internal coalescer", () => {
		it("coalesces evener/thread/resync into one rehydrate call", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), "cursor-after-resync");
			// F2: openProjected binds the coalescer internally.
			await store.getState().openProjected(service, "ref-1");

			// The initial openProjected calls readProjection once.
			const initialReads = service.readProjectionCalls.length;
			expect(initialReads).toBe(1);

			// Emit multiple resync notifications — they should coalesce to one
			// rehydrate (one additional readProjection call).
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());
			store.getState().applyNotification(resyncFrame());

			// The internal coalescer schedules rehydrate via microtask.
			// Wait for microtasks to flush.
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			// F5: One rehydrate call = one additional readProjection call.
			expect(service.readProjectionCalls.length).toBe(initialReads + 1);
		});

		it("unsupported item transition triggers coalesced rehydrate", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }, true), null);
			await store.getState().openProjected(service, "ref-1");

			const initialReads = service.readProjectionCalls.length;

			// An unknown notification method (unsupported item transition) should
			// trigger the internal coalescer, not a crash.
			store.getState().applyNotification({
				method: "item/unknownFutureTransition" as never,
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					turnId: "t1",
					item: { type: "futureType", id: "x", status: "unknown" },
				},
			} as AnyNotification);
			await Promise.resolve();
			await Promise.resolve();
			await Promise.resolve();
			expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
		});
	});

	describe("stale generation completion is ignored", () => {
		it("drops item/completed from an older generation", async () => {
			const { service, store } = await openedStore();
			// Reset to bump generation, then open a new conversation
			store.getState().reset();
			service.openConv = makeConversation({ threadId: "thread-2" });
			await store.getState().open(service, "ref-2");
			// A stale completion for thread-1/ref-1 should be dropped
			const before = store.getState().conversation;
			store.getState().applyNotification(
				historyUpdated({
					items: [{ ...agentMessageItem("item-a", "done", "completed"), turnId: "t1" }],
				}),
			);
			expect(store.getState().conversation).toBe(before);
		});
	});

	describe("rehydrate preserves draft and presentation state", () => {
		it("preserves draft text across rehydrate", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), "cursor-1");
			await store.getState().openProjected(service, "ref-1");
			store.getState().setDraft("my unsent draft");
			// Rehydrate: should preserve the draft
			await store.getState().rehydrate(service);
			expect(store.getState().draft).toBe("my unsent draft");
		});

		it("preserves expandedToolKeys/presentation state across rehydrate", async () => {
			// C7: expandedToolKeys is removed from the production store — presentation
			// state lives in live-ui-store. Rehydrate only preserves draft.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), "cursor-1");
			await store.getState().openProjected(service, "ref-1");
			// Rehydrate should preserve draft but not carry presentation state.
			store.getState().setDraft("my draft");
			await store.getState().rehydrate(service);
			expect(store.getState().draft).toBe("my draft");
		});
	});

	describe("olderCursor survives open/rehydrate", () => {
		it("preserves olderCursor from openProjected through rehydrate", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), "initial-cursor");
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().olderCursor).toBe("initial-cursor");
			// Update the projection result for rehydrate
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-1" }), "updated-cursor");
			await store.getState().rehydrate(service);
			expect(store.getState().olderCursor).toBe("updated-cursor");
		});
	});

	// A rehydrate held open while live frames land, on a thread whose running
	// turn holds two shell calls the projector clusters into one row.
	async function beginHeldClusterRehydrate() {
		const { store, service, thread } = await openRunningTurn(
			[
				{ ...commandExecItem("wire-first", "shell", "inProgress"), transcriptKey: "first" } as ThreadItem,
				{ ...commandExecItem("wire-later", "shell", "inProgress"), transcriptKey: "later" } as ThreadItem,
			],
			{},
			true,
		);
		let release!: (value: ConversationReadProjection) => void;
		service.readProjectionBlock = new Promise((resolve) => {
			release = resolve;
		});
		const rehydratePromise = store.getState().rehydrate(service);
		return { service, store, thread, release, rehydratePromise };
	}
});
