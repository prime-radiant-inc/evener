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
const viewport = vi.hoisted(() => ({ height: 844, fontScale: 1 }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ width: 390, scale: 3, ...viewport }),
	ActionSheetIOS: { showActionSheetWithOptions: actionSheet.show },
	// The real Modal renders its children only while visible.
	Modal: (props: { visible?: boolean; children?: ReactNode }) =>
		props.visible ? createElement("Modal", null, props.children) : null,
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
// Records what the composer holds: whether, and as what kind.
const holds = vi.hoisted(() => ({ calls: [] as [boolean, string][] }));
vi.mock("../alerts/alertsContext", () => ({
	useHoldAlerts: (active: boolean, kind: string) => holds.calls.push([active, kind]),
}));

const light = palettes.light;

beforeEach(() => {
	actionSheet.show.mockReset();
	viewport.height = 844;
	viewport.fontScale = 1;
});

function composer(overrides: Partial<Parameters<typeof Composer>[0]> = {}) {
	const props = {
		value: "",
		editable: true,
		mayRest: true,
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

/** How many model chips the tree holds: 0 while the composer rests (the chip
 * steps aside), 1 once the field is reached for. */
function chips(tree: ReactTestRenderer): number {
	return tree.root.findAll((node) => String(node.props.accessibilityLabel).startsWith("Model: ")).length;
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

	it("keeps a full-height focus target while resting and when the field opens", () => {
		const height = (tree: ReactTestRenderer) => fields(tree)[0]?.props.style.minHeight;
		const { props, tree } = composer();
		const initialField = fields(tree)[0];
		expect(height(tree)).toBe(44);
		act(() => fields(tree)[0]?.props.onFocus());
		expect(height(tree)).toBe(44);
		expect(fields(tree)[0]).toBe(initialField);
		act(() => fields(tree)[0]?.props.onBlur());
		expect(height(tree)).toBe(44);
		// The screen says the draft may not rest (text, whitespace, or an image):
		// a full field, focused or not.
		act(() => tree.update(<Composer {...props} value="half a thought" mayRest={false} />));
		expect(height(tree)).toBe(44);
	});
	it("fits a scaled line before measurement, including the resting field's padding", () => {
		viewport.fontScale = 3;
		const { tree } = composer();
		const style = () => fields(tree)[0]?.props.style;
		expect(style().minHeight).toBeGreaterThanOrEqual(92);
		act(() => fields(tree)[0]?.props.onFocus());
		expect(style().minHeight).toBeGreaterThanOrEqual(72);
	});
	it("keeps native font scaling in charge of measuring an existing draft", () => {
		const { props, tree } = composer({ value: "retained-draft", mayRest: false });
		const field = fields(tree)[0];
		act(() => field?.props.onFocus());
		for (const fontScale of [1, 3, 1]) {
			viewport.fontScale = fontScale;
			act(() => tree.update(<Composer {...props} />));
			expect(fields(tree)[0]).toBe(field);
			expect(field?.props.value).toBe(props.value);
			expect(field?.props.allowFontScaling).toBe(true);
			expect(field?.props.style).toMatchObject({ fontSize: 17, lineHeight: 24 });
		}
	});
	it("leaves native text measurement free to grow and shrink a focused draft", () => {
		const { props, tree } = composer();
		const field = fields(tree)[0];
		act(() => field?.props.onFocus());
		for (const value of ["draft words ".repeat(60), ""]) {
			act(() => tree.update(<Composer {...props} value={value} mayRest={value.length === 0} />));
			expect(fields(tree)[0]).toBe(field);
			expect(field?.props.style.height).toBeUndefined();
			expect(field?.props.style).toMatchObject({ minHeight: 44, maxHeight: 144 });
		}
	});
	it("uses content measurements for expansion without fixing the input height", () => {
		viewport.fontScale = 3;
		viewport.height = 600;
		const { tree } = composer({ value: "draft-data", mayRest: false });
		const measure = (height: number) =>
			act(() => fields(tree)[0]?.props.onContentSizeChange({ nativeEvent: { contentSize: { width: 300, height } } }));
		measure(144);
		expect(fields(tree)[0]?.props.style.height).toBeUndefined();
		expect(fields(tree)[0]?.props.style).toMatchObject({ minHeight: 72, maxHeight: 200 });
		measure(288);
		expect(fields(tree)[0]?.props.style.height).toBeUndefined();
		expect(fields(tree)[0]?.props.style.maxHeight).toBe(200);
		expect(pressable(tree, "Expand editor")).toBeDefined();
		measure(72);
		expect(fields(tree)[0]?.props.style.height).toBeUndefined();
		expect(fields(tree)[0]?.props.style.minHeight).toBe(72);
		expect(pressable(tree, "Expand editor")).toBeUndefined();
	});
	it("offers expansion at the native layout cap, then removes it when a cleared draft shrinks", () => {
		const { props, tree } = composer({ value: "draft words ".repeat(80), mayRest: false });
		const layout = (height: number) =>
			act(() => fields(tree)[0]?.props.onLayout?.({ nativeEvent: { layout: { width: 352, height } } }));
		// iOS can stop reporting content size once its frame reaches the cap.
		layout(144);
		expect(pressable(tree, "Expand editor")).toBeDefined();
		act(() => tree.update(<Composer {...props} value="" mayRest />));
		layout(44);
		expect(pressable(tree, "Expand editor")).toBeUndefined();
	});
	it("keeps composer symbols inside their targets at the largest text size", () => {
		viewport.fontScale = 3.2;
		const { tree } = composer({ value: "1\n2\n3\n4\n5\n6\n7", mayRest: false });
		for (const label of ["Add", "Expand editor"]) {
			const button = pressable(tree, label);
			const bounds = button?.props.style({ pressed: false });
			const size = button?.findByType("SymbolView" as never).props.size;
			expect(size).toBeLessThan(bounds.width);
			expect(size).toBeLessThan(bounds.height);
		}
	});
	// The screen decides whether the draft may rest (an attached image or a
	// read-only field says no); whenever it says no the composer stays open.
	it("keeps a full field whenever the screen says the draft may not rest", () => {
		const { tree } = composer({ mayRest: false });
		expect(fields(tree)[0]?.props.style.minHeight).toBe(44);
		// The screen's answer wins whichever way focus falls.
		act(() => fields(tree)[0]?.props.onFocus());
		expect(fields(tree)[0]?.props.style.minHeight).toBe(44);
		act(() => fields(tree)[0]?.props.onBlur());
		expect(fields(tree)[0]?.props.style.minHeight).toBe(44);
	});
	// The Send/Stop target stays the controls row's 44pt whichever way the field
	// rests, so the composer always holds that target (Jesse's ruling).
	it("keeps a 44pt Send target under a resting field", () => {
		const { tree } = composer();
		expect(fields(tree)[0]?.props.style.minHeight).toBe(44);
		const send = pressable(tree, "Queue message");
		expect(send?.props.style({ pressed: false })).toMatchObject({ width: 44, height: 44 });
	});

	// Collapsed (empty and unfocused) the composer is one row: +, the field, and
	// Send/Stop, with the model chip stepping aside until you reach for it.
	it("collapses to one row, with +, the field and Send sharing it", () => {
		const chip = <ModelChip label="Gpt 6 Astra · XHigh" onPress={() => {}} />;
		const { props, tree } = composer({ settings: chip });
		// The row that holds the field: its parent carries the layout the shape
		// switches (row while collapsed, column while open).
		const rowStyle = () => fields(tree)[0]?.parent?.parent?.props?.style ?? {};
		// Collapsed: one row, and the chip is gone.
		expect(rowStyle()).toMatchObject({ flexDirection: "row" });
		expect(chips(tree)).toBe(0);
		expect(pressable(tree, "Add")).toBeDefined();
		expect(pressable(tree, "Queue message")).toBeDefined();

		// Focusing the field opens it: the chip returns and the layout stacks.
		act(() => fields(tree)[0]?.props.onFocus());
		expect(chips(tree)).toBe(1);
		expect(rowStyle()).toMatchObject({ flexDirection: "column" });
		act(() => fields(tree)[0]?.props.onBlur());
		expect(chips(tree)).toBe(0);
		// A draft keeps it open too.
		act(() => tree.update(<Composer {...props} value="x" mayRest={false} settings={chip} />));
		expect(chips(tree)).toBe(1);
	});
	// Focusing is the only way out of the resting row, so a field that cannot
	// take focus (which the screen reports as mayRest false) must stay open or
	// the model chip is stranded with nothing to reach for.
	it("shows the chip while the screen says the draft may not rest", () => {
		const chip = <ModelChip label="Gpt 6 Astra · XHigh" onPress={() => {}} />;
		const { tree } = composer({ mayRest: false, settings: chip });
		expect(chips(tree)).toBe(1);
		expect(pressable(tree, "Model: Gpt 6 Astra · XHigh. Change model or effort")).toBeDefined();
	});
	it("sits close to the screen's bottom, with a small margin under the card", () => {
		const { tree } = composer();
		// The card's own margin; the frame under it owns the home-indicator room.
		const card = tree.root.find((node) => node.props.testID === "composer-card");
		expect(card.props.style).toMatchObject({ marginBottom: 4 });
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
		// The full-screen editor opens with its own field.
		expect(fields(tree)).toHaveLength(2);
		const editor = fields(tree)[1];
		expect(editor?.props.value).toBe(`${six}\n7`);
		act(() => editor?.props.onChangeText("shorter"));
		expect(props.onChangeText).toHaveBeenCalledWith("shorter");
		act(() => pressable(tree, "Done")?.props.onPress());
		expect(fields(tree)).toHaveLength(1);
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

	it("offers the editor when the window cap hides text before six lines", () => {
		viewport.height = 330;
		const { tree } = composer({ value: "paragraph-data", mayRest: false });
		const field = fields(tree)[0];
		expect(field?.props.style.maxHeight).toBe(110);
		act(() => field?.props.onContentSizeChange({ nativeEvent: { contentSize: { width: 300, height: 144 } } }));
		const expand = pressable(tree, "Expand editor");
		expect(expand).toBeDefined();
		act(() => expand?.props.onPress());
		expect(fields(tree)).toHaveLength(2);
		expect(fields(tree)[1]?.props.value).toBe("paragraph-data");
	});

	it("offers the editor for explicit lines above the viewport cap before measurement", () => {
		viewport.fontScale = 2;
		viewport.height = 600;
		const { tree } = composer({ value: "1\n2\n3\n4\n5", mayRest: false });
		expect(pressable(tree, "Expand editor")).toBeDefined();
	});

	// The caller decides Stop: only it sees the whole draft, text and images
	// alike, so the composer renders whatever slot it is handed.
	it("renders the slot's occupant the caller names, Send or Stop", () => {
		const onStop = vi.fn();
		const stopping = composer({ stop: { onStop, disabled: false } });
		expect(pressable(stopping.tree, "Queue message")).toBeUndefined();
		act(() => pressable(stopping.tree, "Stop")?.props.onPress());
		expect(onStop).toHaveBeenCalledTimes(1);
		expect(stopping.props.onSend).not.toHaveBeenCalled();

		const sending = composer({ value: "half a thought" });
		expect(pressable(sending.tree, "Stop")).toBeUndefined();
		const send = pressable(sending.tree, "Queue message");
		expect(send).toBeDefined();
		act(() => send?.props.onPress());
		expect(sending.props.onSend).toHaveBeenCalledTimes(1);
	});

	it("keeps Send when the session cannot stop", () => {
		const { tree } = composer();
		expect(pressable(tree, "Stop")).toBeUndefined();
		expect(pressable(tree, "Queue message")).toBeDefined();
	});

	it("shows Stop in the slot and stops when pressed, and disables it while a stop is in flight", () => {
		const onStop = vi.fn();
		const { props, tree } = composer({ stop: { onStop, disabled: false } });
		expect(pressable(tree, "Queue message")).toBeUndefined();
		const stop = pressable(tree, "Stop");
		expect(stop?.props.accessibilityState).toMatchObject({ disabled: false });
		expect(stop?.findByType("SymbolView" as never).props).toMatchObject({
			name: "stop.fill",
			tintColor: light.onFill,
		});
		// The slot keeps the Send circle's shape, in the danger fill.
		expect(stop?.findByType("View" as never).props.style).toMatchObject({
			width: 36,
			height: 36,
			backgroundColor: light.danger,
		});
		act(() => stop?.props.onPress());
		expect(onStop).toHaveBeenCalledTimes(1);
		expect(props.onSend).not.toHaveBeenCalled();

		act(() => tree.update(<Composer {...props} stop={{ onStop, disabled: true }} />));
		expect(pressable(tree, "Stop")?.props.disabled).toBe(true);
	});

	it("never offers Steer, Queue, Reconnect or Refresh", () => {
		const { tree } = composer({ value: "1\n2\n3\n4\n5\n6\n7" });
		const labels = tree.root
			.findAll((node) => typeof node.props.accessibilityLabel === "string")
			.map((node) => node.props.accessibilityLabel as string);
		const words = [...labels, ...renderedText(tree).split(" ")];
		for (const word of ["Steer", "Queue", "Reconnect", "Refresh"]) expect(words).not.toContain(word);
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
