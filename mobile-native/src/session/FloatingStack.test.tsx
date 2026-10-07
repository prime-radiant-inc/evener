import { Text } from "react-native";
import { act } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { composerFocusedAs, keyboard, render, renderedText, unmountMountedTrees } from "../renderNative.testkit";
import { FloatingStack, transcriptEndRoomAt } from "./FloatingStack";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

afterEach(() => {
	unmountMountedTrees();
	keyboard.reset();
});

function column(tree: ReturnType<typeof render>) {
	return tree.root.findAll((node) => String(node.type) === "View")[0];
}

describe("what floats above the transcript's end", () => {
	it("stacks the toast, then Next, then the pill, 8pt apart and 10pt up, so none covers another", () => {
		const tree = render(
			<FloatingStack
				toast={<Text>toast</Text>}
				next={<Text>next</Text>}
				pill={<Text>pill</Text>}
				composerFocus={composerFocusedAs(false)}
			/>,
		);
		const stack = column(tree);
		expect(stack.props.style).toMatchObject({ position: "absolute", left: 0, right: 0, bottom: 10, gap: 8 });
		expect(stack.props.style.flexDirection ?? "column").toBe("column");
		const slots = stack.children as ReturnType<typeof column>[];
		expect(slots.map((slot) => slot.findByType("Text" as never).props.children)).toEqual(["toast", "next", "pill"]);
		expect(slots.map((slot) => slot.props.style.alignItems)).toEqual(["center", "flex-end", "center"]);
		expect(slots[1].props.style).toMatchObject({ paddingHorizontal: 16 });
	});

	it("leaves out what doesn't show, so what does keeps its 10pt", () => {
		const tree = render(
			<FloatingStack toast={null} next={<Text>next</Text>} pill={null} composerFocus={composerFocusedAs(false)} />,
		);
		const slots = column(tree).children as ReturnType<typeof column>[];
		expect(slots).toHaveLength(1);
		expect(slots[0].props.style).toMatchObject({ alignItems: "flex-end" });
	});

	// Next steps aside while you type in the composer, reading the keyboard
	// itself so the keyboard coming and going re-renders the stack alone.
	it("steps Next aside while the keyboard is up for the composer, and only then", () => {
		const focus = composerFocusedAs(true);
		const tree = render(<FloatingStack toast={null} next={<Text>next</Text>} pill={null} composerFocus={focus} />);
		act(() => keyboard.show());
		expect(renderedText(tree)).not.toContain("next");
		act(() => focus.set(false));
		expect(renderedText(tree)).toContain("next");
		act(() => focus.set(true));
		act(() => keyboard.hide());
		expect(renderedText(tree)).toContain("next");
	});

	it("has the transcript keep 60pt at its end, grown with the text size and never less, to clear Next", () => {
		expect(transcriptEndRoomAt(0.82)).toBe(60);
		expect(transcriptEndRoomAt(1)).toBe(60);
		expect(transcriptEndRoomAt(1.5)).toBe(90);
	});
});
