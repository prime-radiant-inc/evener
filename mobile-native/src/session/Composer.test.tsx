import { createElement, type ReactNode } from "react";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { Platform } from "react-native";
import { alertRequests, pressable, render, renderedText } from "../renderNative.testkit";
import { Composer, ModelChip } from "./Composer";
import { ComposerFocus } from "./composerFocus";

const actionSheet = vi.hoisted(() => ({ show: vi.fn() }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	ActionSheetIOS: { showActionSheetWithOptions: actionSheet.show },
	// The real Modal renders its children only while visible.
	Modal: (props: { visible?: boolean; children?: ReactNode }) =>
		props.visible ? createElement("Modal", null, props.children) : null,
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
// Records what the composer holds: whether, and as what kind.
const holds = vi.hoisted(() => ({ calls: [] as [boolean, string][] }));
vi.mock("../alerts/alertsContext", () => ({
	useHoldAlerts: (active: boolean, kind: string) => holds.calls.push([active, kind]),
}));

const light = palettes.light;

beforeEach(() => actionSheet.show.mockReset());

function composer(overrides: Partial<Parameters<typeof Composer>[0]> = {}) {
	const props = {
		value: "",
		editable: true,
		onChangeText: vi.fn(),
		placeholder: "Tell the agent something…",
		sendLabel: "Queue message",
		sendEnabled: true,
		onSend: vi.fn(),
		onPhotoLibrary: vi.fn(),
		onCamera: vi.fn(),
		settings: null,
		...overrides,
	};
	return { props, tree: render(<Composer {...props} />) };
}

function fields(tree: ReactTestRenderer): ReactTestInstance[] {
	return tree.root.findAll((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Message");
}

describe("Composer", () => {
	it("reports its field's focus, and lets go of it when it leaves the screen", () => {
		const focus = new ComposerFocus();
		const { tree } = composer({ focus });
		const [field] = fields(tree);
		act(() => field?.props.onFocus());
		expect(focus.getSnapshot()).toBe(true);
		act(() => field?.props.onBlur());
		expect(focus.getSnapshot()).toBe(false);
		act(() => field?.props.onFocus());
		// A composer unmounted while focused (a dock takes its place) reports
		// no blur of its own.
		act(() => tree.unmount());
		expect(focus.getSnapshot()).toBe(false);
	});

	it("shows the placeholder in a 17/24 field", () => {
		const { tree } = composer();
		const [field] = fields(tree);
		expect(field?.props.placeholder).toBe("Tell the agent something…");
		expect(field?.props.multiline).toBe(true);
		expect(field?.props.style).toMatchObject({ fontSize: 17, lineHeight: 24 });
	});

	it("passes typing through", () => {
		const { props, tree } = composer();
		act(() => fields(tree)[0]?.props.onChangeText("hello"));
		expect(props.onChangeText).toHaveBeenCalledWith("hello");
	});

	it("carries the send label on a paper airplane and sends when enabled", () => {
		const { props, tree } = composer();
		const send = pressable(tree, "Queue message");
		expect(send?.props.accessibilityState).toMatchObject({ disabled: false });
		expect(send?.findByType("SymbolView" as never).props).toMatchObject({
			name: "paperplane.fill",
			tintColor: light.onFill,
		});
		act(() => send?.props.onPress());
		expect(props.onSend).toHaveBeenCalledTimes(1);
	});

	it("disables Send when it cannot send", () => {
		const { tree } = composer({ sendEnabled: false });
		const send = pressable(tree, "Queue message");
		expect(send?.props.disabled).toBe(true);
		expect(send?.props.accessibilityState).toMatchObject({ disabled: true });
	});

	it("opens Photo library, Camera, and Commands and skills from +", () => {
		const onCommands = vi.fn();
		const { props, tree } = composer({ onCommands });
		act(() => pressable(tree, "Add")?.props.onPress());
		const [options, choose] = actionSheet.show.mock.calls[0] as [
			{ options: string[]; cancelButtonIndex: number },
			(index: number) => void,
		];
		expect(options.options).toEqual(["Photo library", "Camera", "Commands and skills", "Cancel"]);
		expect(options.cancelButtonIndex).toBe(3);
		act(() => choose(2));
		expect(onCommands).toHaveBeenCalledTimes(1);
		act(() => choose(3));
		expect(onCommands).toHaveBeenCalledTimes(1);
		expect(props.onPhotoLibrary).not.toHaveBeenCalled();
		expect(props.onCamera).not.toHaveBeenCalled();
	});

	it("keeps Commands and skills out of + when the session can't list them", () => {
		const { props, tree } = composer();
		act(() => pressable(tree, "Add")?.props.onPress());
		expect(actionSheet.show).toHaveBeenCalledTimes(1);
		const [options, choose] = actionSheet.show.mock.calls[0] as [
			{ options: string[]; cancelButtonIndex: number },
			(index: number) => void,
		];
		expect(options.options).toEqual(["Photo library", "Camera", "Cancel"]);
		expect(options.cancelButtonIndex).toBe(2);
		act(() => choose(0));
		expect(props.onPhotoLibrary).toHaveBeenCalledTimes(1);
		act(() => choose(1));
		expect(props.onCamera).toHaveBeenCalledTimes(1);
		act(() => choose(2));
		expect(props.onPhotoLibrary).toHaveBeenCalledTimes(1);
		expect(props.onCamera).toHaveBeenCalledTimes(1);
	});

	it("keeps an Alert with the same choices on Android", () => {
		const platform = Platform as { OS: string };
		platform.OS = "android";
		try {
			const onCommands = vi.fn();
			const { props, tree } = composer({ onCommands });
			alertRequests.length = 0;
			act(() => pressable(tree, "Add")?.props.onPress());
			expect(actionSheet.show).not.toHaveBeenCalled();
			const buttons = alertRequests[0]?.buttons ?? [];
			expect(buttons.map((button) => button.text)).toEqual([
				"Photo library",
				"Camera",
				"Commands and skills",
				"Cancel",
			]);
			act(() => buttons[0]?.onPress?.());
			act(() => buttons[1]?.onPress?.());
			act(() => buttons[2]?.onPress?.());
			expect(props.onPhotoLibrary).toHaveBeenCalledTimes(1);
			expect(props.onCamera).toHaveBeenCalledTimes(1);
			expect(onCommands).toHaveBeenCalledTimes(1);
		} finally {
			platform.OS = "ios";
		}
	});

	it("offers the full-screen editor once the text runs past six lines", () => {
		const six = "1\n2\n3\n4\n5\n6";
		const { props, tree } = composer({ value: six });
		expect(pressable(tree, "Expand editor")).toBeUndefined();

		act(() => tree.update(<Composer {...props} value={`${six}\n7`} />));
		const expand = pressable(tree, "Expand editor");
		expect(expand?.findByType("SymbolView" as never).props.name).toBe("arrow.up.left.and.arrow.down.right");
		act(() => expand?.props.onPress());
		expect(renderedText(tree)).toContain("Done");
		const editor = fields(tree)[1];
		expect(editor?.props.value).toBe(`${six}\n7`);
		act(() => editor?.props.onChangeText("shorter"));
		expect(props.onChangeText).toHaveBeenCalledWith("shorter");
		act(() => pressable(tree, "Done")?.props.onPress());
		expect(renderedText(tree)).not.toContain("Done");
	});

	// The full-screen editor's own field takes the keyboard, so what folds
	// while you type in the composer comes back behind it.
	it("lets go of the composer's focus when the full-screen editor opens", () => {
		const focus = new ComposerFocus();
		const { tree } = composer({ value: "1\n2\n3\n4\n5\n6\n7", focus });
		act(() => fields(tree)[0]?.props.onFocus());
		expect(focus.getSnapshot()).toBe(true);
		act(() => pressable(tree, "Expand editor")?.props.onPress());
		expect(focus.getSnapshot()).toBe(false);
	});

	it("offers the editor when long lines wrap past six lines", () => {
		const { tree } = composer({ value: "one long paragraph" });
		expect(pressable(tree, "Expand editor")).toBeUndefined();
		act(() =>
			fields(tree)[0]?.props.onContentSizeChange({
				nativeEvent: { contentSize: { width: 300, height: 7 * 24 } },
			}),
		);
		expect(pressable(tree, "Expand editor")).toBeDefined();
	});

	it("never offers Stop, Steer, Queue, Reconnect or Refresh", () => {
		const { tree } = composer({ value: "1\n2\n3\n4\n5\n6\n7" });
		const labels = tree.root
			.findAll((node) => typeof node.props.accessibilityLabel === "string")
			.map((node) => node.props.accessibilityLabel as string);
		const words = [...labels, ...renderedText(tree).split(" ")];
		for (const word of ["Stop", "Steer", "Queue", "Reconnect", "Refresh"]) expect(words).not.toContain(word);
	});
});

describe("the model chip (spec 8.5)", () => {
	it("names the model and effort on one line in ink-mid, and opens the model sheet", () => {
		const onPress = vi.fn();
		const tree = render(<ModelChip label="GLM 5.3 Vision · XHigh" onPress={onPress} />);
		const chip = pressable(tree, "Model: GLM 5.3 Vision · XHigh. Change model or effort");
		if (!chip) throw new Error("no chip");
		expect(chip.props.style({ pressed: false })).toMatchObject({ minHeight: 44 });
		const text = chip.findByType("Text" as never);
		expect(text.props).toMatchObject({ numberOfLines: 1 });
		expect(text.props.style).toMatchObject({ fontSize: 15, lineHeight: 20, color: light.inkMid });
		expect(chip.findByType("SymbolView" as never).props).toMatchObject({ name: "chevron.down", size: 11 });
		act(() => chip.props.onPress());
		expect(onPress).toHaveBeenCalledOnce();
	});

	it("only names the model when there's nothing to change", () => {
		const tree = render(<ModelChip label="muse-spark-1.3" />);
		expect(tree.root.findAll((node) => String(node.type) === "Pressable")).toEqual([]);
		expect(tree.root.findAll((node) => String(node.type) === "SymbolView")).toEqual([]);
		expect(renderedText(tree)).toBe("muse-spark-1.3");
	});
});

describe("holding banners while you type (spec 13.3)", () => {
	// The composer's own hold; its expanded editor's HoldingModal holds as
	// "covered" beside it.
	const holding = () => holds.calls.filter(([, kind]) => kind === "quiet").at(-1);
	it("holds while the field is focused with text in it, and lets go on a blank field or a blur", () => {
		holds.calls.length = 0;
		const { props, tree } = composer();
		const field = () => fields(tree)[0];
		act(() => field()?.props.onFocus());
		expect(holding()).toEqual([false, "quiet"]);
		act(() => tree.update(<Composer {...props} value="half a thought" />));
		expect(holding()).toEqual([true, "quiet"]);
		// Sending empties the field, and that lets banners go.
		act(() => tree.update(<Composer {...props} value="  " />));
		expect(holding()).toEqual([false, "quiet"]);
		act(() => tree.update(<Composer {...props} value="more" />));
		expect(holding()).toEqual([true, "quiet"]);
		act(() => field()?.props.onBlur());
		expect(holding()).toEqual([false, "quiet"]);
	});
});
