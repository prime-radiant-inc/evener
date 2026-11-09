// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { AnyNotification, InputItem, MutationReceipt } from "@evener/appwire-client";
import { QUEUE_UNAVAILABLE, SEND_UNAVAILABLE, WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import type {
	MutationOptimisticRecord,
	MutationOutboxRecord,
	MutationPersistenceSnapshot,
	MutationRecoveryRecord,
	PendingTurnEntry,
} from "@evener/appwire-client/state/mutation";
import { describe, expect, it } from "vitest";
import type { MobileConversation } from "../../projectedRows";
import {
	createConversationStore,
	type ConversationPendingSubmission,
	type ConversationStoreMutationKind,
} from "./conversation";
import { MutationNotSentError } from "./conversationMutation";
import type {
	ConversationMutationAdmission,
	ConversationMutationPendingPort,
	ConversationMutationRequest,
	ConversationMutationSubmitter,
} from "./conversationMutation";
import {
	ALL_TRUE_CAPS,
	FakeConversationService,
	historyUpdated,
	makeControlledRead,
	makeConversation,
	makeReadProjectionResult,
	makeReceipt,
	makeThread,
	openedStore,
	refusal,
	statusChanged,
	textInput,
	yieldMicrotask,
} from "./conversationTestUtils";

describe("ConversationStore", () => {
	// --- mutation-state tests (Step 3) ------------------------------------------

	describe("mutation state — parameterized send/queue/interrupt", () => {
		type MutationKind = ConversationStoreMutationKind;

		const mutationCases: {
			kind: MutationKind;
			call: (
				store: ReturnType<typeof createConversationStore>,
				service: FakeConversationService,
				input: InputItem[],
			) => Promise<void>;
			callCountField: keyof FakeConversationService;
		}[] = [
			{
				kind: "send",
				call: (store, s, i) => store.getState().send(s, i),
				callCountField: "sendCallCount",
			},
			{
				kind: "queue",
				call: (store, s, i) => store.getState().queue(s, i),
				callCountField: "queueCallCount",
			},
			{
				kind: "interrupt",
				call: (store, s, _i) => store.getState().interrupt(s),
				callCountField: "interruptCallCount",
			},
		];

		// Send is offered at rest; queue and interrupt need a running turn.
		function serviceFor(kind: MutationKind): FakeConversationService {
			const service = new FakeConversationService();
			service.openConv = makeConversation({ status: { type: kind === "send" ? "idle" : "active" } });
			return service;
		}

		for (const { kind, call, callCountField } of mutationCases) {
			it(`${kind}: calls the service ${kind} method`, async () => {
				const service = serviceFor(kind);
				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				if (kind === "interrupt") {
					await call(store, service, []);
				} else {
					await call(store, service, textInput("test"));
				}
				expect(service[callCountField]).toBe(1);
			});

			it(`${kind}: sets pending mutation state while in-flight`, async () => {
				const service = serviceFor(kind);
				// Make the service hang so we can inspect the in-flight state.
				let resolveFn: (() => void) | null = null as (() => void) | null;
				service.receipt = makeReceipt();
				const hangPromise = new Promise<MutationReceipt>((resolve) => {
					resolveFn = () => resolve(makeReceipt());
				});
				if (kind === "send") {
					service.sendShouldReject = null;
					service.send = async () => hangPromise;
				} else if (kind === "queue") {
					service.queue = async () => hangPromise;
				} else {
					service.interrupt = async () => hangPromise;
				}

				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				store.getState().setDraft("unsent draft");

				const p = call(store, service, textInput("test"));
				// While in-flight, the click-to-commit marker is open.
				const pending = store.getState().pendingSubmission;
				expect(pending).toEqual({ kind });
				expect(store.getState().lastAcceptedMutation).toBeNull();

				// Resolve and await
				resolveFn?.();
				await p;
				// After resolution, the markers should be cleared
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().failedSubmission).toBeNull();
				expect(store.getState().lastAcceptedMutation).toEqual({
					kind,
					receipt: expect.objectContaining({ disposition: "applied" }),
				});
			});

			it(`${kind}: restores the exact draft captured at submit on failure`, async () => {
				const service = serviceFor(kind);
				let rejectFn: ((error: Error) => void) | null = null as ((error: Error) => void) | null;
				const hangPromise = new Promise<MutationReceipt>((_resolve, reject) => {
					rejectFn = reject;
				});
				if (kind === "send") {
					service.send = async () => hangPromise;
				} else if (kind === "queue") {
					service.queue = async () => hangPromise;
				} else {
					service.interrupt = async () => hangPromise;
				}

				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				store.getState().setDraft("exact draft text");
				const p = call(store, service, textInput("test"));
				// The attempt owns the click-to-commit marker while in flight.
				// Interrupt never snapshots or clears the draft; send/queue
				// cleared it, holding the exact pre-submit text for the restore.
				expect(store.getState().pendingSubmission).toEqual({ kind });
				if (kind === "interrupt") {
					expect(store.getState().draft).toBe("exact draft text");
				} else {
					expect(store.getState().draft).toBe("");
				}
				rejectFn?.(new Error(`${kind} failed`));
				await p.catch(() => {});
				// The failure restores exactly the draft captured at submit
				// (interrupt captured none, so the draft stays as it was).
				expect(store.getState().draft).toBe("exact draft text");
				expect(store.getState().error).toBe(`${kind} failed`);
			});

			it(`${kind}: clears pending and error on success`, async () => {
				const service = serviceFor(kind);
				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				await call(store, service, textInput("test"));
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().failedSubmission).toBeNull();
				expect(store.getState().error).toBeNull();
			});

			it(`${kind}: restores draft and sets failed state on failure`, async () => {
				const service = serviceFor(kind);
				const rejectErr = new Error(`${kind} conflict`);
				if (kind === "send") {
					service.sendShouldReject = rejectErr;
				} else if (kind === "queue") {
					service.queue = async () => Promise.reject(rejectErr);
				} else {
					service.interrupt = async () => Promise.reject(rejectErr);
				}

				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				store.getState().setDraft("draft to restore");
				await call(store, service, textInput("test"));
				// Error should be set
				expect(store.getState().error).not.toBeNull();
				// The retained failure PERSISTS (not cleared to null)
				expect(store.getState().failedSubmission).not.toBeNull();
				expect(store.getState().failedSubmission?.kind).toBe(kind);
				expect(store.getState().lastAcceptedMutation).toBeNull();
				// Draft should be restored for send/queue (interrupt doesn't
				// snapshot draft, so it's not restored)
				if (kind === "interrupt") {
					// Interrupt doesn't clear draft, so it remains as-is
					expect(store.getState().draft).toBe("draft to restore");
				} else {
					// Send/queue clear draft on submit, restore on failure if no
					// new text was typed. Since no new text was typed, restore happens.
					expect(store.getState().draft).toBe("draft to restore");
				}
			});

			it(`${kind}: an in-flight attempt keeps the pressing generation and its marker open`, async () => {
				const service = serviceFor(kind);
				let resolveFn: (() => void) | null = null as (() => void) | null;
				const hangPromise = new Promise<MutationReceipt>((resolve) => {
					resolveFn = () => resolve(makeReceipt());
				});
				if (kind === "send") {
					service.send = async () => hangPromise;
				} else if (kind === "queue") {
					service.queue = async () => hangPromise;
				} else {
					service.interrupt = async () => hangPromise;
				}

				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				const genBefore = store.getState().conversationGeneration;
				store.getState().setDraft("draft");
				const p = call(store, service, textInput("test"));
				// The attempt runs on the generation that pressed it, with the
				// click-to-commit marker open for exactly that attempt.
				expect(store.getState().conversationGeneration).toBe(genBefore);
				expect(store.getState().pendingSubmission).toEqual({ kind });
				resolveFn?.();
				await p;
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().failedSubmission).toBeNull();
			});
		}

		// The durable wiring: with a mutation submitter supplied, every
		// conversation mutation is admitted through it at the enqueue boundary,
		// the service transport is never called for the mutation, and the store
		// reports admission without inventing a wire receipt.
		for (const { kind, call, callCountField } of mutationCases) {
			it(`${kind}: admits through the durable submitter instead of the service`, async () => {
				const service = serviceFor(kind);
				const requests: ConversationMutationRequest[] = [];
				const submitter: ConversationMutationSubmitter = {
					submit: async (request) => {
						requests.push(request);
						return undefined;
					},
				};
				const store = createConversationStore({
					mutationHubId: "hub-1",
					mutationSubmitter: submitter,
				});
				await store.getState().open(service, "ref-1");
				store.getState().setDraft("durable payload");

				await call(store, service, textInput("durable payload"));

				expect(service[callCountField]).toBe(0);
				expect(requests).toHaveLength(1);
				expect(requests[0]).toMatchObject({
					kind,
					hubId: "hub-1",
					targetRef: "ref-1",
					threadId: "thread-1",
					input: kind === "interrupt" ? [] : textInput("durable payload"),
				});
				expect(requests[0]?.instanceId).toBe("thread-1");
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().failedSubmission).toBeNull();
				expect(store.getState().error).toBeNull();
				expect(store.getState().lastAcceptedMutation).toEqual({
					kind,
					receipt: undefined,
				});
			});
		}

		for (const { kind, call } of mutationCases) {
			it(`${kind}: records the admission's durable id on the in-flight marker`, async () => {
				const service = serviceFor(kind);
				const markers: ConversationPendingSubmission[] = [];
				const store = createConversationStore({
					mutationHubId: "hub-1",
					mutationSubmitter: {
						submit: async () => ({ clientMutationId: `durable-${kind}` }),
					},
				});
				await store.getState().open(service, "ref-1");
				const unsubscribe = store.subscribe((state) => {
					if (state.pendingSubmission !== null) markers.push(state.pendingSubmission);
				});
				await call(store, service, kind === "interrupt" ? [] : textInput("admitted"));
				unsubscribe();

				// The marker carried the durable row's id while the attempt was
				// in flight...
				expect(markers).toContainEqual({ kind, admittedMutationId: `durable-${kind}` });
				// ...and settled as before, without inventing a wire receipt.
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().failedSubmission).toBeNull();
				expect(store.getState().lastAcceptedMutation).toEqual({ kind, receipt: undefined });
				expect(store.getState().lastAcceptedMutation).toHaveProperty("receipt", undefined);
			});
		}

		// Stage B: pendingSubmission is the any-mutation click-to-commit marker
		// the screens read. It is present from the press until the attempt
		// settles (success, MutationNotSentError, or ordinary failure), and it
		// carries the durable row's id once the submitter admits it.
		for (const { kind, call } of mutationCases) {
			it(`${kind}: holds pendingSubmission from the press until admission settles`, async () => {
				const service = serviceFor(kind);
				const admissions: Array<(admission: ConversationMutationAdmission | undefined) => void> = [];
				const store = createConversationStore({
					mutationHubId: "hub-1",
					mutationSubmitter: {
						submit: () => new Promise<ConversationMutationAdmission | undefined>((resolve) => admissions.push(resolve)),
					},
				});
				await store.getState().open(service, "ref-1");
				store.getState().setDraft("pending marker");
				const markers: (ConversationPendingSubmission | null)[] = [];
				const unsubscribe = store.subscribe((state) => markers.push(state.pendingSubmission));

				const submission = call(store, service, kind === "interrupt" ? [] : textInput("pending marker"));

				// The press opened the click-to-commit window, on the any-mutation
				// fact, with no retained failure while the attempt is in flight.
				expect(store.getState().pendingSubmission).toEqual({ kind });
				expect(store.getState().failedSubmission).toBeNull();

				admissions[0]!({ clientMutationId: `durable-${kind}` });
				await submission;
				// The durable id reached the marker while it was in flight...
				expect(markers).toContainEqual({ kind, admittedMutationId: `durable-${kind}` });
				// ...and admission settled it.
				expect(store.getState().pendingSubmission).toBeNull();
				unsubscribe();
			});

			it(`${kind}: clears pendingSubmission on an ordinary failure but keeps the failed marker`, async () => {
				const service = serviceFor(kind);
				const failure = new Error(`${kind} refused`);
				if (kind === "send") {
					service.sendShouldReject = failure;
				} else if (kind === "queue") {
					service.queue = async () => Promise.reject(failure);
				} else {
					service.interrupt = async () => Promise.reject(failure);
				}
				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				store.getState().setDraft("still here");

				await call(store, service, textInput("still here"));

				// The click-to-commit window is over; failed retention is untouched.
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().failedSubmission).toMatchObject({ kind });
			});
		}

		it.each(["send", "queue"] as const)("%s: clears pendingSubmission when nothing was sent", async (kind) => {
			const service = serviceFor(kind);
			const store = createConversationStore({
				mutationHubId: "hub-1",
				mutationSubmitter: {
					submit: async () => {
						throw new MutationNotSentError("not sent");
					},
				},
			});
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("keep me");

			const submission =
				kind === "send"
					? store.getState().send(service, textInput("keep me"))
					: store.getState().queue(service, textInput("keep me"));
			await expect(submission).rejects.toBeInstanceOf(MutationNotSentError);

			expect(store.getState().pendingSubmission).toBeNull();
			// A proven local refusal made nothing durable: no retained failure
			// stays.
			expect(store.getState().failedSubmission).toBeNull();
		});

		it("an older admitted completion cannot clear a newer mutation", async () => {
			const service = serviceFor("send");
			const admissions: Array<(admission: ConversationMutationAdmission | undefined) => void> = [];
			const store = createConversationStore({
				mutationHubId: "hub-1",
				mutationSubmitter: {
					submit: () => new Promise<ConversationMutationAdmission | undefined>((resolve) => admissions.push(resolve)),
				},
			});
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("first");
			const first = store.getState().send(service, textInput("first"));
			store.getState().setDraft("second");
			const second = store.getState().send(service, textInput("second"));
			const secondMarker = store.getState().pendingSubmission;
			expect(secondMarker).toEqual({ kind: "send" });
			expect(secondMarker?.admittedMutationId).toBeUndefined();
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });

			// The older submission is admitted first. Its record was replaced
			// by the newer press, so it must neither write to that attempt's
			// marker nor settle it.
			const firstReceipt = makeReceipt({ clientMutationId: "durable-1" });
			admissions[0]!({ clientMutationId: "durable-1", receipt: firstReceipt });
			await first;
			expect(store.getState().pendingSubmission).toBe(secondMarker);
			// The marker still belongs to the newer attempt, too.
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });
			expect(store.getState().lastAcceptedMutation).toBeNull();

			// The newer mutation settles only with its own admission.
			const secondReceipt = makeReceipt({ clientMutationId: "durable-2" });
			admissions[1]!({ clientMutationId: "durable-2", receipt: secondReceipt });
			await second;
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().lastAcceptedMutation).toEqual({
				kind: "send",
				receipt: secondReceipt,
			});
		});

		// Stage C: failedSubmission is the retained terminal-failure marker,
		// written directly by the failure path — non-null from the attempt's
		// terminal failure until the next press or a lifecycle clear. The
		// rehydrate error-retention decision reads this marker. The error text
		// itself stays in `error` and the draft restore in `draft`, so the
		// marker carries only the failed attempt's identity.
		const expectFailedMarker = (store: ReturnType<typeof createConversationStore>) => {
			expect(store.getState().failedSubmission).toEqual({ kind: "send" });
		};
		it("a failed send retains the failedSubmission marker beside the error and restored draft", async () => {
			const service = serviceFor("send");
			service.sendShouldReject = new Error("send refused");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("draft to restore");

			await store.getState().send(service, textInput("draft to restore"));

			// The marker names exactly which action failed; the attempt's own
			// identity stays in the store's private ownership token.
			expectFailedMarker(store);
			// The retained failure keeps its error and restores the draft per
			// the unchanged revision rule.
			expect(store.getState().error).toBe("send refused");
			expect(store.getState().draft).toBe("draft to restore");
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
		});

		it("a rehydrate success keeps the failed mutation's error while failedSubmission is set", async () => {
			const service = serviceFor("send");
			service.sendShouldReject = new Error("send refused");
			const store = createConversationStore();
			await store.getState().openProjected(service, "ref-1");
			store.getState().setDraft("draft to restore");
			await store.getState().send(service, textInput("draft to restore"));

			// The failed attempt owns the error through the new marker.
			expectFailedMarker(store);
			expect(store.getState().error).toBe("send refused");

			// The read succeeds and publishes; the failed mutation's error
			// persists until a subsequent mutation or open clears it.
			await store.getState().rehydrate(service);

			expectFailedMarker(store);
			expect(store.getState().error).toBe("send refused");
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
		});

		it("admitting the next mutation clears failedSubmission at the press, not at settlement", async () => {
			const service = serviceFor("send");
			service.sendShouldReject = new Error("first refused");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("first");
			await store.getState().send(service, textInput("first"));
			expectFailedMarker(store);

			// The next press ends the previous failure's retention immediately,
			// while the new attempt is still in flight.
			const hang = deferred<MutationReceipt>();
			service.send = () => hang.promise;
			const second = store.getState().send(service, textInput("second"));

			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().pendingSubmission).toEqual({ kind: "send" });

			hang.resolve(makeReceipt());
			await second;
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
		});

		it.each(["send", "queue"] as const)(
			"%s: a MutationNotSentError settles its own record without leaving failedSubmission",
			async (kind) => {
				const service = serviceFor(kind);
				const store = createConversationStore({
					mutationHubId: "hub-1",
					mutationSubmitter: {
						submit: async () => {
							throw new MutationNotSentError("not sent");
						},
					},
				});
				await store.getState().open(service, "ref-1");
				store.getState().setDraft("keep me");

				const submission =
					kind === "send"
						? store.getState().send(service, textInput("keep me"))
						: store.getState().queue(service, textInput("keep me"));
				await expect(submission).rejects.toBeInstanceOf(MutationNotSentError);

				// Current behavior, pinned: the proven local refusal settled
				// its own attempt, left no error, and did not restore the
				// draft — not-sent is not a retained terminal failure, so no
				// marker.
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().error).toBeNull();
				expect(store.getState().draft).toBe("");
				expect(store.getState().failedSubmission).toBeNull();
			},
		);

		it("open clears failedSubmission with the conversation state", async () => {
			const service = serviceFor("send");
			service.sendShouldReject = new Error("send refused");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("draft to restore");
			await store.getState().send(service, textInput("draft to restore"));
			expectFailedMarker(store);

			await store.getState().open(service, "ref-1");

			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
		});

		it("reset clears failedSubmission with the conversation state", async () => {
			const service = serviceFor("send");
			service.sendShouldReject = new Error("send refused");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("draft to restore");
			await store.getState().send(service, textInput("draft to restore"));
			expectFailedMarker(store);

			store.getState().reset();

			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
		});

		it("a binding rebind settles a pending attempt but retains a failed attempt's failedSubmission", async () => {
			// A rebind settles the old binding's PENDING attempt...
			const pendingService = serviceFor("send");
			const hang = deferred<MutationReceipt>();
			pendingService.send = () => hang.promise;
			const pendingStore = createConversationStore();
			await pendingStore.getState().open(pendingService, "ref-1");
			const held = pendingStore.getState().send(pendingService, textInput("in flight"));

			await pendingStore.getState().rehydrate(pendingService);

			expect(pendingStore.getState().failedSubmission).toBeNull();
			expect(pendingStore.getState().pendingSubmission).toBeNull();
			hang.resolve(makeReceipt());
			await held;
			expect(pendingStore.getState().pendingSubmission).toBeNull();

			// ...but a terminal failure's marker persists through the rebind,
			// and the marker persists with it.
			const service = serviceFor("send");
			service.sendShouldReject = new Error("send refused");
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("draft to restore");
			await store.getState().send(service, textInput("draft to restore"));
			expectFailedMarker(store);

			await store.getState().rehydrate(service);

			expectFailedMarker(store);
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
		});

		it("a synchronous reset from an admission subscriber cannot receive the attempt's completion", async () => {
			const service = serviceFor("send");
			const store = createConversationStore({
				mutationHubId: "hub-1",
				mutationSubmitter: {
					submit: async () => ({ clientMutationId: "cmid-sync-reset" }),
				},
			});
			await store.getState().open(service, "ref-1");
			store.getState().setDraft("in flight");
			const unsubscribe = store.subscribe((state) => {
				if (state.pendingSubmission?.admittedMutationId === "cmid-sync-reset") {
					store.getState().reset();
				}
			});
			await store.getState().send(service, textInput("in flight"));
			unsubscribe();
			// The reset retired the attempt inside the admission notification;
			// its completion must not publish into the cleared state.
			expect(store.getState().conversation).toBeNull();
			expect(store.getState().lastAcceptedMutation).toBeNull();
			expect(store.getState().pendingSubmission).toBeNull();
			expect(store.getState().failedSubmission).toBeNull();
		});

		it("a synchronous reset from a read-state subscriber cannot receive the failure's retained state", async () => {
			const service = serviceFor("send");
			service.sendShouldReject = new WireError("action unavailable", -32000, {
				evenerErrorInfo: "actionUnavailable",
			});
			const store = createConversationStore();
			// A projected open binds the read pipeline, so the failure handler's
			// rehydrate request really fires the synchronous read-state
			// notification (a plain open has no read pipeline).
			await store.getState().openProjected(service, "ref-1");
			store.getState().setDraft("draft to restore");
			const unsubscribe = store.subscribe((state) => {
				if (state.latestReadState === "reading") store.getState().reset();
			});
			await store.getState().send(service, textInput("draft to restore"));
			unsubscribe();
			// The reset retired the attempt inside the rehydrate request's
			// synchronous read-state notification; the stale failure must not
			// install its retained state or restore its draft.
			expect(store.getState().failedSubmission).toBeNull();
			expect(store.getState().error).toBeNull();
			expect(store.getState().draft).toBe("");
		});

		it.each(["send", "queue"] as const)(
			"forwards %s composition context without changing encoded input",
			async (kind) => {
				const service = serviceFor(kind);
				const requests: ConversationMutationRequest[] = [];
				const input = textInput("encoded marker payload");
				const context = {
					composer: { text: "original composer text", attachments: [], mentions: [] },
					recoverySourceIds: ["source-1"],
				};
				const store = createConversationStore({
					mutationHubId: "hub-1",
					mutationSubmitter: {
						submit: async (request) => {
							requests.push(request);
							return undefined;
						},
					},
				});
				await store.getState().open(service, "ref-1");
				await store.getState()[kind](service, input, context);
				expect(requests[0]?.input).toBe(input);
				expect(requests[0]?.composer).toBe(context.composer);
				expect(requests[0]?.recoverySourceIds).toBe(context.recoverySourceIds);
				expect(requests[0]).not.toHaveProperty("context");
			},
		);

		it("refuses a submitter without a mutation hub id", () => {
			expect(() =>
				createConversationStore({
					mutationSubmitter: { submit: async () => undefined },
				}),
			).toThrow(/mutationHubId is required/);
		});

		// send and queue are one submission with a kind-specific availability
		// check: where the check refuses, the store is untouched; where it
		// allows, the same transition runs and only the kind differs.
		it.each([
			{ kind: "send", status: "idle", other: "queue" },
			{ kind: "queue", status: "active", other: "send" },
		] as const)(
			"$kind runs the shared submission transition and refuses where $other's state is wrong",
			async ({ kind, status }) => {
				const service = new FakeConversationService();
				service.openConv = makeConversation({ status: { type: status } });
				const store = createConversationStore();
				await store.getState().open(service, "ref-1");
				const refused = kind === "send" ? "queue" : "send";
				store.getState().setDraft("keep me");
				await expect(store.getState()[refused](service, textInput("x"))).rejects.toThrow();
				expect(store.getState().draft).toBe("keep me");
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().failedSubmission).toBeNull();

				await store.getState()[kind](service, textInput("x"));
				expect(store.getState().draft).toBe("");
				expect(store.getState().pendingSubmission).toBeNull();
				expect(store.getState().lastAcceptedMutation?.kind).toBe(kind);
				expect(service.sendCallCount).toBe(kind === "send" ? 1 : 0);
				expect(service.queueCallCount).toBe(kind === "queue" ? 1 : 0);
			},
		);

		it("send has an independent capability gate from queue", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Only send is disabled, in the state that offers it.
			service.openConv = makeConversation({
				capabilities: { ...ALL_TRUE_CAPS, send: false },
			});
			await store.getState().open(service, "ref-1");
			await expect(store.getState().send(service, textInput("x"))).rejects.toThrow(SEND_UNAVAILABLE);
			// queue should work since queue capability is true, once a turn runs
			store.getState().applyNotification(statusChanged({ type: "active" }));
			await store.getState().queue(service, textInput("x"));
			expect(store.getState().error).toBeNull();
		});

		it("queue has an independent capability gate from send", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// Only queue is disabled, in the state that offers it.
			service.openConv = makeConversation({
				status: { type: "active" },
				capabilities: { ...ALL_TRUE_CAPS, queue: false },
			});
			await store.getState().open(service, "ref-1");
			await expect(store.getState().queue(service, textInput("x"))).rejects.toThrow(QUEUE_UNAVAILABLE);
			// send should work since send capability is true, once the turn ends
			store.getState().applyNotification(statusChanged({ type: "idle" }));
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).toBeNull();
		});
	});

	// Slice 7a: the durable pending-row foundation. An in-flight mutation must
	// not be a per-process memory fact: a host-bound seam reads the target's
	// durable outbox/optimistic records and projects them through the shared
	// reconciliation (#2140's shape), so a cold reopen shows the mutation, a
	// rejected or reflected one drops out, and a settled one clears.
	describe("durable pending rows — the host-bound durable projection", () => {
		const TARGET = "hub-1::ref-1";

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

		function recovery(over: Partial<MutationRecoveryRecord> = {}): MutationRecoveryRecord {
			return { ...outbox(), recoveryKind: "rejected", ...over };
		}

		function optimistic(over: Partial<MutationOptimisticRecord> = {}): MutationOptimisticRecord {
			return { ...outbox(), state: "accepted", ...over };
		}

		function fakePort(initial: Partial<MutationPersistenceSnapshot> = {}) {
			let snapshot: MutationPersistenceSnapshot = {
				outbox: [],
				optimistic: [],
				recovery: [],
				...initial,
			};
			const listeners = new Set<() => void>();
			const port = {
				targetRef: TARGET,
				read: async (): Promise<MutationPersistenceSnapshot> => snapshot,
				subscribe: (listener: () => void) => {
					listeners.add(listener);
					return () => {
						listeners.delete(listener);
					};
				},
				isOwnMutationRecord: () => true,
				publish(next: Partial<MutationPersistenceSnapshot>) {
					snapshot = { outbox: [], optimistic: [], recovery: [], ...next };
					for (const listener of [...listeners]) listener();
				},
				listenerCount: () => listeners.size,
			};
			return port satisfies ConversationMutationPendingPort & {
				publish(next: Partial<MutationPersistenceSnapshot>): void;
				listenerCount(): number;
			};
		}

		async function openStore(conversation: MobileConversation = makeConversation()) {
			const service = new FakeConversationService();
			service.openConv = conversation;
			const store = createConversationStore();
			await store.getState().open(service, "ref-1");
			return store;
		}

		// A model whose daemon projection still reports the mutation as pending —
		// the source that carries a settled-out id, and so the one that needs the
		// carried submitted-here provenance for `fromThisClient`/`createdAt`.
		function authoritativeModel(id: string): MobileConversation {
			return makeConversation({
				pendingMutations: [
					{
						clientMutationId: id,
						method: "turn/start",
						input: textInput("carried"),
						executionState: "accepted",
						projectionState: "pending",
					},
				],
			});
		}

		it("shows a durable in-flight row after a cold reopen", async () => {
			const port = fakePort({ outbox: [outbox()] });
			// First app run: the store follows the mutation's durable row.
			const first = await openStore();
			first.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(first.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-1"]);

			// Restart: a fresh store with no memory of the submission, over the same
			// durable storage. Nothing shows until the durable seam is bound...
			const restarted = await openStore();
			expect(restarted.getState().pendingMutations ?? null).toBeNull();
			restarted.getState().bindPendingMutations(port);
			await yieldMicrotask();
			const rows = restarted.getState().pendingMutations;
			expect(rows).toHaveLength(1);
			expect(rows?.[0]).toMatchObject({
				id: "cmid-1",
				method: "send",
				text: "hello",
				state: "submitting",
				source: "outbox",
			} satisfies Partial<PendingTurnEntry>);
		});

		it("a rejected mutation supersedes its pending row — no double row, no zombie", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			// Rejection: the outbox row leaves storage and a recovery row takes over.
			port.publish({
				recovery: [recovery({ recoveryReason: "no such thread" })],
			});
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("clears exactly at settlement and never early", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			// Still submitting: a storage notification that re-serves the same row
			// keeps it — settlement is the row leaving storage, not a timer.
			port.publish({ outbox: [outbox()] });
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);
			// The dispatcher deletes the settled row.
			port.publish({});
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("a mutation the model already reflects never projects as a pending row", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore(
				makeConversation({
					queue: { revision: 1, clientMutationIds: ["cmid-1"] },
				}),
			);
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("ignores a record another target owns", async () => {
			const port = fakePort({
				outbox: [outbox({ targetRef: "hub-2::ref-9" })],
			});
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("unbind stops following and clears; a later change cannot resurrect it", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			const unbind = store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			unbind();
			expect(port.listenerCount()).toBe(0);
			expect(store.getState().pendingMutations).toBeNull();

			port.publish({ outbox: [outbox()] });
			await yieldMicrotask();
			expect(store.getState().pendingMutations ?? null).toBeNull();
		});

		it("a failed durable read leaves the last projection standing", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const originalRead = port.read;
			let fail = false;
			port.read = () => {
				if (fail) return Promise.reject(new Error("storage unavailable"));
				return originalRead();
			};
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			fail = true;
			port.publish({});
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);
		});

		it("a slower older read cannot resurrect a row a newer read removed", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			// Hold the next read open; a later read (the settlement) resolves first.
			let releaseRead: (() => void) | null = null as (() => void) | null;
			const heldRead = new Promise<void>((resolve) => {
				releaseRead = resolve;
			});
			let held = false;
			port.read = async () => {
				if (!held) {
					held = true;
					await heldRead;
					return { outbox: [outbox()], optimistic: [], recovery: [] };
				}
				return { outbox: [], optimistic: [], recovery: [] };
			};
			port.publish({}); // the held, older read
			port.publish({}); // the newer read: the row is gone
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);

			releaseRead?.();
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("rebinding clears the prior target's rows even when the new read fails", async () => {
			const first = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(first);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			// A replacement seam whose first read rejects must not leave the retired
			// target's rows on screen.
			const replacement = fakePort();
			replacement.read = () => Promise.reject(new Error("storage unavailable"));
			store.getState().bindPendingMutations(replacement);
			expect(store.getState().pendingMutations ?? null).toBeNull();
			await yieldMicrotask();
			expect(store.getState().pendingMutations ?? null).toBeNull();
		});

		it("a model write drops a now-reflected row without a storage notification", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			// The model now reflects the id through a live frame — no durable read.
			store.getState().applyNotification({
				method: "thread/queueChanged",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					queue: { revision: 2, clientMutationIds: ["cmid-1"] },
				},
			} as AnyNotification);
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("opening a different thread retires the prior target's seam", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(port.listenerCount()).toBe(1);
			expect(store.getState().pendingMutations).toHaveLength(1);

			const other = new FakeConversationService();
			other.openConv = makeConversation({ ref: "ref-2", threadId: "thread-2" });
			await store.getState().open(other, "ref-2");

			expect(port.listenerCount()).toBe(0);
			expect(store.getState().pendingMutations ?? null).toBeNull();
		});

		it("bindPendingMutationsIfUnbound binds once and leaves a live seam untouched", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			const unbind = store.getState().bindPendingMutationsIfUnbound(port);
			expect(unbind).not.toBeNull();
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);
			expect(port.listenerCount()).toBe(1);

			// Already bound: a second call is a no-op, leaving the live subscription
			// and the published rows in place (no churn).
			const second = fakePort({ outbox: [outbox({ clientMutationId: "cmid-2" })] });
			expect(store.getState().bindPendingMutationsIfUnbound(second)).toBeNull();
			expect(second.listenerCount()).toBe(0);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);
			expect(store.getState().pendingMutations?.[0]?.id).toBe("cmid-1");
		});

		it("bindPendingMutationsIfUnbound re-establishes the seam after a thread open retires it", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutationsIfUnbound(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			// A host-initiated open (the /clear cleared callback, a refresh) retires
			// the seam and clears the rows.
			const other = new FakeConversationService();
			other.openConv = makeConversation();
			await store.getState().open(other, "ref-1");
			expect(port.listenerCount()).toBe(0);
			expect(store.getState().pendingMutations ?? null).toBeNull();

			// The host's rebind re-establishes it: the durable rows are followed
			// again for the reopened conversation.
			const rebound = fakePort({ outbox: [outbox()] });
			expect(store.getState().bindPendingMutationsIfUnbound(rebound)).not.toBeNull();
			await yieldMicrotask();
			expect(rebound.listenerCount()).toBe(1);
			expect(store.getState().pendingMutations).toHaveLength(1);
		});

		it("reopening the same ref through a different target retires the prior seam", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(port.listenerCount()).toBe(1);

			// The same wire ref through a different hub/service: the durable target
			// key differs, and the store cannot see the hub, so the seam retires.
			const otherHub = new FakeConversationService();
			otherHub.openConv = makeConversation({
				ref: "ref-1",
				threadId: "thread-1",
			});
			await store.getState().open(otherHub, "ref-1");

			expect(port.listenerCount()).toBe(0);
			expect(store.getState().pendingMutations ?? null).toBeNull();
		});

		it("a read a newer read superseded still records submitted-here provenance", async () => {
			const port = fakePort({});
			const store = await openStore(authoritativeModel("cmid-9"));
			// The first read is held and observes the durable record; a newer read
			// resolves first against storage where the record is already settled out.
			let releaseRead: (() => void) | null = null as (() => void) | null;
			const heldRead = new Promise<void>((resolve) => {
				releaseRead = resolve;
			});
			let held = false;
			port.read = async () => {
				if (!held) {
					held = true;
					await heldRead;
					return {
						outbox: [outbox({ clientMutationId: "cmid-9" })],
						optimistic: [],
						recovery: [],
					};
				}
				return { outbox: [], optimistic: [], recovery: [] };
			};
			store.getState().bindPendingMutations(port);
			port.publish({}); // the newer read: the record is gone
			await yieldMicrotask();
			await yieldMicrotask();
			releaseRead?.(); // the held, older read resolves late
			await yieldMicrotask();
			await yieldMicrotask();

			// The superseded read's provenance must reach the already-published row
			// with no further storage notification.
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-9",
				fromThisClient: true,
				createdAt: 7,
			});
		});

		it("a same-target rebind keeps the client's submitted-here provenance", async () => {
			const store = await openStore(authoritativeModel("cmid-7"));
			const first = fakePort({
				outbox: [outbox({ clientMutationId: "cmid-7" })],
			});
			store.getState().bindPendingMutations(first);
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-7",
				// The daemon's own projection describes the same id, so the entry is
				// authoritative from the start; the durable record supplies provenance.
				source: "authoritative",
				fromThisClient: true,
			});

			// A same-target rebind whose read shows the record settled out: the
			// daemon still reports the id, and the carried provenance must survive
			// the rebind to keep `fromThisClient`/`createdAt` honest.
			const rebound = fakePort({});
			store.getState().bindPendingMutations(rebound);
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-7",
				source: "authoritative",
				fromThisClient: true,
				createdAt: 7,
			});
		});

		it("a failed open does not leave the retired target's rows visible", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			const failing = new FakeConversationService();
			failing.open = async () => {
				throw new Error("open failed");
			};
			await store.getState().open(failing, "ref-1");
			expect(store.getState().status).toBe("error");
			expect(store.getState().pendingMutations ?? null).toBeNull();
		});

		it("a same-target reopen keeps the client's submitted-here provenance", async () => {
			const store = await openStore(authoritativeModel("cmid-7"));
			const port = fakePort({
				outbox: [outbox({ clientMutationId: "cmid-7" })],
			});
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();

			// Reopen the same target, then rebind with the durable record gone: the
			// daemon still reports the id, and the carried provenance must survive.
			const reopen = new FakeConversationService();
			reopen.openConv = authoritativeModel("cmid-7");
			await store.getState().open(reopen, "ref-1");
			const rebound = fakePort({});
			store.getState().bindPendingMutations(rebound);
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-7",
				fromThisClient: true,
				createdAt: 7,
			});
		});

		it("a synchronous read throw on a notification keeps the last projection", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			port.read = () => {
				throw new Error("storage unavailable");
			};
			port.publish({});
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);
		});

		it("a synchronous read throw on the first read leaves the projection null", async () => {
			const port = fakePort();
			port.read = () => {
				throw new Error("storage unavailable");
			};
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations ?? null).toBeNull();
		});

		it("a foreign-target record never pollutes submitted-here provenance", async () => {
			const port = fakePort({
				outbox: [outbox({ clientMutationId: "cmid-9", targetRef: "hub-2::ref-9" })],
			});
			const store = await openStore(authoritativeModel("cmid-9"));
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();

			// The foreign row is not projected (target-scoped) — and it must not have
			// recorded the id as this client's own submission either.
			const rows = store.getState().pendingMutations;
			expect(rows?.map((row) => row.id)).toEqual(["cmid-9"]);
			expect(rows?.[0]?.fromThisClient).toBe(false);
			expect(rows?.[0]?.createdAt).toBeUndefined();
		});

		it("a failed read does not suppress an earlier successful read", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			// Read #1 is held; read #2 fails before #1 resolves. #1's newer state
			// must still publish — a failed read advances nothing.
			let releaseRead: (() => void) | null = null as (() => void) | null;
			const heldRead = new Promise<void>((resolve) => {
				releaseRead = resolve;
			});
			let invocation = 0;
			port.read = () => {
				invocation += 1;
				if (invocation === 1) {
					return heldRead.then(() => ({
						outbox: [],
						optimistic: [],
						recovery: [],
					}));
				}
				return Promise.reject(new Error("storage unavailable"));
			};
			port.publish({});
			port.publish({});
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			releaseRead?.();
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("a conversation write that does not change the rows keeps the projection reference", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			const before = store.getState().pendingMutations;
			expect(before).toHaveLength(1);

			store.getState().applyNotification({
				method: "thread/queueChanged",
				params: { threadId: "thread-1", ref: "ref-1", queue: { revision: 9 } },
			} as AnyNotification);
			expect(store.getState().pendingMutations).toBe(before);
		});

		it("openProjected retires the prior target's seam", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(port.listenerCount()).toBe(1);

			const other = new FakeConversationService();
			other.openConv = makeConversation({ ref: "ref-2", threadId: "thread-2" });
			await store.getState().openProjected(other, "ref-2");
			expect(port.listenerCount()).toBe(0);
			expect(store.getState().pendingMutations ?? null).toBeNull();
		});

		it("settles the ids a live frame proves delivered and clears them at once", async () => {
			const port = fakePort({ optimistic: [optimistic({ clientMutationId: "cmid-3" })] });
			const settled: string[][] = [];
			const configured = {
				...port,
				settleIdentities: async (ids: readonly string[]) => {
					settled.push([...ids]);
					port.publish({ optimistic: [] });
				},
			};
			const store = await openStore(makeConversation({}, true));
			store.getState().bindPendingMutations(configured);
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-3"]);

			store.getState().applyNotification(
				historyUpdated({
					items: [{ type: "user", id: "item-1", turnId: "turn-1", clientMutationId: "cmid-3" }],
					snapshot: { incarnation: "inc-1", length: 2 },
				}),
			);

			await yieldMicrotask();
			expect(settled).toEqual([["cmid-3"]]);
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("settles nothing for a frame about another thread", async () => {
			const port = fakePort({ optimistic: [optimistic({ clientMutationId: "cmid-3" })] });
			const settled: string[][] = [];
			const store = await openStore(makeConversation({}, true));
			store.getState().bindPendingMutations({
				...port,
				settleIdentities: async (ids: readonly string[]) => void settled.push([...ids]),
			});
			await yieldMicrotask();

			store.getState().applyNotification({
				method: "history/updated",
				params: {
					threadId: "thread-other",
					ref: "ref-other",
					bootGeneration: "1",
					epoch: 1,
					snapshot: { incarnation: "inc-1", length: 2 },
					items: [{ type: "user", id: "item-1", turnId: "turn-1", clientMutationId: "cmid-3" }],
				},
			} as AnyNotification);

			await yieldMicrotask();
			expect(settled).toEqual([]);
			expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual(["cmid-3"]);
		});

		it("projects an optimistic record", async () => {
			const port = fakePort({
				optimistic: [optimistic({ clientMutationId: "cmid-3" })],
			});
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-3",
				source: "optimistic",
				state: "accepted",
			});
		});

		it("an optimistic record the model reflects never projects", async () => {
			const port = fakePort({
				optimistic: [optimistic({ clientMutationId: "cmid-3" })],
			});
			const store = await openStore(
				makeConversation({
					queue: { revision: 1, clientMutationIds: ["cmid-3"] },
				}),
			);
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("ignores an optimistic record another target owns", async () => {
			const port = fakePort({
				optimistic: [optimistic({ targetRef: "hub-2::ref-9" })],
			});
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("a durable read that re-serves identical rows keeps the projection reference", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			const before = store.getState().pendingMutations;
			expect(before).toHaveLength(1);

			port.publish({ outbox: [outbox()] });
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toBe(before);
		});

		it("a record the ownership predicate rejects never claims this client's provenance", async () => {
			const port = fakePort({
				outbox: [outbox({ clientMutationId: "cmid-4" })],
			});
			port.isOwnMutationRecord = () => false;
			const store = await openStore(authoritativeModel("cmid-4"));
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-4",
				fromThisClient: false,
			});

			// After the record settles out, the daemon still reports the id: the
			// rejected predicate must have left nothing in the carried provenance.
			const rebound = fakePort({});
			rebound.isOwnMutationRecord = () => false;
			store.getState().bindPendingMutations(rebound);
			await yieldMicrotask();
			const rows = store.getState().pendingMutations;
			expect(rows?.[0]).toMatchObject({ id: "cmid-4", fromThisClient: false });
			expect(rows?.[0]?.createdAt).toBeUndefined();
		});

		it("a conversation write never exposes a stale pending row to a subscriber", async () => {
			const port = fakePort({ outbox: [outbox()] });
			const store = await openStore();
			store.getState().bindPendingMutations(port);
			await yieldMicrotask();
			expect(store.getState().pendingMutations).toHaveLength(1);

			// A subscriber must never observe the model reflecting the mutation while
			// pendingMutations still holds its row: the reconcile rides the same write
			// as the model commit.
			let sawStale = false;
			const unsubscribe = store.subscribe((state) => {
				const reflected = state.conversation?.queue?.clientMutationIds ?? [];
				if (reflected.includes("cmid-1") && (state.pendingMutations ?? []).some((row) => row.id === "cmid-1")) {
					sawStale = true;
				}
			});
			store.getState().applyNotification({
				method: "thread/queueChanged",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					queue: { revision: 2, clientMutationIds: ["cmid-1"] },
				},
			} as AnyNotification);
			unsubscribe();

			expect(sawStale).toBe(false);
			expect(store.getState().pendingMutations).toEqual([]);
		});

		it("a read in flight across a same-target rebind still records provenance", async () => {
			const first = fakePort({});
			let releaseRead: (() => void) | null = null as (() => void) | null;
			const heldRead = new Promise<void>((resolve) => {
				releaseRead = resolve;
			});
			let reads = 0;
			first.read = async () => {
				reads += 1;
				if (reads === 1) {
					await heldRead;
					return {
						outbox: [outbox({ clientMutationId: "cmid-11" })],
						optimistic: [],
						recovery: [],
					};
				}
				return { outbox: [], optimistic: [], recovery: [] };
			};
			const store = await openStore(authoritativeModel("cmid-11"));
			store.getState().bindPendingMutations(first); // read #1 held in flight

			// A same-target rebind: its read resolves against storage where the
			// record has already settled out, while the daemon still reports the id.
			const rebound = fakePort({});
			store.getState().bindPendingMutations(rebound);
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-11",
				fromThisClient: false,
			});

			// The in-flight read lands after the rebind; its provenance must stick.
			releaseRead?.();
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().pendingMutations?.[0]).toMatchObject({
				id: "cmid-11",
				fromThisClient: true,
				createdAt: 7,
			});
		});

		// Each case runs on idle and on systemError. The owner's ruling on #2514
		// (merged the same day): a session resting on a failed turn reports
		// status systemError until its next turn, and deriveSendQueueAvailability
		// already treats systemError as a live resting snapshot
		// (LIVE_SNAPSHOT_STATUSES). The queue gate must behave the same on this
		// status as it does on idle.
		it.each(["idle", "systemError"] as const)(
			"queues a second message while this client's own send is still unreflected, resting on %s",
			async (status) => {
				const { service, store } = await openedStore(makeConversation({ status: { type: status } }));
				store.getState().bindPendingMutations(fakePort({ outbox: [outbox()] }));
				await yieldMicrotask();
				expect(store.getState().conversation?.status.type).toBe(status);
				await store.getState().queue(service, textInput("second"));
				expect(store.getState().error).toBeNull();
				expect(service.queueCallCount).toBe(1);
			},
		);

		it.each(["idle", "systemError"] as const)(
			"still refuses a queue resting on %s with nothing of this client's in flight",
			async (status) => {
				const { service, store } = await openedStore(makeConversation({ status: { type: status } }));
				store.getState().bindPendingMutations(fakePort({ outbox: [outbox({ state: "canceled" })] }));
				await yieldMicrotask();
				await expect(store.getState().queue(service, textInput("second"))).rejects.toThrow("no active turn");
				expect(service.queueCallCount).toBe(0);
			},
		);
	});

	// The web's rule (decision 2): a refused mutation surfaces its typed error
	// and the model converges through the reducer — one coalesced reread of the
	// authoritative snapshot — not through a bespoke capability read and write.
	describe("actionUnavailable surfaces the error and requests one coalesced reread", () => {
		it("surfaces the error and rereads once; the reread's capabilities are what the composer sees", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({ evener: { ref: "ref-1", capabilities: { ...ALL_TRUE_CAPS }, queue: { revision: 0 } } }),
			);
			await store.getState().openProjected(service, "ref-1");
			expect(store.getState().conversation?.capabilities.send).toBe(true);
			const readsBefore = service.readProjectionCalls.length;
			// The hub refuses: its capabilities moved on without a status frame we saw.
			service.sendShouldReject = refusal();
			service.readProjectionResult = makeReadProjectionResult(
				makeThread({
					evener: { ref: "ref-1", capabilities: { ...ALL_TRUE_CAPS, send: false }, queue: { revision: 0 } },
				}),
			);
			const ctrl = makeControlledRead(service);
			await store.getState().send(service, textInput("x"));
			expect(store.getState().error).not.toBeNull();
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
			await ctrl.ready(1);
			ctrl.release();
			await ctrl.completed(1);
			await yieldMicrotask();
			expect(service.readProjectionCalls.length).toBe(readsBefore + 1);
			expect(store.getState().conversation?.capabilities.send).toBe(false);
		});

		// The non-projected open() binds no read pipeline and no production screen
		// uses it (a compatibility surface for test mocks): a refusal there
		// surfaces the error and issues no read at all.
		it("plain open(): actionUnavailable surfaces the error and issues no read", async () => {
			const { service, store } = await openedStore();
			const readsBefore = service.readProjectionCalls.length;
			service.sendShouldReject = refusal();
			await store.getState().send(service, textInput("x"));
			await yieldMicrotask();
			await yieldMicrotask();
			expect(store.getState().error).not.toBeNull();
			expect(store.getState().failedSubmission).toMatchObject({ kind: "send" });
			expect(service.readProjectionCalls.length).toBe(readsBefore);
		});
	});
});
