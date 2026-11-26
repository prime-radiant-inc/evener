import { GlassView } from "expo-glass-effect";
import { type SFSymbol, SymbolView } from "expo-symbols";
import type { ReactNode } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { useSystemGlass } from "../design/systemGlass";
import type { Palette } from "../design/tokens";

export interface SessionHeaderState {
	id: string;
	title: string;
	leading: "back" | "sessions";
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
		(header.leading === "back" || header.leading === "sessions") &&
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

function ControlGroup({ glass, palette, children }: { glass: boolean; palette: Palette; children: ReactNode }) {
	return glass ? (
		<GlassView glassEffectStyle="regular" colorScheme={palette.scheme} isInteractive style={styles.group}>
			{children}
		</GlassView>
	) : (
		<View
			style={[
				styles.group,
				{ backgroundColor: palette.inset, borderColor: palette.edgeStrong, borderWidth: StyleSheet.hairlineWidth },
			]}
		>
			{children}
		</View>
	);
}

export function SessionHeader({
	header,
	palette,
	onAction,
}: {
	header: SessionHeaderState;
	palette: Palette;
	onAction(action: HeaderAction): void;
}) {
	const glass = useSystemGlass();
	function control(action: HeaderAction) {
		const { symbol, label } = controls[action];
		const count = action === "sessions" ? header.needsYou : 0;
		const spoken = count ? `${label}, ${count} ${count === 1 ? "needs" : "need"} you` : label;
		return (
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={spoken}
				accessibilityShowsLargeContentViewer
				accessibilityLargeContentTitle={spoken}
				onPress={() => onAction(action)}
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
			<ControlGroup glass={glass} palette={palette}>
				{control(header.leading)}
			</ControlGroup>
			{!header.collapsed && (
				<Text accessibilityRole="header" numberOfLines={1} style={[styles.title, { color: palette.inkHi }]}>
					{header.title}
				</Text>
			)}
			<ControlGroup glass={glass} palette={palette}>
				{control("find")}
				{control("overview")}
			</ControlGroup>
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
	group: { flexDirection: "row", alignItems: "center", borderRadius: 24 },
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
