// A pinned category's section (spec 7.1) mounted with only its native edges
// mocked; the fold memory is the real FoldedSections over an in-memory
// kv-store.
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { PinnedEmptyHint, PinnedSection, useCategoryFolds } from "./PinnedSections";

const harness = vi.hoisted(() => ({ kv: new Map<string, string>() }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => harness.kv.set(key, value),
		removeItemSync: (key: string) => harness.kv.delete(key),
	},
}));

const palette = paletteFor("light");
const release: NavigationPinSectionDescriptor = { id: "release", name: "Release", count: 2 };

let hubCount = 0;
const hubId = () => `pinned-hub-${++hubCount}`;

interface Props {
	hub: string;
	section?: NavigationPinSectionDescriptor;
	onMenu?: (() => void) | null;
	changing?: boolean;
}
/** One category as the Board draws it, its fold kept by useCategoryFolds. */
function Category({ hub, section = release, onMenu = null, changing = false }: Props) {
	const folds = useCategoryFolds(hub);
	return (
		<PinnedSection
			section={section}
			folded={folds.isFolded(section.id)}
			onToggle={() => folds.setFolded(section.id, !folds.isFolded(section.id))}
			onMenu={onMenu}
			changing={changing}
		/>
	);
}

const header = (tree: ReactTestRenderer) => tree.root.find((node) => node.props.testID === "pin-header");
const menuButton = (tree: ReactTestRenderer) =>
	tree.root.findAll(
		(node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "Release, category menu",
	);
const textsOf = (node: ReactTestInstance) =>
	node
		.findAll((child) => child.type === ("Text" as never))
		.flatMap((child) => [child.props.children].flat().filter((part): part is string => typeof part === "string"));
const HINT = "Touch and hold a session and choose Pin to category.";
type Style = Record<string, unknown>;
const flatten = (style: unknown): Style =>
	[typeof style === "function" ? style({ pressed: false }) : style]
		.flat(Number.POSITIVE_INFINITY)
		.reduce<Style>((all, part) => ({ ...all, ...(part ?? {}) }), {});

it("draws the header as a band header: pin, the name in capitals, the hub's count and a fold chevron", () => {
	// The count is the catalog's, the hub's number, whatever has loaded.
	const tree = render(<Category hub={hubId()} />);
	const press = header(tree);
	expect(press.type).toBe("Pressable");
	expect(press.props.accessibilityRole).toBe("button");
	expect(press.props.accessibilityLabel).toBe("Release, 2 sessions");
	expect(press.props.accessibilityState).toEqual({ expanded: true });
	expect(flatten(press.props.style).minHeight).toBeGreaterThanOrEqual(44);
	const symbols = press.findAllByType("SymbolView" as never).map((node) => [node.props.name, node.props.tintColor]);
	// The pin leads; the chevron sits at the trailing edge, as Idle's does.
	expect(symbols).toEqual([
		["pin.fill", palette.inkLow],
		["chevron.right", palette.inkLow],
	]);
	expect(textsOf(press)).toEqual(["Release", " · 2"]);
	// The name and its count read as one label, "RELEASE · 2", as a band
	// header's do: only the count's own spaces sit between them, so the row
	// adds no gap and the pin keeps its distance with a margin of its own.
	expect(flatten(press.props.style).columnGap ?? 0).toBe(0);
	expect(press.findAllByType("SymbolView" as never)[0].props.style).toMatchObject({ marginRight: 6 });
	const name = press.findAll((node) => node.type === ("Text" as never))[0];
	// The name keeps the user's casing underneath, so VoiceOver reads it as
	// written; only the drawing is in capitals.
	expect(flatten(name.props.style)).toMatchObject({
		fontSize: 13,
		fontWeight: "600",
		letterSpacing: 0.4,
		color: palette.inkMid,
		textTransform: "uppercase",
	});
	// No ⋯ while no change can go out.
	expect(menuButton(tree)).toHaveLength(0);
});

it("shows ⋯ as its own 44pt target when a change can go out", () => {
	const onMenu = vi.fn();
	const tree = render(<Category hub={hubId()} onMenu={onMenu} />);
	const [menu] = menuButton(tree);
	expect(menu.props.accessibilityRole).toBe("button");
	const style = flatten(menu.props.style);
	expect(style.minWidth).toBeGreaterThanOrEqual(44);
	expect(style.minHeight).toBeGreaterThanOrEqual(44);
	expect(menu.findAllByType("SymbolView" as never).map((node) => [node.props.name, node.props.tintColor])).toEqual([
		["ellipsis.circle", palette.inkLow],
	]);
	act(() => menu.props.onPress());
	expect(onMenu).toHaveBeenCalledTimes(1);
});

it("starts unfolded, folds on a tap, and keeps the fold across a remount", () => {
	const hub = hubId();
	const tree = render(<Category hub={hub} />);
	expect(header(tree).props.accessibilityState).toEqual({ expanded: true });
	act(() => header(tree).props.onPress());
	expect(header(tree).props.accessibilityState).toEqual({ expanded: false });
	expect(JSON.parse(harness.kv.get(`evener.native.board-sections.${hub}`) ?? "null")).toEqual({ "pin:release": true });
	act(() => tree.unmount());
	const again = render(<Category hub={hub} />);
	expect(header(again).props.accessibilityState).toEqual({ expanded: false });
	act(() => header(again).props.onPress());
	expect(header(again).props.accessibilityState).toEqual({ expanded: true });
});

it("says how to pin to an empty category in the Board's secondary text", () => {
	const hint = render(<PinnedEmptyHint />).root.find((node) => node.type === ("Text" as never));
	expect(hint.props.children).toBe(HINT);
	expect(flatten(hint.props.style)).toMatchObject({ fontSize: 15, color: palette.inkMid, marginHorizontal: 16 });
});

it("dims while a change to it is on its way", () => {
	const hub = hubId();
	const tree = render(<Category hub={hub} />);
	const section = () => tree.root.find((node) => node.props.testID === "pin-section");
	expect(flatten(section().props.style).opacity ?? 1).toBe(1);
	act(() => tree.update(<Category hub={hub} changing />));
	expect(flatten(section().props.style).opacity).toBe(0.5);
});
