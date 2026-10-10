import { afterEach, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render, unmountMountedTrees } from "../renderNative.testkit";
import { HeaderButton } from "./HeaderButton";

// The text size the phone is set to: 1 is the default (Large).
const text = vi.hoisted(() => ({ fontScale: 1 }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ fontScale: text.fontScale, scale: 2, width: 390, height: 844 }),
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

afterEach(() => {
	unmountMountedTrees();
	text.fontScale = 1;
});

it("keeps its label whole on one line at the largest text sizes, growing no further than xxxLarge (#3311)", () => {
	// Accessibility XXXL: Body is 53pt, about 3.1 times its 17pt default.
	text.fontScale = 53 / 17;
	const tree = render(<HeaderButton label="Cancel" onPress={() => {}} />);
	const label = tree.root.findByType("Text" as never);
	expect(label.props.numberOfLines).toBe(1);
	// Body's xxxLarge size, the largest before the accessibility sizes.
	expect(label.props.style.fontSize).toBe(23);
	// Text that stops growing offers the Large Content Viewer, as Apple asks.
	const button = tree.root.findByProps({ accessibilityRole: "button" });
	expect(button.props.accessibilityShowsLargeContentViewer).toBe(true);
	expect(button.props.accessibilityLargeContentTitle).toBe("Cancel");
});

it("follows Dynamic Type below xxxLarge", () => {
	text.fontScale = 19 / 17;
	const label = render(<HeaderButton label="Done" strong onPress={() => {}} />).root.findByType("Text" as never);
	expect(label.props.style.fontSize).toBeCloseTo(19);
});

it("truncates a long label within its slot rather than pushing past it", () => {
	const tree = render(<HeaderButton label="Terminer et enregistrer" strong onPress={() => {}} />);
	const label = tree.root.findByType("Text" as never);
	expect(label.props.style.flexShrink).toBe(1);
	const button = tree.root.findByProps({ accessibilityRole: "button" });
	expect(button.props.style({ pressed: false }).maxWidth).toBe("100%");
});
