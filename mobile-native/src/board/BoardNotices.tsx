import type { NativeStackNavigationProp } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { Pressable, Text, View } from "react-native";
import type { Routes } from "../screens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { Notice } from "./notices";

/** Opens the Hub at a notice's subject: a host, a provider's sign-in or a
 * plugin. The Board's notice rows and a tapped notice banner both use it. */
export function openNotice(
	navigation: Pick<NativeStackNavigationProp<Routes>, "navigate">,
	hubId: string,
	notice: Notice,
): void {
	// Each link names `pop`: with the Hub already open, it hands the link to the
	// page already in the Hub's stack, popping a detail pushed over it, rather
	// than stacking a second list page (React Navigation 7 only reuses a route
	// that is focused or named by `pop`).
	if (notice.kind === "signIn")
		// Ruling 25: the Hub opens at that provider's sign-in, its home kept under the page.
		navigation.navigate("Hub", {
			screen: "Providers",
			params: { hubId, focus: notice.providerId, signIn: true },
			initial: false,
			pop: true,
		});
	else if (notice.kind === "host")
		// Ruling 25: the Hub opens at that host, its home kept under the page.
		navigation.navigate("Hub", {
			screen: "Hosts",
			params: { hubId, focus: notice.sourceId },
			initial: false,
			pop: true,
		});
	else {
		// Ruling 25 again: the Hub opens at Plugins with that plugin's detail.
		navigation.navigate("Hub", {
			screen: "Plugins",
			params: { hubId, focus: { plugin: notice.pluginId, marketplace: notice.marketplace } },
			initial: false,
			pop: true,
		});
	}
}

/** The Board's notices, one row each under the chips (spec 7.1). */
export function BoardNotices({
	hubId,
	notices,
	navigation,
	connected,
}: {
	hubId: string;
	notices: Notice[];
	navigation: Pick<NativeStackNavigationProp<Routes>, "navigate">;
	/** Whether the hub is reachable. A notice's action needs it (ruling 21):
	 * while the hub is out of reach the action hides, and the sentence stays. */
	connected: boolean;
}) {
	if (!notices.length) return null;
	return notices.map((notice) => (
		<NoticeRow
			key={notice.key}
			text={notice.text}
			action={connected ? { label: notice.action, onPress: () => openNotice(navigation, hubId, notice) } : undefined}
		/>
	));
}

/** A hub-level notice on the page, like a row: the mark says it needs you,
 * so there's no box. Its action, when it has one, is blue. */
export function NoticeRow({ text, action }: { text: string; action?: { label: string; onPress: () => void } }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			testID="notice"
			style={{
				marginTop: 4,
				marginHorizontal: 16,
				paddingTop: 8,
				paddingBottom: 10,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 10,
				borderBottomWidth: 0.5,
				borderColor: palette.edge,
			}}
		>
			<View style={{ width: 22, alignItems: "center" }}>
				<SymbolView name="exclamationmark.triangle.fill" size={17 * scale} tintColor={palette.attention} />
			</View>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ flex: 1, fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
			>
				{text}
			</Text>
			{action ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`${action.label}, ${text}`}
					onPress={action.onPress}
					// The action draws 30pt tall; the slop reaches into the row's
					// padding for a 44pt target.
					hitSlop={{ top: 7, bottom: 7 }}
					style={({ pressed }) => ({ minHeight: 30, justifyContent: "center", opacity: pressed ? 0.6 : 1 })}
				>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, fontWeight: "600", color: palette.accentInk }}
					>
						{action.label}
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}
