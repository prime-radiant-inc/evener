import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, unmountMountedTrees } from "../renderNative.testkit";
import { ComposeButton } from "./ComposeButton";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

const palette = paletteFor("light");

afterEach(() => {
	unmountMountedTrees();
});

function compose(over: { disabled?: boolean; onPress?: () => void } = {}) {
	const onPress = over.onPress ?? (() => {});
	const tree = render(<ComposeButton disabled={over.disabled ?? false} onPress={onPress} />);
	const button = tree.root.find(
		(node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "New session",
	);
	return { tree, onPress, button };
}

it("draws a New session button as an accent-filled circle with a pencil symbol", () => {
	const { button } = compose();
	expect(button.props.style({ pressed: false })).toMatchObject({
		width: 56,
		height: 56,
		borderRadius: 28,
		backgroundColor: palette.accentFill,
		opacity: 1,
	});
	expect(button.findByType("SymbolView" as never).props).toMatchObject({
		name: "square.and.pencil",
		size: 24,
		tintColor: palette.onFill,
	});
});

it("opens a new session when pressed", () => {
	const onPress = vi.fn();
	const { button } = compose({ onPress });
	act(() => button.props.onPress());
	expect(onPress).toHaveBeenCalledTimes(1);
});

it("dims and does nothing while the connection is away", () => {
	const onPress = vi.fn();
	const { button } = compose({ disabled: true, onPress });
	expect(button.props.disabled).toBe(true);
	expect(button.props.accessibilityState).toEqual({ disabled: true });
	expect(button.props.style({ pressed: false }).opacity).toBe(0.4);
	act(() => button.props.onPress?.());
	expect(onPress).not.toHaveBeenCalled();
});
