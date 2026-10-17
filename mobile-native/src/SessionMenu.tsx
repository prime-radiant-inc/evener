import { Pressable, ScrollView, View } from "react-native";
import type { NativeStackHeaderItemMenuAction, NativeStackHeaderItemMenuSubmenu } from "@react-navigation/native-stack";
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "./alerts/HoldingModal";
import { space } from "./design/tokens";
import { Group, Row } from "./sheet/Grouped";
import { Copy, styles, useColors } from "./ui";
import { sessionMenu, type SessionMenuAction, type SessionMenuInput } from "./session/sessionMenu";

export type SessionDestination = "find" | "session" | "tasks" | "notes" | "subagents" | "pin" | "delete";

export interface SessionMenuProps {
	title: string;
	hubName: string;
	/** The same facts the iOS header menu reads (sessionMenu's input), so both
	 * surfaces draw their rows from one source. */
	menu: Omit<SessionMenuInput, "choose">;
	/** Whether the Delete row shows: a saved session that isn't loaded. */
	deletionAvailable: boolean;
	close: () => void;
	choose: (destination: SessionDestination) => void;
}

type Entry = NativeStackHeaderItemMenuAction | NativeStackHeaderItemMenuSubmenu;

/** A menu entry as the sheet draws it: the entry's own label, description,
 * destructive flag and state, with where its press goes. The level submenu's
 * rows arrive the same way, their `state` becoming the checked row. */
interface SheetRow {
	label: string;
	sub?: string;
	destructive: boolean;
	checked: boolean;
	disabled: boolean;
	act: SessionMenuAction | null;
}

/** The menu's entries opened out flat, its submenus in place: the level
 * submenu's rows show in the sheet as the checked rows the native menu folds
 * under one heading. */
function flatEntries(items: readonly Entry[]): Entry[] {
	return items.flatMap((entry) => (entry.type === "submenu" ? flatEntries(entry.items) : [entry]));
}

/** The label the level submenu carries ("Detail level · Full" or the bare
 * heading), which the sheet shows as its group's label. */
function levelLabel(items: readonly Entry[]): string | undefined {
	const [first] = items;
	return first?.type === "submenu" ? first.label : undefined;
}

/** Where a menu action goes on this side: the destinations the screen opens,
 * or null for the ones the screen runs itself (stop, archive, shut down),
 * which the sheet hands back through `act`. */
const DESTINATIONS: Partial<Record<SessionMenuAction["kind"], SessionDestination>> = {
	find: "find",
	activity: "subagents",
	tasks: "tasks",
	notes: "notes",
	info: "session",
	pin: "pin",
};

/** The rows the sheet draws for the menu's entries, in the menu's order. */
export function sessionMenuSheetRows(input: SessionMenuInput): { label?: string; rows: SheetRow[] } {
	const [item] = sessionMenu(input);
	if (item?.type !== "menu") return { rows: [] };
	const entries = item.menu.items;
	const label = levelLabel(entries);
	const rows: SheetRow[] = [];
	const walk = (list: readonly Entry[]) => {
		for (const entry of list) {
			if (entry.type === "submenu") {
				walk(entry.items);
				continue;
			}
			rows.push({
				label: entry.label,
				sub: entry.description,
				destructive: entry.destructive === true,
				checked: entry.state === "on",
				disabled: entry.disabled === true,
				act: null,
			});
		}
	};
	walk(entries);
	return { label, rows: rows.map((row) => ({ ...row, act: actionOf(row.label, input) })) };
}

/** The action a label's press carries, taken from the entry's own onPress by
 * the choose the caller wired: the sheet routes a row exactly where the native
 * menu routes its item. */
function actionOf(label: string, input: SessionMenuInput): SessionMenuAction | null {
	let found: SessionMenuAction | null = null;
	sessionMenu({ ...input, choose: (action) => void (found = action) })
		.flatMap((item) => (item.type === "menu" ? flatEntries(item.menu.items) : []))
		.filter((entry) => entry.type === "action")
		.forEach((entry) => {
			if (entry.label === label) entry.onPress();
		});
	return found;
}

/** The Android/older-RN twin of the ⋯ menu (screens.tsx: iOS draws
 * sessionMenu() as a native UIMenu and never mounts this; Android keeps the
 * sheet). Its rows are regenerated from the same sessionMenu() data, so the
 * two surfaces cannot drift: the same labels, in the same order, with the same
 * descriptions, destructive tones and disabled states. */
export function SessionMenu({ title, hubName, menu, deletionAvailable, close, choose }: SessionMenuProps) {
	const { palette } = useColors();
	const { rows } = sessionMenuSheetRows({ ...menu, choose: () => {} });
	return (
		<HoldingModal transparent onRequestClose={close}>
			<View style={[styles.fill, { justifyContent: "flex-end" }]}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Dismiss session actions"
					onPress={close}
					style={{
						position: "absolute",
						inset: 0,
						backgroundColor: "#00000066",
					}}
				/>
				<SafeAreaView
					edges={["bottom", "left", "right"]}
					style={{
						maxHeight: "80%",
						backgroundColor: palette.canvas,
						borderTopLeftRadius: 24,
						borderTopRightRadius: 24,
					}}
				>
					<ScrollView contentContainerStyle={{ paddingBottom: 12 }}>
						<View style={{ paddingHorizontal: space.margin, paddingTop: 20, gap: 4 }}>
							<Copy>{title}</Copy>
							<Copy muted>{hubName}</Copy>
						</View>
						<Group section="session-menu">
							{rows.map((row, index) => (
								<Row
									// A detail level and an action can share a label
									// ("Activity"), so the row's place keys it.
									key={`${row.label}:${index}`}
									label={row.label}
									sub={row.sub}
									checked={row.checked ? true : undefined}
									disabled={row.disabled}
									tone={row.destructive ? "danger" : "accent"}
									onPress={() => {
										close();
										const kind = row.act?.kind;
										const destination = kind ? DESTINATIONS[kind] : undefined;
										if (destination) choose(destination);
									}}
								/>
							))}
						</Group>
						{deletionAvailable ? (
							<Group>
								<Row label="Delete saved session" tone="danger" onPress={() => choose("delete")} />
							</Group>
						) : null}
					</ScrollView>
				</SafeAreaView>
			</View>
		</HoldingModal>
	);
}
