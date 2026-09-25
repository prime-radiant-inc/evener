import { describe, expect, it } from "vitest";
import {
	hydrateThread,
	makeTranscriptDisplayConfig,
} from "@evener/appwire-client";
import type {
	AskQuestionRef,
	ContentLevel,
	EvenerThread,
	ItemModel,
	ProjectedEntry,
	Thread,
	ThreadCapabilities,
	ThreadItem,
	ThreadModel,
	TurnModel,
	Turn,
} from "@evener/appwire-client";
import {
	liveAsksFor,
	projectConversation,
	projectedRow,
	projectTimeline,
} from "./projectedRows";
import type { MobileTimelineItem } from "./projectedRows";

// The row adapter maps the shared projector's ProjectedEntry kinds onto the
// native MobileTimelineItem union. D24-6 re-homed the row vocabulary and the
// timeline projection into this module (the private family
// mobile/src/conversation/ is deleted), so these tests pin BOTH the entry→row
// mapping and the whole-model pipeline: clustering, attachments, failure rows,
// the per-turn cache, and the five-level differential the deleted oracle owned.

function item(overrides: Partial<ItemModel> & { type: string }): ItemModel {
	return { id: "i1", turnId: "t1", text: "", ...overrides };
}

function itemEntry(it: ItemModel, isMessage = false): ProjectedEntry {
	return { kind: "item", id: it.id, turnId: it.turnId, sourceIndex: 0, item: it, isMessage };
}

function thinkingEntry(it: ItemModel): ProjectedEntry {
	return { kind: "thinking", id: it.id, turnId: it.turnId, sourceIndex: 0, item: it };
}

function intentEntry(it: ItemModel, over: { failed?: boolean } = {}): ProjectedEntry {
	return {
		kind: "intent",
		id: `intent:${it.id}`,
		turnId: it.turnId,
		sourceIndex: 0,
		sourceItemId: it.id,
		rationale: it.description?.trim() || "Action summary unavailable",
		failed: over.failed ?? false,
		item: it,
	};
}

function criticalEntry(it: ItemModel, summary = "Action summary unavailable", redacted = false): ProjectedEntry {
	return {
		kind: "critical",
		id: it.id,
		turnId: it.turnId,
		sourceIndex: 0,
		sourceItemId: it.id,
		item: it,
		summary,
		redacted,
	};
}

const ask: AskQuestionRef = {
	key: "call-1:0",
	callId: "call-1",
	header: "Confirm",
	question: "Proceed?",
	options: [{ label: "Yes", detail: "go" }],
	multiSelect: false,
};

function asksFor(callId: string): ReadonlyMap<string, AskQuestionRef[]> {
	return new Map([[callId, [{ ...ask, callId, key: `${callId}:0` }]]]);
}

describe("projectedRow — item entries", () => {
	it("maps a user message to the user row, carrying its transcript entry index", () => {
		const row = projectedRow(itemEntry(item({ type: "userMessage", text: "hi", transcriptEntryIndex: 7 }), true));
		expect(row).toEqual<MobileTimelineItem>({ kind: "user", id: "i1", text: "hi", transcriptEntryIndex: 7 });
	});

	it("omits transcriptEntryIndex when the user item has none", () => {
		const row = projectedRow(itemEntry(item({ type: "userMessage", text: "hi" }), true));
		expect(row).toEqual<MobileTimelineItem>({ kind: "user", id: "i1", text: "hi" });
	});

	it("maps a user-sourced steering item to the same user row, without an entry index", () => {
		const row = projectedRow(
			itemEntry(item({ type: "steering", text: "steer", source: "user", transcriptEntryIndex: 3 })),
		);
		expect(row).toEqual<MobileTimelineItem>({ kind: "user", id: "i1", text: "steer" });
	});

	it("maps an agent message to the assistant row and joins pending deltas", () => {
		const row = projectedRow(
			itemEntry(item({ type: "agentMessage", text: "hel", pendingText: ["lo", "!"] }), true),
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "assistant",
			id: "i1",
			markdown: "hello!",
			streaming: false,
		});
	});

	it("marks an assistant row streaming while the item is in progress", () => {
		const row = projectedRow(itemEntry(item({ type: "agentMessage", text: "x", status: "inProgress" }), true));
		expect(row).toEqual<MobileTimelineItem>({ kind: "assistant", id: "i1", markdown: "x", streaming: true });
	});

	it("maps a reasoning item to a collapsed reasoning activity with its joined text", () => {
		const row = projectedRow(itemEntry(item({ type: "reasoning", text: "thought" })));
		expect(row).toEqual<MobileTimelineItem>({
			kind: "activity",
			id: "i1",
			label: "Reasoning",
			family: "reasoning",
			state: "completed",
			detail: { output: "thought" },
		});
	});

	it("marks a reasoning activity running while the turn is in progress", () => {
		const row = projectedRow(itemEntry(item({ type: "reasoning", text: "t" })), { turnStatus: "inProgress" });
		expect(row).toMatchObject({ kind: "activity", family: "reasoning", state: "running" });
	});

	it("maps a command execution to a tool activity with its full detail", () => {
		const row = projectedRow(
			itemEntry(
				item({
					type: "commandExecution",
					toolName: "shell",
					description: "Run ls",
					argumentsJSON: '{"cmd":"ls"}',
					output: "a\nb",
					callId: "call-1",
				}),
			),
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "activity",
			id: "i1",
			label: "shell",
			family: "tool",
			state: "completed",
			detail: {
				description: "Run ls",
				arguments: '{"cmd":"ls"}',
				output: "a\nb",
				callId: "call-1",
			},
		});
	});

	it("falls back to the description, then Tool, for a tool's label", () => {
		expect(projectedRow(itemEntry(item({ type: "commandExecution", description: " List " })))).toMatchObject({
			label: "List",
		});
		expect(projectedRow(itemEntry(item({ type: "commandExecution" })))).toMatchObject({ label: "Tool" });
	});

	it("falls back to Tool for a blank or whitespace-only tool label", () => {
		expect(projectedRow(itemEntry(item({ type: "commandExecution", description: "   " })))).toMatchObject({
			label: "Tool",
		});
		expect(projectedRow(itemEntry(item({ type: "commandExecution", toolName: "  " })))).toMatchObject({
			label: "Tool",
		});
	});

	it("drops a tool duration whose timestamps do not parse", () => {
		const row = projectedRow(
			itemEntry(
				item({
					type: "commandExecution",
					toolName: "shell",
					startedAt: "not-a-date",
					completedAt: "also-not",
				}),
			),
		);
		expect(row).toMatchObject({ kind: "activity" });
		expect((row as Extract<MobileTimelineItem, { kind: "activity" }>).detail.durationMs).toBeUndefined();
	});

	it("summarizes an ask_user activity's questions in its detail description", () => {
		const row = projectedRow(
			itemEntry(
				item({
					type: "commandExecution",
					toolName: "ask_user",
					argumentsJSON: JSON.stringify({
						questions: [{ header: "Confirm", question: "Proceed?", options: [{ label: "Yes", detail: "go" }] }],
					}),
				}),
			),
		);
		expect(row).toMatchObject({ kind: "activity", family: "tool", detail: { description: "Questions: Confirm" } });
	});

	it("marks a failed tool activity failed, regardless of its settled status", () => {
		const row = projectedRow(itemEntry(item({ type: "commandExecution", toolName: "shell", error: "boom" })));
		expect(row).toMatchObject({ kind: "activity", family: "tool", state: "failed" });
	});

	it("maps a daemon steering item to an informational notice", () => {
		const row = projectedRow(itemEntry(item({ type: "steering", text: "steer", steeringKind: "note" })));
		expect(row).toEqual<MobileTimelineItem>({
			kind: "notice",
			id: "i1",
			origin: "steering",
			steeringKind: "note",
			family: "informational",
			tone: "info",
			text: "steer",
		});
	});

	it("maps a warning steering kind to a warning-tone notice", () => {
		const row = projectedRow(itemEntry(item({ type: "steering", text: "loop", steeringKind: "loop-detected" })));
		expect(row).toMatchObject({ kind: "notice", origin: "steering", family: "warning", tone: "warning" });
	});

	it("maps a system message to a notice carrying its event kind, family and exit code", () => {
		const row = projectedRow(
			itemEntry(item({ type: "systemMessage", text: "failed", eventKind: "error", exitCode: 1 })),
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "notice",
			id: "i1",
			origin: "system",
			family: "warning",
			tone: "warning",
			text: "failed",
			eventKind: "error",
			exitCode: 1,
		});
	});

	it.each<[string, string, string]>([
		["system_prompt", "hidden-instruction", "system"],
		["prompt_loaded", "hidden-instruction", "system"],
		["environment", "system-prelude", "system"],
		["round_timings", "diagnostic", "system"],
		["plugin_loaded", "lifecycle", "system"],
		["hook_completed", "lifecycle", "system"],
		["unknown_future_kind", "unknown-system", "system"],
	])("classifies system event %s as family %s / tone %s", (eventKind, family, tone) => {
		const row = projectedRow(itemEntry(item({ type: "systemMessage", text: "x", eventKind })));
		expect(row).toMatchObject({ kind: "notice", origin: "system", family, tone });
	});

	it("maps a warning item to the attention failure row", () => {
		const row = projectedRow(
			itemEntry(item({ type: "warning", text: "disk low", warning: { title: "Space", hint: "free some" } })),
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "failure",
			id: "i1",
			title: "Space",
			detail: "disk low — free some",
		});
	});

	it("drops a warning whose every part is blank", () => {
		expect(projectedRow(itemEntry(item({ type: "warning" })))).toBeNull();
	});

	it("maps an unknown item type to a neutral unknown activity", () => {
		const row = projectedRow(itemEntry(item({ type: "futureThing", text: "body", output: "out" })));
		expect(row).toEqual<MobileTimelineItem>({
			kind: "activity",
			id: "i1",
			label: "Activity",
			family: "unknown",
			state: "completed",
			detail: { output: "body" },
		});
	});

	it("maps an answerable ask_user call to a question row", () => {
		const row = projectedRow(
			itemEntry(item({ type: "commandExecution", toolName: "ask_user", callId: "call-1" })),
			{ asks: asksFor("call-1") },
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "question",
			id: "i1",
			questions: [{ ...ask, callId: "call-1", key: "call-1:0" }],
		});
	});

	it("keeps an ask_user with no answerable ask as an ordinary tool activity", () => {
		const row = projectedRow(itemEntry(item({ type: "commandExecution", toolName: "ask_user", callId: "call-1" })));
		expect(row).toMatchObject({ kind: "activity", family: "tool" });
	});

	it("carries the item's transcript identity onto every row it builds", () => {
		const row = projectedRow(
			itemEntry(
				item({
					type: "commandExecution",
					toolName: "shell",
					transcriptKey: "tk-1",
					position: { entry: 2, item: 5 },
				}),
			),
		);
		expect(row).toMatchObject({ transcriptKey: "tk-1", position: { entry: 2, item: 5 } });
	});

	it("returns null for a ProjectedEntry kind outside the known union", () => {
		expect(projectedRow({ kind: "futureKind" } as unknown as ProjectedEntry)).toBeNull();
	});
});

describe("projectedRow — thinking entries", () => {
	it("maps a thinking entry to a content-free live-thought placeholder activity", () => {
		const row = projectedRow(thinkingEntry(item({ type: "reasoning", text: "secret thought" })));
		expect(row).toEqual<MobileTimelineItem>({
			kind: "activity",
			id: "i1",
			label: "Reasoning",
			family: "reasoning",
			state: "running",
			detail: {},
		});
	});

	it("never leaks the thought's text from a thinking placeholder", () => {
		const row = projectedRow(thinkingEntry(item({ type: "reasoning", text: "secret thought" })));
		expect(JSON.stringify(row)).not.toContain("secret thought");
	});
});

describe("projectedRow — intent entries", () => {
	it("maps an intent entry to the tool activity row it summarizes", () => {
		const row = projectedRow(
			intentEntry(item({ type: "commandExecution", toolName: "read_file", description: "Read a.ts" })),
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "activity",
			id: "i1",
			label: "read_file",
			family: "tool",
			state: "completed",
			// The operator's summary-only ruling marks the row: it carries
			// only its summary line, nothing to expand.
			summaryOnly: true,
			detail: { description: "Read a.ts" },
		});
	});

	it("marks a failed intent's activity failed", () => {
		const row = projectedRow(
			intentEntry(item({ type: "commandExecution", toolName: "shell", error: "boom" }), { failed: true }),
		);
		expect(row).toMatchObject({ kind: "activity", family: "tool", state: "failed" });
	});

	it("carries the projector's rationale as the activity detail description", () => {
		const row = projectedRow(
			intentEntry(item({ type: "commandExecution", toolName: "read_file", description: "  Read a.ts  " })),
		);
		expect(row).toMatchObject({ kind: "activity", detail: { description: "Read a.ts" } });
	});

	// The operator's ruling (Sep-25): a summarized tool-action row carries
	// ONLY its summary line at compact levels — its full detail (arguments,
	// output, error, exit code, duration, call id) appears at
	// tools/activity/full, where the projector routes the call through its
	// item entry instead. The row is marked summaryOnly so the presentation
	// layer renders the line without an expansion affordance.
	it("carries only its summary line, dropping the full detail the source item holds", () => {
		const row = projectedRow(
			intentEntry(
				item({
					type: "commandExecution",
					toolName: "shell",
					description: "Run ls",
					argumentsJSON: '{"cmd":"ls"}',
					output: "a\nb",
					exitCode: 0,
					startedAt: "2024-01-01T00:00:00.000Z",
					completedAt: "2024-01-01T00:00:01.000Z",
					callId: "call-1",
				}),
			),
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "activity",
			id: "i1",
			label: "shell",
			family: "tool",
			state: "completed",
			summaryOnly: true,
			detail: { description: "Run ls" },
		});
	});

	it("carries no detail at all when the projector's summary is unavailable", () => {
		// ACTION_SUMMARY_UNAVAILABLE means the source carried no description;
		// the row keeps nothing but the marker, and the presentation layer's
		// own summary fallback (the write_file path) renders the line.
		const row = projectedRow(
			intentEntry(
				item({
					type: "commandExecution",
					toolName: "shell",
					argumentsJSON: '{"cmd":"ls"}',
					output: "a\nb",
				}),
			),
		);
		expect(row).toMatchObject({
			kind: "activity",
			summaryOnly: true,
			detail: {},
		});
	});

	// The native attention rule outranks the projector's summarization (the
	// D24-4 disclosed contract: a failed/running activity renders critical,
	// with its full detail, at every level). The summary-only ruling covers
	// the SETTLED summarized row; a failed or still-running call the projector
	// routed through its intent entry keeps everything.
	it("keeps a failed call's full detail and renders it critical, not summary-only", () => {
		const row = projectedRow(
			intentEntry(
				item({ type: "commandExecution", toolName: "shell", error: "boom", exitCode: 1 }),
				{ failed: true },
			),
		);
		expect(row).toMatchObject({
			kind: "activity",
			state: "failed",
			detail: { error: "boom", exitCode: 1 },
		});
		expect(row?.kind === "activity" && row.summaryOnly).toBeUndefined();
	});

	it("keeps a running call's full detail while its turn is in progress", () => {
		const row = projectedRow(
			intentEntry(
				item({
					type: "commandExecution",
					toolName: "shell",
					description: "Read config",
					output: "partial output",
					status: "inProgress",
				}),
			),
			{ turnStatus: "inProgress" },
		);
		expect(row).toMatchObject({
			kind: "activity",
			state: "running",
			detail: { description: "Read config", output: "partial output" },
		});
		expect(row?.kind === "activity" && row.summaryOnly).toBeUndefined();
	});

	it("honors the projector's failed intent classification even when the source item carries no signal", () => {
		// The projector sets intent.failed (hasItemFailure at projection time); the
		// adapter must render a failed activity from that classification rather
		// than re-deriving from the item alone.
		const row = projectedRow(
			intentEntry(item({ type: "commandExecution", toolName: "shell" }), { failed: true }),
		);
		expect(row).toMatchObject({ kind: "activity", family: "tool", state: "failed" });
	});
});

describe("projectedRow — critical entries", () => {
	it("maps a redacted critical reasoning to a neutral failure row, never the thought", () => {
		const row = projectedRow(
			criticalEntry(item({ type: "reasoning", text: "secret thought" }), "Thought not shown", true),
		);
		expect(row).toEqual<MobileTimelineItem>({
			kind: "failure",
			id: "i1",
			title: "Thought not shown",
			detail: "",
		});
		expect(JSON.stringify(row)).not.toContain("secret thought");
	});

	it("maps a critical system message to a warning notice", () => {
		const row = projectedRow(criticalEntry(item({ type: "systemMessage", text: "boom", eventKind: "tool_repair" })));
		expect(row).toMatchObject({ kind: "notice", origin: "system", family: "lifecycle" });
	});

	it("maps a critical warning item to the attention failure row", () => {
		const row = projectedRow(
			criticalEntry(item({ type: "warning", text: "msg", warning: { title: "Title" } }), "Title"),
		);
		expect(row).toEqual<MobileTimelineItem>({ kind: "failure", id: "i1", title: "Title", detail: "msg" });
	});

	it("maps a critical failed tool call to a failed tool activity, not a failure row", () => {
		const row = projectedRow(criticalEntry(item({ type: "commandExecution", toolName: "shell", error: "boom" })));
		expect(row).toEqual<MobileTimelineItem>({
			kind: "activity",
			id: "i1",
			label: "shell",
			family: "tool",
			state: "failed",
			detail: { error: "boom" },
		});
	});

	it("maps a critical answerable interaction to a question row", () => {
		const row = projectedRow(
			criticalEntry(item({ type: "commandExecution", toolName: "ask_user", callId: "call-1" })),
			{ asks: asksFor("call-1") },
		);
		expect(row).toMatchObject({ kind: "question", id: "i1" });
	});

	it("maps a critical user-sourced steering to the user row", () => {
		const row = projectedRow(criticalEntry(item({ type: "steering", text: "steer", source: "user" })));
		expect(row).toEqual<MobileTimelineItem>({ kind: "user", id: "i1", text: "steer" });
	});
});

// --- the re-homed pipeline coverage (D24-6) ------------------------------------
//
// The deleted private family's oracle (mobile/src/conversation/project.test.ts,
// 2931 lines) pinned the pre-re-home projection. Its obligations re-home here:
// the per-kind entry→row mapping was already this suite's 42 landed tests; the
// blocks below port the pipeline obligations the oracle owned — question
// recaps, the per-turn cache, and the D24-3 five-level differential — updated
// where the operator's summary-only ruling (Sep-25) changed what a summarized
// row carries at compact levels.

const ALL_TRUE_CAPS: ThreadCapabilities = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: false,
	queue: true,
	goal: true,
	rename: true,
};

function evenerThread(over: Partial<EvenerThread> = {}): EvenerThread {
	return {
		ref: "ref-1",
		capabilities: ALL_TRUE_CAPS,
		queue: { revision: 0 },
		...over,
	};
}

function wireTurn(id: string, items: ThreadItem[], over: Partial<Turn> = {}): Turn {
	return { id, items, itemsView: "default", status: "completed", ...over };
}

function wireItem(
	over: Partial<ThreadItem> & { id: string; type: string },
): ThreadItem {
	return { turnId: "turn-1", ...over } as ThreadItem;
}

function wireThread(turns: Turn[], over: Partial<Thread> = {}): Thread {
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
		evener: evenerThread(),
		...over,
	} as unknown as Thread;
}

describe("completed question recaps", () => {
	const argumentsJson = JSON.stringify({
		questions: [
			{
				header: "header-alpha",
				question: "prompt-alpha",
				options: [{ label: "option-alpha", detail: "detail-alpha" }],
			},
			{
				header: "header-beta",
				question: "prompt-beta",
				options: [{ label: "option-beta", detail: "detail-beta" }],
			},
		],
	});

	// An answered ask_user is an ordinary (failed or completed) tool
	// activity: the projector routes an unanswered ask to critical and the
	// mapping's question card only while it is still answerable.
	function recap(overrides: Partial<ThreadItem> = {}, answered = true) {
		const projected = projectConversation(
			hydrateThread(
				{
					thread: wireThread([
						wireTurn("t1", [
							wireItem({
								id: "ask-recap",
								type: "commandExecution",
								toolName: "ask_user",
								status: "completed",
								argumentsJson,
								...overrides,
							}),
							...(answered
								? [
										wireItem({
											id: "answer-recap",
											type: "userMessage",
											text: "opaque-answer",
										}),
									]
								: []),
						]),
					]),
				},
				"ref-1",
				0,
			),
		);
		expect(projected.items.map((row) => row.kind)).not.toContain("question");
		const activity = projected.items.find((row) => row.kind === "activity");
		expect(activity?.kind).toBe("activity");
		if (activity?.kind !== "activity") throw new Error("question recap missing");
		return activity;
	}

	it("retains posted headers and arguments after a later answer", () => {
		const activity = recap();
		expect(activity.state).toBe("completed");
		expect(activity.detail.description).toContain("header-alpha");
		expect(activity.detail.description).toContain("header-beta");
		expect(activity.detail.description).not.toContain("opaque-answer");
		expect(activity.detail.arguments).toBe(argumentsJson);
	});

	it("preserves an authoritative description without adding inferred details", () => {
		expect(recap({ description: "authored-description" }).detail.description).toBe(
			"authored-description",
		);
	});

	it("keeps failed questions non-actionable with their question context and error", () => {
		const activity = recap({ error: "opaque-error", output: "opaque-output" }, false);
		expect(activity.state).toBe("failed");
		expect(activity.detail.description).toContain("header-alpha");
		expect(activity.detail).toMatchObject({
			error: "opaque-error",
			output: "opaque-output",
			arguments: argumentsJson,
		});
	});

	it.each(["{", "{}", JSON.stringify({ questions: [{ header: "unvalidated-header" }] })])(
		"does not invent context from malformed arguments %s",
		(malformed) => {
			const activity = recap({ argumentsJson: malformed });
			expect(activity.detail.description).toBeUndefined();
			expect(activity.detail.arguments).toBe(malformed);
		},
	);
});

describe("rowsForProjectedTurn's per-turn cache", () => {
	function askItem(callId: string): ItemModel {
		return {
			id: `item_${callId}`,
			turnId: "t1",
			type: "commandExecution",
			toolName: "ask_user",
			callId,
			status: "completed",
			argumentsJSON: JSON.stringify({
				questions: [{ header: "H", question: "Q", options: [{ label: "A", detail: "d" }] }],
			}),
		} as ItemModel;
	}

	function askRefs(callId: string): AskQuestionRef[] {
		return [
			{
				key: `${callId}:0`,
				callId,
				header: "H",
				question: "Q",
				options: [{ label: "A", detail: "d" }],
				multiSelect: false,
			},
		];
	}

	// A turn's own question row depends on `asks` too — whether ITS OWN
	// ask_user calls are still answerable. Two projectTimeline calls over the
	// SAME turn object, with call_1 answerable the first time and resolved the
	// second, must not reuse the first call's question row.
	it("does not reuse a cached question row once the turn's own ask becomes resolved", () => {
		const sharedTurn: TurnModel = { id: "t1", status: "completed", items: [askItem("call_1")] } as TurnModel;
		const answerable = new Map<string, AskQuestionRef[]>([["call_1", askRefs("call_1")]]);
		const firstPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, answerable);
		expect(firstPass.some((row) => row.kind === "question")).toBe(true);

		const resolved = new Map<string, AskQuestionRef[]>(); // call_1 no longer answerable
		const secondPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, resolved);
		expect(secondPass.some((row) => row.kind === "question")).toBe(false);
	});

	// The inverse: a genuinely unrelated `asks` map is exactly the case the
	// cache SHOULD reuse for — measured so the hit path stays covered, not just
	// the invalidation path.
	it("reuses the cached row set when the turn's own asks are unaffected", () => {
		const sharedTurn: TurnModel = { id: "t1", status: "completed", items: [askItem("call_1")] } as TurnModel;
		const asksA = new Map<string, AskQuestionRef[]>([["call_1", askRefs("call_1")]]);
		const firstPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, asksA);
		const asksB = new Map<string, AskQuestionRef[]>([
			["call_1", askRefs("call_1")],
			["call_unrelated", askRefs("call_unrelated")],
		]);
		const secondPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, asksB);
		expect(secondPass).toEqual(firstPass);
	});

	// The cache keys on the config's VALUE, not its object identity: the seam
	// resolves the user's display config per publish (a fresh object every
	// call), so a reference-keyed cache would re-derive every turn's rows on
	// every frame despite the value never changing.
	it("reuses cached rows across value-equal config objects", () => {
		const sharedTurn: TurnModel = { id: "t1", status: "completed", items: [askItem("call_1")] } as TurnModel;
		const asks = new Map<string, AskQuestionRef[]>([["call_1", askRefs("call_1")]]);
		const config = makeTranscriptDisplayConfig(
			{ kind: "preset", level: "chat" },
			{ roundTimings: true, systemEvents: true, promptEvents: true, hookExits: "all" },
		);
		const firstPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, asks, config);
		const secondPass = projectTimeline({ turns: [sharedTurn] } as unknown as ThreadModel, asks, {
			...config,
		});
		expect(secondPass[0]).toBe(firstPass[0]);
	});
});

// --- the D24-3 differential, re-homed (D24-6) ----------------------------------
//
// projectTimeline hands visibility and ordering to the package's projectThread
// and maps each surviving ProjectedEntry onto the native row. FULL_ROWS below
// is the pre-re-home projection captured verbatim on main (the deleted oracle's
// own golden); every content-level case must reproduce exactly the subset the
// shared projector keeps — byte-identical rows for surviving items, with only
// the projector's own reclassifications differing: a live current thought
// becomes a content-free placeholder, a redacted critical reasoning becomes a
// neutral failure row, and (the summary-only ruling) an intent row carries ONLY
// its summary line at compact levels.

const DIFF_ASK_ARGS = JSON.stringify({
	questions: [
		{
			header: "Proceed?",
			question: "Run the full audit?",
			options: [{ label: "Yes", detail: "go" }],
		},
	],
});

// One thread that walks every row family: user input with images, human
// steering, every notice origin, a tool run that clusters, a failed tool, a
// settled thought, a warning, an unknown forward-compatible type, a pending
// ask, a running tool run with output images, a streaming reply, a live
// current thought, and a failed turn that still owes the reader its error.
function differentialThread(): Thread {
	return wireThread(
		[
			wireTurn("t1", [
				wireItem({
					id: "u1",
					turnId: "t1",
					type: "userMessage",
					text: "please audit the config",
					images: [{ type: "image", name: "cat.png", mediaType: "image/png", url: "http://x/cat.png" }],
				}),
				wireItem({ id: "steer-user", turnId: "t1", type: "steering", source: "user", text: "include the diff" }),
				wireItem({ id: "sys-prompt", turnId: "t1", type: "systemMessage", eventKind: "system_prompt", text: "PROMPT LOADED" }),
				wireItem({ id: "sys-error", turnId: "t1", type: "systemMessage", eventKind: "error", text: "provider hiccup" }),
				wireItem({ id: "sys-compact", turnId: "t1", type: "systemMessage", eventKind: "compaction", text: "context compacted" }),
				wireItem({
					id: "c1",
					turnId: "t1",
					type: "commandExecution",
					toolName: "shell",
					description: "  run the audit  ",
					status: "completed",
					startedAt: 1000,
					completedAt: 1500,
					callId: "call-1",
				}),
				wireItem({ id: "c2", turnId: "t1", type: "commandExecution", toolName: "grep", description: "grep the results", status: "completed" }),
				wireItem({ id: "c3", turnId: "t1", type: "commandExecution", toolName: "shell", status: "completed", error: "boom", exitCode: 1 }),
				wireItem({ id: "r1", turnId: "t1", type: "reasoning", text: "auditing quietly", status: "completed" }),
				wireItem({ id: "w1", turnId: "t1", type: "warning", text: "disk almost full", status: "completed" }),
				wireItem({ id: "unk1", turnId: "t1", type: "telemetryPing", text: "opaque payload" }),
				wireItem({
					id: "ask1",
					turnId: "t1",
					type: "commandExecution",
					toolName: "ask_user",
					callId: "call-ask",
					status: "completed",
					argumentsJson: DIFF_ASK_ARGS,
				}),
			]),
			wireTurn(
				"t2",
				[
					wireItem({ id: "c4", turnId: "t2", type: "commandExecution", toolName: "read_file", description: "read config", status: "inProgress" }),
					wireItem({
						id: "c5",
						turnId: "t2",
						type: "commandExecution",
						toolName: "view",
						status: "completed",
						outputImages: [{ source: "screenshot", name: "shot.png", url: "http://x/shot.png" }],
					}),
					wireItem({ id: "a2", turnId: "t2", type: "agentMessage", text: "working on it" }),
					wireItem({ id: "r2", turnId: "t2", type: "reasoning", text: "secret live thought", status: "inProgress" }),
				],
				{ status: "inProgress" },
			),
			wireTurn(
				"t3",
				[wireItem({ id: "r3", turnId: "t3", type: "reasoning", text: "final thought", status: "completed" })],
				{ status: "failed", error: { message: "provider exploded", title: "Provider error", hint: "retry" } },
			),
		],
		{ evener: evenerThread({ askPending: true }) },
	);
}

// The wire cannot carry a warning map (only the reducer's live fold stamps
// one), so the differential model stamps it the way the live fixtures do.
function differentialModel(): ThreadModel {
	const model = hydrateThread({ thread: differentialThread() }, "ref-1", 0);
	const warning = model.turns[0]?.items.find((entry) => entry.id === "w1");
	if (!warning) throw new Error("differential fixture lost its warning item");
	warning.warning = { title: "Low disk", hint: "clean up" };
	return model;
}

// Every advanced gate open, so the content vector is the only axis that
// varies across the five levels.
function levelConfig(level: ContentLevel) {
	return makeTranscriptDisplayConfig(
		{ kind: "preset", level },
		{ roundTimings: true, systemEvents: true, promptEvents: true, hookExits: "all" },
	);
}

function rowsAt(model: ThreadModel, level: ContentLevel): MobileTimelineItem[] {
	return projectTimeline(model, liveAsksFor(model), levelConfig(level));
}

const rowId = (row: MobileTimelineItem): string => row.id;

// The projection of the differential thread under the show-everything default
// — the byte-identical contract the seam's config-less callers rely on. The
// last activity row merges the live thought (r2) and the failed turn's settled
// thought (r3) into one cross-turn run.
const FULL_ROWS: MobileTimelineItem[] = [
	{ kind: "user", id: "u1", text: "please audit the config" },
	{
		kind: "attachments",
		id: "u1:attachments",
		items: [{ id: "u1:0", src: "http://x/cat.png", name: "cat.png" }],
	},
	{ kind: "user", id: "steer-user", text: "include the diff" },
	{
		kind: "notice",
		id: "sys-prompt",
		origin: "system",
		family: "hidden-instruction",
		tone: "system",
		text: "PROMPT LOADED",
		eventKind: "system_prompt",
	},
	{
		kind: "notice",
		id: "sys-error",
		origin: "system",
		family: "warning",
		tone: "warning",
		text: "provider hiccup",
		eventKind: "error",
	},
	{
		kind: "notice",
		id: "sys-compact",
		origin: "system",
		family: "lifecycle",
		tone: "system",
		text: "context compacted",
		eventKind: "compaction",
	},
	{
		kind: "activity",
		id: "c1",
		label: "shell",
		family: "tool",
		state: "completed",
		detail: { description: "  run the audit  ", durationMs: 500, callId: "call-1" },
		members: [
			{
				id: "c1",
				label: "shell",
				family: "tool",
				state: "completed",
				detail: { description: "  run the audit  ", durationMs: 500, callId: "call-1" },
			},
			{
				id: "c2",
				label: "grep",
				family: "tool",
				state: "completed",
				detail: { description: "grep the results" },
			},
		],
	},
	{
		kind: "activity",
		id: "c3",
		label: "shell",
		family: "tool",
		state: "failed",
		detail: { error: "boom", exitCode: 1 },
	},
	{
		kind: "activity",
		id: "r1",
		label: "Reasoning",
		family: "reasoning",
		state: "completed",
		detail: { output: "auditing quietly" },
	},
	{ kind: "failure", id: "w1", title: "Low disk", detail: "disk almost full — clean up" },
	{
		kind: "activity",
		id: "unk1",
		label: "Activity",
		family: "unknown",
		state: "completed",
		detail: { output: "opaque payload" },
	},
	{
		kind: "question",
		id: "ask1",
		questions: [
			{
				key: "call-ask:0",
				callId: "call-ask",
				header: "Proceed?",
				question: "Run the full audit?",
				options: [{ label: "Yes", detail: "go", recommended: false }],
				multiSelect: false,
			},
		],
	},
	{
		kind: "activity",
		id: "c4",
		label: "read_file",
		family: "tool",
		state: "running",
		detail: { description: "read config" },
		members: [
			{
				id: "c4",
				label: "read_file",
				family: "tool",
				state: "running",
				detail: { description: "read config" },
			},
			{ id: "c5", label: "view", family: "tool", state: "completed", detail: {} },
		],
	},
	{
		kind: "attachments",
		id: "c5:attachments",
		items: [{ id: "c5:out:0", src: "http://x/shot.png", name: "shot.png", source: "screenshot" }],
		sourceTranscriptKey: "c5",
	},
	{ kind: "assistant", id: "a2", markdown: "working on it", streaming: true },
	{
		kind: "activity",
		id: "r2",
		label: "Reasoning",
		family: "reasoning",
		state: "running",
		detail: { output: "secret live thought" },
		members: [
			{
				id: "r2",
				label: "Reasoning",
				family: "reasoning",
				state: "running",
				detail: { output: "secret live thought" },
			},
			{
				id: "r3",
				label: "Reasoning",
				family: "reasoning",
				state: "completed",
				detail: { output: "final thought" },
			},
		],
	},
	{ kind: "failure", id: "failure:t3", title: "Provider error", detail: "provider exploded\nretry" },
];

describe("the timeline projection delegates to the shared projector", () => {
	const model = differentialModel();

	it("reproduces the show-everything rows byte-for-byte under the default config", () => {
		// The config-less call is the seam's default: the swap must not change
		// one byte of what it produces.
		expect(projectConversation(model).items).toEqual(FULL_ROWS);
		// The no-config default is exactly the full-visibility config.
		expect(projectTimeline(model)).toEqual(rowsAt(model, "full"));
	});

	it.each([
		["chat", true],
		["intent", true],
		["tools", false],
		["activity", false],
	] as const)(
		"level %s: keeps exactly the rows the shared projector keeps, byte-identical except its own reclassifications",
		(level, intentRows) => {
			const rows = rowsAt(model, level);
			// Visibility: every show-everything row survives except the settled
			// thought r1, which the projector hides while the reasoning flag is
			// off. The cross-turn r2+r3 cluster cannot form — the projector
			// reclassifies both members — so r3 becomes its own row here.
			expect(rows.map(rowId)).toEqual([
				"u1",
				"u1:attachments",
				"steer-user",
				"sys-prompt",
				"sys-error",
				"sys-compact",
				"c1",
				"c3",
				"w1",
				"unk1",
				"ask1",
				"c4",
				"c5:attachments",
				"a2",
				"r2",
				"r3",
				"failure:t3",
			]);

			// Byte parity: every surviving row the projector did not reclassify
			// is the show-everything row for the same item, field for field.
			// The summary-only ruling reclassifies the summarized cluster (c1)
			// at compact levels; the thinking placeholder (r2) and the redacted
			// critical reasoning (r3) are the projector's own reclassifications.
			// The running c4 also joins that set at compact levels — not its own
			// row (the attention rule keeps the running call's full detail,
			// pinned below) but its cluster's SECOND member, the settled view
			// call c5, which the ruling summarizes. The failed c3 stays
			// byte-identical at every level.
			const reclassified = intentRows ? ["c1", "c4", "r2", "r3"] : ["r2", "r3"];
			for (const row of rows) {
				if (reclassified.includes(rowId(row))) continue;
				expect(row).toEqual(FULL_ROWS.find((golden) => rowId(golden) === rowId(row)));
			}

			// The live current thought becomes a content-free placeholder.
			expect(rows.find((row) => rowId(row) === "r2")).toEqual({
				kind: "activity",
				id: "r2",
				label: "Reasoning",
				family: "reasoning",
				state: "running",
				detail: {},
			});
			// The failed turn's settled thought explains the turn without its text.
			expect(rows.find((row) => rowId(row) === "r3")).toEqual({
				kind: "failure",
				id: "r3",
				title: "Thought not shown",
				detail: "",
			});

			// A summarized tool call is a summary-only row at compact levels:
			// the same activity, carrying ONLY the projector's trimmed
			// rationale as its summary line (the cluster and each member —
			// the operator's summary-only ruling). At call levels the row is
			// the show-everything one verbatim.
			const c1 = rows.find((row) => rowId(row) === "c1");
			if (c1?.kind !== "activity") throw new Error("differential lost the c1 cluster");
			expect(c1.detail.description).toBe(intentRows ? "run the audit" : "  run the audit  ");
			if (intentRows) {
				expect(c1.summaryOnly).toBe(true);
				expect(c1.detail.arguments).toBeUndefined();
				expect(c1.detail.output).toBeUndefined();
				expect(c1.detail.durationMs).toBeUndefined();
				expect(c1.detail.callId).toBeUndefined();
			} else {
				expect(c1.summaryOnly).toBeUndefined();
				expect(c1.detail).toEqual({
					description: "  run the audit  ",
					durationMs: 500,
					callId: "call-1",
				});
			}
			const c1Member = c1.members?.[0];
			expect(c1Member?.detail.description).toBe(intentRows ? "run the audit" : "  run the audit  ");
			expect(c1Member?.summaryOnly).toBe(intentRows ? true : undefined);
			expect(c1.members?.[1]?.detail.description).toBe("grep the results");

			// The attention carve-out: a failed call the projector routed through
			// its intent entry keeps its full detail and its failed state at
			// every level, so the reader can always see why it failed.
			const c3 = rows.find((row) => rowId(row) === "c3");
			if (c3?.kind !== "activity") throw new Error("differential lost c3");
			expect(c3.state).toBe("failed");
			expect(c3.summaryOnly).toBeUndefined();
			expect(c3.detail).toEqual({ error: "boom", exitCode: 1 });

			// The running call is the same attention carve-out: full detail,
			// running state, at every level.
			const c4 = rows.find((row) => rowId(row) === "c4");
			if (c4?.kind !== "activity") throw new Error("differential lost the c4 cluster");
			expect(c4.state).toBe("running");
			expect(c4.summaryOnly).toBeUndefined();
			expect(c4.detail).toEqual({ description: "read config" });
			// The cluster's settled member is the summarized one.
			expect(c4.members?.[1]?.summaryOnly).toBe(intentRows ? true : undefined);
		},
	);

	it("never leaks a hidden thought's text at any level", () => {
		for (const level of ["chat", "intent", "tools", "activity", "full"] as const) {
			if (level === "full") continue;
			const shown = JSON.stringify(rowsAt(model, level));
			expect(shown).not.toContain("auditing quietly");
			expect(shown).not.toContain("secret live thought");
			expect(shown).not.toContain("final thought");
		}
		// ...while the full level still shows all three, exactly as the
		// show-everything projection does.
		const fullShown = JSON.stringify(rowsAt(model, "full"));
		expect(fullShown).toContain("auditing quietly");
		expect(fullShown).toContain("secret live thought");
		expect(fullShown).toContain("final thought");
	});
});
