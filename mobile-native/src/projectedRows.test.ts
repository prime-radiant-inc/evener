import { describe, expect, it } from "vitest";
import type { AskQuestionRef, ItemModel, ProjectedEntry } from "@evener/appwire-client";
import type { MobileTimelineItem } from "../../mobile/src/conversation/project";
import { projectedRow } from "./projectedRows";

// The row adapter maps the shared projector's ProjectedEntry kinds onto the
// native MobileTimelineItem union exactly as mobile/src/conversation/project.ts
// builds those rows today, so the ~23 existing consumers compile and behave
// unchanged when a later slice swaps projectTimeline for projectThread + this
// adapter. Every expectation below therefore pins the CURRENT native row shape
// (project.ts's projectItem / warningItem / failureItem), not a redesigned one.

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
