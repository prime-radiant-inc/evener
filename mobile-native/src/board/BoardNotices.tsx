import type { NativeStackNavigationProp } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { Pressable, Text, View } from "react-native";
import type { Routes } from "../screens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { Notice } from "./notices";

/** The Board's notices, one row each under the chips (spec 7.1). Each action
 * opens today's screen for its kind; phase 5's Hub opens at the subject. */
export function BoardNotices({
	hubId,
	notices,
	navigation,
}: {
	hubId: string;
	notices: Notice[];
	navigation: Pick<NativeStackNavigationProp<Routes>, "navigate">;
}) {
	if (!notices.length) return null;
	const open = (notice: Notice) => {
		if (notice.kind === "signIn") navigation.navigate("Providers", { hubId });
		else if (notice.kind === "host")
			// Ruling 25: the Hub opens at that host, its home kept under the page.
			navigation.navigate("Hub", { screen: "Hosts", params: { hubId, focus: notice.sourceId }, initial: false });
		else navigation.navigate("Plugins", { hubId });
	};
	return notices.map((notice) => (
		<NoticeRow key={notice.key} text={notice.text} action={{ label: notice.action, onPress: () => open(notice) }} />
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
