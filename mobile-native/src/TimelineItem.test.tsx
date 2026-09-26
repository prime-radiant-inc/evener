// The option rows a question timeline item renders key on their POSITION in
// the ask (timeline.ts's questionOptionKey), never on the label: the store's
// publish bounds every label the timeline carries (projectedRows.ts's
// truncateItem through boundQuestion, at MAX_ITEM_BYTES), so two options
// whose labels share a prefix past the bound cut to the same string. Keyed
// on that label — the pre-fix expression `${question.key}:${option.label}`
// — both rows answered to ONE React key, and React reported the duplicate
// on every render. This mounts the real TimelineItem with two such options
// (bounded exactly the way the store publishes them) and pins the absence
// of that report; the pure key's contract is pinned separately in
// timeline.test.ts.
import { expect, it, vi } from "vitest";
import type { AskQuestionRef } from "@evener/appwire-client";
import {
	boundQuestion,
	MAX_ITEM_BYTES,
	truncateText,
	type MobileTimelineItem,
} from "./projectedRows";
import { TimelineItem } from "./TimelineItem";
import { nativeModuleMock, render } from "./renderNative.testkit";

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
// The question case renders only Copy rows; these two leaves drag in the
// native module graph (expo-clipboard, expo-secure-store, the connection
// stack) that the case never touches.
vi.mock("./MarkdownResponse", () => ({ MarkdownResponse: () => null }));
vi.mock("./TranscriptImages", () => ({ TranscriptImages: () => null }));

const ask: AskQuestionRef = {
	key: "call:0",
	callId: "call",
	header: "Choose",
	question: "Pick one",
	multiSelect: false,
	options: [],
};

// The store's bounded publish of one ask whose two options share a prefix
// past the display bound, so both labels cut to the same string.
function boundedAskRow(first: string, second: string): MobileTimelineItem {
	const questions = [
		boundQuestion(
			{
				...ask,
				options: [
					{ label: first, detail: "" },
					{ label: second, detail: "" },
				],
			},
			(text) => truncateText(text, MAX_ITEM_BYTES),
		),
	];
	return { kind: "question", id: "ask-1", questions };
}

it("renders an ask's option rows without a duplicate-key report when the bounded labels collide", () => {
	const prefix = "x".repeat(MAX_ITEM_BYTES * 2);
	const row = boundedAskRow(`${prefix}-first-tail`, `${prefix}-second-tail`);
	// The collision is real: the store's publish cuts both labels to the
	// same copy, so the pre-fix label-based key answered for both rows.
	const bounded = row.kind === "question" ? row.questions[0] : undefined;
	expect(bounded?.options[0].label).toBe(bounded?.options[1].label);

	const errors: string[] = [];
	const spy = vi.spyOn(console, "error").mockImplementation((...args) => {
		errors.push(args.map(String).join(" "));
	});
	try {
		render(<TimelineItem item={row} hubId="hub" sessionRef="session" />);
	} finally {
		spy.mockRestore();
	}
	expect(errors.filter((line) => /same key/.test(line))).toEqual([]);
});
