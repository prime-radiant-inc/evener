import type { NavigationSessionSummary } from "@evener/appwire-client";
import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, textOf } from "../renderNative.testkit";
import { NextCapsule, nextCapsuleMaxWidth } from "./NextCapsule";

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

	// Small, at the trailing edge (spec 8.3): the whole capsule stays within
	// 60% of the screen's width (the test window is 390pt), and a long title
	// gives way inside it, so the transcript's leading side stays free to drag.
	it("stays within 60% of the width, its title giving way", () => {
		const long = { ...target, title: "Audit tool descriptions for implied options across every plugin" };
		const tree = render(<NextCapsule target={long} onOpen={() => {}} onHold={() => {}} />);
		const style = capsule(tree).props.style({ pressed: false });
		expect(style).toMatchObject({ maxWidth: 234 });
		const title = tree.root.findAll((node) => String(node.type) === "Text")[1];
		expect(title.props.style).toMatchObject({ flexShrink: 1 });
		expect(title.props.style.maxWidth).toBeUndefined();
	});

	// On a wide window (an iPad, landscape) 60% is no longer small: the
	// capsule stops at 320pt.
	it("stops at 320pt on a wide window", () => {
		expect(nextCapsuleMaxWidth(390)).toBe(234);
		expect(nextCapsuleMaxWidth(402)).toBe(241);
		expect(nextCapsuleMaxWidth(1024)).toBe(320);
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
