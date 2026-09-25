// D24 slices 5+6 — the transcript content level, applied at the seam. The
// store's projection (projectConversation, re-homed to the shared-projector
// row adapter) takes the user's resolved display config, so which rows exist
// and what each carries is decided ONCE, at projection time, inside the
// store's display boundary — not again at the screen (the rejected screens
// route re-projected per render, thrashing the per-turn row cache twice per
// row-changing frame and bypassing capItems/truncateItem).
//
// This suite adapts the D24-5 behavior bar (codex/d24-5-content-levels,
// screens.projectDisplay.test.ts) to the seam route: the same behavior — chat
// hides a settled activity and empties a running thought, full shows
// everything, a level change re-projects, and the hub's shipped mobile default
// applies with no native toggle — asserted against the seam's own projection
// and the presentation layer that maps it. It also pins the operator's
// summary-only ruling (Sep-25): a summarized tool-action row carries ONLY its
// summary line at compact levels; its full detail returns at
// tools/activity/full.
import { describe, expect, it } from "vitest";
import type { Thread, ThreadItem, Turn } from "@evener/appwire-client";
import {
	hydrateThread,
	makeTranscriptDisplayConfig,
	shippedMobileConfig,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { projectConversation } from "./projectedRows";
import { projectNativeTranscript } from "./transcriptPresentation";

const CAPS = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
};

function item(over: Partial<ThreadItem> & { id: string; type: string }): ThreadItem {
	return { turnId: "t1", ...over } as ThreadItem;
}

function turn(id: string, items: ThreadItem[], over: Partial<Turn> = {}): Turn {
	return { id, items, itemsView: "default", status: "completed", ...over };
}

function thread(turns: Turn[]): Thread {
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1_000_000,
		updatedAt: 1_000_000,
		status: { type: "ready" },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns,
		evener: { ref: "ref-1", capabilities: CAPS, queue: {} },
	} as unknown as Thread;
}

const SHELL_ARGS = JSON.stringify({ cmd: "ls" });
const SHELL_OUTPUT = "tool output text";

// One thread walking the bar's subjects: a summarized tool action (a completed
// shell call with a description the projector trims into its rationale, plus
// full arguments/output), a settled thought, and a live current thought on a
// running turn.
function levelsThread(): Thread {
	return thread([
		turn("t1", [
			item({ id: "u1", type: "userMessage", text: "please audit the config" }),
			item({
				id: "c1",
				type: "commandExecution",
				toolName: "shell",
				description: "  run the audit  ",
				argumentsJson: SHELL_ARGS,
				output: SHELL_OUTPUT,
				outputImages: [{ source: "screenshot", name: "audit.png", url: "http://x/audit.png" }],
				status: "completed",
			}),
			item({
				id: "f1",
				type: "commandExecution",
				toolName: "shell",
				description: "audit the failure",
				outputImages: [{ source: "screenshot", name: "failure.png", url: "http://x/failure.png" }],
				status: "failed",
				error: "opaque failure text",
			}),
			item({ id: "r1", type: "reasoning", text: "auditing quietly", status: "completed" }),
		]),
		turn(
			"t2",
			[item({ id: "r2", type: "reasoning", text: "secret live thought", status: "inProgress" })],
			{ status: "inProgress" },
		),
	]);
}

// The seam's own projection, at a config: exactly what the store publishes.
function rowsAt(config: TranscriptDisplayConfigV1 | null) {
	const model = hydrateThread({ thread: levelsThread() }, "ref-1", 0);
	return projectConversation(model, undefined, config ?? undefined).items;
}

const rowById = (items: ReturnType<typeof rowsAt>, id: string) =>
	items.find((row) => row.id === id);

const chat = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
const intent = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" });
const tools = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
const full = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });

describe("the seam projects the conversation at the user's content level", () => {
	it("hides a settled thought and empties a running one at chat", () => {
		const rows = rowsAt(chat);
		expect(JSON.stringify(rows)).not.toContain("auditing quietly");
		// The projector's content-free placeholder: the row survives (a live
		// thought is attention-worthy) but carries no thought text.
		expect(rowById(rows, "r2")).toMatchObject({
			kind: "activity",
			label: "Reasoning",
			state: "running",
		});
		expect(JSON.stringify(rows)).not.toContain("secret live thought");
	});

	it("keeps the thoughts and the tool's full detail at full", () => {
		const rows = rowsAt(full);
		expect(rowById(rows, "r1")).toMatchObject({
			kind: "activity",
			detail: { output: "auditing quietly" },
		});
		// The running thought is a live current item entry at full: same
		// reasoning family as the settled thought, so the two cluster across
		// the turn boundary and the live one rides as a member.
		const r1 = rowById(rows, "r1");
		expect(r1?.kind === "activity" && r1.members?.some(
			(member) => member.id === "r2" && member.detail.output === "secret live thought",
		)).toBe(true);
		expect(rowById(rows, "c1")).toMatchObject({
			kind: "activity",
			detail: {
				description: "  run the audit  ",
				arguments: SHELL_ARGS,
				output: SHELL_OUTPUT,
			},
		});
	});

	it("re-projects when the level changes", () => {
		// The same model, two levels: the rows differ exactly where the level
		// decides (the settled thought exists at full and not at chat).
		expect(rowsAt(chat)).not.toEqual(rowsAt(full));
		expect(rowById(rowsAt(chat), "r1")).toBeUndefined();
		expect(rowById(rowsAt(full), "r1")).toBeDefined();
	});

	it("applies the hub's shipped mobile default with no native toggle", () => {
		// shippedMobileConfig is the hub's own default (intent): the hub serves
		// it whenever no stored config exists, so the store receives it with no
		// local switch. The projection must honor it — and value-equal configs
		// project the same rows (the per-turn cache keys the config's value).
		expect(shippedMobileConfig.content).toEqual({ kind: "preset", level: "intent" });
		expect(rowsAt(shippedMobileConfig)).toEqual(rowsAt(intent));
		expect(JSON.stringify(rowsAt(shippedMobileConfig))).not.toContain("auditing quietly");
		expect(JSON.stringify(rowsAt(shippedMobileConfig))).not.toContain("secret live thought");
	});

	it("leaves the projection at show-everything when there is no config", () => {
		const rows = rowsAt(null);
		expect(rowById(rows, "r1")).toMatchObject({
			kind: "activity",
			detail: { output: "auditing quietly" },
		});
		expect(rowById(rows, "c1")).toMatchObject({
			detail: { arguments: SHELL_ARGS, output: SHELL_OUTPUT },
		});
	});
});

describe("the summary-only ruling at compact levels", () => {
	// The operator's ruling (Sep-25): today a summarized tool-action row keeps
	// its full detail (arguments/output payload) at every content level, with
	// only its summary line trimmed. At compact levels the row carries ONLY
	// its summary line; the full detail appears at tools/activity/full.
	it("a summarized tool-action row carries only its summary line at chat and intent", () => {
		for (const config of [chat, intent]) {
			const rows = rowsAt(config);
			const c1 = rowById(rows, "c1");
			if (c1?.kind !== "activity") throw new Error(`the ${config.content} level lost the summarized tool row`);
			expect(c1.detail.description).toBe("run the audit");
			expect(c1.detail.arguments).toBeUndefined();
			expect(c1.detail.output).toBeUndefined();
			expect(c1.detail.error).toBeUndefined();
			expect(c1.detail.exitCode).toBeUndefined();
			expect(c1.detail.durationMs).toBeUndefined();
		}
	});

	it("the full detail returns at tools, activity and full", () => {
		for (const config of [tools, full]) {
			const rows = rowsAt(config);
			const c1 = rowById(rows, "c1");
			if (c1?.kind !== "activity") throw new Error(`the ${config.content} level lost the tool row`);
			expect(c1.detail).toMatchObject({
				description: "  run the audit  ",
				arguments: SHELL_ARGS,
				output: SHELL_OUTPUT,
			});
		}
	});

	it("a failed call keeps its error and renders as attention at every level", () => {
		// The native attention rule outranks the projector's summarization
		// (D24-4's disclosed contract): the failed call the projector routed
		// through its intent entry at compact levels still carries its full
		// detail and renders critical, so the reader can always see why a
		// call failed — the summary-only ruling covers the settled row.
		for (const config of [chat, intent, tools, full]) {
			const conversation = projectConversation(
				hydrateThread({ thread: levelsThread() }, "ref-1", 0),
				undefined,
				config,
			);
			const f1 = conversation.items.find((row) => row.id === "f1");
			if (f1?.kind !== "activity")
				throw new Error(`the ${config.content} level lost the failed call`);
			expect(f1.state).toBe("failed");
			expect(f1.summaryOnly).toBeUndefined();
			expect(f1.detail.error).toBe("opaque failure text");
			const presented = projectNativeTranscript(conversation, config);
			expect(presented.activityPresentation.get("f1")).toMatchObject({
				mode: "critical",
			});
		}
	});

	it("no compact level leaks the summarized row's output through the cluster", () => {
		for (const config of [chat, intent]) {
			const shown = JSON.stringify(rowsAt(config));
			expect(shown).not.toContain(SHELL_OUTPUT);
		}
	});

	it("a summarized call's output images leave with its output; a failed call's stay", () => {
		// Review round 3 (Low): the attachments ARE output — a settled
		// summarized call drops its images with its text at the compact
		// levels, while the failed call keeps everything per the attention
		// carve-out, and the settled call's images return at the levels
		// that show its full detail.
		const attachmentIdsAt = (config: TranscriptDisplayConfigV1 | null) =>
			rowsAt(config)
				.filter((row) => row.kind === "attachments")
				.flatMap((row) => (row.kind === "attachments" ? row.items.map((ref) => ref.id) : []));
		for (const config of [chat, intent]) {
			expect(attachmentIdsAt(config)).not.toContain("c1:out:0");
		}
		for (const config of [tools, full]) {
			expect(attachmentIdsAt(config)).toContain("c1:out:0");
		}
		for (const config of [chat, intent, tools, full]) {
			expect(attachmentIdsAt(config)).toContain("f1:out:0");
		}
	});
});

describe("the presentation layer maps the level-correct rows without filtering", () => {
	// Once the seam projects at the user's level, the presentation layer's own
	// row filtering (the retired activityMode branch) is redundant — and
	// wrong: it would re-decide visibility from the config a second time. The
	// presentation keeps every row the seam produced and only reshapes them
	// (cluster member expansion, attachment adjacency).
	it("keeps every row the seam projected at the level", () => {
		const conversation = projectConversation(
			hydrateThread({ thread: levelsThread() }, "ref-1", 0),
			undefined,
			intent,
		);
		const presented = projectNativeTranscript(conversation, intent);
		for (const row of conversation.items) {
			const present =
				presented.items.some((candidate) => candidate.id === row.id) ||
				// An attachments row re-seats beside the cluster member that
				// produced it; its id still names it.
				row.kind === "attachments" ||
				// A clustered row expands into its members' own rows.
				(row.kind === "activity" &&
					row.members?.some((member) =>
						presented.items.some((candidate) => candidate.id === member.id),
					));
			expect(present, `presentation dropped seam row ${row.id}`).toBe(true);
		}
	});

	it("renders the summarized row from its own summary line", () => {
		const conversation = projectConversation(
			hydrateThread({ thread: levelsThread() }, "ref-1", 0),
			undefined,
			intent,
		);
		const presented = projectNativeTranscript(conversation, intent);
		expect(presented.activityPresentation.get("c1")).toEqual({
			mode: "intent",
			summary: "run the audit",
		});
		const atTools = projectNativeTranscript(
			projectConversation(hydrateThread({ thread: levelsThread() }, "ref-1", 0), undefined, tools),
			tools,
		);
		expect(atTools.activityPresentation.get("c1")).toEqual({ mode: "full" });
	});
});
