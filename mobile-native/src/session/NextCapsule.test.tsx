import type { NavigationSessionSummary } from "@evener/appwire-client";
import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, textOf } from "../renderNative.testkit";
import { NextCapsule } from "./NextCapsule";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");

const target: NavigationSessionSummary = {
	ref: "local:fail",
	host_id: "local",
	session_id: "fail",
	title: "Fix retry loop",
	project: "evener",
	state: "errored",
	kind: "session",
	live: true,
	children: [],
};

function capsule(tree: ReturnType<typeof render>) {
	return tree.root.findAll((node) => String(node.type) === "Pressable")[0];
}

describe("the Next capsule (spec 8.3)", () => {
	it("reads Next, then the session it opens, then a chevron", () => {
		const tree = render(<NextCapsule target={target} onOpen={() => {}} onHold={() => {}} />);
		expect(capsule(tree).props.accessibilityLabel).toBe("Next, Fix retry loop");
		const [next, title] = tree.root.findAll((node) => String(node.type) === "Text");
		expect(textOf(next)).toBe("Next");
		expect(next.props.style).toMatchObject({ color: palette.accentInk, fontWeight: "600" });
		expect(textOf(title)).toBe("Fix retry loop");
		expect(title.props).toMatchObject({ numberOfLines: 1 });
		expect(title.props.style).toMatchObject({ color: palette.inkHi });
		const chevron = tree.root.findAll((node) => String(node.type) === "SymbolView")[0];
		expect(chevron.props.name).toBe("chevron.right");
	});

	it("opens the session on a tap, and the list on a hold", () => {
		const onOpen = vi.fn();
		const onHold = vi.fn();
		const tree = render(<NextCapsule target={target} onOpen={onOpen} onHold={onHold} />);
		capsule(tree).props.onPress();
		expect(onOpen).toHaveBeenCalledTimes(1);
		expect(onHold).not.toHaveBeenCalled();
		capsule(tree).props.onLongPress();
		expect(onHold).toHaveBeenCalledTimes(1);
	});
});
