// A Board row's long-press menu (spec 7.3): a preview card that opens the
// session, then the row's actions. It is a native sheet route (ruling 28),
// which renders beside the Board, so it reads the row and hands each answer
// back through the host the Board provides: the Board's actions run through
// its one organization journal and its BoardStops (ruling 16), which a sheet
// can't mount a second time. Copy link waits for a session deep link
// (ruling 27).
import { useNavigation } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { type SFSymbol, SymbolView } from "expo-symbols";
import { cloneElement, type ReactElement, useEffect } from "react";
import { Pressable, ScrollView, Text, View } from "react-native";
import type { Routes } from "../screens";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type ClassifiedRow, stateWord, whyLine } from "./attention";
import { Fact, WhyText } from "./BoardRow";
import { ROW_ACTION_LABELS, type RowAction } from "./rowActions";
import { StateMark } from "./StateMark";

export const ROW_ACTION_SYMBOLS: Record<RowAction, SFSymbol> = {
	pin: "pin.fill",
	markRead: "circle",
	markUnread: "circle.fill",
	stop: "stop.fill",
	shutDown: "power",
	archive: "archivebox",
	unarchive: "archivebox",
	rename: "pencil",
};

/** What the row menu sheet reads from the Board, and hands its answers to. */
export interface RowMenuHost {
	/** The row as the Board shows it now, or undefined once it left. A session
	 * can show twice (Live and a project's Archived tier), so the sheet asks
	 * by the tier it was opened from, not just the ref. */
	item(ref: string, archived: boolean): ClassifiedRow | undefined;
	actions(item: ClassifiedRow, archived: boolean): RowAction[];
	hostLabel(hostId: string): string;
	act(item: ClassifiedRow, action: RowAction): void;
	/** What waits for the connection on this row, to cancel (phase 6 ruling
	 * 18): each held action's id and its "Cancel …" label. */
	held(item: ClassifiedRow): { id: string; label: string }[];
	cancel(id: string): void;
	openSession(item: ClassifiedRow): void;
	/** The menu went away, however it closed. */
	closed(): void;
}

/** The Board's row menu hosts, keyed by `sheetKey(hubId)`. */
export const rowMenuHosts = sheetHosts<RowMenuHost>();

/** The session a menu is about (spec 7.3): its state, why, project and
 * host. Pressing it opens the session; the menu has no "Open" item. */
export function RowPreviewCard({
	item,
	hostLabel,
	onPress,
}: {
	item: ClassifiedRow;
	hostLabel: (hostId: string) => string;
	onPress: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { row, state } = item;
	// Finished, Idle and Shut down have no reason: the word says it.
	const why = whyLine(item) ?? { text: stateWord(state) };
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Open ${row.title}`}
			onPress={onPress}
			style={({ pressed }) => ({
				marginHorizontal: 16,
				marginVertical: 8,
				padding: 12,
				rowGap: 4,
				borderRadius: 12,
				borderWidth: 0.5,
				borderColor: palette.edge,
				backgroundColor: pressed ? palette.pressed : palette.surface,
			})}
		>
			<View style={{ flexDirection: "row", alignItems: "flex-start", columnGap: 4 }}>
				<View style={{ height: 22 * scale, justifyContent: "center" }}>
					<StateMark state={state} />
				</View>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={2}
					style={{ flex: 1, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.inkHi }}
				>
					{row.title}
				</Text>
			</View>
			<WhyText why={why} />
			<View style={{ flexDirection: "row", alignItems: "center", columnGap: 8 }}>
				{row.project ? <Fact glyph="folder" text={row.project} scale={scale} shrink /> : null}
				<Fact glyph="server.rack" text={hostLabel(row.host_id)} scale={scale} shrink />
			</View>
		</Pressable>
	);
}

/** The row menu as a sheet route (ruling 28), opening at half height. */
export function RowMenuSheet({ route }: NativeStackScreenProps<Routes, "RowMenuSheet">) {
	const { hubId, ref, archived } = route.params;
	const navigation = useNavigation();
	// `host` is read when the sheet goes away, by then from its last render.
	const sheet = useSheet({ onClosed: () => host?.closed() });
	const host = useSheetHost(rowMenuHosts, sheetKey(hubId), sheet);
	const item = host?.item(ref, archived);
	// A row that left the Board leaves nothing to act on. (A Board that went
	// away is useSheetHost's to close.)
	const rowLeft = host !== undefined && item === undefined;
	useEffect(() => {
		if (rowLeft) sheet.finish();
	}, [rowLeft, sheet]);
	if (!host || !item) return null;
	// The sheet leaves first, then answers, so a screen the Board pushes or a
	// question it asks isn't under the sheet (ruling 28).
	const leaveThen = (answer: () => void) =>
		sheet.finish(() => {
			navigation.goBack();
			answer();
		});
	return (
		<Sheet done={{ label: "Close", onPress: () => sheet.finish() }}>
			<ScrollView contentInsetAdjustmentBehavior="automatic">
				<RowPreviewCard
					item={item}
					hostLabel={host.hostLabel}
					onPress={() => leaveThen(() => host.openSession(item))}
				/>
				{host.actions(item, archived).map((action) => (
					<MenuItem
						key={action}
						label={ROW_ACTION_LABELS[action]}
						symbol={ROW_ACTION_SYMBOLS[action]}
						danger={action === "shutDown"}
						onPress={() => leaveThen(() => host.act(item, action))}
					/>
				))}
				{host.held(item).map(({ id, label }) => (
					<MenuItem key={id} label={label} symbol="xmark.circle" onPress={() => leaveThen(() => host.cancel(id))} />
				))}
			</ScrollView>
		</Sheet>
	);
}

export interface RowMenuProps {
	item: ClassifiedRow;
	actions: readonly RowAction[];
	hostLabel: (hostId: string) => string;
	onOpenSession: () => void;
	onAction: (action: RowAction) => void;
	/** A native menu's open and close; the sheet's are its route and its
	 * host's `closed()`. */
	onOpenChange?: (open: boolean) => void;
	onOpenSheet: () => void;
	/** The row, which takes the long press. */
	children: ReactElement<{ onLongPress?: () => void; delayLongPress?: number }>;
}

/** How long a press lasts before it opens the menu. */
const LONG_PRESS_MS = 500;

/** A row's long-press menu. The menu is the RowMenuSheet route: a long press
 * calls `onOpenSheet()`. The row itself takes the press, since a touch goes
 * to the innermost pressable and a wrapping one would never see it. The
 * other props are what a native context menu needs (item, actions,
 * hostLabel, onOpenSession, onAction, onOpenChange), so one can replace the
 * sheet here without its callers changing. */
export function RowMenu({ onOpenSheet, children }: RowMenuProps) {
	return cloneElement(children, { onLongPress: onOpenSheet, delayLongPress: LONG_PRESS_MS });
}

function MenuItem({
	label,
	symbol,
	danger = false,
	onPress,
}: {
	label: string;
	symbol: SFSymbol;
	danger?: boolean;
	onPress: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				paddingHorizontal: 16,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 12,
				backgroundColor: pressed ? palette.pressed : palette.canvas,
			})}
		>
			<SymbolView name={symbol} size={18 * scale} tintColor={palette.inkMid} />
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 17 * scale, color: danger ? palette.dangerInk : palette.inkHi }}
			>
				{label}
			</Text>
		</Pressable>
	);
}
