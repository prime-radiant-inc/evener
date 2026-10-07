import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, renderedText, unmountMountedTrees } from "../renderNative.testkit";
import { sheetKey } from "../sheet/sheetHosts";
import type { BoardState, ClassifiedRow } from "./attention";
import { BoardRow } from "./BoardRow";
import type { RowAction } from "./rowActions";
import { ROW_ACTION_SYMBOLS, RowMenu, type RowMenuHost, RowMenuSheet, RowPreviewCard, rowMenuHosts } from "./RowMenu";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), dispatch: vi.fn() }));

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: () => {},
}));

const palette = paletteFor("light");
const row = (over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref: "studio:fix",
	host_id: "studio",
	session_id: "fix",
	title: "Fix retry loop",
	project: "evener",
	state: "errored",
	kind: "session",
	live: true,
	children: [],
	updated_at: "2026-09-26T12:00:00Z",
	...over,
});
const item = (state: BoardState, over: Partial<NavigationSessionSummary> = {}): ClassifiedRow => ({
	row: row(over),
	state,
});
const hostLabel = (hostId: string) => (hostId === "studio" ? "Studio Mac" : hostId);

type Style = Record<string, unknown>;
const flatten = (style: unknown): Style =>
	[style].flat(Number.POSITIVE_INFINITY).reduce<Style>((all, part) => ({ ...all, ...(part ?? {}) }), {});
const textNode = (tree: ReactTestRenderer, content: string) =>
	tree.root.find((node) => node.type === ("Text" as never) && [node.props.children].flat().includes(content));
const pressableLabelled = (tree: ReactTestRenderer, label: string) =>
	tree.root.find((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === label);
const symbols = (root: ReactTestInstance) =>
	root.findAllByType("SymbolView" as never).map((node) => node.props.name as string);

beforeEach(() => {
	navigation.goBack.mockClear();
	navigation.dispatch.mockClear();
});

describe("the preview card (spec 7.3)", () => {
	it("shows the title, the state word and its reason, the project and the host, and opens the session", () => {
		const open = vi.fn();
		const tree = render(<RowPreviewCard item={item("failed")} hostLabel={hostLabel} onPress={open} />);
		const text = renderedText(tree);
		expect(text).toContain("Fix retry loop");
		expect(text).toContain("Failed");
		expect(text).toContain("open the session to see what went wrong");
		expect(text).toContain("evener");
		expect(text).toContain("Studio Mac");
		expect(flatten(textNode(tree, "Failed").props.style)).toMatchObject({
			fontWeight: "600",
			color: palette.dangerInk,
		});
		expect(symbols(tree.root)).toEqual(expect.arrayContaining(["xmark.octagon.fill", "folder", "server.rack"]));
		const card = pressableLabelled(tree, "Open Fix retry loop");
		act(() => card.props.onPress());
		expect(open).toHaveBeenCalledOnce();
	});

	it("says a quiet state's word alone, in inkMid", () => {
		const tree = render(
			<RowPreviewCard item={item("idle", { state: "idle" })} hostLabel={hostLabel} onPress={() => {}} />,
		);
		expect(flatten(textNode(tree, "Idle").props.style)).toMatchObject({ color: palette.inkMid });
		expect(renderedText(tree)).not.toContain(" · ");
	});

	it("says what a working session is doing", () => {
		const tree = render(
			<RowPreviewCard
				item={item("working", { state: "active", running_job_count: 1, running_job_command: "npm test" })}
				hostLabel={hostLabel}
				onPress={() => {}}
			/>,
		);
		expect(renderedText(tree)).toContain("Running npm test");
	});
});

describe("the row menu sheet (ruling 28)", () => {
	const ACTIONS: RowAction[] = ["pin", "stop", "shutDown", "archive", "rename"];
	function host(over: Partial<RowMenuHost> = {}, items: ClassifiedRow[] = [item("working", { state: "active" })]) {
		const calls: string[] = [];
		navigation.goBack.mockImplementation(() => calls.push("goBack"));
		const provided: RowMenuHost = {
			item: (ref) => items.find((candidate) => candidate.row.ref === ref),
			actions: () => ACTIONS,
			hostLabel,
			act: vi.fn((_, action) => calls.push(`act:${action}`)),
			openSession: vi.fn(() => calls.push("open")),
			held: () => [],
			cancel: vi.fn((id) => calls.push(`cancel:${id}`)),
			closed: vi.fn(),
			...over,
		};
		return { provided, calls };
	}
	const owner = {};
	// Every sheet a test mounts, so one that fails before its own unmount
	// can't leave a sheet behind that answers the next test's host.
	const sheets: ReactTestRenderer[] = [];
	afterEach(() => {
		unmountMountedTrees();
		for (const tree of sheets.splice(0)) act(() => tree.unmount());
		rowMenuHosts.release(sheetKey("hub-1"), owner);
	});
	function mountSheet(provided: RowMenuHost | undefined, ref = "studio:fix", archived = false) {
		if (provided) rowMenuHosts.provide(sheetKey("hub-1"), owner, provided);
		const tree = render(
			<RowMenuSheet
				route={{ key: "menu", name: "RowMenuSheet", params: { hubId: "hub-1", ref, archived } } as never}
				navigation={navigation as never}
			/>,
		);
		sheets.push(tree);
		return tree;
	}

	it("shows Close, the card and the row's actions in order, Shut down in dangerInk", () => {
		const { provided } = host();
		const tree = mountSheet(provided);
		expect(renderedText(tree)).toContain("Close");
		expect(pressableLabelled(tree, "Open Fix retry loop")).toBeDefined();
		const labels = ["Pin to category…", "Stop", "Shut down", "Archive", "Rename"];
		const rows = tree.root.findAll(
			(node) => node.type === ("Pressable" as never) && labels.includes(node.props.accessibilityLabel),
		);
		expect(rows.map((node) => node.props.accessibilityLabel)).toEqual(labels);
		for (const [index, node] of rows.entries()) {
			expect(flatten(node.props.style({ pressed: false }))).toMatchObject({ minHeight: 44 });
			expect(symbols(node)).toEqual([ROW_ACTION_SYMBOLS[ACTIONS[index] as RowAction]]);
			const label = node.findByType("Text" as never);
			expect(flatten(label.props.style)).toMatchObject({
				fontSize: 17,
				color: labels[index] === "Shut down" ? palette.dangerInk : palette.inkHi,
			});
		}
	});

	it("leaves, then acts", () => {
		const { provided, calls } = host();
		const tree = mountSheet(provided);
		act(() => pressableLabelled(tree, "Shut down").props.onPress());
		expect(calls).toEqual(["goBack", "act:shutDown"]);
		expect(provided.act).toHaveBeenCalledWith(item("working", { state: "active" }), "shutDown");
	});

	it("offers to cancel what waits for the connection, after the actions (phase 6 ruling 18)", () => {
		const { provided, calls } = host({ held: () => [{ id: "held-1", label: "Cancel Archive" }] });
		const tree = mountSheet(provided);
		act(() => pressableLabelled(tree, "Cancel Archive").props.onPress());
		expect(calls).toEqual(["goBack", "cancel:held-1"]);
	});

	it("leaves, then opens the session from the card", () => {
		const { provided, calls } = host();
		const tree = mountSheet(provided);
		act(() => pressableLabelled(tree, "Open Fix retry loop").props.onPress());
		expect(calls).toEqual(["goBack", "open"]);
	});

	it("closes on Close, and tells the Board once when it goes away", () => {
		const { provided } = host();
		const tree = mountSheet(provided);
		act(() => pressableLabelled(tree, "Close").props.onPress());
		expect(navigation.goBack).toHaveBeenCalledOnce();
		expect(provided.closed).not.toHaveBeenCalled();
		act(() => tree.unmount());
		expect(provided.closed).toHaveBeenCalledOnce();
	});

	it("renders nothing and leaves when the Board is gone", () => {
		const tree = mountSheet(undefined);
		expect(tree.toJSON()).toBeNull();
		expect(navigation.goBack).toHaveBeenCalledOnce();
	});

	it("renders nothing and leaves when the row has left the Board", () => {
		const { provided } = host();
		const tree = mountSheet(provided, "studio:gone");
		expect(tree.toJSON()).toBeNull();
		expect(navigation.goBack).toHaveBeenCalledOnce();
	});
});

describe("the row's long press, with the sheet", () => {
	it("opens the sheet after a 500ms press", () => {
		const openSheet = vi.fn();
		const shown = item("finished", { state: "idle" });
		const tree = render(
			<RowMenu
				item={shown}
				actions={["markRead"]}
				hostLabel={hostLabel}
				onOpenSession={() => {}}
				onAction={() => {}}
				onOpenChange={() => {}}
				onOpenSheet={openSheet}
			>
				<BoardRow
					item={shown}
					variant="quiet"
					moving={false}
					connected
					usual={{}}
					hostLabel={hostLabel}
					hasDraft={false}
					msSinceRead={null}
					now={Date.UTC(2026, 8, 26, 12, 0)}
					onOpen={() => {}}
				/>
			</RowMenu>,
		);
		const pressable = tree.root.findByType("Pressable" as never);
		expect(pressable.props.delayLongPress).toBe(500);
		act(() => pressable.props.onLongPress());
		expect(openSheet).toHaveBeenCalledOnce();
	});
});
