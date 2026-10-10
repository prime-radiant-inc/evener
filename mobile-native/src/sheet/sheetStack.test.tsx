import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { act } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render, unmountMountedTrees } from "../renderNative.testkit";
import { useSheetStackOptions } from "./sheetStack";

// The text size the phone is set to: 1 is the default (Large).
const text = vi.hoisted(() => ({ fontScale: 1 }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ fontScale: text.fontScale, scale: 2, width: 390, height: 844 }),
}));

afterEach(() => {
	unmountMountedTrees();
	text.fontScale = 1;
});

function titleStyle(): NativeStackNavigationOptions["headerTitleStyle"] {
	let options: NativeStackNavigationOptions | undefined;
	function Probe() {
		options = useSheetStackOptions();
		return null;
	}
	render(<Probe />);
	return options?.headerTitleStyle;
}

// Spec 16.2: a sheet's title is SF Pro semibold 17, Headline, so it follows
// Dynamic Type as the header's Done does, and like Done stops at xxxLarge,
// where the bar's fixed height still holds it (audit N11).
it("titles its pages semibold at 17pt at the default text size", () => {
	expect(titleStyle()).toEqual({ color: palettes.light.inkHi, fontSize: 17, fontWeight: "600" });
});

it("grows its titles with Dynamic Type up to xxxLarge", () => {
	text.fontScale = 19 / 17;
	expect(titleStyle()).toMatchObject({ fontSize: 19 });
	text.fontScale = 23 / 17;
	expect(titleStyle()).toMatchObject({ fontSize: 23 });
});

it("stops its titles growing at the accessibility sizes", () => {
	text.fontScale = 53 / 17;
	expect(titleStyle()).toMatchObject({ fontSize: 23 });
});

// The navigator takes these as screenOptions on every render, so they keep
// their identity until the colors or the text size change.
it("hands the navigator the same options until the text size changes", () => {
	const seen: NativeStackNavigationOptions[] = [];
	function Probe(_: { tick: number }) {
		seen.push(useSheetStackOptions());
		return null;
	}
	const tree = render(<Probe tick={0} />);
	act(() => tree.update(<Probe tick={1} />));
	expect(seen[1]).toBe(seen[0]);
	text.fontScale = 23 / 17;
	act(() => tree.update(<Probe tick={2} />));
	expect(seen[2]).not.toBe(seen[1]);
});
