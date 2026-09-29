// Whether the system's Liquid Glass draws the bars, and a stack screen's nav
// bar options for it.
import { describe, expect, it, vi } from "vitest";
import { navBarGlassOptions } from "./systemGlass";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

describe("navBarGlassOptions", () => {
	it("is transparent and clear over the screen's own glass, with no system edge effect on top", () => {
		expect(navBarGlassOptions(true, "#FAF9F6")).toEqual({
			headerTransparent: true,
			headerStyle: { backgroundColor: "transparent" },
			scrollEdgeEffects: { top: "hidden" },
		});
	});

	// Off the glass (no Liquid Glass, Reduce Transparency on, Android) the bar
	// is what the stack gives every screen: native-stack's own defaults (not
	// transparent, the automatic edge effect) on App.tsx's page-colored
	// headerStyle.
	it("is the stack's default opaque bar off the glass", () => {
		expect(navBarGlassOptions(false, "#FAF9F6")).toEqual({
			headerTransparent: false,
			headerStyle: { backgroundColor: "#FAF9F6" },
			scrollEdgeEffects: { top: "automatic" },
		});
	});
});
