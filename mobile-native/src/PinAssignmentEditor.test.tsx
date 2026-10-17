// The pin editor's blocking reason line names what the screen is actually
// waiting on. When a previous pin change left a journal checkpoint, "confirm
// the previous pin change" is right; when only the screen's own first read
// failed, the same line must not blame a change the user never made. These
// mount the real editor through the native testkit and read its rendered text.
import { createElement } from "react";
import { expect, it, vi } from "vitest";
import { PinAssignmentEditor, type PinAssignmentEditorProps } from "./PinAssignmentEditor";
import { markers } from "./markerTestUtils";
import { render } from "./renderNative.testkit";
import { Row } from "./sheet/Grouped";

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
	expect(markers(tree, "pin-status")).toEqual(["unread"]);
});

it("names the previous change when a checkpoint is present", () => {
	const tree = render(createElement(PinAssignmentEditor, props({ uncertain: true, previousChange: true })));
	expect(markers(tree, "pin-status")).toEqual(["previous-change"]);
});

it("lays its choices out as grouped rows, not text-glyph radios", () => {
	const tree = render(
		createElement(
			PinAssignmentEditor,
			props({
				sections: [
					{ id: "s1", name: "Release prep", count: 2 },
					{ id: "s2", name: "Reading list", count: 1 },
				],
				selection: { kind: "existing", sectionId: "s1" },
			}),
		),
	);
	// The picker's choices and the create-a-category row are Grouped rows, so
	// their check draws as a symbol in the row's glyph column, never as text.
	const rows = tree.root.findAllByType(Row);
	expect(rows).toHaveLength(3);
	expect(rows.map((row) => row.props.checked)).toEqual([true, false, false]);
	// A text radio renders its glyph inside the label; no row label carries one.
	expect(rows.map((row) => row.props.label).every((label) => !/[○●]/.test(label))).toBe(true);
});
