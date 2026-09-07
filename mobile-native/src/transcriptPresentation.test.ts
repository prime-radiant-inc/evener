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

it("keeps null config unfiltered and does not mutate the canonical items", () => {
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
	];
	const result = projectNativeTranscript(conversation(items), null);
	expect(result.items).toEqual(items);
	expect(result.items).not.toBe(items);
	expect(result.expandByDefault).toBe(false);
	expect(result.usage).toBeNull();
});

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
