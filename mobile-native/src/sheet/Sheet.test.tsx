import { ScrollView, Text } from "react-native";
import { act, type ReactTestRendererJSON } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { alertRequests, render, renderHook, renderedText } from "../renderNative.testkit";
import { Sheet, useSheet } from "./Sheet";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), dispatch: vi.fn() }));
const guard = vi.hoisted(() => ({
	prevented: false,
	onPrevent: null as null | ((options: { data: { action: unknown } }) => void),
}));

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: (prevent: boolean, callback: (options: { data: { action: unknown } }) => void) => {
		guard.prevented = prevent;
		guard.onPrevent = callback;
	},
}));

const swipeDown = { type: "POP", payload: { count: 1 } };

beforeEach(() => {
	navigation.goBack.mockClear();
	navigation.dispatch.mockClear();
	guard.onPrevent = null;
	alertRequests.length = 0;
});

describe("closing a sheet (spec 6)", () => {
	it("closes a sheet with nothing unsaved at once", () => {
		const sheet = renderHook(() => useSheet());
		expect(guard.prevented).toBe(false);
		sheet.result.current.close();
		expect(navigation.goBack).toHaveBeenCalledOnce();
		expect(alertRequests).toEqual([]);
	});

	it("asks before discarding unsaved input, and only Discard lets the sheet go", () => {
		renderHook(() => useSheet({ dirty: true, discardTitle: "Discard this comment?" }));
		expect(guard.prevented).toBe(true);
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(alertRequests).toHaveLength(1);
		const [ask] = alertRequests;
		expect(ask?.title).toBe("Discard this comment?");
		expect(ask?.buttons?.map((button) => button.text)).toEqual(["Keep editing", "Discard"]);
		ask?.buttons?.[0]?.onPress?.();
		expect(navigation.dispatch).not.toHaveBeenCalled();
		ask?.buttons?.[1]?.onPress?.();
		expect(navigation.dispatch).toHaveBeenCalledWith(swipeDown);
	});

	it("leaves without asking when it finishes on purpose", () => {
		const sheet = renderHook(() => useSheet({ dirty: true }));
		sheet.result.current.finish();
		expect(navigation.goBack).toHaveBeenCalledOnce();
		const pop = { type: "GO_BACK" };
		act(() => guard.onPrevent?.({ data: { action: pop } }));
		expect(navigation.dispatch).toHaveBeenCalledWith(pop);
		expect(alertRequests).toEqual([]);
	});

	it("resets finishing once it leaves, so a second finish is judged on its own", () => {
		const sheet = renderHook(() => useSheet({ dirty: true }));
		sheet.result.current.finish();
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(alertRequests).toEqual([]);

		// A stale `finishing` flag would still read true here, bypassing the
		// prompt for a swipe that never called finish().
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(alertRequests).toHaveLength(1);

		// A genuine second finish still bypasses the prompt, on its own merits.
		navigation.dispatch.mockClear();
		sheet.result.current.finish();
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(navigation.dispatch).toHaveBeenCalledWith(swipeDown);
		expect(alertRequests).toHaveLength(1);
	});

	it("lets `then` remove the sheet when it leads somewhere else", () => {
		const sheet = renderHook(() => useSheet({ dirty: true }));
		const then = vi.fn();
		sheet.result.current.finish(then);
		expect(then).toHaveBeenCalledOnce();
		expect(navigation.goBack).not.toHaveBeenCalled();
	});

	it("tells its owner once, when it goes away", () => {
		const onClosed = vi.fn();
		const sheet = renderHook(() => useSheet({ onClosed }));
		sheet.rerender();
		expect(onClosed).not.toHaveBeenCalled();
		sheet.unmount();
		expect(onClosed).toHaveBeenCalledOnce();
	});
});

describe("the sheet's chrome", () => {
	const pressable = (tree: ReturnType<typeof render>, label: string) =>
		tree.root.find((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label);

	it("draws the header and the body as the screen's only two children", () => {
		const cancel = vi.fn();
		const add = vi.fn();
		const tree = render(
			<Sheet
				title="Comment"
				onCancel={cancel}
				done={{ label: "Add", disabled: true, onPress: add }}
				accessory={<Text>Pinned under the title</Text>}
			>
				<ScrollView />
			</Sheet>,
		);
		const children = tree.toJSON() as ReactTestRendererJSON[];
		expect(children.map((child) => child.type)).toEqual(["View", "ScrollView"]);
		expect(children[0]?.props.collapsable).toBe(false);
		expect(renderedText(tree)).toContain("Comment");
		expect(renderedText(tree)).toContain("Pinned under the title");
		act(() => pressable(tree, "Cancel").props.onPress());
		expect(cancel).toHaveBeenCalledOnce();
		expect(pressable(tree, "Add").props.disabled).toBe(true);
	});

	it("reads Done unless the sheet names its own verb, and leaves out what it isn't given", () => {
		const done = vi.fn();
		const tree = render(
			<Sheet title="Tasks" done={{ onPress: done }}>
				<ScrollView />
			</Sheet>,
		);
		act(() => pressable(tree, "Done").props.onPress());
		expect(done).toHaveBeenCalledOnce();
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Cancel")).toEqual([]);
	});

	it("caps a long title's width, as the prototype does, so it truncates before it squeezes Cancel", () => {
		const tree = render(
			<Sheet title="Sign in to codex-jesse-fsck.com" onCancel={() => {}}>
				<ScrollView />
			</Sheet>,
		);
		const title = tree.root.findByProps({ accessibilityRole: "header" });
		expect(title.props.numberOfLines).toBe(1);
		expect(title.props.style.maxWidth).toBe("56%");
	});
});
