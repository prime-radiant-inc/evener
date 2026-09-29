// The pin editor's blocking reason line names what the screen is actually
// waiting on. When a previous pin change left a journal checkpoint, "confirm
// the previous pin change" is right; when only the screen's own first read
// failed, the same line must not blame a change the user never made. These
// mount the real editor through the native testkit and read its rendered text.
import { createElement } from "react";
import { expect, it, vi } from "vitest";
import { PinAssignmentEditor, type PinAssignmentEditorProps } from "./PinAssignmentEditor";
import { render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

function props(overrides: Partial<PinAssignmentEditorProps> = {}): PinAssignmentEditorProps {
	return {
		title: "Pin session",
		hubName: "Hub",
		sections: [],
		selection: null,
		change: () => {},
		connected: true,
		canEdit: true,
		loading: false,
		stale: false,
		remaining: 0,
		pending: false,
		uncertain: true,
		previousChange: false,
		error: null,
		refresh: () => {},
		more: () => {},
		save: () => {},
		unpin: () => {},
		close: () => {},
		...overrides,
	};
}

it("blames the failed load, not a previous change, when there is no checkpoint", () => {
	const tree = render(createElement(PinAssignmentEditor, props({ uncertain: true, previousChange: false })));
	const text = renderedText(tree);
	expect(text).toContain("Check this session's pins before editing.");
	expect(text).not.toContain("previous pin change");
});

it("keeps the previous-change wording when a checkpoint is present", () => {
	const tree = render(createElement(PinAssignmentEditor, props({ uncertain: true, previousChange: true })));
	expect(renderedText(tree)).toContain("Check the previous pin change before editing it.");
});
