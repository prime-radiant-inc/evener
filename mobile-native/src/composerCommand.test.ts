import { expect, it, vi } from "vitest";
import { parseSlashToken, spliceSlashCommand } from "@evener/appwire-client";
import type { InputItem } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationMutationContext } from "./conversation/state/conversationMutation";
import type { MobileConversation } from "./projectedRows";
import { builtinComposerItems, composerCommand, submitComposerCommand } from "./composerCommand";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { NativeMutationRuntime, nativeMutationTargetKey, type NativeMutationRequest } from "./nativeMutationRuntime";
import { editQueuedMessageIntoDraft, reconcileEditedResends } from "./queuedMessageControls";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", async () => ({
	randomUUID: (await import("node:crypto")).randomUUID,
	getRandomValues: (array: Uint8Array) => array,
}));

it.each(["queue", "steer"] as const)("captures /%s body metadata before encoding", async (kind) => {
	const { database, port } = openSqliteSyncDouble();
	try {
		const repository = new DraftRepository(port);
		const destination = { hubId: "hub-1", sessionRef: "ref-1" };
		const prefix = `/${kind} `;
		repository.write(destination, {
			draft: `${prefix}/skill payload`,
			unconfirmed: null,
			composer: {
				skillNames: ["skill"],
				mentions: [{ kind: "skill", name: "skill", offset: prefix.length }],
				recoveryIds: ["source-1", "source-2"],
			},
		});
		const document = new DraftDocument(
			() => repository,
			destination,
			undefined,
			() => "composer-command-capture",
		);
		const submissions: Array<{ kind: string; input: InputItem[]; context?: ConversationMutationContext }> = [];
		await submitComposerCommand(document, {} as Parameters<typeof submitComposerCommand>[1], {
			isCurrent: () => true,
			local: async () => {},
			openAside: () => {},
			cleared: () => {},
			turn: () => conversation({ queue: true, steer: true }, "active"),
			drainQueue: async () => {},
			drainRefusal: () => null,
			submit: async (submittedKind, input, context) => {
				submissions.push({ kind: submittedKind, input, context });
			},
			stop: async () => {},
			reasoning: () => null,
		});
		expect(submissions).toHaveLength(1);
		expect(submissions[0].kind).toBe(kind);
		expect(submissions[0].context).toEqual({
			composer: {
				text: "/skill payload",
				attachments: [],
				skillNames: ["skill"],
				mentions: [{ kind: "skill", name: "skill", offset: 0 }],
			},
			recoverySourceIds: ["source-1", "source-2"],
			draftCaptureToken: "composer-command-capture",
		});
		expect(submissions[0].input).toEqual([
			{ type: "text", text: "/skill payload", mentions: [{ kind: "skill", name: "skill", offset: 0 }] },
			{ type: "skill", name: "skill" },
		]);
	} finally {
		database.close();
	}
});

it.each(["queue", "steer"] as const)("persists distinct captured tokens for two /%s admissions", async (kind) => {
	const drafts = openSqliteSyncDouble();
	const mutations = openSqliteSyncDouble();
	const destination = { hubId: "hub-1", sessionRef: "ref-1" };
	const targetKey = nativeMutationTargetKey(destination.hubId, destination.sessionRef);
	const client = new FakeClient("closed");
	let nextMutation = 0;
	const runtime = new NativeMutationRuntime(mutations.port, {
		createMutationId: () => `lineage-mutation-${++nextMutation}`,
		setInterval: () => 1,
		clearInterval: () => {},
	});
	const repository = new DraftRepository(drafts.port);
	let nextCapture = 0;
	const document = new DraftDocument(
		() => repository,
		destination,
		undefined,
		() => `capture-${++nextCapture}`,
	);
	runtime.registerTarget(destination.hubId, destination.sessionRef, client, {
		threadId: "thread-1",
		instanceId: "instance-1",
	});
	try {
		const capturedTokens: string[] = [];
		for (const body of ["first", "second"]) {
			document.edit(`/${kind} ${body}`);
			await submitComposerCommand(document, {} as Parameters<typeof submitComposerCommand>[1], {
				isCurrent: () => true,
				local: async () => {},
				openAside: () => {},
				cleared: () => {},
				turn: () => conversation({ queue: true, steer: true }, "active"),
				drainQueue: async () => {},
				drainRefusal: () => null,
				submit: async (submittedKind, input, context) => {
					capturedTokens.push(repository.read(destination).unconfirmedCaptureToken ?? "");
					await runtime.submit({
						hubId: destination.hubId,
						targetRef: destination.sessionRef,
						threadId: "thread-1",
						instanceId: "instance-1",
						kind: submittedKind,
						input,
						composer: context?.composer,
						draftCaptureToken: context?.draftCaptureToken,
					});
				},
				stop: async () => {},
				reasoning: () => null,
			});
		}

		const rows = (await runtime.read(targetKey)).outbox;
		expect(rows).toHaveLength(2);
		expect(rows.map((row) => row.composerText)).toEqual(["first", "second"]);
		const admittedTokens = rows.map((row) => row.draftCaptureToken);
		expect(capturedTokens).toEqual(["capture-1", "capture-2"]);
		expect(admittedTokens).toEqual(capturedTokens);
		expect(new Set(admittedTokens).size).toBe(2);
	} finally {
		await runtime.stop();
		drafts.database.close();
		mutations.database.close();
	}
});

it.each(["queue", "steer"] as const)(
	"/%s retains an edited source until the real resend receipt settles",
	async (kind) => {
		const drafts = openSqliteSyncDouble();
		const mutations = openSqliteSyncDouble();
		const destination = { hubId: "hub-1", sessionRef: "ref-1" };
		const targetKey = nativeMutationTargetKey(destination.hubId, destination.sessionRef);
		const client = new FakeClient("closed");
		let nextMutation = 0;
		const runtime = new NativeMutationRuntime(mutations.port, {
			createMutationId: () => `mutation-${++nextMutation}`,
			setInterval: () => 1,
			clearInterval: () => {},
		});
		const repository = new DraftRepository(drafts.port);
		const document = new DraftDocument(() => repository, destination);
		runtime.registerTarget(destination.hubId, destination.sessionRef, client, {
			threadId: "thread-1",
			instanceId: "instance-1",
		});
		const request = (
			kind: "send" | "queue" | "steer",
			input: InputItem[] = [{ type: "text", text: "source message" }],
			context?: ConversationMutationContext,
		): NativeMutationRequest => ({
			hubId: destination.hubId,
			targetRef: destination.sessionRef,
			threadId: "thread-1",
			instanceId: "instance-1",
			kind,
			input,
			...(context?.composer === undefined ? {} : { composer: context.composer }),
			...(context?.recoverySourceIds === undefined ? {} : { recoverySourceIds: context.recoverySourceIds }),
		});
		try {
			await runtime.submit(request("send"));
			const sourceId = (await runtime.read(targetKey)).outbox[0].clientMutationId;
			expect(await editQueuedMessageIntoDraft(runtime, targetKey, sourceId, document)).toBe("restored");
			document.edit(`/${kind} source message`);

			const submitted: Array<{ kind: string; input: InputItem[]; context?: ConversationMutationContext }> = [];
			await submitComposerCommand(document, {} as Parameters<typeof submitComposerCommand>[1], {
				isCurrent: () => true,
				local: async () => {},
				openAside: () => {},
				cleared: () => {},
				turn: () => conversation({ queue: true, steer: true }, "active"),
				drainQueue: async () => {},
				drainRefusal: () => null,
				submit: async (submittedKind, input, context) => {
					submitted.push({ kind: submittedKind, input, context });
				},
				stop: async () => {},
				reasoning: () => null,
			});

			expect(submitted).toHaveLength(1);
			expect(submitted[0]).toMatchObject({
				kind,
				input: [{ type: "text", text: "source message" }],
				context: { recoverySourceIds: [sourceId] },
			});
			expect(repository.read(destination)).toMatchObject({
				draft: "",
				unconfirmed: `/${kind} source message`,
				unconfirmedComposer: { recoveryIds: [sourceId] },
			});
			expect(repository.read(destination).completedCommandRecoveryIds).toBeUndefined();
			expect((await runtime.read(targetKey)).recovery.map((source) => source.clientMutationId)).toEqual([sourceId]);

			await runtime.submit(request(kind, submitted[0].input, submitted[0].context));
			const receipt = await runtime.readRecoveryResend(targetKey, [sourceId]);
			expect(receipt).toBeDefined(); // Real outbox receipt, not the handler callback's success value.
			expect(receipt).toMatchObject({ sourceIds: [sourceId] });
			const admitted = await runtime.read(targetKey);
			expect(admitted.outbox).toHaveLength(1);
			expect(admitted.outbox[0].clientMutationId).toBe(receipt?.clientMutationId);
			expect(admitted.outbox[0]).toMatchObject({
				method: kind === "queue" ? "turn/queue" : "turn/steer",
				payload: { input: submitted[0].input },
				composerText: submitted[0].context?.composer?.text,
			});
			await reconcileEditedResends(runtime, targetKey, document);
			expect(document.getSnapshot().record.unconfirmed).toBeNull();
			expect(document.getSnapshot().record.completedCommandRecoveryIds).toBeUndefined();
			expect((await runtime.read(targetKey)).recovery).toEqual([]);
		} finally {
			await runtime.stop();
			drafts.database.close();
			mutations.database.close();
		}
	},
);

it.each([
	["/compact", "compact", ""],
	["/compact \t", "compact", ""],
	["/shutdown", "shutdown", ""],
	["/goal first\nsecond", "goal", "first\nsecond"],
	["/goal", "goal", ""],
])("recognizes the web invocation %s", (text, id, argsText) => {
	expect(composerCommand(text)).toMatchObject({ command: { id }, argsText });
	expect(composerCommand(text, 1)).toBeNull();
});

it("keeps explicitly selected leading /compact out of builtin submission", async () => {
	const { database, port } = openSqliteSyncDouble();
	try {
		const destination = { hubId: "hub-1", sessionRef: "ref-1" };
		const repository = new DraftRepository(port);
		repository.write(destination, {
			draft: "/compact",
			unconfirmed: null,
			composer: {
				commandNames: ["compact"],
				mentions: [{ kind: "command", name: "compact", offset: 0 }],
				recoveryIds: ["source-compact"],
			},
		});
		const document = new DraftDocument(() => repository, destination);
		const submitted: Array<{ kind: string; input: InputItem[]; context?: ConversationMutationContext }> = [];
		const compact = vi.fn(async () => {});
		await submitComposerCommand(document, { compact } as unknown as Parameters<typeof submitComposerCommand>[1], {
			isCurrent: () => true,
			local: async () => {},
			openAside: () => {},
			cleared: () => {},
			turn: () => conversation({ compact: true }),
			drainQueue: async () => {},
			drainRefusal: () => null,
			submit: async (kind, input, context) => {
				submitted.push({ kind, input, context });
			},
			stop: async () => {},
			reasoning: () => null,
		});
		expect(composerCommand("/compact", 0, document.getSnapshot().record.composer)).toBeNull();
		expect(
			composerCommand("/compact", 0, {
				skillNames: ["compact"],
				mentions: [{ kind: "skill", name: "compact", offset: 0 }],
			}),
		).toBeNull();
		expect(
			composerCommand("/compact", 0, {
				skillNames: ["compact"],
				mentions: [{ kind: "skill", name: "compact", offset: 1 }],
			}),
		).toMatchObject({ command: { id: "compact" } });
		expect(compact).not.toHaveBeenCalled();
		expect(submitted).toEqual([]);
	} finally {
		database.close();
	}
});

it.each([
	"/compact extra",
	"/shutdown extra",
	"/Compact",
	" /compact",
	"/plugin:compact",
	"/unknown",
	"/goal\nobjective",
])("leaves ordinary messages intact: %s", (text) => {
	expect(composerCommand(text)).toBeNull();
});

// The completion registry reads the conversation the way the web's palette
// does (@evener/appwire-client's sessionControls): the
// harness's capabilities, and for steering the status and queue depth too.
function conversation(capabilities: Partial<MobileConversation["capabilities"]>, status = "idle", depth = 0) {
	return { capabilities, status: { type: status }, queue: { revision: 0, depth } };
}

function ids(items: ReturnType<typeof builtinComposerItems>) {
	return items.map((item) => item.invocation);
}

// The hub advertises steer as harness support (not "a turn is running"), so an
// idle steering harness carries steer:true; the menu applies the status:
// /steer needs a running turn, /drain-as-steer a running turn or a queue a
// Stop parked (idle with depth > 0).
it.each([
	["idle", 0, false, false],
	["idle", 1, false, true],
	// Argless /drain-as-steer sends the queue alone: nothing queued, nothing offered.
	["active", 0, true, false],
	["active", 1, true, true],
])("at status %s with queue depth %d offers /steer=%s and /drain-as-steer=%s", (status, depth, steer, drain) => {
	const offered = ids(builtinComposerItems(conversation({ steer: true, queue: true }, status, depth)));
	expect(offered.includes("/steer")).toBe(steer);
	expect(offered.includes("/drain-as-steer")).toBe(drain);
});

// /interrupt is Stop: sessionControls' stop, an active status and the
// interrupt capability, never the capability alone.
it.each([
	["idle", false],
	["active", true],
])("at status %s offers /interrupt=%s on a harness that advertises interrupt", (status, offered) => {
	const items = ids(builtinComposerItems(conversation({ interrupt: true }, status)));
	expect(items.includes("/interrupt")).toBe(offered);
});

it("offers capability-backed builtins without inventing unsupported actions", () => {
	const items = builtinComposerItems(conversation({ compact: true, goal: true }));
	expect(items.map((item) => item.invocation)).toContain("/compact");
	expect(items.map((item) => item.invocation)).toContain("/goal");
	expect(items.map((item) => item.invocation)).toContain("/project");
	expect(items.map((item) => item.invocation)).toContain("/tasks");
	expect(items.map((item) => item.invocation)).not.toContain("/clear");
	expect(items.map((item) => item.invocation)).not.toContain("/interrupt");
	expect(items.map((item) => item.invocation)).not.toContain("/fork");
});

it("recomputes completion availability when session capabilities change", () => {
	expect(builtinComposerItems(conversation({ clear: true })).some((item) => item.invocation === "/clear")).toBe(true);
	expect(builtinComposerItems(conversation({ clear: false })).some((item) => item.invocation === "/clear")).toBe(false);
});

it("inserts a builtin through the web splice contract without losing surrounding draft text", () => {
	const item = builtinComposerItems(conversation({ goal: true })).find((item) => item.invocation === "/goal");
	if (!item) throw new Error("missing goal completion");
	const draft = "/go keep this objective";
	const token = parseSlashToken(draft, 3);
	if (!token) throw new Error("missing slash token");
	const result = spliceSlashCommand(draft, token, item.invocation);
	expect(result.text).toBe("/goal keep this objective");
	expect(composerCommand(result.text)).toMatchObject({
		command: { id: "goal" },
		argsText: "keep this objective",
	});
});
