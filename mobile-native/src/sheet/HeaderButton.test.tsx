import { expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { HeaderButton } from "./HeaderButton";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

it("names its action for VoiceOver in the accent ink, semibold when it finishes the page", () => {
	const onPress = vi.fn();
	const tree = render(<HeaderButton label="Save" strong onPress={onPress} />);
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

it("says it's busy while its action runs", () => {
	const button = render(<HeaderButton label="Saving…" disabled busy onPress={() => {}} />).root.findByProps({
		accessibilityRole: "button",
	});
	expect(button.props.accessibilityState).toEqual({ disabled: true, busy: true });
});

it("reads in ink-low while its action can't run, as iOS draws an unavailable bar button", () => {
	const tree = render(<HeaderButton label="Start" strong disabled onPress={() => {}} />);
	expect(tree.root.findByType("Text" as never).props.style).toMatchObject({
		color: palettes.light.inkLow,
		fontWeight: "600",
	});
	const style = tree.root.findByProps({ accessibilityRole: "button" }).props.style({ pressed: false });
	expect(style.opacity).toBe(1);
});
