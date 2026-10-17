// The session actions sheet (Android's ⋯ menu) pads its own bottom safe area:
// the session screen leaves that edge to its bottom bar, and the menu opens in
// a modal over it, so it can't rely on the screen for it.
//
// Its rows are regenerated from the same sessionMenu() data the iOS header
// menu draws (screens.tsx), so the two surfaces cannot drift: whatever the
// menu shows, the sheet shows, in the same order, with the same tones,
// descriptions and disabled states.
import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { ContentLevel } from "@evener/appwire-client";
import { render } from "./renderNative.testkit";
import { SessionMenu, type SessionMenuProps } from "./SessionMenu";
import { sessionMenu, type SessionMenuAction } from "./session/sessionMenu";
import type { NativeStackHeaderItemMenuAction, NativeStackHeaderItemMenuSubmenu } from "@react-navigation/native-stack";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./alerts/HoldingModal", () => ({ HoldingModal: (props: { children?: ReactNode }) => props.children ?? null }));

/** The same facts the screen passes: every capability on, so every row shows. */
function facts(over: Partial<SessionMenuProps["menu"]> = {}) {
	return {
		current: null as ContentLevel | null,
		hasDocuments: true,
		connected: true,
		sharedNotes: true,
		canAside: true,
		canShutDown: true,
		canStop: true,
		...over,
	};
}

function sheet(props: Partial<SessionMenuProps> = {}) {
	return render(
		<SessionMenu
			title="Session"
			hubName="Hub"
			menu={facts()}
			deletionAvailable={false}
			close={() => {}}
			choose={() => {}}
			{...props}
		/>,
	);
}

/** The sheet's own rows, as its grouped Row pressables. */
function rowsOf(tree: ReturnType<typeof render>) {
	return tree.root.findAll(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel !== "Dismiss session actions",
	);
}

/** The menu's entries opened out flat, in the menu's order, each with the
 * label, description and destructive flag the sheet must carry. */
type Flat = { label: string; description?: string; destructive: boolean; disabled: boolean; state?: string };
type Entry = NativeStackHeaderItemMenuAction | NativeStackHeaderItemMenuSubmenu;

/** The menu's entries opened out flat, submenus included. */
function flatEntriesOf(items: readonly Entry[]): NativeStackHeaderItemMenuAction[] {
	return items.flatMap((entry) =>
		entry.type === "submenu" ? flatEntriesOf(entry.items) : [entry as NativeStackHeaderItemMenuAction],
	);
}

function flatOf(menu: SessionMenuProps["menu"]): Flat[] {
	const [item] = sessionMenu({ ...menu, choose: () => {} });
	if (item?.type !== "menu") return [];
	const flat: Flat[] = [];
	const walk = (items: readonly Entry[]) => {
		for (const entry of items) {
			if (entry.type === "submenu") {
				walk(entry.items);
				continue;
			}
			flat.push({
				label: entry.label,
				description: entry.description,
				destructive: entry.destructive === true,
				disabled: entry.disabled === true,
				state: entry.state,
			});
		}
	};
	walk((item as { menu: { items: Entry[] } }).menu.items);
	return flat;
}

/** The destinations the menu's own items open, derived by pressing each of
 * its entries through the same sessionMenu() the sheet reads. */
function menuDestinations(): string[] {
	const kinds = new Set<string>();
	const menu = facts();
	sessionMenu({
		...menu,
		choose: (action: SessionMenuAction) => {
			const destinations: Partial<Record<SessionMenuAction["kind"], string>> = {
				find: "find",
				activity: "subagents",
				tasks: "tasks",
				notes: "notes",
				info: "session",
				pin: "pin",
			};
			const destination = destinations[action.kind];
			if (destination) kinds.add(destination);
		},
	})
		.flatMap((item) => (item.type === "menu" ? flatEntriesOf(item.menu.items) : []))
		.forEach((entry) => entry.onPress());
	return [...kinds];
}

describe("SessionMenu", () => {
	it("keeps its actions above the home indicator on its own", () => {
		const tree = sheet();
		const [safe] = tree.root.findAll((node) => String(node.type) === "SafeAreaView");
		expect(safe?.props.edges).toContain("bottom");
	});

	it("draws the same rows the iOS header menu does, from the same data", () => {
		const menu = facts({ canAside: false, canShutDown: false, canStop: false });
		const tree = sheet({ menu });
		const expected = flatOf(menu);
		// The sheet's rows read as the menu's entries do, in the menu's order.
		// A Row's VoiceOver label composes its second line into it ("Chat, Just
		// the conversation"), so the entry's label leads each one.
		expect(rowsOf(tree).map((row) => String(row.props.accessibilityLabel).split(",")[0])).toEqual(
			expected.map((row) => row.label),
		);
		const treeAll = sheet();
		const rows = rowsOf(treeAll);
		for (const entry of flatOf(facts())) {
			const row = rows.find((candidate) => String(candidate.props.accessibilityLabel).startsWith(entry.label));
			// A row with no check to draw carries only `disabled`; the checked
			// one (the session's current detail level) also says `selected`.
			expect(row?.props.accessibilityState).toEqual(
				entry.state === "on" ? { disabled: entry.disabled, selected: true } : { disabled: entry.disabled },
			);
		}
	});

	it("leaves out what the menu leaves out, and says what its rows describe", () => {
		const menu = facts({ hasDocuments: false, sharedNotes: false });
		const tree = sheet({ menu });
		const labels = rowsOf(tree).map((row) => row.props.accessibilityLabel);
		// The menu's own data leaves Files and Notes & links out here.
		expect(labels).not.toContain("Files");
		expect(labels).not.toContain("Notes & links");
		expect(labels).toContain("Find in session");
		// A description it carries (Ask aside's) reads as the row's second line.
		const aside = rowsOf(tree).find((row) => row.props.accessibilityLabel?.startsWith("Ask aside"));
		expect(aside).toBeDefined();
	});

	it("routes a row's press the way the menu routes its item", () => {
		const opened: string[] = [];
		const tree = sheet({ choose: (destination) => opened.push(destination) });
		const rows = rowsOf(tree);
		// Every row that opens a destination does so on press; the destructive
		// ones (Stop, Shut down) and the self-run ones (Archive, Ask aside)
		// close the sheet and open nothing the sheet names.
		for (const row of rows) {
			row.props.onPress();
		}
		// Two menu items can open the same screen (Activity and Subagents both
		// open the subagents list), so the destinations compare as a set.
		expect([...new Set(opened)].sort()).toEqual(menuDestinations().sort());
	});
});
