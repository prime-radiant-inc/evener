import { expect, it, vi } from "vitest";
import { typeRoles } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { DisplayProvider, type ReadingRoles, readingRoles, useReadingType } from "./displayContext";
import { DisplayPreferences } from "./displayPreferences";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

it("keeps the serif roles for Serif", () => {
	expect(readingRoles("serif").agentProse.fontFamily).toBe(typeRoles.agentProse.fontFamily);
});

it("drops the serif for Sans, keeps every size, and leaves Menlo alone", () => {
	const sans = readingRoles("sans");
	for (const name of Object.keys(typeRoles) as (keyof typeof typeRoles)[]) {
		expect(sans[name].fontSize).toBe(typeRoles[name].fontSize);
		expect(sans[name].lineHeight).toBe(typeRoles[name].lineHeight);
	}
	expect(sans.agentProse.fontFamily).toBeUndefined();
	expect(sans.yourMessage.fontFamily).toBeUndefined();
	expect(sans.machine.fontFamily).toBe("Menlo");
});

it("reads the phone's choice, and the serif with no provider", () => {
	const seen: { roles: ReadingRoles | null } = { roles: null };
	function Probe() {
		seen.roles = useReadingType();
		return null;
	}
	render(<Probe />);
	expect(seen.roles?.agentProse.fontFamily).toBe(typeRoles.agentProse.fontFamily);
	const values = new Map<string, string>();
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	});
	prefs.set({ readingFont: "sans" });
	render(
		<DisplayProvider value={prefs}>
			<Probe />
		</DisplayProvider>,
	);
	expect(seen.roles?.agentProse.fontFamily).toBeUndefined();
});
