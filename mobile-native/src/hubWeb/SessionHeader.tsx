import { useNavigation } from "@react-navigation/native";
import type { NativeStackHeaderItemButton, NativeStackNavigationProp } from "@react-navigation/native-stack";
import { type SFSymbol, SymbolView } from "expo-symbols";
import { useLayoutEffect } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { GlassControlGroup } from "../design/GlassControlGroup";
import type { Palette } from "../design/tokens";
import type { Routes } from "../screens";

export interface SessionHeaderState {
	id: string;
	title: string;
	leading: "back" | "sessions" | null;
	needsYou: number;
	collapsed: boolean;
	height: number;
}

export function isSessionHeader(value: unknown): value is SessionHeaderState {
	if (!value || typeof value !== "object") return false;
	const header = value as Partial<SessionHeaderState>;
	return (
		typeof header.id === "string" &&
		header.id.length > 0 &&
		typeof header.title === "string" &&
		(header.leading === "back" || header.leading === "sessions" || header.leading === null) &&
		typeof header.needsYou === "number" &&
		Number.isSafeInteger(header.needsYou) &&
		header.needsYou >= 0 &&
		typeof header.collapsed === "boolean" &&
		typeof header.height === "number" &&
		Number.isFinite(header.height) &&
		header.height > 0
	);
}

const controls = {
	back: { symbol: "chevron.backward", label: "Back" },
	sessions: { symbol: "sidebar.leading", label: "Sessions" },
	find: { symbol: "magnifyingglass", label: "Find in session" },
	overview: { symbol: "sidebar.trailing", label: "Overview" },
} as const satisfies Record<string, { symbol: SFSymbol; label: string }>;
export type HeaderAction = keyof typeof controls;

interface HeaderProps {
	header: SessionHeaderState;
	palette: Palette;
	onAction(id: string, action: HeaderAction): void;
}

function headerControl(action: HeaderAction, needsYou: number) {
	const { symbol, label } = controls[action];
	const count = action === "sessions" ? needsYou : 0;
	const spoken = count ? `${label}, ${count} ${count === 1 ? "needs" : "need"} you` : label;
	return { symbol, label, count, spoken };
}

export function EdgeSessionToolbar({ header, palette, onAction }: HeaderProps) {
	const navigation = useNavigation<NativeStackNavigationProp<Routes, "Workspace">>();
	useLayoutEffect(() => {
		function button(action: HeaderAction): NativeStackHeaderItemButton {
			const { symbol, label, spoken, count } = headerControl(action, header.needsYou);
			return {
				type: "button",
				label,
				accessibilityLabel: spoken,
				icon: { type: "sfSymbol", name: symbol },
				badge: count ? { value: count } : undefined,
				onPress: () => onAction(header.id, action),
			};
		}
		navigation.setOptions({
			headerShown: true,
			headerTransparent: true,
			headerBackVisible: false,
			headerTitle: header.collapsed ? "" : header.title,
			headerStyle: { backgroundColor: "transparent" },
			headerTintColor: palette.inkHi,
			headerTitleStyle: { color: palette.inkHi },
			unstable_headerLeftItems: () => (header.leading ? [button(header.leading)] : []),
			unstable_headerRightItems: () => [button("find"), button("overview")],
		});
		return () => navigation.setOptions({ headerShown: false });
	}, [header, palette, onAction, navigation]);
	return null;
}

export function SessionHeader({ header, glass, palette, onAction }: HeaderProps & { glass: boolean }) {
	function control(action: HeaderAction) {
		const { symbol, count, spoken } = headerControl(action, header.needsYou);
		return (
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={spoken}
				accessibilityShowsLargeContentViewer
				accessibilityLargeContentTitle={spoken}
				onPress={() => onAction(header.id, action)}
				style={({ pressed }) => [styles.button, { opacity: pressed ? 0.55 : 1 }]}
			>
				<SymbolView name={symbol} size={19} weight="semibold" tintColor={palette.inkHi} />
				{count > 0 && (
					<Text style={[styles.badge, { color: palette.attentionInk, backgroundColor: palette.attentionBg }]}>
						{count}
					</Text>
				)}
			</Pressable>
		);
	}
	return (
		<View
			pointerEvents="box-none"
			style={[
				styles.header,
				{
					height: header.height,
					backgroundColor: header.collapsed ? "transparent" : palette.surface,
				},
			]}
		>
			{header.leading && (
				<GlassControlGroup glass={glass} palette={palette}>
					{control(header.leading)}
				</GlassControlGroup>
			)}
			{!header.collapsed && (
				<Text accessibilityRole="header" numberOfLines={1} style={[styles.title, { color: palette.inkHi }]}>
					{header.title}
				</Text>
			)}
			<GlassControlGroup glass={glass} palette={palette}>
				{control("find")}
				{control("overview")}
			</GlassControlGroup>
		</View>
	);
}

const styles = StyleSheet.create({
	header: {
		position: "absolute",
		top: 0,
		left: 0,
		right: 0,
		flexDirection: "row",
		alignItems: "center",
		justifyContent: "space-between",
		paddingHorizontal: 8,
		gap: 10,
	},
	title: { flex: 1, fontSize: 17, fontWeight: "600" },
	button: {
		alignItems: "center",
		justifyContent: "center",
		minWidth: 44,
		minHeight: 44,
		flexDirection: "row",
		gap: 3,
		paddingHorizontal: 9,
	},
	badge: { fontSize: 12, fontWeight: "700", borderRadius: 8, overflow: "hidden", paddingHorizontal: 4 },
});
