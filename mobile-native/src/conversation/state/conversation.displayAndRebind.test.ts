// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { MutationReceipt, Thread, ThreadCapabilities, ThreadItem } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import type {
	MutationAttachmentRef,
	MutationOutboxRecord,
	MutationPersistenceSnapshot,
} from "@evener/appwire-client/state/mutation";
import { describe, expect, it, vi } from "vitest";
import type { MobileConversation } from "../../projectedRows";
import * as project from "../../projectedRows";
import type { ConversationReadProjection } from "../services/conversation";
import { createConversationStore, MAX_ITEM_BYTES, TRUNCATION_MARKER } from "./conversation";
import type { ConversationMutationPendingPort } from "./conversationMutation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	agentMessageItem,
	askUserItem,
	commandExecItem,
	historyUpdated,
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
	reasoningItem,
	refusal,
	resyncFrame,
	rowById,
	rows,
	runningTurnThread,
	statusChanged,
	textInput,
	turnsPage,
	userMessageItem,
	warningFrame,
	wireTurn,
	wireTurnFragment,
	yieldMicrotask,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	// D24 slices 5+6: the conversation's projection follows the user's display
	// config. The level is store state (setDisplayConfig), so the projection
	// runs ONCE, at the user's level, through the same display boundary
	// (capAndTruncate: seating, cap, truncation) every other rebuild passes
	// through — no screen-level re-projection (the rejected D24-5 route
	// re-projected per render, thrashing the per-turn row cache and bypassing
	// the boundary).
	describe("the conversation's projection follows the user's display config", () => {
		const intentConfig = makeTranscriptDisplayConfig({
			kind: "preset",
			level: "intent",
		});
		const fullConfig = makeTranscriptDisplayConfig({
			kind: "preset",
			level: "full",
		});

		// One settled turn holding the level's three subjects: a summarized
		// tool action (a completed shell call whose description the projector
		// trims into its rationale, plus full arguments/output), a settled
		// thought, and a user message.
		function configLevelsThread(): Thread {
			return makeThread({
				turns: [
					makeTurn({
						id: "t0",
						status: "completed",
						items: [
							userMessageItem("u1", "please audit the config"),
							{
								...commandExecItem("c1", "shell", "completed"),
								description: "  run the audit  ",
								argumentsJson: JSON.stringify({ cmd: "ls" }),
								output: "tool output text",
							} as ThreadItem,
							reasoningItem("r1", "auditing quietly"),
						],
					}),
				],
			});
		}

		it("re-projects the live conversation when the level changes", async () => {
			const store = await openProjectedThread(configLevelsThread());
			// No config: the show-everything default the seam has always
			// projected, still on screen.
			expect(rowById(store, "r1")).toMatchObject({ kind: "activity" });
			expect(rowById(store, "c1")).toMatchObject({
				detail: { output: "tool output text" },
			});

			store.getState().setDisplayConfig(intentConfig);
			expect(store.getState().displayConfig).toEqual(intentConfig);
			// The settled thought is gone at intent; the summarized tool row
			// carries only its trimmed summary line (the summary-only ruling).
			expect(rowById(store, "r1")).toBeUndefined();
			const c1 = rowById(store, "c1");
			expect(c1).toMatchObject({
				kind: "activity",
				detail: { description: "run the audit" },
			});
			expect(JSON.stringify(rows(store))).not.toContain("tool output text");

			store.getState().setDisplayConfig(fullConfig);
			expect(rowById(store, "r1")).toMatchObject({ kind: "activity" });
			expect(rowById(store, "c1")).toMatchObject({
				detail: { output: "tool output text" },
			});
		});

		it("treats a value-equal config as no change — the boundary does not re-run", async () => {
			const store = await openProjectedThread(configLevelsThread());
			store.getState().setDisplayConfig(intentConfig);
			const before = store.getState().conversation;
			expect(before).not.toBeNull();
			// The provider hands the store a fresh object per publish; the
			// level is keyed by the config's VALUE (configFingerprint), so an
			// equal value republishes nothing.
			store.getState().setDisplayConfig(makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }));
			expect(store.getState().conversation).toBe(before);
		});

		it("runs the level re-projection through the same display boundary", async () => {
			// An oversized summary line is bounded in the published rows exactly
			// as any other row text: capAndTruncate seats, caps and truncates
			// once, on the config-projected rows.
			const hugeSummary = "s".repeat(MAX_ITEM_BYTES + 100);
			const thread = makeThread({
				turns: [
					makeTurn({
						id: "t0",
						status: "completed",
						items: [{ ...commandExecItem("c1", "shell", "completed"), description: hugeSummary } as ThreadItem],
					}),
				],
			});
			const store = await openProjectedThread(thread);
			store.getState().setDisplayConfig(intentConfig);
			const c1 = rowById(store, "c1");
			expect(c1).toMatchObject({ kind: "activity" });
			const summary = c1?.kind === "activity" ? c1.detail.description : undefined;
			if (summary === undefined) throw new Error("no summarized row");
			expect(summary.endsWith(TRUNCATION_MARKER)).toBe(true);
		});

		// Review round 2 (Medium): a level change hides rows without
		// withdrawing them from the model, and a transient warning must not
		// retire just because the row it anchored to became hidden — the
		// store re-anchors it to the nearest preceding row the new level
		// still shows, so it stays on screen at the compact level and comes
		// back when the level does.
		it("keeps a transient warning whose anchor the level hides", async () => {
			// The warning lands over an idle thread — no active turn to fold
			// into — so the store holds it on its transient surface, anchored
			// to the last model row, the thought.
			const thread = makeThread({
				turns: [
					makeTurn({
						id: "t1",
						status: "completed",
						items: [userMessageItem("u1", "please audit"), reasoningItem("r1", "the settled thought that anchors")],
					}),
				],
			});
			// Versioned: turn/started and item/completed are gone (Task 15); a
			// new turn and its item now arrive together in one history/updated
			// frame, which only merges against a v6 model.
			const store = await openProjectedThread(thread, true);
			store.getState().applyNotification(warningFrame({ title: "Provider", message: "careful" }));
			// A later reply arrives on a new turn, so the anchored thought
			// sits mid-timeline.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
					items: [{ ...agentMessageItem("a2", "working on it", "completed"), turnId: "t2" }],
				}),
			);
			expect(rows(store).some((row) => row.kind === "failure" && row.id === "warning:1")).toBe(true);

			// At intent the thought is hidden; the warning re-anchors to the
			// user message above it and stays on screen.
			store.getState().setDisplayConfig(intentConfig);
			expect(rows(store).some((row) => row.kind === "failure" && row.id === "warning:1")).toBe(true);

			// And it survives the round trip back to full.
			store.getState().setDisplayConfig(fullConfig);
			expect(rows(store).some((row) => row.kind === "failure" && row.id === "warning:1")).toBe(true);
		});

		// Review round 3 (Medium), superseding round 2's Low: the bound RUNS
		// at every publish (round 2), but its keep-window is LEVEL-INDEPENDENT
		// — the display level decides what renders, never what payload leaves
		// the model. A window taken from the level's own rows would shed every
		// turn the level hides, and the switch back to the richer level could
		// not rebuild what the re-projection reads: the payloads would be gone
		// from the model. The show-everything cap is the retention truth; only
		// the cap window decides what leaves.
		it("keeps a level-hidden turn's payload for the switch back", async () => {
			const thread = makeThread({
				turns: [
					makeTurn({
						id: "t0",
						status: "completed",
						items: [reasoningItem("r1", "the thought")],
					}),
					makeTurn({
						id: "t1",
						status: "completed",
						items: [userMessageItem("u1", "hi")],
					}),
				],
			});
			const store = await openProjectedThread(thread);
			// Show-everything keeps both turns' payloads.
			expect(store.getState().conversation?.turns[0]?.items).toHaveLength(1);
			store.getState().setDisplayConfig(intentConfig);
			// t0's every row is hidden at intent — hidden, not withdrawn: the
			// payload stays in the model.
			expect(store.getState().conversation?.turns[0]?.items).toHaveLength(1);
			expect(rowById(store, "r1")).toBeUndefined();
			// Switch back: the hidden row reappears from the kept payload.
			store.getState().setDisplayConfig(fullConfig);
			expect(rowById(store, "r1")).toMatchObject({ kind: "activity" });
		});

		// Review round 3 (Medium, the frame publish): a row-changing frame
		// while at a coarse level bounds against the same level-independent
		// window — the frame must not shed the payload of a turn the level
		// hides either, or the switch back would lose it the same way.
		it("a row-changing frame at a coarse level keeps the hidden turn's payload", async () => {
			const thread = makeThread({
				turns: [
					makeTurn({
						id: "t0",
						status: "completed",
						items: [reasoningItem("r1", "the thought")],
					}),
					makeTurn({
						id: "t1",
						status: "completed",
						items: [userMessageItem("u1", "hi")],
					}),
				],
			});
			// Versioned: turn/started is gone (Task 15); a new turn now arrives
			// as a history/updated frame, which only merges against a v6 model.
			const store = await openProjectedThread(thread, true);
			store.getState().setDisplayConfig(intentConfig);
			// A row-changing frame lands at intent: a new turn starts.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "t2", itemsView: "default", status: "inProgress" }],
				}),
			);
			expect(store.getState().conversation?.turns[0]?.items).toHaveLength(1);
			store.getState().setDisplayConfig(fullConfig);
			expect(rowById(store, "r1")).toMatchObject({ kind: "activity" });
		});

		// RoboRev panel: the rehydrate bound against its level-projected window
		// too — a rehydrate at a coarse level skeletonized the turns it hid, so
		// the switch back to full rendered them empty. The keep-window is the
		// level-independent union at this site as well.
		it("a rehydrate at a coarse level keeps the hidden turn's payload", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [userMessageItem("u1", "hi")],
						}),
					],
				}),
			);
			// The older page lands at the show-everything default: its payload
			// is retained, and the turn is PAGE HISTORY now — the rehydrate's
			// merge path runs over it.
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{ ...reasoningItem("r1", "the thought", "completed"), turnId: "t0" } as ThreadItem,
						]),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("loaded");
			expect(store.getState().conversation?.turns[0]?.items).toHaveLength(1);
			store.getState().setDisplayConfig(intentConfig);
			// The rehydrate re-reads at the coarse level; the level-hidden turn's
			// payload must survive the merge's bound.
			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.turns[0]?.items).toHaveLength(1);
			store.getState().setDisplayConfig(fullConfig);
			expect(rowById(store, "r1")).toMatchObject({ kind: "activity" });
		});

		// RoboRev panel: the loadOlder bound against its level-projected window
		// as well. The page's own accounting (the admitted item keys, F10's
		// inputs) is level-independent; only the payload bound was not.
		it("an older page loaded at a coarse level keeps the hidden turn's payload", async () => {
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [
						makeTurn({
							id: "t1",
							status: "completed",
							items: [userMessageItem("u1", "hi")],
						}),
					],
				}),
			);
			store.getState().setDisplayConfig(intentConfig);
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{ ...reasoningItem("r1", "the thought", "completed"), turnId: "t0" } as ThreadItem,
						]),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			const result = await store.getState().loadOlder(service);
			expect(result.status).toBe("loaded");
			// The level-hidden turn's payload survives the merge.
			expect(store.getState().conversation?.turns[0]?.items).toHaveLength(1);
			store.getState().setDisplayConfig(fullConfig);
			expect(rowById(store, "r1")).toMatchObject({ kind: "activity" });
		});
	});

	// A page load republishes every retained row through the same bound. The row
	// a reader sees carries its BOUNDED text, so re-bounding it means re-cutting
	// 64 KiB again per publish unless the cache answers — and the cache is what
	// makes a page load cost only the page's own rows.
	// Whether the bound cut a row whose text is made of `filler`: a truncateText
	// call whose (oversized) argument carries that filler is the bound doing the
	// work again, not a cache hit.
	function cutsOf(truncate: { mock: { calls: readonly unknown[][] } }, filler: string): number {
		return truncate.mock.calls.filter(
			(call) => typeof call[0] === "string" && call[0].length > 1_000 && call[0].startsWith(filler),
		).length;
	}

	it("does not re-cut an unchanged oversized row when an older page lands", async () => {
		const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
		const olderOversized = "y".repeat(MAX_ITEM_BYTES + 100);
		const service = new FakeConversationService();
		service.readProjectionResult = {
			...makeReadProjectionResult(runningTurnThread([agentMessageItem("a1", oversized, "completed")])),
			olderCursor: "cursor-1",
		};
		service.olderItems = {
			items: [{ kind: "assistant", id: "old", markdown: olderOversized, streaming: false }],
		};
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		const row = rowById(store, "a1");
		const bounded = row && "markdown" in row ? row.markdown : undefined;
		if (bounded === undefined) throw new Error("no bounded row");
		expect(bounded.endsWith(TRUNCATION_MARKER)).toBe(true);

		// Cutting a row's text calls truncateText on it; a row that comes out of
		// the cache is not cut at all, so truncateText never sees its text.
		const truncate = vi.spyOn(project, "truncateText");
		try {
			await store.getState().loadOlder(service);
			expect(rowById(store, "old")).toBeDefined();
			// The page's own oversized row is the new work.
			expect(cutsOf(truncate, "y")).toBeGreaterThan(0);
			// The row already on screen is not: its bounded text comes from the cache.
			expect(cutsOf(truncate, "x")).toBe(0);
		} finally {
			truncate.mockRestore();
		}
	});

	// A delta re-projects the turn it landed in, not the transcript. The reducer
	// hands every other turn back by reference, so their rows come from the
	// per-turn cache — observable through the work the projection would otherwise
	// repeat: a settled tool call's duration costs two Date.parse calls per
	// projection, and an older turn's must not be paid again for a delta into the
	// newest one.

	// The bound's cache belongs to the conversation: every string in it is held
	// by that conversation's model or its rows, so a conversation that has been
	// dropped must not leave its text behind in the cache. Observable without a
	// test-only accessor, through the work the cache avoids: inside one
	// conversation a settled row is not re-encoded, and a conversation opened
	// after the old one was dropped encodes its text again.
	describe("the display bound's cache belongs to the conversation", () => {
		// Oversized, because that is the row the cache exists for: a row under the
		// limit is measured without allocating and never reaches the encoder.
		const settledText = "z".repeat(MAX_ITEM_BYTES + 100);

		function encodesOfSettledText(truncate: { mock: { calls: readonly unknown[][] } }): number {
			return cutsOf(truncate, "z");
		}

		it.each([
			["close", (store: ReturnType<typeof createConversationStore>) => store.getState().close()],
			["reset", (store: ReturnType<typeof createConversationStore>) => store.getState().reset()],
		] as const)("releases it on %s", async (_label, drop) => {
			const items = [agentMessageItem("a1", settledText, "completed")];
			const { store, service } = await openRunningTurn(items);

			const truncate = vi.spyOn(project, "truncateText");
			try {
				// Within the conversation the cache holds: a frame republishes the
				// rows and the unchanged text is taken from it, not re-cut.
				truncate.mockClear();
				store.getState().applyNotification(statusChanged({ type: "running" }));
				expect(encodesOfSettledText(truncate)).toBe(0);

				drop(store);

				// The next conversation binds its own text: nothing was carried over.
				truncate.mockClear();
				service.readProjectionResult = makeReadProjectionResult(runningTurnThread(items));
				await store.getState().openProjected(service, "ref-1");
				expect(encodesOfSettledText(truncate)).toBeGreaterThan(0);
			} finally {
				truncate.mockRestore();
			}
		});

		// suspendProjected keeps the conversation and its rows on screen for the
		// resume, so the cache still belongs to something live: every string in it
		// is still retained by those rows, clearing it would recover no memory,
		// and the resume's re-read republishes the same text.
		it("keeps it across a suspend and resume, which keep the rows", async () => {
			const items = [agentMessageItem("a1", settledText, "completed")];
			const { store, service } = await openRunningTurn(items);

			store.getState().suspendProjected();
			const truncate = vi.spyOn(project, "truncateText");
			try {
				service.readProjectionResult = makeReadProjectionResult(runningTurnThread(items));
				await store.getState().resumeProjected(service, "ref-1");
				expect(encodesOfSettledText(truncate)).toBe(0);
			} finally {
				truncate.mockRestore();
			}
		});
	});

	// The display bound runs over every retained row on every publish. The model
	// hands back the same string reference for an item no frame touched, so only
	// the rows whose text actually changed are re-encoded; a transcript of
	// settled rows costs nothing per delta.

	// The one path the table above does not walk: a reread's own snapshot, where the
	// authoritative short version arrives as a read rather than a settle.
	describe("the byte bound across a reread", () => {
		it("bounds an oversized row on open and shows the reread's short version", async () => {
			const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
			const { service, store } = await openProjectedThreadWith(
				makeThread({
					turns: [makeTurn({ id: "t0", items: [agentMessageItem("X", oversized, "completed")] })],
				}),
			);
			const bounded = rowById(store, "X");
			expect(bounded?.kind === "assistant" && bounded.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);

			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					turns: [makeTurn({ id: "t0", items: [agentMessageItem("X", "short", "completed")] })],
				}),
			);
			await store.getState().rehydrate(service);
			expect(rowById(store, "X")).toMatchObject({ kind: "assistant", markdown: "short" });
		});
	});

	describe("C1: wrong-service send/queue/interrupt => zero A calls", () => {
		for (const kind of ["send", "queue", "interrupt"] as const) {
			it(`wrong-service ${kind} => zero B calls, zero state change`, async () => {
				const serviceA = new FakeConversationService();
				serviceA.openConv = makeConversation({ status: { type: kind === "send" ? "idle" : "active" } });
				const store = createConversationStore();
				await store.getState().openProjected(serviceA, "ref-1");
				const pendingBefore = store.getState().pendingSubmission;
				const failedBefore = store.getState().failedSubmission;
				const draftBefore = store.getState().draft;
				const errorBefore = store.getState().error;
				const serviceB = new FakeConversationService();
				// Call with wrong serviceB — rejected at the boundary.
				if (kind === "send") await store.getState().send(serviceB, textInput("x"));
				else if (kind === "queue") await store.getState().queue(serviceB, textInput("x"));
				else await store.getState().interrupt(serviceB);
				// C1: zero state change — pending/draft/error unchanged.
				expect(store.getState().pendingSubmission).toBe(pendingBefore);
				expect(store.getState().failedSubmission).toBe(failedBefore);
				expect(store.getState().draft).toBe(draftBefore);
				expect(store.getState().error).toBe(errorBefore);
				// C1: serviceB mutation method was never called.
				if (kind === "send") expect(serviceB.sendCallCount).toBe(0);
				else if (kind === "queue") expect(serviceB.queueCallCount).toBe(0);
				else expect(serviceB.interruptCallCount).toBe(0);
			});
		}
	});

	// --- I1: Error ownership — revision equality ONLY --------------------------------

	describe("newer clear-to-null and ABA-null own error", () => {
		it("page failure after newer clear-to-null: page settles loading, preserves null (no write)", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// Start loadOlder that will fail (hangs first). It captures entryErrorRev.
			const rejectOlderHolder: { fn?: (e: Error) => void } = {};
			const hangOlder = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((_, reject) => {
				rejectOlderHolder.fn = reject;
			});
			service.olderItems = hangOlder as never;
			const olderP = store.getState().loadOlder(service);
			expect(store.getState().loadingOlder).toBe(true);

			// While L is in-flight, a send fails (sets error, increments errorOwnerRev),
			// then another send succeeds (clears error to null, increments errorOwnerRev
			// again). Now errorOwnerRev has advanced past L's captured entryErrorRev.
			// Error is null — this is a newer clear-to-null that owns the error.
			service.sendShouldReject = new Error("initial error");
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBe("initial error");
			service.sendShouldReject = null;
			await store.getState().send(service, textInput("y"));
			expect(store.getState().error).toBeNull();

			// Now L fails — it must settle loadingOlder but NOT write error
			// (errorOwnerRev changed during the await — the send's clear-to-null
			// is a newer error owner).
			rejectOlderHolder.fn?.(new Error("page boom"));
			await olderP.catch(() => {});
			expect(store.getState().loadingOlder).toBe(false);
			// Error stays null — the send's clear-to-null owns it.
			expect(store.getState().error).toBeNull();
		});

		it("rehydrate already-null during mutation: no spurious errorOwnerRev increment, send clears pending", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");

			// Set an error via a failed send, then clear with a succeeding send.
			service.sendShouldReject = new Error("initial error");
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBe("initial error");
			service.sendShouldReject = null;
			await store.getState().send(service, textInput("clear"));
			expect(store.getState().error).toBeNull();

			// Start a send that hangs.
			const resolveSendHolder: { fn?: (r: MutationReceipt) => void } = {};
			const hangSend = new Promise<MutationReceipt>((r) => {
				resolveSendHolder.fn = r;
			});
			service.send = async () => hangSend;
			const sendP = store.getState().send(service, textInput("y"));
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });

			// While send is in-flight, a rehydrate on the SAME service clears error.
			// Error is already null — rehydrate does NOT increment errorOwnerRev
			// (the already-null guard prevents ABA-null from a no-op clear).
			await store.getState().rehydrate(service);
			expect(store.getState().error).toBeNull();

			// Now the send succeeds. Since errorOwnerRev did NOT change (rehydrate
			// didn't clear a non-null error), the send CAN clear error (to null).
			// This exercises the already-null no-spurious-increment path, NOT a
			// genuine newer clear-to-null race.
			resolveSendHolder.fn?.(makeReceipt());
			await sendP;
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().error).toBeNull();
		});

		it("genuine newer null-owner race: page failure cannot overwrite mutation's null owner while mutation pending", async () => {
			// Load-bearing ordering: hold a page failure and hold a newer mutation.
			// The page captures the OLD error rev (before the mutation starts).
			// The newer mutation start writes the newer null owner (clears error
			// via set on submit, incrementing errorOwnerRev). While the mutation
			// is still pending, reject/await the PAGE — it must NOT write its old
			// error because errorOwnerRev advanced. Assert error remains null and
			// mutation remains pending. Only afterward resolve the mutation and
			// finish. This prevents the mutation success's null from masking the
			// page behavior — the page rejection is observed BEFORE the mutation
			// settles.
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");

			// Set a non-null error first so the page failure captures an old
			// errorOwnerRev (error is non-null).
			service.sendShouldReject = new Error("initial error");
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBe("initial error");

			// Start a page failure that hangs — it captures entryErrorRev while
			// error is "initial error" (old rev).
			store.setState({ olderCursor: "cursor-1" });
			const rejectPageHolder: { fn?: (e: Error) => void } = {};
			const hangPageFail = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((_, reject) => {
				rejectPageHolder.fn = reject;
			});
			service.olderItems = hangPageFail as never;
			const pageP = store.getState().loadOlder(service);
			expect(store.getState().loadingOlder).toBe(true);

			// Start a newer send that hangs — the send action clears error to null
			// on submit (set error: null), incrementing errorOwnerRev. This is the
			// newer null owner. The page's captured entryErrorRev is now stale.
			service.sendShouldReject = null;
			const resolveSendHolder: { fn?: (r: MutationReceipt) => void } = {};
			const hangSend = new Promise<MutationReceipt>((r) => {
				resolveSendHolder.fn = r;
			});
			service.send = async () => hangSend;
			const sendP = store.getState().send(service, textInput("y"));
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			// The send's submit cleared the old error to null (newer null owner).
			expect(store.getState().error).toBeNull();

			// Now reject the PAGE while the mutation is still pending. The page
			// captured the old entryErrorRev (when error was "initial error").
			// errorOwnerRev has since advanced (the send's clear). The page must
			// settle loadingOlder but NOT write its error — an incorrect old page
			// error would be visible here.
			rejectPageHolder.fn?.(new Error("page boom"));
			await pageP.catch(() => {});
			expect(store.getState().loadingOlder).toBe(false);
			// Error remains null — the page's old error rev cannot overwrite
			// the newer null owner.
			expect(store.getState().error).toBeNull();
			// Mutation remains pending — the page rejection did not settle it.
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });

			// Only now resolve the mutation success. The send captured
			// entryErrorRev after its own clear, so it can clear error. But error
			// is already null — the key assertion is that the page never wrote.
			resolveSendHolder.fn?.(makeReceipt());
			await sendP;
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
			// Error stays null throughout — page never wrote, send's null held.
			expect(store.getState().error).toBeNull();
		});

		it("mutation failure after newer ABA-null: installs failed pending, preserves null", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");

			// Set an error via a failed send, then clear with a succeeding send.
			service.sendShouldReject = new Error("boom");
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBe("boom");
			service.sendShouldReject = null;
			await store.getState().send(service, textInput("clear"));
			expect(store.getState().error).toBeNull();

			// Start a send that hangs then fails.
			const rejectSendHolder: { fn?: (e: Error) => void } = {};
			const hangFail = new Promise<MutationReceipt>((_, reject) => {
				rejectSendHolder.fn = reject;
			});
			service.send = async () => hangFail;
			const sendP = store.getState().send(service, textInput("y"));

			// While send is in-flight, set error via a page failure, then rehydrate
			// clears it to null — this is a real ABA-null (error was non-null).
			store.setState({ olderCursor: "cursor-1" });
			service.olderItems = Promise.reject(new Error("page boom")) as never;
			await store
				.getState()
				.loadOlder(service)
				.catch(() => {});
			expect(store.getState().error).toBe("page boom");
			// Rehydrate on the SAME service clears the non-null error — increments
			// errorOwnerRev. This is a real newer error owner (clear of non-null).
			await store.getState().rehydrate(service);
			expect(store.getState().error).toBeNull();

			// Now the send fails — it must install failed pending but NOT write
			// error (errorOwnerRev changed — the rehydrate's clear owns it).
			rejectSendHolder.fn?.(new Error("mutation boom"));
			await sendP.catch(() => {});
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
			// Error stays null — the rehydrate's clear owns it.
			expect(store.getState().error).toBeNull();
		});

		it("held page failure after newer mutation error: page settles loading, preserves mutation error", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// Start loadOlder that will fail (hangs first).
			const rejectOlderHolder: { fn?: (e: Error) => void } = {};
			const hangOlder = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((_, reject) => {
				rejectOlderHolder.fn = reject;
			});
			service.olderItems = hangOlder as never;
			const olderP = store.getState().loadOlder(service);

			// While L is in-flight, a send fails with a non-null error.
			service.sendShouldReject = new Error("mutation boom");
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBe("mutation boom");

			// Now L fails — it must settle loadingOlder but NOT overwrite the error.
			rejectOlderHolder.fn?.(new Error("page boom"));
			await olderP.catch(() => {});
			expect(store.getState().loadingOlder).toBe(false);
			expect(store.getState().error).toBe("mutation boom");
		});

		it("held mutation failure after newer page error: installs failed pending, preserves page error", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.setState({ olderCursor: "cursor-1" });

			// Start a send that hangs then fails.
			const rejectSendHolder: { fn?: (e: Error) => void } = {};
			const hangFail = new Promise<MutationReceipt>((_, reject) => {
				rejectSendHolder.fn = reject;
			});
			service.sendShouldReject = null;
			service.send = async () => hangFail;
			const sendP = store.getState().send(service, textInput("x"));

			// While send is in-flight, loadOlder fails with a non-null error.
			service.olderItems = Promise.reject(new Error("page boom")) as never;
			await store
				.getState()
				.loadOlder(service)
				.catch(() => {});
			expect(store.getState().error).toBe("page boom");

			// Now the send fails — it must install failed pending but NOT overwrite.
			rejectSendHolder.fn?.(new Error("mutation boom"));
			await sendP.catch(() => {});
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
			expect(store.getState().error).toBe("page boom");
		});
	});

	// --- I5: Binding safety — full state snapshot comparison -------------------------

	describe("rebind A->B then late A completion => zero state change (full snapshot)", () => {
		// Helper: snapshot the entire public state for comparison.
		function snapshotState(store: ReturnType<typeof createConversationStore>) {
			const s = store.getState();
			return {
				ref: s.ref,
				profileId: s.profileId,
				connectionGeneration: s.connectionGeneration,
				conversationGeneration: s.conversationGeneration,
				conversation: s.conversation,
				olderCursor: s.olderCursor,
				loadingOlder: s.loadingOlder,
				status: s.status,
				error: s.error,
				draft: s.draft,
				pendingSubmission: s.pendingSubmission,
				failedSubmission: s.failedSubmission,
			};
		}

		it("page: controlled A->B then late A success => entire state unchanged", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-A" }),
				olderCursor: "cursor-A",
			};
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			store.setState({ olderCursor: "cursor-A" });

			// Start loadOlder on A that hangs (success).
			const resolveOlderHolder: { fn?: () => void } = {};
			const hangOlder = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((r) => {
				resolveOlderHolder.fn = () => r({ items: [{ kind: "user", id: "old-A", text: "old" }] });
			});
			serviceA.olderItems = hangOlder as never;
			const olderP = store.getState().loadOlder(serviceA);

			// Rebind to B via rehydrate.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-B" }),
				olderCursor: null,
			};
			await store.getState().rehydrate(serviceB);

			// Snapshot state after rebind.
			const stateAfterRebind = snapshotState(store);
			const readsA = serviceA.readProjectionCalls.length;

			// Now A's loadOlder resolves — zero state change.
			resolveOlderHolder.fn?.();
			await olderP;

			// C1/I5: entire public state must be identical.
			expect(snapshotState(store)).toEqual(stateAfterRebind);
			// No additional A reads.
			expect(serviceA.readProjectionCalls.length).toBe(readsA);
		});

		it("page: controlled A->B then late A failure => entire state unchanged", async () => {
			const serviceA = new FakeConversationService();
			serviceA.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-A" }),
				olderCursor: "cursor-A",
			};
			const store = createConversationStore();
			await store.getState().openProjected(serviceA, "ref-A");
			store.setState({ olderCursor: "cursor-A" });

			// Start loadOlder on A that hangs (will fail).
			const rejectOlderHolder: { fn?: (e: Error) => void } = {};
			const hangOlder = new Promise<{
				items: MobileConversation["items"];
				nextCursor?: string;
			}>((_, reject) => {
				rejectOlderHolder.fn = reject;
			});
			serviceA.olderItems = hangOlder as never;
			const olderP = store.getState().loadOlder(serviceA);

			// Rebind to B.
			const serviceB = new FakeConversationService();
			serviceB.readProjectionResult = {
				conversation: makeConversation({ threadId: "thread-B" }),
				olderCursor: null,
			};
			await store.getState().rehydrate(serviceB);

			const stateAfterRebind = snapshotState(store);

			// A's loadOlder fails — zero state change.
			rejectOlderHolder.fn?.(new Error("A page boom"));
			await olderP.catch(() => {});

			expect(snapshotState(store)).toEqual(stateAfterRebind);
		});

		// Table: late A failure for send/queue/interrupt
		for (const kind of ["send", "queue", "interrupt"] as const) {
			it(`mutation: controlled A->B then late A ${kind} failure => entire state unchanged`, async () => {
				const serviceA = new FakeConversationService();
				serviceA.readProjectionResult = {
					conversation: makeConversation({
						threadId: "thread-A",
						status: { type: kind === "send" ? "idle" : "active" },
					}),
					olderCursor: null,
				};
				const store = createConversationStore();
				await store.getState().openProjected(serviceA, "ref-A");

				// Start a mutation on A that hangs then fails.
				const rejectMutationHolder: { fn?: (e: Error) => void } = {};
				const hangFail = new Promise<MutationReceipt>((_, reject) => {
					rejectMutationHolder.fn = reject;
				});
				if (kind === "send") {
					serviceA.sendShouldReject = null;
					serviceA.send = async () => hangFail;
				} else if (kind === "queue") {
					serviceA.queue = async () => hangFail;
				} else {
					serviceA.interrupt = async () => hangFail;
				}

				let mutationP: Promise<void>;
				if (kind === "send") mutationP = store.getState().send(serviceA, textInput("x"));
				else if (kind === "queue") mutationP = store.getState().queue(serviceA, textInput("x"));
				else mutationP = store.getState().interrupt(serviceA);
				expect(store.getState().pendingSubmission).toEqual({ kind });

				// Rebind to B.
				const serviceB = new FakeConversationService();
				serviceB.readProjectionResult = {
					conversation: makeConversation({ threadId: "thread-B" }),
					olderCursor: null,
				};
				await store.getState().rehydrate(serviceB);

				const stateAfterRebind = snapshotState(store);
				const callsA = serviceA.sendCallCount + serviceA.queueCallCount + serviceA.interruptCallCount;

				// A's mutation fails — zero state change.
				rejectMutationHolder.fn?.(new Error("A mutation boom"));
				await mutationP.catch(() => {});

				expect(snapshotState(store)).toEqual(stateAfterRebind);
				// No additional A mutation calls after rebind.
				const callsAAfter = serviceA.sendCallCount + serviceA.queueCallCount + serviceA.interruptCallCount;
				expect(callsAAfter).toBe(callsA);
			});
		}

		// Table: late A SUCCESS for send/queue/interrupt — full object reference + notification count
		for (const kind of ["send", "queue", "interrupt"] as const) {
			it(`mutation: controlled A->B then late A ${kind} success => entire state unchanged`, async () => {
				const serviceA = new FakeConversationService();
				serviceA.readProjectionResult = {
					conversation: makeConversation({
						threadId: "thread-A",
						status: { type: kind === "send" ? "idle" : "active" },
					}),
					olderCursor: null,
				};
				const store = createConversationStore();
				await store.getState().openProjected(serviceA, "ref-A");

				// Start a mutation on A that hangs then succeeds.
				const resolveMutationHolder: { fn?: (r: MutationReceipt) => void } = {};
				const hangSuccess = new Promise<MutationReceipt>((r) => {
					resolveMutationHolder.fn = r;
				});
				if (kind === "send") {
					serviceA.sendShouldReject = null;
					serviceA.send = async () => hangSuccess;
				} else if (kind === "queue") {
					serviceA.queue = async () => hangSuccess;
				} else {
					serviceA.interrupt = async () => hangSuccess;
				}

				let mutationP: Promise<void>;
				if (kind === "send") mutationP = store.getState().send(serviceA, textInput("x"));
				else if (kind === "queue") mutationP = store.getState().queue(serviceA, textInput("x"));
				else mutationP = store.getState().interrupt(serviceA);

				// Rebind to B — the rebind transition settles A's pending attempt
				// (clears the click-to-commit marker) and increments the binding
				// epoch.
				// The service binding changes, but ref and conversation generation
				// must remain exact so late-A suppression is proven by binding identity,
				// not by ordinary ref/generation staleness.
				const refBeforeRebind = store.getState().ref;
				const generationBeforeRebind = store.getState().conversationGeneration;
				const serviceB = new FakeConversationService();
				serviceB.readProjectionResult = {
					conversation: makeConversation({ threadId: "thread-B" }),
					olderCursor: null,
				};
				await store.getState().rehydrate(serviceB);

				// After rebind to B fully completes, explicitly prove ref and
				// generation did not change; only service identity changed.
				const stateB = store.getState();
				expect(stateB.ref).toBe(refBeforeRebind);
				expect(stateB.conversationGeneration).toBe(generationBeforeRebind);
				expect(stateB.conversation?.threadId).toBe("thread-B");

				// A's pending attempt was settled by the rebind — verify both
				// markers are null and no failure was retained for it.
				expect(stateB.pendingSubmission).toBeNull();
				expect(stateB.failedSubmission).toBeNull();

				// Capture the EXACT full state object reference. Not a partial
				// snapshot — the entire public state object must be the same
				// reference after late A success (zero Zustand set => same object).
				const exactStateAfterB = store.getState();

				// Subscribe and count ALL Zustand notifications. Any set() call
				// produces a notification, so notification count 0 proves zero set.
				let notificationCount = 0;
				const unsub = store.subscribe(() => {
					notificationCount++;
				});

				const callsA = serviceA.sendCallCount + serviceA.queueCallCount + serviceA.interruptCallCount;
				const readsA = serviceA.readProjectionCalls.length;

				try {
					// A's mutation succeeds — zero state change. The operation binding
					// is stale (isOperationBindingCurrent returns false because the
					// binding epoch/service changed), so the success path returns
					// before any set() call. No Zustand set, no notification.
					resolveMutationHolder.fn?.(makeReceipt());
					await mutationP;

					// The entire public state object must be the SAME reference (===).
					// No partial/deep snapshot — if any set() was called, Zustand
					// returns a new object and this fails.
					expect(store.getState()).toBe(exactStateAfterB);
					// Notification count 0 — no set() fired.
					expect(notificationCount).toBe(0);
					// ref and generation remain the exact pre-rebind values.
					expect(store.getState().ref).toBe(refBeforeRebind);
					expect(store.getState().conversationGeneration).toBe(generationBeforeRebind);
					// No additional A reads after rebind.
					expect(serviceA.readProjectionCalls.length).toBe(readsA);
					// No additional A mutation calls after rebind.
					const callsAAfter = serviceA.sendCallCount + serviceA.queueCallCount + serviceA.interruptCallCount;
					expect(callsAAfter).toBe(callsA);
				} finally {
					unsub();
				}
			});
		}
	});

	// --- I3: a page's settled ask renders through the projection's own gate ----------

	describe("a page ask renders as its tool row while askPending is false", () => {
		it("a page ask_user item renders as its tool row, other items/order/cursor retained, askPending unchanged", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = readServing(
				makeConversation({ threadId: "thread-1", askPending: false }),
				"cursor-1",
			);
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().conversation?.askPending).toBe(false);

			// The page carries a settled ask beside ordinary history. The old
			// state-boundary question filter is gone with the row seam (D23d):
			// rows re-project from the model through liveAskQuestions, which
			// gates on askPending — false here — so the ask renders as the tool
			// row it is and no question row appears.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment("t0", [
							{
								...askUserItem(
									"ask-old",
									JSON.stringify({
										questions: [
											{
												header: "Which?",
												question: "Pick one",
												options: [{ label: "yes" }],
												multi_select: false,
											},
										],
									}),
								),
								position: { entry: 1, item: 0 },
							},
							{
								...agentMessageItem("msg-old", "old message", "completed"),
								position: { entry: 2, item: 0 },
							},
						]),
					],
					"cursor-2",
				),
				nextCursor: "cursor-2",
			};
			await store.getState().loadOlder(service);

			// askPending must remain false.
			expect(store.getState().conversation?.askPending).toBe(false);
			// No question items in the conversation.
			const items = store.getState().conversation?.items ?? [];
			expect(items.some((i) => i.kind === "question")).toBe(false);
			// The ask's own row is retained — the tool row it is.
			expect(items.some((i) => i.id === "ask-old")).toBe(true);
			// Other content retained.
			expect(items.some((i) => i.id === "msg-old")).toBe(true);
			// Cursor retained.
			expect(store.getState().olderCursor).toBe("cursor-2");
		});
	});

	describe("a refusal's reread carries the reread's capabilities into the conversation", () => {
		it("the conversation's capabilities match the reread's snapshot", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const withCaps = (capabilities: ThreadCapabilities) =>
				makeReadProjectionResult(
					makeThread({ evener: { ref: "ref-1", capabilities, queue: { revision: 0 } } }),
					capabilities,
				);
			service.readProjectionResult = withCaps({ ...ALL_TRUE_CAPS });
			await store.getState().openProjected(service, "ref-1");
			service.sendShouldReject = refusal();
			service.readProjectionResult = withCaps({ ...ALL_TRUE_CAPS, send: false });
			const ctrl = makeControlledRead(service);
			await store.getState().send(service, textInput("x"));
			await ctrl.ready(1);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			expect(store.getState().conversation?.capabilities.send).toBe(false);
		});
	});

	describe("Task3: store-owned external error publication seam", () => {
		it("exact current owner writes generic message and survives a subsequent rehydrate", async () => {
			// The dispatcher publishes a generic sanitized external error against the
			// exact current ref+generation. The write goes through the wrapped set so
			// errorOwnerRev advances. A rehydrate that started BEFORE publication
			// captured the older errorOwnerRev; when it completes it must preserve
			// the newer external error rather than clearing it — proving the
			// revision advanced through the wrapped set.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			const ref = store.getState().ref;
			const gen = store.getState().conversationGeneration;
			expect(ref).toBe("ref-1");
			const readsAfterOpen = service.readProjectionCalls.length;

			// Start a rehydrate (R) that hangs — it captures errorOwnerRev at entry
			// (before the external error is published). Use the controlled read's
			// level-triggered barriers, not microtask guesses: started(1) confirms
			// the read began; ready(1) confirms orig(ref) resolved and the read is
			// parked at the release gate, so R's entry errorOwnerRev is captured.
			const ctrl = makeControlledRead(service);
			store.getState().applyNotification(resyncFrame());
			await ctrl.started(1);
			await ctrl.ready(1);
			expect(ctrl.getStartedCount()).toBe(1);
			expect(ctrl.getDoneCount()).toBe(0);

			// While R is in-flight, publish the external error against the exact
			// current owner. This goes through the wrapped set, advancing
			// errorOwnerRev past R's captured value.
			store.getState().publishExternalError("external failure", ref, gen);
			expect(store.getState().error).toBe("external failure");

			// Before release, capture the pre-reconcile conversation and install a
			// subscription barrier that resolves only when the conversation identity
			// changes (the rehydrate commit). The subscription is installed BEFORE
			// release so it cannot miss a synchronous commit during release, and it
			// unsubscribes synchronously on match so cleanup never leaves a blocked
			// read. Then release, await exact completed(1), and await the reconcile
			// barrier — no yieldMicrotask/sleeps/polling.
			const preReleaseConv = store.getState().conversation;
			const reconcileP =
				store.getState().conversation !== preReleaseConv
					? Promise.resolve()
					: new Promise<void>((resolve) => {
							const unsub = store.subscribe((s) => {
								if (s.conversation !== preReleaseConv) {
									unsub();
									resolve();
								}
							});
						});
			ctrl.release();
			await ctrl.completed(1);
			await reconcileP;
			expect(ctrl.getStartedCount()).toBe(1);
			expect(ctrl.getDoneCount()).toBe(1);
			// Exactly one rehydrate read ran (the initial open read is excluded).
			expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);

			// The external error survives the rehydrate.
			expect(store.getState().error).toBe("external failure");
		});

		it("stale ref and stale generation make zero set, same reference, zero notifications", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");

			// Track subscriber notifications. Use a strict identity snapshot so a
			// no-op (same state object reference) fires zero callbacks.
			const notifications: string[] = [];
			const unsubscribe = store.subscribe((s) => {
				notifications.push(s.error ?? "null");
			});

			const stateBefore = store.getState();

			// Stale ref — the store holds "ref-1", caller passes "ref-stale".
			store.getState().publishExternalError("stale ref error", "ref-stale", store.getState().conversationGeneration);
			expect(store.getState().error).toBeNull();
			expect(store.getState()).toBe(stateBefore);
			expect(notifications).toHaveLength(0);

			// Stale generation — caller passes an old generation.
			store.getState().publishExternalError("stale gen error", "ref-1", 999);
			expect(store.getState().error).toBeNull();
			expect(store.getState()).toBe(stateBefore);
			expect(notifications).toHaveLength(0);

			// Stale ref AND stale generation together.
			store.getState().publishExternalError("both stale", "ref-stale", 999);
			expect(store.getState().error).toBeNull();
			expect(store.getState()).toBe(stateBefore);
			expect(notifications).toHaveLength(0);

			unsubscribe();
		});

		it("reset/open transition rejects old expected owner", async () => {
			// After a reset (or open of a new conversation), a publishExternalError
			// call carrying the OLD ref+generation must make zero state changes.
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(makeThread());
			await store.getState().openProjected(service, "ref-1");
			const oldRef = store.getState().ref;
			const oldGen = store.getState().conversationGeneration;

			// Reset to idle — increments generation and clears ref.
			store.getState().reset();
			expect(store.getState().ref).toBeNull();
			expect(store.getState().conversationGeneration).not.toBe(oldGen);

			const notifications: string[] = [];
			const unsubscribe = store.subscribe((s) => {
				notifications.push(s.error ?? "null");
			});
			const stateBefore = store.getState();

			// Publish against the OLD owner — must be rejected (zero set).
			store.getState().publishExternalError("post-reset error", oldRef, oldGen);
			expect(store.getState().error).toBeNull();
			expect(store.getState()).toBe(stateBefore);
			expect(notifications).toHaveLength(0);

			unsubscribe();

			// Open a new conversation — the old owner is still rejected.
			service.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-2" }));
			await store.getState().openProjected(service, "ref-2");
			expect(store.getState().ref).toBe("ref-2");

			const stateAfterOpen = store.getState();
			const notifications2: string[] = [];
			const unsubscribe2 = store.subscribe((s) => {
				notifications2.push(s.error ?? "null");
			});

			// Publish against the OLD owner (ref-1, old gen) — rejected.
			store.getState().publishExternalError("post-open error", oldRef, oldGen);
			expect(store.getState()).toBe(stateAfterOpen);
			expect(notifications2).toHaveLength(0);

			// Publishing against the NEW owner succeeds.
			store.getState().publishExternalError("new owner error", "ref-2", store.getState().conversationGeneration);
			expect(store.getState().error).toBe("new owner error");

			unsubscribe2();
		});
	});
});

// Task 5a: the store owns canonical-read validity and pending readiness so a
// shared native store can answer "is this model the accepted current latest
// window, and are its durable pending rows the verified current ones?" before
// a second view borrows either. These tests drive the real store through its
// real scheduler, pending port and service seams; nothing here fabricates a
// durable identity or drains a fixed number of microtasks.
describe("store-owned canonical read validity and pending readiness", () => {
	const TARGET = "hub-1::ref-1";

	function projectionOf(
		conversation: MobileConversation,
		olderCursor: string | null = null,
	): ConversationReadProjection {
		return {
			conversation,
			olderCursor,
		};
	}

	function outbox(over: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
		return {
			version: 1,
			clientMutationId: "cmid-1",
			targetRef: TARGET,
			method: "turn/start",
			payload: {},
			attachments: [],
			optimisticDisplay: { method: "turn/start", input: textInput("hello") },
			intentSequence: 0,
			createdAt: 7,
			state: "submitting",
			...over,
		};
	}

	type PendingRows = Pick<MutationPersistenceSnapshot<MutationAttachmentRef>, "outbox" | "optimistic">;

	// A real port shape over a scripted durable store: reads answer at a
	// callers-chosen boundary (holdReads) or immediately, and a notification
	// fires the exact listener the store subscribed. This is the storage edge,
	// not a store stand-in.
	function createStorage(initial: PendingRows = { outbox: [], optimistic: [] }) {
		let snapshot: PendingRows = { outbox: initial.outbox, optimistic: initial.optimistic };
		const listeners = new Set<() => void>();
		const reads: {
			promise: Promise<PendingRows>;
			resolve: (value: PendingRows) => void;
			reject: (reason: unknown) => void;
		}[] = [];
		let holdReads = false;
		let failNext = false;
		const port = {
			targetRef: TARGET,
			read: (): Promise<PendingRows> => {
				if (failNext) {
					failNext = false;
					return Promise.reject(new Error("storage unavailable"));
				}
				if (!holdReads) return Promise.resolve(snapshot);
				const read = deferred<PendingRows>();
				reads.push(read);
				return read.promise;
			},
			subscribe: (listener: () => void) => {
				listeners.add(listener);
				return () => {
					listeners.delete(listener);
				};
			},
			isOwnMutationRecord: () => true,
		} satisfies ConversationMutationPendingPort;
		return {
			port,
			reads,
			hold() {
				holdReads = true;
			},
			failNextRead() {
				failNext = true;
			},
			notify(next?: Partial<PendingRows>) {
				if (next !== undefined) {
					snapshot = { outbox: next.outbox ?? [], optimistic: next.optimistic ?? [] };
				}
				for (const listener of [...listeners]) listener();
			},
			snapshot: () => snapshot,
		};
	}

	it("canonical readiness is unknown before the first read", () => {
		const store = createConversationStore();
		expect(store.getState().latestReadState).toBe("unknown");
		expect(store.getState().canonicalSession()).toBeNull();
	});

	it("is reading through the initial open and ready only on its accepted publication", async () => {
		const service = new FakeConversationService();
		const read = deferred<ConversationReadProjection>();
		service.readProjectionBlock = read.promise;
		const store = createConversationStore();
		const opening = store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("reading");
		expect(store.getState().canonicalSession()).toBeNull();

		read.resolve(projectionOf(makeConversation()));
		await opening;
		expect(store.getState().latestReadState).toBe("ready");
		expect(store.getState().canonicalSession()).toBe(store.getState().conversation);
	});

	it("a failed latest read is error and never canonical", async () => {
		const service = new FakeConversationService();
		const read = deferred<ConversationReadProjection>();
		service.readProjectionBlock = read.promise;
		const store = createConversationStore();
		const opening = store.getState().openProjected(service, "ref-1");
		read.reject(new Error("read failed"));
		await opening;
		expect(store.getState().latestReadState).toBe("error");
		expect(store.getState().canonicalSession()).toBeNull();
	});

	it("ensureLatest reads through the scheduler, coalesces concurrent asks, and restores only on publication", async () => {
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");

		const read = deferred<ConversationReadProjection>();
		service.readProjectionBlock = read.promise;
		const first = store.getState().ensureLatest();
		const second = store.getState().ensureLatest();
		expect(store.getState().latestReadState).toBe("reading");
		expect(store.getState().canonicalSession()).toBeNull();

		read.resolve(projectionOf(store.getState().conversation!));
		await Promise.all([first, second]);
		// One coalesced read: the open's read plus exactly one ensureLatest read.
		expect(service.readProjectionCalls).toHaveLength(2);
		expect(store.getState().latestReadState).toBe("ready");
		expect(store.getState().canonicalSession()).toBe(store.getState().conversation);
	});

	it("a discarded read does not restore canonical readiness", async () => {
		const service = new FakeConversationService();
		service.openConv = makeConversation({}, true);
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");

		// The held model claims a longer same-incarnation history, so the next
		// read (length 0) is applyReadModel's own "discard" disposition.
		const held = store.getState().conversation!;
		store.setState({ conversation: { ...held, history: held.history && { ...held.history, length: 10 } } });
		await store.getState().rehydrate(service);
		expect(store.getState().latestReadState).not.toBe("ready");
		expect(store.getState().canonicalSession()).toBeNull();
	});

	it("an invalidated or failed history is not canonical until an accepted read replaces it", async () => {
		const base = makeConversation({}, true);
		const heldHistory = base.history!;
		for (const marker of [
			{ invalidatedAtGeneration: 0, awaited: { bootGeneration: "1", epoch: 1 } },
			{ failed: "transcript unavailable" },
		]) {
			const service = new FakeConversationService();
			service.openConv = { ...base, history: { ...heldHistory, ...marker } };
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().latestReadState).toBe("unknown");
			expect(store.getState().canonicalSession()).toBeNull();

			// A newer accepted latest-window read replaces the invalidated history
			// (and an accepted merge clears the failed marker), restoring authority.
			service.readProjectionBlock = Promise.resolve(
				projectionOf({ ...makeConversation({}, true), history: { ...heldHistory, epoch: 2 } }),
			);
			await store.getState().ensureLatest();
			expect(store.getState().latestReadState).toBe("ready");
			expect(store.getState().canonicalSession()).not.toBeNull();
		}
	});

	it("paging and display reprojection never restore an invalidated canonical read", async () => {
		const service = new FakeConversationService();
		service.olderCursor = "cursor-1";
		service.olderItems = {
			turnsPage: turnsPage([wireTurnFragment("t2", [{ type: "userMessage", id: "old-1", text: "old" }])]),
		};
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");

		const read = deferred<ConversationReadProjection>();
		service.readProjectionBlock = read.promise;
		const latest = store.getState().ensureLatest();
		expect(store.getState().latestReadState).toBe("reading");

		store.getState().setDisplayConfig(makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }));
		const page = await store.getState().loadOlder(service);
		expect(page.status).toBe("loaded");
		expect(store.getState().latestReadState).toBe("reading");
		expect(store.getState().canonicalSession()).toBeNull();

		read.resolve(projectionOf(store.getState().conversation!));
		await latest;
		expect(store.getState().latestReadState).toBe("ready");
	});

	it("a resync frame loses canonical readiness synchronously and the accepted read restores it", async () => {
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");

		const read = deferred<ConversationReadProjection>();
		service.readProjectionBlock = read.promise;
		store.getState().applyNotification(resyncFrame());
		expect(store.getState().latestReadState).toBe("reading");
		expect(store.getState().canonicalSession()).toBeNull();

		read.resolve(projectionOf(store.getState().conversation!));
		await store.getState().ensureLatest();
		expect(store.getState().latestReadState).toBe("ready");
	});

	it("a reconnect loses canonical readiness until the resumed read publishes", async () => {
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");

		store.getState().suspendProjected();
		expect(store.getState().latestReadState).toBe("unknown");
		expect(store.getState().canonicalSession()).toBeNull();

		const read = deferred<ConversationReadProjection>();
		service.readProjectionBlock = read.promise;
		const resuming = store.getState().resumeProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("reading");
		read.resolve(projectionOf(service.openConv));
		await resuming;
		expect(store.getState().latestReadState).toBe("ready");
	});

	it("pending readiness is unknown before the first durable read", async () => {
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		storage.hold();
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		store.getState().bindPendingMutations(storage.port);

		expect(store.getState().pendingMutationsReady).toBe(false);
		expect(store.getState().pendingMutations).toBeNull();

		storage.reads[0]!.resolve({ outbox: [outbox()], optimistic: [] });
		await storage.reads[0]!.promise;
		expect(store.getState().pendingMutationsReady).toBe(true);
	});

	it("a pending read failure stays unknown and keeps the prior rows", async () => {
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		store.getState().bindPendingMutations(storage.port);
		await store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(true);

		storage.failNextRead();
		await store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(false);
		expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-1"]);
	});

	it("a target storage notification clears readiness synchronously without demanding the dispatch-blocking read", async () => {
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		const service = new FakeConversationService();
		const canonicalRead = deferred<ConversationReadProjection>();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		store.getState().bindPendingMutations(storage.port);
		await store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(true);
		const readsAfterOpen = service.readProjectionCalls.length;
		const priorRows = store.getState().pendingMutations;
		expect(priorRows).toHaveLength(1);

		// Hold the pending read the notification starts; hold the canonical read
		// only for the demand this test makes explicitly afterwards.
		service.readProjectionBlock = canonicalRead.promise;
		storage.hold();
		storage.notify({ outbox: [], optimistic: [] });

		expect(store.getState().pendingMutationsReady).toBe(false);
		// Prior rows stay readable for display, never as a verified empty set.
		expect(store.getState().pendingMutations).toBe(priorRows);
		// Canonical authority is fail-closed at once, and the store does not
		// itself start the authoritative read: on native that read blocks the
		// target's ordinary dispatch between the dispatcher's mark and request.
		expect(store.getState().latestReadState).toBe("unknown");
		expect(store.getState().canonicalSession()).toBeNull();
		expect(service.readProjectionCalls).toHaveLength(readsAfterOpen);

		storage.reads[0]!.resolve({ outbox: [], optimistic: [] });
		await storage.reads[0]!.promise;
		expect(store.getState().pendingMutationsReady).toBe(true);
		expect(store.getState().pendingMutations).toEqual([]);
		// The pending read alone never restores canonical authority.
		expect(store.getState().latestReadState).toBe("unknown");

		// A consumer that can afford the read demands it; its accepted
		// publication restores authority.
		canonicalRead.resolve(projectionOf(store.getState().conversation!));
		await store.getState().ensureLatest();
		expect(store.getState().latestReadState).toBe("ready");
	});

	it("a pre-notification read cannot certify a later write", async () => {
		const storage = createStorage({ outbox: [], optimistic: [] });
		storage.hold();
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		store.getState().bindPendingMutations(storage.port);
		expect(store.getState().pendingMutationsReady).toBe(false);

		// Read #1 starts before the write; the notification starts read #2.
		const preNotification = storage.reads[0]!;
		storage.notify({ outbox: [outbox({ clientMutationId: "cmid-later" })], optimistic: [] });
		const notificationRead = storage.reads[1]!;
		expect(store.getState().pendingMutationsReady).toBe(false);

		// The stale read answers first: its rows display, its answer certifies
		// nothing about the later write.
		preNotification.resolve({ outbox: [outbox({ clientMutationId: "cmid-stale" })], optimistic: [] });
		await preNotification.promise;
		expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-stale"]);
		expect(store.getState().pendingMutationsReady).toBe(false);

		notificationRead.resolve({ outbox: [outbox({ clientMutationId: "cmid-later" })], optimistic: [] });
		await notificationRead.promise;
		expect(store.getState().pendingMutationsReady).toBe(true);
		expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-later"]);
	});

	it("a failed refresh leaves readiness false and the durable row intact", async () => {
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		store.getState().bindPendingMutations(storage.port);
		await store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(true);

		storage.failNextRead();
		await store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(false);
		expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-1"]);
		expect(storage.snapshot().outbox.map((record) => record.clientMutationId)).toEqual(["cmid-1"]);
	});

	it("a failed latest read that raced pagination still settles latest-read error", async () => {
		const service = new FakeConversationService();
		service.olderCursor = "cursor-1";
		service.olderItems = {
			turnsPage: turnsPage([wireTurnFragment("t2", [{ type: "userMessage", id: "old-1", text: "old" }])]),
		};
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");

		const latestRead = deferred<ConversationReadProjection>();
		service.readProjectionBlock = latestRead.promise;
		const readStarted = deferred<void>();
		const readProjection = service.readProjection.bind(service);
		service.readProjection = async (ref: string) => {
			readStarted.resolve();
			return readProjection(ref);
		};
		const latest = store.getState().ensureLatest();
		// The read is outstanding (its tokens captured) before the page starts.
		await readStarted.promise;
		expect(store.getState().latestReadState).toBe("reading");
		// A page starts and completes while that read is outstanding. Its page
		// token moves, which must not silence the failed read's own validity.
		const page = await store.getState().loadOlder(service);
		expect(page.status).toBe("loaded");

		latestRead.reject(new Error("read failed"));
		await latest;
		expect(store.getState().latestReadState).toBe("error");
		expect(store.getState().canonicalSession()).toBeNull();
		// The reader's displayed error still comes only from an owner entitled to
		// overwrite it; the page owner changed, so no error text was published.
		expect(store.getState().error).toBeNull();

		// The one read actor retries; a later successful latest read restores
		// canonical authority.
		service.readProjectionBlock = null;
		await store.getState().ensureLatest();
		expect(store.getState().latestReadState).toBe("ready");
		expect(store.getState().canonicalSession()).not.toBeNull();
	});

	it("a transient pending-read failure recovers on connection recovery", async () => {
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		storage.hold();
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		store.getState().bindPendingMutations(storage.port);
		expect(storage.reads).toHaveLength(1);
		storage.reads[0]!.reject(new Error("storage unavailable"));
		await storage.reads[0]!.promise.catch(() => undefined);
		expect(store.getState().pendingMutationsReady).toBe(false);

		// Reconnect a retained conversation: the bound port survives, so an
		// idempotent rebind would perform no read and the transient failure would
		// strand Retry until a full remount.
		store.getState().suspendProjected();
		const resuming = store.getState().resumeProjected(service, "ref-1");
		expect(storage.reads).toHaveLength(2);
		storage.reads[1]!.resolve({ outbox: [outbox({ clientMutationId: "cmid-2" })], optimistic: [] });
		await storage.reads[1]!.promise;
		await resuming;

		expect(store.getState().pendingMutationsReady).toBe(true);
		expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-2"]);
		expect(store.getState().latestReadState).toBe("ready");
		expect(store.getState().canonicalSession()).not.toBeNull();
	});

	it("a storage notification preserves an outstanding latest-read failure", async () => {
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		store.getState().bindPendingMutations(storage.port);
		await store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(true);

		// A retained-open latest read fails. The entry read actor keys its retry
		// demand on exactly this state; "unknown" would cancel it without a read.
		const failedRead = deferred<ConversationReadProjection>();
		service.readProjectionBlock = failedRead.promise;
		const latest = store.getState().ensureLatest();
		failedRead.reject(new Error("read failed"));
		await latest;
		expect(store.getState().latestReadState).toBe("error");
		expect(store.getState().canonicalSession()).toBeNull();

		// A target storage notification races the failure. It invalidates display
		// authority, but the outstanding failure and its retry demand survive.
		storage.notify({ outbox: [], optimistic: [] });
		expect(store.getState().latestReadState).toBe("error");
		expect(store.getState().canonicalSession()).toBeNull();

		// The actor retries; the retry's accepted read clears the failure and
		// restores canonical authority.
		service.readProjectionBlock = null;
		await store.getState().ensureLatest();
		expect(store.getState().latestReadState).toBe("ready");
		expect(store.getState().canonicalSession()).not.toBeNull();
	});

	// A deferred older page is not a latest-window read: its own invalidation
	// branch must not rewrite whichever read state the latest authority is in.
	function holdInvalidatedHistory(store: ReturnType<typeof createConversationStore>, service: FakeConversationService) {
		const held = store.getState().conversation!;
		store.setState({
			conversation: {
				...held,
				history: held.history && {
					...held.history,
					invalidatedAtGeneration: 0,
					awaited: { bootGeneration: "1", epoch: 1 },
					pendingIncarnation: "inc-2",
				},
			},
			olderCursor: "cursor-1",
		});
		// The page of the incarnation the thread already awaits defers: the merge
		// leaves the invalidated history in place with no newer signal.
		service.olderItems = {
			turnsPage: {
				data: [wireTurn("t-old", 5, 1)],
				nextCursor: "cursor-2",
				bootGeneration: "1",
				epoch: 1,
				snapshot: { incarnation: "inc-2", length: 1 },
			},
			nextCursor: "cursor-2",
		};
	}

	it("a deferred older page during a held latest read keeps it reading", async () => {
		const service = new FakeConversationService();
		service.openConv = makeConversation({}, true);
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");
		holdInvalidatedHistory(store, service);

		const latestRead = deferred<ConversationReadProjection>();
		service.readProjectionBlock = latestRead.promise;
		const readStarted = deferred<void>();
		const readProjection = service.readProjection.bind(service);
		service.readProjection = async (ref: string) => {
			readStarted.resolve();
			return readProjection(ref);
		};
		const latest = store.getState().ensureLatest();
		await readStarted.promise;
		expect(store.getState().latestReadState).toBe("reading");

		const page = await store.getState().loadOlder(service);
		expect(page.status).toBe("ignored");
		expect(store.getState().latestReadState).toBe("reading");
		expect(store.getState().canonicalSession()).toBeNull();

		latestRead.reject(new Error("read failed"));
		await latest;
	});

	it("a deferred older page during a failed latest read keeps error and its retry demand", async () => {
		const service = new FakeConversationService();
		service.openConv = makeConversation({}, true);
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		expect(store.getState().latestReadState).toBe("ready");
		holdInvalidatedHistory(store, service);

		const failedRead = deferred<ConversationReadProjection>();
		service.readProjectionBlock = failedRead.promise;
		const latest = store.getState().ensureLatest();
		failedRead.reject(new Error("read failed"));
		await latest;
		expect(store.getState().latestReadState).toBe("error");

		const page = await store.getState().loadOlder(service);
		expect(page.status).toBe("ignored");
		// The failure, which is the entry read actor's retry demand, survives.
		expect(store.getState().latestReadState).toBe("error");
		expect(store.getState().canonicalSession()).toBeNull();

		// The actor retries; the accepted read restores authority.
		service.readProjectionBlock = null;
		await store.getState().ensureLatest();
		expect(store.getState().latestReadState).toBe("ready");
		expect(store.getState().canonicalSession()).not.toBeNull();
	});

	it("retirement resolves an awaited refresh without undoing the commit", async () => {
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		const service = new FakeConversationService();
		const store = createConversationStore();
		await store.getState().openProjected(service, "ref-1");
		const unbind = store.getState().bindPendingMutations(storage.port);
		await store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(true);

		storage.hold();
		const refreshed = store.getState().refreshPendingMutations();
		expect(store.getState().pendingMutationsReady).toBe(false);

		// Retirement resolves the waiter even though the held storage read
		// never answers, and the committed durable record stands.
		unbind();
		await refreshed;
		expect(store.getState().pendingMutationsReady).toBe(false);
		expect(storage.snapshot().outbox.map((record) => record.clientMutationId)).toEqual(["cmid-1"]);
	});

	it("ordinary Send and Queue routing ignores canonical and pending readiness", async () => {
		// Send: an idle session routes turn/start even while canonical is
		// unready and the durable projection is not certified.
		const sendService = new FakeConversationService();
		const sendStore = createConversationStore();
		await sendStore.getState().openProjected(sendService, "ref-1");
		const storage = createStorage({ outbox: [outbox()], optimistic: [] });
		storage.hold();
		sendStore.getState().bindPendingMutations(storage.port);
		const canonicalRead = deferred<ConversationReadProjection>();
		sendService.readProjectionBlock = canonicalRead.promise;
		void sendStore.getState().ensureLatest();
		sendStore.getState().setDraft("hello");
		await sendStore.getState().send(sendService, textInput("hello"));
		expect(sendService.sendCallCount).toBe(1);
		expect(sendStore.getState().lastAcceptedMutation?.kind).toBe("send");

		// Queue: an active session routes the same input as a queue entry.
		const queueService = new FakeConversationService();
		queueService.openConv = makeConversation({ status: { type: "active" } });
		const queueStore = createConversationStore();
		await queueStore.getState().openProjected(queueService, "ref-1");
		await queueStore.getState().queue(queueService, textInput("queued"));
		expect(queueService.queueCallCount).toBe(1);
		expect(queueStore.getState().lastAcceptedMutation?.kind).toBe("queue");
	});
});
