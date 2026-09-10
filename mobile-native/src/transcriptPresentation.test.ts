import { expect, it } from "vitest";
import {
	makeTranscriptDisplayConfig,
	presetContent,
} from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import type {
	MobileConversation,
	MobileTimelineItem,
} from "../../mobile/src/conversation/model";
import { projectNativeTranscript } from "./transcriptPresentation";

function conversation(items: MobileTimelineItem[]): MobileConversation {
	return {
		items,
		usage: { inputTokens: 10, outputTokens: 20, cost: "$1" },
	} as MobileConversation;
}

const member = (
	id: string,
	description: string,
	item: number,
): NonNullable<
	Extract<MobileTimelineItem, { kind: "activity" }>["members"]
>[number] => ({
	id,
	label: "shell",
	family: "tool",
	state: "completed",
	detail: { description, output: id },
	transcriptKey: `key-${id}`,
	position: { entry: 1, item },
});

it.each([null, undefined])(
	"flattens every clustered member and preserves attachments when preferences are %s",
	(config) => {
		const items: MobileTimelineItem[] = [
			{
				kind: "activity",
				id: "cluster",
				label: "shell",
				family: "tool",
				state: "completed",
				detail: {},
				members: [member("a", "first", 0), member("b", "second", 1)],
			},
			{
				kind: "attachments",
				id: "b:attachments",
				sourceTranscriptKey: "key-b",
				items: [{ id: "image", src: "data:image/png;base64,x" }],
			},
		];
		const result = projectNativeTranscript(conversation(items), config);
		expect(result.items).toEqual([
			{ kind: "activity", ...member("a", "first", 0) },
			{ kind: "activity", ...member("b", "second", 1) },
			items[1],
		]);
		expect(result.activityPresentation).toEqual(
			new Map([
				["a", { mode: "full" }],
				["b", { mode: "full" }],
			]),
		);
		expect(result.items).not.toBe(items);
		expect(result.expandByDefault).toBe(false);
		expect(result.usage).toBeNull();
	},
);

it("unrolls members, applies preset content, and keeps source-linked attachments", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "activity",
			id: "cluster",
			label: "shell",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
		{
			kind: "attachments",
			id: "b:attachments",
			sourceTranscriptKey: "key-b",
			items: [{ id: "image", src: "data:image/png;base64,x" }],
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"a",
		"b",
		"b:attachments",
	]);
	expect(result.items[0]).toMatchObject({ position: { entry: 1, item: 0 } });
	expect(result.activityPresentation.get("a")).toEqual({ mode: "full" });
	expect(result.activityPresentation.get("b")).toEqual({ mode: "full" });
});

it("projects intent and critical activity modes without dropping active or unknown context", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "activity",
			id: "tools",
			label: "shell",
			family: "tool",
			state: "completed",
			detail: { description: "Inspect source" },
		},
		{
			kind: "activity",
			id: "failed",
			label: "shell",
			family: "tool",
			state: "failed",
			detail: { error: "failed" },
		},
		{
			kind: "activity",
			id: "active",
			label: "shell",
			family: "tool",
			state: "running",
			detail: {},
		},
		{
			kind: "activity",
			id: "unknown",
			label: "Activity",
			family: "unknown",
			state: "completed",
			detail: {},
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "custom", ...presetContent("chat") }),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"tools",
		"failed",
		"active",
		"unknown",
	]);
	expect(result.activityPresentation.get("tools")).toEqual({
		mode: "intent",
		summary: "Inspect source",
	});
	expect(result.activityPresentation.get("active")).toMatchObject({
		mode: "critical",
	});
	expect(result.activityPresentation.get("failed")).toMatchObject({
		mode: "critical",
	});
	expect(result.activityPresentation.get("unknown")).toEqual({ mode: "full" });
});

it.each(["chat", "intent", "tools", "activity", "full"] as const)(
	"honors preset %s content and expansion defaults",
	(level) => {
		const result = projectNativeTranscript(
			conversation([
				{
					kind: "activity",
					id: "reasoning",
					label: "reasoning",
					family: "reasoning",
					state: "completed",
					detail: {},
				},
			]),
			makeTranscriptDisplayConfig({ kind: "preset", level }),
		);
		expect(result.expandByDefault).toBe(presetContent(level).expandByDefault);
		expect(result.items).toHaveLength(level === "full" ? 1 : 0);
	},
);

it("applies typed system-event flags and masks usage fields independently", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "notice",
			id: "prompt",
			origin: "system",
			family: "system-prelude",
			tone: "system",
			text: "prompt",
			eventKind: "prompt_loaded",
		},
		{
			kind: "notice",
			id: "hook",
			origin: "system",
			family: "diagnostic",
			tone: "system",
			text: "hook",
			eventKind: "hook_completed",
			exitCode: 4,
		},
		{
			kind: "notice",
			id: "routine",
			origin: "system",
			family: "diagnostic",
			tone: "system",
			text: "routine",
			eventKind: "environment",
		},
		{
			kind: "notice",
			id: "error",
			origin: "system",
			family: "warning",
			tone: "system",
			text: "error",
			eventKind: "error",
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig(
			{ kind: "preset", level: "chat" },
			{ promptEvents: true, tokenCounts: false, estimatedCost: false },
		),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"prompt",
		"hook",
		"error",
	]);
	expect(result.usage).toMatchObject({
		inputTokens: undefined,
		outputTokens: undefined,
		cost: undefined,
	});
});

it("does not mutate clustered members or source items while projecting", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "activity",
			id: "cluster",
			label: "tools",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
	];
	const before = structuredClone(items);
	projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "custom", ...presetContent("full") }),
	);
	expect(items).toEqual(before);
});

it("preserves canonical interleaving and defers source-linked attachments to members", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "notice",
			id: "before",
			origin: "steering",
			family: "informational",
			tone: "info",
			text: "before",
		},
		{
			kind: "attachments",
			id: "b:image",
			sourceTranscriptKey: "key-b",
			items: [{ id: "image", src: "data:image/png;base64,x" }],
		},
		{
			kind: "activity",
			id: "cluster",
			label: "tools",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
		{
			kind: "notice",
			id: "after",
			origin: "steering",
			family: "informational",
			tone: "info",
			text: "after",
		},
	];
	const result = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
	);
	expect(result.items.map((item) => item.id)).toEqual([
		"before",
		"a",
		"b",
		"b:image",
		"after",
	]);
});

it("keeps missing-description tool calls critical when full details are disabled", () => {
	const item: MobileTimelineItem = {
		kind: "activity",
		id: "missing",
		label: "shell",
		family: "tool",
		state: "completed",
		detail: {},
	};
	const result = projectNativeTranscript(
		conversation([item]),
		makeTranscriptDisplayConfig({
			kind: "custom",
			toolIntent: false,
			toolCalls: false,
			reasoning: false,
			expandByDefault: false,
		}),
	);
	expect(result.activityPresentation.get("missing")).toMatchObject({
		mode: "critical",
	});
});

it.each([
	["completed", "wrote 26 bytes to /tmp/request-16.txt", "Write"],
	["failed", "permission denied", "Write"],
	["running", undefined, "Write"],
] as const)(
	"derives a bounded write_file action summary from its target path when description is absent (%s)",
	(state, output, label) => {
		const result = projectNativeTranscript(
			conversation([
				{
					kind: "activity",
					id: "write",
					label: "write_file",
					family: "tool",
					state,
					detail: {
						arguments: JSON.stringify({
							file_path: "/tmp/request-16.txt",
							content: "private file contents",
						}),
						...(output ? { output } : {}),
						error: state === "failed" ? output : undefined,
					},
				},
			]),
			makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
		);
		const presentation = result.activityPresentation.get("write");
		expect(presentation?.summary).toBe(`${label} /tmp/request-16.txt`);
		expect(presentation?.summary).not.toContain("private file contents");
	},
);

it("keeps each member attachment adjacent while messages and unkeyed warnings retain order", () => {
	const items: MobileTimelineItem[] = [
		{
			kind: "user",
			id: "user",
			text: "Start",
			position: { entry: 0, item: 0 },
		},
		{
			kind: "notice",
			id: "warning",
			origin: "system",
			family: "warning",
			tone: "warning",
			text: "Keep",
		},
		{
			kind: "attachments",
			id: "image-a",
			sourceTranscriptKey: "key-a",
			items: [],
		},
		{
			kind: "activity",
			id: "cluster",
			label: "Tools",
			family: "tool",
			state: "completed",
			detail: {},
			members: [member("a", "first", 0), member("b", "second", 1)],
		},
		{
			kind: "attachments",
			id: "image-b",
			sourceTranscriptKey: "key-b",
			items: [],
		},
		{
			kind: "assistant",
			id: "reply",
			markdown: "Done",
			streaming: false,
			position: { entry: 2, item: 0 },
		},
	];
	const full = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({ kind: "preset", level: "full" }),
	);
	expect(full.items.map((item) => item.id)).toEqual([
		"user",
		"warning",
		"a",
		"image-a",
		"b",
		"image-b",
		"reply",
	]);
	const hidden = projectNativeTranscript(
		conversation(items),
		makeTranscriptDisplayConfig({
			kind: "custom",
			toolCalls: false,
			toolIntent: false,
			reasoning: false,
			expandByDefault: false,
		}),
	);
	expect(hidden.items.map((item) => item.id)).toEqual([
		"user",
		"warning",
		"image-a",
		"image-b",
		"reply",
	]);
});

it("falls back safely for malformed or empty write_file arguments", () => {
	for (const argumentsValue of [
		"{",
		"null",
		"{}",
		JSON.stringify({ file_path: "   " }),
		JSON.stringify({ file_path: 42 }),
		JSON.stringify({ path: "/tmp/unsupported-field.txt" }),
		JSON.stringify({ content: "private" }),
	]) {
		const result = projectNativeTranscript(
			conversation([
				{
					kind: "activity",
					id: argumentsValue,
					label: "write_file",
					family: "tool",
					state: "completed",
					detail: { arguments: argumentsValue },
				},
			]),
			makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
		);
		expect(result.activityPresentation.get(argumentsValue)?.summary).toBe(
			"Action summary unavailable",
		);
	}
});

it("bounds a derived write_file target without exposing its content", () => {
	const target = `/tmp/${"a".repeat(300)}`;
	const result = projectNativeTranscript(
		conversation([
			{
				kind: "activity",
				id: "write-long",
				label: "write_file",
				family: "tool",
				state: "completed",
				detail: {
					arguments: JSON.stringify({ file_path: target, content: "private" }),
				},
			},
		]),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
	);
	const summary = result.activityPresentation.get("write-long")?.summary;
	expect(summary).toHaveLength("Write ".length + 256);
	expect(summary?.endsWith("...")).toBe(true);
	expect(summary).not.toContain("private");
});

it("keeps an authoritative write_file description ahead of derived details", () => {
	const result = projectNativeTranscript(
		conversation([
			{
				kind: "activity",
				id: "write-described",
				label: "write_file",
				family: "tool",
				state: "completed",
				detail: {
					description: "Save the fixture",
					arguments: JSON.stringify({
						file_path: "/tmp/request.txt",
						content: "private",
					}),
				},
			},
		]),
		makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
	);
	expect(result.activityPresentation.get("write-described")?.summary).toBe(
		"Save the fixture",
	);
});
