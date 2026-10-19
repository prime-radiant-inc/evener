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
			onAction={() => {}}
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

	it("hands every row's action to the chooser, level rows and self-run actions included", () => {
		const chosen: SessionMenuAction[] = [];
		const tree = sheet({ choose: (destination) => {}, onAction: (action) => chosen.push(action) });
		const rows = rowsOf(tree);
		expect(rows.length).toBeGreaterThan(0);
		for (const row of rows) {
			row.props.onPress();
		}
		// Every entry the menu can produce arrives as its own action: the five
		// level rows, and one of each action kind the facts allow. Pressing
		// through the menu's own entries derives the expected set.
		const expected = new Set<string>();
		sessionMenu({
			...facts(),
			choose: (action) => expected.add(action.kind === "level" ? `level:${action.level}` : action.kind),
		})
			.flatMap((item) => (item.type === "menu" ? flatEntriesOf(item.menu.items) : []))
			.forEach((entry) => entry.onPress());
		const got = new Set(chosen.map((action) => (action.kind === "level" ? `level:${action.level}` : action.kind)));
		expect(got).toEqual(expected);
	});
});
