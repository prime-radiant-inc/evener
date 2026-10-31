// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { ThreadItem } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { createConversationStore } from "./conversation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	askUserItem,
	makeConversation,
	makeReceipt,
	makeThread,
	makeTurn,
	openProjectedThreadWith,
	openedStore,
	rows,
	textInput,
	turnsPage,
	userMessageItem,
	wireTurnFragment,
} from "./conversationTestUtils";

// --- store tests -------------------------------------------------------------

// RoboRev round 2 (panel Low): makeConversation's row-fixture conversion
// must fail loudly when a SUPPORTED shape breaks — the unconditional catch
// degraded a malformed fixture (or any hydration/projection failure) to the
// pre-D23d literal rows, so the store's open path ran against rows no
// re-projection could reproduce and a D23d regression in the page
// merge/projection passed unnoticed. Only the genuinely unsupported row
// kinds — the ones wirePageFromRows refuses on shape — may fall back.
describe("makeConversation's row-fixture conversion", () => {
	it("fails loudly when a supported fixture's conversion breaks instead of degrading to literal rows", () => {
		// An attachments row whose source key names no row in the same fixture
		// is a malformed SUPPORTED shape: wirePageFromRows refuses it, and the
		// refusal must surface rather than silently running the test on the
		// literal rows.
		expect(() =>
			makeConversation({
				items: [
					{ kind: "user", id: "u1", text: "hello" },
					{
						kind: "attachments",
						id: "no-source:attachments",
						items: [{ id: "a1", src: "data:image/png;base64,AAAA" }],
					},
				],
			}),
		).toThrow(/attachments row names no source row/);
	});

	it("keeps the literal fallback for rows no wire shape expresses", () => {
		// A notice row has no wire shape this seam can build: its tests read
		// the opened rows without re-projecting, so it must still fall back to
		// the literal items over a model with no turns.
		const conv = makeConversation({
			items: [
				{
					kind: "notice",
					id: "notice-1",
					origin: "system",
					family: "warning",
					tone: "warning",
					text: "careful",
				},
			],
		});
		expect(conv.turns).toEqual([]);
		expect(conv.items.map((row) => row.id)).toEqual(["notice-1"]);
	});

	// RoboRev round 5 (panel Low): the unsupported-row refusal must not end
	// validation. A malformed SUPPORTED row appearing AFTER an unsupported
	// one used to ride the literal fallback the refusal triggered — its
	// conversion never ran, so the shape failed silently. Every row
	// validates before the fallback fires.
	it("fails loudly when a malformed supported row follows an unsupported one", () => {
		expect(() =>
			makeConversation({
				items: [
					{
						kind: "notice",
						id: "notice-1",
						origin: "system",
						family: "warning",
						tone: "warning",
						text: "careful",
					},
					{ kind: "activity", id: "act-1", label: "shell", family: "tool", state: "failed", detail: {} },
				],
			}),
		).toThrow(/a failed activity row needs detail.error/);
	});
});

// RoboRev round 4 (panel M3): the fake's two loadOlder scripting seams are
// mutually exclusive — a script that set BOTH the legacy row fixture and a
// wire turns page used to discard the rows silently, the same silent
// fixture degradation class the conversion seam's UnsupportedRowShapeError
// fixed. The seam must throw, so a fixture that scripts both fails its test
// at setup instead of running against a page it did not describe.
describe("FakeConversationService.loadOlder's scripted page seams", () => {
	it("refuses a script that sets both items and turnsPage", async () => {
		const service = new FakeConversationService();
		// The union type keeps the seams disjoint for every static script;
		// the runtime throw covers the dynamically-built scripts this test
		// deliberately builds, so the cast scripts what the type forbids.
		service.olderItems = {
			turnsPage: turnsPage([], undefined),
			items: [{ kind: "user", id: "u1", text: "hello" }],
			nextCursor: undefined,
		} as unknown as typeof service.olderItems;
		await expect(service.loadOlder("cursor-1")).rejects.toThrow(/script either items or turnsPage, not both/);
	});
});

describe("ConversationStore", () => {
	describe("open", () => {
		it("transitions idle -> opening -> open and stores conversation", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			expect(store.getState().status).toBe("idle");
			const p = store.getState().open(service, "ref-1");
			expect(store.getState().status).toBe("opening");
			await p;
			const s = store.getState();
			expect(s.status).toBe("open");
			expect(s.ref).toBe("ref-1");
			expect(s.conversation).not.toBeNull();
			expect(s.conversation?.threadId).toBe("thread-1");
		});

		it("increments conversation generation on open", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const gen0 = store.getState().conversationGeneration;
			await store.getState().open(service, "ref-1");
			const gen1 = store.getState().conversationGeneration;
			expect(gen1).toBeGreaterThan(gen0);
		});

		it("stores olderCursor from conversation if available", async () => {
			const { store } = await openedStore(makeConversation());
			expect(store.getState().olderCursor).toBeNull();
		});
	});

	describe("loadOlder", () => {
		it("sets loadingOlder and prepends items", async () => {
			const service = new FakeConversationService();
			service.olderItems = {
				items: [{ kind: "user", id: "older-result", text: "older" }],
				nextCursor: "next",
			};
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			// Set a cursor so loadOlder has a page to request.
			store.setState({ olderCursor: "cursor-1" });
			const p = store.getState().loadOlder(service);
			expect(store.getState().loadingOlder).toBe(true);
			await expect(p).resolves.toEqual({
				status: "loaded",
				itemKeys: ["older-result"],
			});
			expect(store.getState().loadingOlder).toBe(false);
		});

		it("does not request when olderCursor is null and nothing was trimmed", async () => {
			const service = new FakeConversationService();
			service.olderItems = { items: [], nextCursor: "next" };
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			// olderCursor is null after open — loadOlder should not request.
			expect(store.getState().olderCursor).toBeNull();
			await expect(store.getState().loadOlder(service)).resolves.toEqual({
				status: "ignored",
			});
			expect(store.getState().loadingOlder).toBe(false);
		});
	});

	describe("the model owns the older page", () => {
		// D23d: loadOlder merges the page into the model with the package's
		// mergeOlderItemPage and the rows re-project from that model; the
		// service no longer projects display rows from the page
		// (projectOlderTurns and its fake Thread are gone), so the page's own
		// wire turns are the whole input.
		it("renders an older page's rows from the merged model", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [makeTurn({ id: "t1", status: "completed", items: [] })],
				}),
			);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{ ...userMessageItem("m1", "page text"), turnId: "t0", status: "completed" } as ThreadItem,
						]),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("loaded");
			if (result.status !== "loaded") return;
			expect(result.itemKeys).toContain("m1");
			// The page's row projects from the merged model the moment the page
			// lands...
			expect(rows(store).some((row) => row.kind === "user" && row.id === "m1" && row.text === "page text")).toBe(true);
			// ...the model carries the page's turn ahead of the live one...
			expect(store.getState().conversation?.turns.map((turn) => turn.id)).toEqual(["t0", "t1"]);
			// ...and the wire cursor is the model's own field, not a row-side copy.
			expect(store.getState().conversation?.olderCursor).toBe("c2");
			expect(store.getState().olderCursor).toBe("c2");
		});

		it("a page fragment's images supplement the live item the window holds sparse", async () => {
			// The window holds the item without its images; the older page
			// re-serves the same item carrying them. The package merge's fragment
			// rule (newer field ?? older field) folds the page's images in — the
			// web's rule (decision 2), now that rows project from the model and
			// no row filter or model-side strip runs ahead of the merge.
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [{ ...userMessageItem("user", "look"), turnId: "t1", status: "completed" } as ThreadItem],
						}),
					],
				}),
			);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{
								...userMessageItem("user", "look"),
								transcriptKey: "user",
								turnId: "t0",
								status: "completed",
								images: [{ type: "image", mediaType: "image/png", data: "AQID" }],
							} as ThreadItem,
						]),
					],
					undefined,
				),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			expect(rows(store).find((row) => row.kind === "attachments")).toMatchObject({
				id: "user:attachments",
				items: [{ src: "data:image/png;base64,AQID" }],
			});
		});

		it("renders a settled page ask as a tool row while a live ask stays the only question", async () => {
			// I3, measured: liveAskQuestions gates its whole scan on askPending
			// and excludes every ask before the newest resolution item (a user
			// message settles the whole pending set at once), so a page's
			// settled ask cannot project as an actionable question row once
			// pages merge into the model — the derivation is the filter, and the
			// service-side projection filter died with projectOlderTurns.
			const askArgs =
				'{"questions":[{"header":"Choose","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}';
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					evener: {
						ref: "ref-1",
						capabilities: { ...ALL_TRUE_CAPS },
						queue: { revision: 0 },
						askPending: true,
					},
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [askUserItem("ask-live", askArgs)],
						}),
					],
				}),
			);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[wireTurnFragment("t0", [askUserItem("ask-old", askArgs), userMessageItem("reply", "the answer")])],
					undefined,
				),
				nextCursor: undefined,
			};
			await store.getState().loadOlder(service);
			// The page's settled ask is history: a tool row, never a question row.
			expect(rows(store).some((row) => row.kind === "question" && row.id === "ask-old")).toBe(false);
			expect(rows(store).some((row) => row.kind === "activity" && row.id === "ask-old")).toBe(true);
			// The live ask keeps its answerable card.
			expect(rows(store).some((row) => row.kind === "question" && row.id === "ask-live")).toBe(true);
		});

		// Task 17 (v6 read path): "projects a failed page turn's failure row
		// from the model and keeps it against a clean covering reread" removed
		// here. It pinned page-owned content resisting a "clean covering"
		// rehydrate that carries no error for the turn — the pre-v6 claim
		// heuristic (settlePageFailureClaims), which read "the page owns this
		// failure" as reason enough to keep it even though a later read said
		// otherwise. mergeHistory replaces a turn's scalars (including error)
		// wholesale by VERSION, never by which side "claims" the content: a
		// turn's error genuinely persists across a covering refresh exactly
		// when that refresh's own version does not supersede the one that
		// recorded it (an equal-or-lower version, or none at all — this is
		// what a real daemon actually sends, since transcript entries are
		// permanent), which reducer.history.test.ts's merge-by-version suite
		// already covers directly.
	});

	describe("setDraft", () => {
		it("sets draft text", () => {
			const store = createConversationStore();
			store.getState().setDraft("hello world");
			expect(store.getState().draft).toBe("hello world");
		});
	});

	describe("send", () => {
		it("clears draft on success", async () => {
			const service = new FakeConversationService();
			service.receipt = makeReceipt({ clientMutationId: "cmid-99" });
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("my message");
			const p = store.getState().send(service, textInput("my message"));
			// While in-flight, the draft should be cleared.
			expect(store.getState().draft).toBe("");
			await p;
			expect(store.getState().draft).toBe("");
		});

		it("publishes a replayed Hub receipt as success without retry or failure", async () => {
			const service = new FakeConversationService();
			service.receipt = makeReceipt({ disposition: "replayed" });
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("idempotent retry");

			await store.getState().send(service, textInput("idempotent retry"));

			expect(service.sendCallCount).toBe(1);
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().error).toBeNull();
			expect(store.getState().lastAcceptedMutation).toEqual({
				kind: "send",
				receipt: expect.objectContaining({ disposition: "replayed" }),
			});
		});

		it("restores draft on conflict and shows error", async () => {
			const service = new FakeConversationService();
			service.sendShouldReject = new Error("conflict");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("my message");
			await store.getState().send(service, textInput("my message"));
			const s = store.getState();
			expect(s.draft).toBe("my message");
			expect(s.error).not.toBeNull();
		});

		it("does not auto-retry on failure", async () => {
			const service = new FakeConversationService();
			service.sendShouldReject = new Error("conflict");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("my message");
			await store.getState().send(service, textInput("my message"));
			expect(service.sendCallCount).toBe(1);
			// Wait a microtask to ensure no retry is scheduled
			await Promise.resolve();
			expect(service.sendCallCount).toBe(1);
		});
	});
});
