import { expect, it } from "vitest";
import { parseSlashToken, spliceSlashCommand } from "@evener/appwire-client";
import type { InputItem } from "@evener/appwire-client";
import type { ConversationMutationContext } from "../../mobile/src/state/conversationMutation";
import type { MobileConversation } from "./projectedRows";
import { builtinComposerItems, composerCommand, submitComposerCommand } from "./composerCommand";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";

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
		const document = new DraftDocument(() => repository, destination);
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
		});
		expect(submissions[0].input).toEqual([
			{ type: "text", text: "/skill payload", mentions: [{ kind: "skill", name: "skill", offset: 0 }] },
			{ type: "skill", name: "skill" },
		]);
	} finally {
		database.close();
	}
});

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
