import { expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { HeaderButton } from "./HeaderButton";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

it("names its action for VoiceOver in the accent ink, semibold when it finishes the page", () => {
	const onPress = vi.fn();
	const tree = render(<HeaderButton label="Save" emphasized onPress={onPress} />);
	const button = tree.root.findByProps({ accessibilityRole: "button" });
	expect(button.props.accessibilityLabel).toBe("Save");
	expect(tree.root.findByType("Text" as never).props.style).toMatchObject({
		color: palettes.light.accentInk,
		fontWeight: "600",
	});
	button.props.onPress();
	expect(onPress).toHaveBeenCalledTimes(1);
});

it("holds, and says so, while its action can't run", () => {
	const button = render(<HeaderButton label="Save" disabled onPress={() => {}} />).root.findByProps({
		accessibilityRole: "button",
	});
	expect(button.props.disabled).toBe(true);
	expect(button.props.accessibilityState).toEqual({ disabled: true });
});
