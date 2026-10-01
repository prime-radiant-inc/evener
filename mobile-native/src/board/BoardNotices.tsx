import { type NavigationState, StackActions } from "@react-navigation/core";
import type { NativeStackNavigationProp } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { Pressable, Text, View } from "react-native";
import type { HubRoutes } from "../hub/hubSheetContext";
import type { Routes } from "../screens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { Notice } from "./notices";

/** What a link needs to open the Hub: the Board's navigation object, or the
 * alert banner's container ref. `getState` is the root stack's state in both. */
type HubNavigation = Pick<NativeStackNavigationProp<Routes>, "navigate" | "dispatch"> & {
	getState(): NavigationState | undefined;
};

/** A nested link into the Hub: the page it opens and that page's params. */
type HubLink = { [Name in keyof HubRoutes]: { screen: Name; params: HubRoutes[Name] } }[keyof HubRoutes];

/** Opens the Hub at a linked page. While the Hub is the root stack's top, the
 * link reaches the Hub's own stack by dispatch, targeted at the Hub navigator.
 * When the Hub already holds the page the link names, that dispatch is a
 * StackActions.popTo: a page it pops runs its beforeRemove guard (a host edit,
 * a provider detail holding a pasted key) and asks first (#3524). A nested
 * navigate carrying `pop` would skip that guard, and one without it stacks a
 * second list page over the pushed detail. When the Hub holds no such page,
 * the link is a StackActions.push instead: a popTo with no page to land on
 * would drop every page under the Hub's focused one, its home included. With
 * the Hub closed, the link opens it as it always has. */
function openHub(navigation: HubNavigation, link: HubLink): void {
	const state = navigation.getState();
	const hub = state?.routes[state.index];
	const pages = hub?.name === "Hub" ? hub.state?.routes : undefined;
	if (hub?.state != null && pages != null) {
		const action = pages.some((page) => page.name === link.screen)
			? StackActions.popTo(link.screen, link.params)
			: StackActions.push(link.screen, link.params);
		navigation.dispatch({ ...action, target: hub.state.key });
		return;
	}
	navigation.navigate("Hub", { ...link, initial: false });
}

/** Opens the Hub at a notice's subject: a host, a provider's sign-in or a
 * plugin. The Board's notice rows and a tapped notice banner both use it. */
export function openNotice(navigation: HubNavigation, hubId: string, notice: Notice): void {
	if (notice.kind === "signIn")
		// Ruling 25: the Hub opens at that provider's sign-in, its home kept under the page.
		openHub(navigation, {
			screen: "Providers",
			params: { hubId, focus: notice.providerId, signIn: true },
		});
	else if (notice.kind === "host")
		// Ruling 25: the Hub opens at that host, its home kept under the page.
		openHub(navigation, { screen: "Hosts", params: { hubId, focus: notice.sourceId } });
	else {
		// Ruling 25 again: the Hub opens at Plugins with that plugin's detail.
		openHub(navigation, {
			screen: "Plugins",
			params: { hubId, focus: { plugin: notice.pluginId, marketplace: notice.marketplace } },
		});
	}
}

/** Opens the Hub at Providers, its home kept under the page: where a sign-in
 * error that names no provider sends you. */
export function openProviders(navigation: HubNavigation, hubId: string): void {
	openHub(navigation, { screen: "Providers", params: { hubId } });
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
	navigation: HubNavigation;
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
