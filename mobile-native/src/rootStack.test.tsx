import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { afterEach, expect, it, vi } from "vitest";
import { palettes } from "./design/tokens";
import { render } from "./renderNative.testkit";
import { useRootStackOptions } from "./rootStack";

// The text size the phone is set to: 1 is the default (Large).
const text = vi.hoisted(() => ({ fontScale: 1 }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ fontScale: text.fontScale, scale: 2, width: 390, height: 844 }),
}));

afterEach(() => {
	text.fontScale = 1;
});

function options(): NativeStackNavigationOptions | undefined {
	let options: NativeStackNavigationOptions | undefined;
	function Probe() {
		options = useRootStackOptions();
		return null;
	}
	render(<Probe />);
	return options;
}

// The app's own pages (Delete saved session, Fork, …) title themselves as a
// sheet's pages do: Headline, following Dynamic Type up to xxxLarge (spec
// 16.2, audit N11).
it("titles the app's pages semibold at 17pt on the page color at the default text size", () => {
	expect(options()).toMatchObject({
		headerStyle: { backgroundColor: palettes.light.page },
		headerTintColor: palettes.light.accentInk,
		headerTitleStyle: { color: palettes.light.inkHi, fontSize: 17, fontWeight: "600" },
		contentStyle: { backgroundColor: palettes.light.page },
	});
});

it("grows the app's page titles with Dynamic Type, stopping at xxxLarge", () => {
	text.fontScale = 23 / 17;
	expect(options()?.headerTitleStyle).toMatchObject({ fontSize: 23 });
	text.fontScale = 53 / 17;
	expect(options()?.headerTitleStyle).toMatchObject({ fontSize: 23 });
});
