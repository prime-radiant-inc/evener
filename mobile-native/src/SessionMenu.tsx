import { Pressable, ScrollView, View } from "react-native";
import type { NativeStackHeaderItemMenuAction, NativeStackHeaderItemMenuSubmenu } from "@react-navigation/native-stack";
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "./alerts/HoldingModal";
import { space } from "./design/tokens";
import { Group, Row } from "./sheet/Grouped";
import { Copy, styles, useColors } from "./ui";
import { sessionMenu, type SessionMenuAction, type SessionMenuInput } from "./session/sessionMenu";

export type SessionDestination = "find" | "session" | "tasks" | "notes" | "agents" | "jobs" | "pin" | "delete";

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
	/** Where each row's action goes: the screen's own chooser, the same
	 * chooseSessionAction the iOS header menu's items press. */
	onAction: (action: SessionMenuAction) => void;
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

/** The rows the sheet draws for the menu's entries, in the menu's order. */
export function sessionMenuSheetRows(input: SessionMenuInput): { rows: SheetRow[] } {
	// Each entry's own press carries its action; pressing an entry through the
	// same sessionMenu() the sheet reads records what its row must hand the
	// screen. This is per entry, not per label: a detail level and an action
	// can share a label ("Activity"), and the level's row must carry the level.
	let act: SessionMenuAction | null = null;
	const [item] = sessionMenu({ ...input, choose: (action) => void (act = action) });
	if (item?.type !== "menu") return { rows: [] };
	const entries = item.menu.items;
	const rows: SheetRow[] = [];
	const walk = (list: readonly Entry[]) => {
		for (const entry of list) {
			if (entry.type === "submenu") {
				walk(entry.items);
				continue;
			}
			act = null;
			entry.onPress();
			rows.push({
				label: entry.label,
				sub: entry.description,
				destructive: entry.destructive === true,
				checked: entry.state === "on",
				disabled: entry.disabled === true,
				act,
			});
		}
	};
	walk(entries);
	return { rows };
}

/** The Android/older-RN twin of the ⋯ menu (screens.tsx: iOS draws
 * sessionMenu() as a native UIMenu and never mounts this; Android keeps the
 * sheet). Its rows are regenerated from the same sessionMenu() data, so the
 * two surfaces cannot drift: the same labels, in the same order, with the same
 * descriptions, destructive tones and disabled states. */
export function SessionMenu({ title, hubName, menu, deletionAvailable, close, choose, onAction }: SessionMenuProps) {
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
										if (row.act) onAction(row.act);
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
