// A pinned category's section (spec 7.1) mounted with only its native edges
// mocked; the fold memory is the real FoldedSections over an in-memory
// kv-store.
import type { NavigationPinSectionDescriptor, NavigationSessionSummary } from "@evener/appwire-client";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { boardState } from "./attention";
import { BoardRow } from "./BoardRow";
import type { RowContext } from "./BoardRows";
import { PinnedSection, useCategoryFolds } from "./PinnedSections";
import { StateMark } from "./StateMark";

const harness = vi.hoisted(() => ({ kv: new Map<string, string>() }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => harness.kv.set(key, value),
		removeItemSync: (key: string) => harness.kv.delete(key),
	},
}));

const palette = paletteFor("light");
const NOW = Date.UTC(2026, 8, 26, 12, 0);
const session = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	updated_at: new Date(NOW - 5 * 60_000).toISOString(),
	...over,
});
const release: NavigationPinSectionDescriptor = { id: "release", name: "Release", count: 2 };
const building = session("local:build", { title: "Build docs", state: "active" });
const shipped = session("local:ship", { title: "Ship it" });

let hubCount = 0;
const hubId = () => `pinned-hub-${++hubCount}`;

interface Props {
	hub: string;
	section?: NavigationPinSectionDescriptor;
	page?: { loaded: boolean; rows: NavigationSessionSummary[] };
	onMenu?: (() => void) | null;
	changing?: boolean;
	onOpen?: (row: NavigationSessionSummary) => void;
}
/** One category as the Board draws it, its fold kept by useCategoryFolds. */
function Category({ hub, section = release, page, onMenu = null, changing = false, onOpen = () => {} }: Props) {
	const folds = useCategoryFolds(hub);
	const context: RowContext = {
		connected: true,
		usual: { project: "evener", host: "local" },
		hostLabel: (id) => id,
		now: NOW,
		onOpen,
		draftRefs: new Set(),
		activityOf: () => undefined,
		msSinceRead: null,
		swipes: () => ({ trailing: [], dimmed: false }),
	};
	return (
		<PinnedSection
			section={section}
			page={page}
			classify={(row) => ({ row, state: boardState(row, false, true) })}
			context={context}
			folded={folds.isFolded(section.id)}
			onToggle={() => folds.setFolded(section.id, !folds.isFolded(section.id))}
			onMenu={onMenu}
			changing={changing}
		/>
	);
}
const loaded = (rows: NavigationSessionSummary[]) => ({ loaded: true, rows });

const header = (tree: ReactTestRenderer) => tree.root.find((node) => node.props.testID === "pin-header");
const menuButton = (tree: ReactTestRenderer) =>
	tree.root.findAll(
		(node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "Release, category menu",
	);
const rows = (tree: ReactTestRenderer) => tree.root.findAllByType(BoardRow);
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
	const tree = render(<Category hub={hubId()} page={loaded([building])} />);
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
	const tree = render(<Category hub={hubId()} page={loaded([])} onMenu={onMenu} />);
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
	const tree = render(<Category hub={hub} page={loaded([building, shipped])} />);
	expect(rows(tree)).toHaveLength(2);
	act(() => header(tree).props.onPress());
	expect(rows(tree)).toHaveLength(0);
	expect(header(tree).props.accessibilityState).toEqual({ expanded: false });
	expect(JSON.parse(harness.kv.get(`evener.native.board-sections.${hub}`) ?? "null")).toEqual({ "pin:release": true });
	act(() => tree.unmount());
	const again = render(<Category hub={hub} page={loaded([building, shipped])} />);
	expect(rows(again)).toHaveLength(0);
	act(() => header(again).props.onPress());
	expect(rows(again)).toHaveLength(2);
});

it("lists its sessions as quiet one-line rows with still marks, even a working one, and opens them", () => {
	const onOpen = vi.fn();
	const tree = render(<Category hub={hubId()} page={loaded([building, shipped])} onOpen={onOpen} />);
	expect(rows(tree).map((row) => [row.props.variant, row.props.moving])).toEqual([
		["quiet", false],
		["quiet", false],
	]);
	expect(tree.root.findAllByType(StateMark).map((mark) => mark.props.moving)).toEqual([false, false]);
	expect(rows(tree)[0].props.item.state).toBe("working");
	act(() => rows(tree)[1].findByType("Pressable" as never).props.onPress());
	expect(onOpen).toHaveBeenCalledWith(shipped);
});

it("says how to pin to an empty category only once its page has loaded", () => {
	const hub = hubId();
	const empty = { ...release, count: 0 };
	const tree = render(<Category hub={hub} section={empty} />);
	expect(textsOf(tree.root)).not.toContain(HINT);
	expect(rows(tree)).toHaveLength(0);
	act(() => tree.update(<Category hub={hub} section={empty} page={{ loaded: false, rows: [] }} />));
	expect(textsOf(tree.root)).not.toContain(HINT);
	act(() => tree.update(<Category hub={hub} section={empty} page={loaded([])} />));
	const hint = tree.root.find((node) => node.type === ("Text" as never) && node.props.children === HINT);
	expect(flatten(hint.props.style)).toMatchObject({ fontSize: 15, color: palette.inkMid, marginHorizontal: 16 });
	// Folded, it says nothing.
	act(() => header(tree).props.onPress());
	expect(textsOf(tree.root)).not.toContain(HINT);
});

it("dims while a change to it is on its way", () => {
	const hub = hubId();
	const tree = render(<Category hub={hub} page={loaded([shipped])} />);
	const section = () => tree.root.find((node) => node.props.testID === "pin-section");
	expect(flatten(section().props.style).opacity ?? 1).toBe(1);
	act(() => tree.update(<Category hub={hub} page={loaded([shipped])} changing />));
	expect(flatten(section().props.style).opacity).toBe(0.5);
});
