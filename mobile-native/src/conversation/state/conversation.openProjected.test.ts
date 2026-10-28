// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { AnyNotification } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { describe, expect, it } from "vitest";
import type { MobileConversation } from "../../projectedRows";
import type { ConversationReadProjection } from "../services/conversation";
import { createConversationStore, olderPageKey } from "./conversation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	makeControlledRead,
	makeConversation,
	makeReadProjectionResult,
	makeThread,
	openedStore,
	readServing,
	statusChanged,
	textInput,
	yieldMicrotask,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	describe("openProjected", () => {
		// The initial read is the same thread/read (subscribe + replaceSubscription)
		// as a reread: a frame delivered before its response is already folded
		// into the snapshot, so it is neither replayed nor a reason to reread.
		it("does not reread for a frame that precedes the initial read's response", async () => {
			const service = new FakeConversationService();
			service.readProjectionResult = makeReadProjectionResult(makeThread({ status: { type: "idle" } }));
			const store = createConversationStore();
			const ctrl = makeControlledRead(service);
			const opening = store.getState().openProjected(service, "ref-1");
			await ctrl.started(1);
			service.notificationHandler?.(statusChanged({ type: "active" }));
			await ctrl.ready(1);
			ctrl.release();
			await ctrl.completed(1);
			await opening;
			await yieldMicrotask();
			expect(service.readProjectionCalls).toHaveLength(1);
			expect(store.getState().conversation?.status).toEqual({ type: "idle" });
		});

		it("uses readProjection to set conversation and cursor", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation({ threadId: "thread-proj" }), "cursor-initial");
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().conversation?.threadId).toBe("thread-proj");
			expect(store.getState().olderCursor).toBe("cursor-initial");
			expect(store.getState().status).toBe("open");
		});

		it("subscribes to notifications", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			// The store should have subscribed for notifications
			expect(service.notificationHandler).not.toBeNull();
		});

		it("preserves olderCursor across openProjected", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(makeConversation(), "page-1");
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().olderCursor).toBe("page-1");
		});

		it("resets draft from prior thread (I8)", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Set draft from a prior thread
			store.getState().setDraft("old thread draft");
			// Open a new projected conversation
			service.readProjectionResult = readServing(makeConversation(), null);
			await store.getState().openProjected(service, "ref-1");
			// Draft should be reset — not carried from the prior thread.
			expect(store.getState().draft).toBe("");
		});

		it("suspends without clearing the displayed conversation and blocks mutations", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			const before = store.getState().conversation;
			const oldHandler = service.notificationHandler;

			store.getState().suspendProjected();
			expect(store.getState().conversation).toBe(before);
			expect(store.getState().status).toBe("opening");
			await store.getState().send(service, textInput("blocked"));
			expect(service.sendCallCount).toBe(0);

			oldHandler?.({
				method: "thread/status/changed",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					status: { type: "running" },
				},
			} as AnyNotification);
			expect(store.getState().conversation).toBe(before);
		});

		it("retains the display while resuming and installs a fresh subscribed read", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			const before = store.getState().conversation;
			store.getState().suspendProjected();
			let release!: (value: ConversationReadProjection) => void;
			service.readProjectionBlock = new Promise((resolve) => {
				release = resolve;
			});

			const resume = store.getState().resumeProjected(service, "ref-1");
			expect(store.getState().conversation).toBe(before);
			expect(store.getState().status).toBe("opening");
			expect(service.readProjectionCalls).toHaveLength(2);
			await store.getState().send(service, textInput("blocked while resuming"));
			expect(service.sendCallCount).toBe(0);
			release({
				// The real service's hydrateThread stamps ThreadModel.olderCursor
				// from the same response field this wrapper's own olderCursor
				// carries (threadFields, reducer.ts) — a real resumed read could
				// never hand back a conversation whose own cursor field disagrees
				// with the wrapper's.
				conversation: { ...(before as MobileConversation), olderCursor: "cursor-resumed" },
				olderCursor: "cursor-resumed",
			});
			await resume;
			expect(store.getState().status).toBe("open");
			expect(store.getState().conversation?.threadId).toBe(before?.threadId);
			expect(store.getState().olderCursor).toBe("cursor-resumed");
			expect(service.notificationHandler).not.toBeNull();
		});

		// Retention is keyed by the session, not the service object: a rebuilt
		// client resumes the same ref and keeps the display. A read that names a
		// different thread still replaces it, on the read's own identity.
		it("replaces the retained display when the resume's read names another thread", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				items: [{ kind: "user", id: "held", text: "held" }],
			});
			const otherService = new FakeConversationService();
			otherService.openConv = makeConversation({
				threadId: "thread-2",
				instanceId: "instance-2",
				items: [{ kind: "user", id: "replacement", text: "replacement" }],
			});
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["held"]);
			store.getState().suspendProjected();
			await store.getState().resumeProjected(otherService, "ref-1");
			expect(store.getState().conversation?.threadId).toBe("thread-2");
			// The held row is gone: the read's instance identity replaced the
			// history rather than merging into it.
			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["replacement"]);
			expect(store.getState().status).toBe("open");
		});

		function projectionFor(service: FakeConversationService): ConversationReadProjection {
			return {
				conversation: service.openConv,
				olderCursor: null,
			};
		}

		// A session suspended mid-life, resumed by a fresh service object (the
		// connection client a reconnect or foreground rebuilds) whose read is held
		// open, so a test can land frames or assert the retained rows first.
		async function beginRebuiltClientResume(versioned: boolean) {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: [{ kind: "user", id: "u1", text: "first" }] }, versioned);
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.getState().suspendProjected();
			const rebuilt = new FakeConversationService();
			rebuilt.openConv = service.openConv;
			const read = deferred<ConversationReadProjection>();
			rebuilt.readProjectionBlock = read.promise;
			const resume = store.getState().resumeProjected(rebuilt, "ref-1");
			await yieldMicrotask();
			return { store, rebuilt, read, resume };
		}

		// A rebuilt client (a reconnect, or the app returning to the front)
		// resumes the same session through a new service object. The transcript
		// stays on screen and the fresh read merges into it.
		it("retains the transcript while a rebuilt client's read lands", async () => {
			const { store, rebuilt, read, resume } = await beginRebuiltClientResume(false);
			const ids = () => store.getState().conversation?.items.map((item) => item.id);
			expect(ids()).toEqual(["u1"]);
			expect(store.getState().status).toBe("opening");

			read.resolve(projectionFor(rebuilt));
			await resume;
			expect(store.getState().status).toBe("open");
			expect(ids()).toEqual(["u1"]);
		});

		// The subscription is already installed and nothing else will deliver a
		// frame that races the read, so the store must fold it in itself.
		it("keeps a frame that lands while a rebuilt client's read is in flight", async () => {
			const { store, rebuilt, read, resume } = await beginRebuiltClientResume(true);
			const ids = () => store.getState().conversation?.items.map((item) => item.id);
			rebuilt.notificationHandler?.({
				method: "history/updated",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 1 },
					turns: [{ id: "t2", status: "completed" }],
					items: [{ type: "userMessage", id: "u2", text: "second", turnId: "t2" }],
				},
			} as AnyNotification);
			expect(ids()).toContain("u2");

			read.resolve(projectionFor(rebuilt));
			await resume;
			expect(ids()).toContain("u2");
		});

		// A resync that invalidates the retained history while the read is in
		// flight asks for one authoritative reread. The request cannot be served
		// while the resume is still opening, so it must be remembered and drained
		// when the resume settles — otherwise the store opens on invalidated
		// history with nothing scheduled to repair it.
		it("repairs history when a resync lands while the rebuilt client's read is in flight", async () => {
			const { rebuilt, read, resume } = await beginRebuiltClientResume(true);
			rebuilt.notificationHandler?.({
				method: "evener/thread/resync",
				params: { threadId: "thread-1", ref: "ref-1", bootGeneration: "1", epoch: 2 },
			} as AnyNotification);

			const readsBefore = rebuilt.readProjectionCalls.length;
			rebuilt.readProjectionBlock = null;
			read.resolve(projectionFor(rebuilt));
			await resume;
			await yieldMicrotask();
			expect(rebuilt.readProjectionCalls.length).toBeGreaterThan(readsBefore);
		});

		it("retains the suspended display when the fresh subscribed read fails", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			const before = store.getState().conversation;
			store.getState().suspendProjected();
			service.readProjectionBlock = Promise.reject(new Error("resume failed"));

			await store.getState().resumeProjected(service, "ref-1");
			expect(store.getState().conversation).toBe(before);
			expect(store.getState().status).toBe("error");
			expect(store.getState().error).toBe("resume failed");
			await store.getState().send(service, textInput("blocked"));
			expect(service.sendCallCount).toBe(0);
		});

		it("preserves completed older-page history across a resumed latest read", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const latest = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-1",
				items: [{ kind: "user", id: "new", text: "new", position: { entry: 2, item: 0 } }],
			});
			service.readProjectionResult = readServing(latest, "cursor-1");
			service.olderItems = {
				items: [{ kind: "user", id: "old", text: "old", position: { entry: 1, item: 0 } }],
				nextCursor: "cursor-2",
			};
			await store.getState().openProjected(service, "ref-1");
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["old", "new"]);
			store.getState().suspendProjected();
			let release!: (value: ConversationReadProjection) => void;
			service.readProjectionBlock = new Promise((resolve) => {
				release = resolve;
			});
			const resume = store.getState().resumeProjected(service, "ref-1");
			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["old", "new"]);
			release({
				conversation: latest,
				olderCursor: null,
			});
			await resume;
			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["old", "new"]);
			expect(store.getState().olderCursor).toBe("cursor-2");

			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["old", "new"]);
			expect(store.getState().olderCursor).toBe("cursor-2");

			const replacement = makeConversation({
				threadId: "thread-1",
				instanceId: "instance-2",
				items: [{ kind: "user", id: "replacement", text: "replacement" }],
			});
			service.readProjectionBlock = null;
			service.readProjectionResult = readServing(replacement, "fresh-cursor");
			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.items.map((item) => item.id)).toEqual(["replacement"]);
			expect(store.getState().olderCursor).toBe("fresh-cursor");
		});

		it("invalidates an open that is still awaiting its first projection", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			let release!: (value: ConversationReadProjection) => void;
			service.readProjectionBlock = new Promise((resolve) => {
				release = resolve;
			});
			const opening = store.getState().openProjected(service, "ref-1");
			store.getState().suspendProjected();
			release({
				conversation: makeConversation(),
				olderCursor: null,
			});
			await opening;
			expect(store.getState().conversation).toBeNull();
			expect(store.getState().status).toBe("opening");
		});
	});

	describe("loadOlder retained cap and ordering", () => {
		// A long live session trims its oldest rows at the cap. Scrolling up
		// must still bring older history into view.
		it("shows an older page past the cap while you read above the live end", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({
				items: Array.from({ length: 600 }, (_, i) => ({ kind: "user" as const, id: `live-${i}`, text: `live ${i}` })),
			});
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = {
				items: Array.from({ length: 20 }, (_, i) => ({ kind: "user" as const, id: `older-${i}`, text: `older ${i}` })),
				nextCursor: "cursor-2",
				hasEarlierItems: true,
			};
			await store.getState().loadOlder(service);
			const ids = store.getState().conversation?.items.map((row) => row.id) ?? [];
			expect(ids[0]).toBe("older-0");
			expect(ids.at(-1)).toBe("live-599");
			expect(store.getState().olderCursor).toBe("cursor-2");
		});

		// Positioned rows, as a hub serves them: row i sits at entry i.
		const positionedRow = (i: number) => ({
			kind: "user" as const,
			id: `row-${i}`,
			text: `row ${i}`,
			position: { entry: i, item: 0 },
		});
		const positionedRows = (from: number, to: number) =>
			Array.from({ length: to - from }, (_, i) => positionedRow(from + i));

		it("pages the rows the cap trimmed back, in order, once you scroll up", async () => {
			const service = new FakeConversationService();
			// Watched from its start, so the first read held it all: no cursor.
			service.openConv = makeConversation({ items: positionedRows(0, 600) });
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			expect(store.getState().conversation?.items[0]?.id).toBe("row-100");
			expect(store.getState().trimmedAbove).toBe(true);
			expect(store.getState().olderCursor).toBeNull();

			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined, hasEarlierItems: false };
			expect((await store.getState().loadOlder(service)).status).toBe("loaded");
			// The page names the oldest row kept, with no cursor to rebase.
			expect(service.olderRequests).toEqual([{ cursor: null, before: { entry: 100, item: 0 } }]);
			expect(store.getState().conversation?.items.map((row) => row.id)).toEqual(
				positionedRows(0, 600).map((row) => row.id),
			);
			expect(store.getState().trimmedAbove).toBe(false);
		});

		it("rebases the cursor it holds onto the oldest row kept", async () => {
			const { service, store } = await openedStore(makeConversation({ items: positionedRows(100, 700) }));
			store.setState({ olderCursor: "cursor-1" });
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(100, 200), nextCursor: "cursor-1" };
			await store.getState().loadOlder(service);
			expect(service.olderRequests).toEqual([{ cursor: "cursor-1", before: { entry: 200, item: 0 } }]);
		});

		it("keeps rows from every loaded page through a refresh and a reconnect", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: positionedRows(200, 300) });
			service.olderCursor = "cursor-2";
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(100, 200), nextCursor: "cursor-1" };
			expect((await store.getState().loadOlder(service)).status).toBe("loaded");
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined, hasEarlierItems: false };
			expect((await store.getState().loadOlder(service)).status).toBe("loaded");
			const expected = positionedRows(0, 300).map((row) => row.id);
			expect(store.getState().conversation?.items.map((row) => row.id)).toEqual(expected);

			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.items.map((row) => row.id)).toEqual(expected);

			await store.getState().resumeProjected(service, "ref-1");
			expect(store.getState().conversation?.items.map((row) => row.id)).toEqual(expected);
		});

		it("keeps the pages you load while reading above, and trims them when you return to the live end", async () => {
			const { service, store } = await openedStore(makeConversation({ items: positionedRows(100, 550) }));
			store.setState({ olderCursor: "cursor-1" });
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined };
			await store.getState().loadOlder(service);
			// 550 rows, past the cap: none trimmed while you read above them.
			expect(store.getState().conversation?.items).toHaveLength(550);
			expect(store.getState().conversation?.items[0]?.id).toBe("row-0");
			expect(store.getState().trimmedAbove).toBe(false);

			store.getState().setFollowingLiveEnd(true);
			expect(store.getState().conversation?.items).toHaveLength(500);
			expect(store.getState().conversation?.items[0]?.id).toBe("row-50");
			expect(store.getState().trimmedAbove).toBe(true);
		});

		// Trimmed rows could never come back from a hub that doesn't page
		// from a before position (an older hub, or a thread on another host
		// until #3176), so its threads keep every row.
		it("never trims a thread whose read doesn't say pageBefore", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({
				items: positionedRows(0, 600),
				capabilities: { ...ALL_TRUE_CAPS, pageBefore: false },
			});
			const store = createConversationStore();
			await store.getState().open(service, "host:thread-1");
			expect(store.getState().conversation?.items).toHaveLength(600);
			expect(store.getState().trimmedAbove).toBe(false);
		});

		// A status frame that names no pageBefore (one straight from a daemon;
		// the hub stamps the frames it relays) leaves the last answer, so
		// trimming stays on.
		it("keeps trimming after a status frame that doesn't name pageBefore", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: positionedRows(0, 600) });
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			const { pageBefore: _dropped, ...daemonCaps } = ALL_TRUE_CAPS;
			store.getState().applyNotification({
				method: "thread/status/changed",
				params: { threadId: "thread-1", ref: "ref-1", status: { type: "idle" }, capabilities: daemonCaps },
			} as unknown as AnyNotification);
			// Read above, page the trimmed rows back, and return: the window
			// trims again.
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined, hasEarlierItems: false };
			expect((await store.getState().loadOlder(service)).status).toBe("loaded");
			expect(store.getState().conversation?.items).toHaveLength(600);
			store.getState().setFollowingLiveEnd(true);
			expect(store.getState().conversation?.items).toHaveLength(500);
			expect(store.getState().conversation?.items[0]?.id).toBe("row-100");
		});

		// A read of the same instance from a hub that no longer pages from a
		// before position (a hub downgraded under a running daemon) can't bring
		// the trimmed rows back, so the store asks it for nothing above them.
		it("asks for no page above a trim once a read stops saying pageBefore", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: positionedRows(0, 600) });
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().trimmedAbove).toBe(true);
			service.openConv = makeConversation({
				items: positionedRows(0, 600),
				capabilities: { ...ALL_TRUE_CAPS, pageBefore: false },
			});
			await store.getState().rehydrate(service);
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined, hasEarlierItems: false };
			expect((await store.getState().loadOlder(service)).status).toBe("ignored");
			expect(service.olderRequests).toEqual([]);
		});

		// A page attempt's key tells every cursor and trim boundary apart, whatever
		// characters the hub's opaque cursor holds.
		it("keys a page attempt so no cursor can pass for a cursor and a boundary", () => {
			const conversation = makeConversation({ items: positionedRows(1, 3) });
			const cursorOnly = olderPageKey({ olderCursor: "abc@1.0", trimmedAbove: false, conversation });
			const cursorAndBoundary = olderPageKey({ olderCursor: "abc", trimmedAbove: true, conversation });
			expect(cursorOnly).not.toBe(cursorAndBoundary);
		});

		// The hub stamps pageBefore on the status frames it relays, so a value
		// there is the hub's answer as much as a read's.
		const statusFrame = (pageBefore: unknown, threadId = "thread-1") =>
			({
				method: "thread/status/changed",
				params: { threadId, ref: "ref-1", status: { type: "idle" }, capabilities: { ...ALL_TRUE_CAPS, pageBefore } },
			}) as unknown as AnyNotification;
		const openRows = async (service: FakeConversationService, pageBefore: boolean) => {
			service.openConv = makeConversation({
				items: positionedRows(0, 600),
				capabilities: { ...ALL_TRUE_CAPS, pageBefore },
			});
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			return store;
		};
		const returnToEnd = (store: ReturnType<typeof createConversationStore>) => {
			store.getState().setFollowingLiveEnd(false);
			store.getState().setFollowingLiveEnd(true);
		};

		it("starts trimming when a status frame says the hub pages before", async () => {
			const store = await openRows(new FakeConversationService(), false);
			expect(store.getState().conversation?.items).toHaveLength(600);
			store.getState().applyNotification(statusFrame(true));
			returnToEnd(store);
			expect(store.getState().conversation?.items).toHaveLength(500);
		});

		// A hub that stops paging before (a downgrade, rare) stops future trims
		// at once. Rows already trimmed stay trimmed for this open, and nothing
		// is asked for above them; reopening the session reads them back.
		it("stops trimming at once when a status frame says the hub doesn't page before", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: positionedRows(100, 550) });
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined };
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.items).toHaveLength(550);
			store.getState().applyNotification(statusFrame(false));
			store.getState().setFollowingLiveEnd(true);
			expect(store.getState().conversation?.items).toHaveLength(550);
			expect(store.getState().trimmedAbove).toBe(false);
		});

		it("keeps rows already trimmed when the hub stops paging before, until a reopen reads them back", async () => {
			const service = new FakeConversationService();
			const store = await openRows(service, true);
			expect(store.getState().conversation?.items).toHaveLength(500);
			store.getState().applyNotification(statusFrame(false));
			expect(store.getState().trimmedAbove).toBe(true);
			expect(store.getState().conversation?.items).toHaveLength(500);
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined, hasEarlierItems: false };
			expect((await store.getState().loadOlder(service)).status).toBe("ignored");
			expect(service.olderRequests).toEqual([]);
			service.openConv = makeConversation({
				items: positionedRows(0, 600),
				capabilities: { ...ALL_TRUE_CAPS, pageBefore: false },
			});
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().conversation?.items).toHaveLength(600);
			expect(store.getState().trimmedAbove).toBe(false);
		});

		// A capability belongs to a thread: a frame for another thread under
		// this ref (a reset, say) says nothing about this one.
		it("ignores another thread's pageBefore true", async () => {
			const store = await openRows(new FakeConversationService(), false);
			store.getState().applyNotification(statusFrame(true, "thread-other"));
			returnToEnd(store);
			expect(store.getState().conversation?.items).toHaveLength(600);
		});

		it("ignores another thread's pageBefore false", async () => {
			const service = new FakeConversationService();
			const store = await openRows(service, true);
			store.getState().applyNotification(statusFrame(false, "thread-other"));
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: undefined, hasEarlierItems: false };
			expect((await store.getState().loadOlder(service)).status).toBe("loaded");
		});

		it.each([
			["a string", "yes"],
			["null", null],
		] as const)("keeps trimming past a pageBefore that is %s", async (_kind, said) => {
			const store = await openRows(new FakeConversationService(), true);
			store.getState().applyNotification(statusFrame(said));
			returnToEnd(store);
			expect(store.getState().conversation?.items).toHaveLength(500);
			expect(store.getState().trimmedAbove).toBe(true);
		});

		it("never starts trimming on a pageBefore that isn't a boolean", async () => {
			const store = await openRows(new FakeConversationService(), false);
			store.getState().applyNotification(statusFrame("yes"));
			returnToEnd(store);
			expect(store.getState().conversation?.items).toHaveLength(600);
		});

		// Back at the end while an older page is in flight: the trim drops the
		// rows the page was asked from above, so the page is dropped too, or
		// merged it would leave those rows a hole nothing pages back.
		it("drops a page in flight when the return to the end trims", async () => {
			const { service, store } = await openedStore(makeConversation({ items: positionedRows(100, 700) }));
			store.setState({ olderCursor: "cursor-1" });
			store.getState().setFollowingLiveEnd(false);
			service.olderItems = { items: positionedRows(0, 100), nextCursor: "cursor-2", hasEarlierItems: true };
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.items[0]?.id).toBe("row-0");

			let deliver!: (page: unknown) => void;
			service.olderItems = new Promise((resolve) => {
				deliver = resolve;
			}) as never;
			const inFlight = store.getState().loadOlder(service);
			store.getState().setFollowingLiveEnd(true);
			expect(store.getState().loadingOlder).toBe(false);
			expect(store.getState().trimmedAbove).toBe(true);
			deliver({ turnsPage: { data: [], nextCursor: "cursor-3" }, nextCursor: "cursor-3" });
			expect(await inFlight).toEqual({ status: "ignored" });
			expect(store.getState().olderCursor).toBe("cursor-2");
			expect(store.getState().conversation?.items[0]?.id).toBe("row-200");
		});

		// A read of another instance replaces the window, and the rows it
		// trimmed with it.
		it("forgets a trim when a read replaces the instance", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: positionedRows(0, 600) });
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().trimmedAbove).toBe(true);
			service.openConv = makeConversation({ items: positionedRows(0, 10), instanceId: "instance-2" });
			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.items).toHaveLength(10);
			expect(store.getState().trimmedAbove).toBe(false);
		});

		// The hub answers stale only for a boundary the transcript doesn't hold:
		// the thread was reset or rewritten since the read. Re-read it, as for
		// any stale cursor, with or without a cursor held.
		it.each([
			["with a cursor", "cursor-1"],
			["without one", null],
		] as const)("re-reads a thread whose page above the trimmed rows comes back stale %s", async (_held, cursor) => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: positionedRows(0, 600) });
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: cursor });
			store.getState().setFollowingLiveEnd(false);
			const reads = service.readProjectionCalls.length;
			service.olderItems = Promise.reject(
				new WireError("stale transcript cursor", -32020, { evenerErrorInfo: "transcriptItemCursorStale" }),
			) as never;
			await store.getState().loadOlder(service);
			expect(service.readProjectionCalls).toHaveLength(reads + 1);
		});

		// The hub answers the page before the transcript's first item with an
		// empty, exhausted page: nothing is older, so the trim is spent.
		it("stops paging when the page above the trimmed rows is empty and exhausted", async () => {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ items: positionedRows(0, 600) });
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.getState().setFollowingLiveEnd(false);
			const reads = service.readProjectionCalls.length;
			service.olderItems = { turnsPage: { data: [] }, hasEarlierItems: false };
			await store.getState().loadOlder(service);
			expect(service.readProjectionCalls).toHaveLength(reads);
			expect(store.getState().trimmedAbove).toBe(false);
			expect(store.getState().olderCursor).toBeNull();
			expect(await store.getState().loadOlder(service)).toEqual({ status: "ignored" });
		});

		it("enforces a 500-item retained cap at the store level", async () => {
			const service = new FakeConversationService();
			// Generate 600 items from loadOlder; only 500 should be retained.
			const manyItems = Array.from({ length: 600 }, (_, i) => ({
				kind: "user" as const,
				id: `item-${i}`,
				text: `msg ${i}`,
			}));
			service.olderItems = { items: manyItems, nextCursor: undefined };
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			// Set a cursor so loadOlder has a page to request.
			store.setState({ olderCursor: "cursor-1" });
			await store.getState().loadOlder(service);
			const conv = store.getState().conversation;
			expect(conv).not.toBeNull();
			// The store should cap retained items at 500.
			expect(conv?.items.length).toBeLessThanOrEqual(500);
		});

		it("I7: prepends older items and retains the newest live tail at cap", async () => {
			const service = new FakeConversationService();
			// Create 600 existing items + 600 older items = 1200 total. Cap is 500.
			// Prepend ordering should keep the newest 500 (the live tail at the end
			// of the merged array), so the most recent items remain visible.
			const existingItems = Array.from({ length: 600 }, (_, i) => ({
				kind: "user" as const,
				id: `existing-${i}`,
				text: `existing ${i}`,
			}));
			const olderItems = Array.from({ length: 600 }, (_, i) => ({
				kind: "user" as const,
				id: `older-${i}`,
				text: `older ${i}`,
			}));
			service.openConv = makeConversation({ items: existingItems });
			service.olderItems = { items: olderItems, nextCursor: undefined };
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			// Set a cursor so loadOlder has a page to request.
			store.setState({ olderCursor: "cursor-1" });
			// open() caps existing items to 500 (newest)
			expect(store.getState().conversation?.items.length).toBe(500);
			await store.getState().loadOlder(service);
			const conv = store.getState().conversation;
			expect(conv?.items.length).toBe(500);
			// The newest items (from existing) should be at the tail, since
			// we trim from the oldest (front) in prepend mode to retain the
			// live tail.
			expect(conv?.items[499]?.id).toBe("existing-599");
		});

		// A trim is "the cap discarded rows", not "the final count reached the
		// cap": capItems slices to the newest RETAINED_ITEM_CAP rows and then
		// drops a leading attachment whose source fell off the cut, so a full
		// merge that drops an orphan ends below the cap.
		const pairSource = {
			kind: "user" as const,
			id: "src-1",
			text: "source row",
		};
		const pairAttachment = {
			kind: "attachments" as const,
			id: "src-1:attachments",
			items: [{ id: "img-1", src: "data:image/png;base64,AAA" }],
		};
		const fillers = (count: number) =>
			Array.from({ length: count }, (_, i) => ({
				kind: "user" as const,
				id: `filler-${i}`,
				text: `filler ${i}`,
			}));

		it("keeps paging when the cap discards a whole page while following, even though the orphan drop leaves the count below the cap", async () => {
			const service = new FakeConversationService();
			// Open with 502 rows whose 500-cut splits the pair: the attachment
			// survives the slice as its first row (index 2 of 502, the first the
			// newest-500 slice keeps), its source one slot earlier does not, and
			// the orphan drop leaves 499 retained rows.
			service.openConv = makeConversation({
				items: [{ kind: "user" as const, id: "head", text: "head row" }, pairSource, pairAttachment, ...fillers(499)],
			});
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			expect(store.getState().conversation?.items.length).toBe(499);
			store.setState({ olderCursor: "cursor-1" });
			// The page is the split pair again: the cap cuts the source and the
			// orphan drop removes the attachment, so this load retains nothing.
			service.olderItems = {
				items: [pairSource, pairAttachment],
				nextCursor: "cursor-2",
				hasEarlierItems: true,
			};
			await store.getState().loadOlder(service);
			const conv = store.getState().conversation;
			expect(conv?.items.some((row) => row.id === "src-1" || row.id === "src-1:attachments")).toBe(false);
			// Paging goes on: the page landed while the reader followed the live
			// end, so the cap trimmed it, and the next page names the oldest row
			// kept.
			expect(store.getState().olderCursor).toBe("cursor-2");
			expect(store.getState().trimmedAbove).toBe(true);
			expect(store.getState().hasEarlierItems).toBe(true);
		});

		it("keeps paging after a merge the cap did not trim, even when the retained count reaches the cap", async () => {
			const { service, store } = await openedStore(makeConversation({ items: fillers(499) }));
			store.setState({ olderCursor: "cursor-1" });
			// One older row: the merged projection reaches exactly the cap and
			// the pre-cap set never overflowed it, so the wire's own paging
			// signal stands.
			service.olderItems = {
				items: [pairSource],
				nextCursor: "cursor-2",
				hasEarlierItems: true,
			};
			await store.getState().loadOlder(service);
			const conv = store.getState().conversation;
			expect(conv?.items[0]?.id).toBe("src-1");
			expect(store.getState().olderCursor).toBe("cursor-2");
			expect(store.getState().hasEarlierItems).toBe(true);
		});
	});
});
